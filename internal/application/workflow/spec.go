// Package workflow (application) traduz o arquivo YAML para o dominio.
//
// A separacao existe porque a secao 22 do plano e explicita: depois de publicado,
// o banco e a fonte da verdade, nao o arquivo. O YAML e ENTRADA de publicacao —
// entra aqui, vira dominio, e o dominio e que persiste. Mudar o formato do
// arquivo nao deve tocar as invariantes do grafo.
package workflow

import (
	"fmt"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	dominio "github.com/AreteAcademy/bravis/internal/domain/workflow"
)

// Spec espelha o YAML, e so isso. Campos frouxos aqui, invariantes no dominio.
type Spec struct {
	Name      string       `yaml:"name"`
	Schedule  string       `yaml:"schedule"`
	Type      string       `yaml:"type"`
	Tags      []string     `yaml:"tags"`
	Image     string       `yaml:"image"`
	Resources ResourceSpec `yaml:"resources"`
	Params    []ParamSpec  `yaml:"params"`
	// Concurrency e o `concurrency.limit` do Kestra, com o mesmo nome que a
	// maioria dos orquestradores usa.
	Concurrency int        `yaml:"concurrency"`
	Steps       []StepSpec `yaml:"steps"`
}

// ResourceSpec e o pedido de CPU e memoria no formato do Kubernetes.
//
// `limits` separado de `requests` porque a diferenca entre os dois e a diferenca
// entre "quanto reservo" e "quando me matam": um dbt que estoura o limite morre
// com OOMKilled, um que so passa do request continua rodando.
type ResourceSpec struct {
	CPU    string `yaml:"cpu"`
	Memory string `yaml:"memory"`
	Limits struct {
		CPU    string `yaml:"cpu"`
		Memory string `yaml:"memory"`
	} `yaml:"limits"`
}

func (r ResourceSpec) dominio() dominio.Resources {
	return dominio.Resources{
		CPU: r.CPU, Memory: r.Memory,
		CPULimit: r.Limits.CPU, MemoryLimit: r.Limits.Memory,
	}
}

// ParamSpec e um parametro de execucao como escrito no arquivo.
//
//	params:
//	  - name: load_full
//	    type: boolean
//	    default: "false"
//	  - name: start_date
//	    type: string
//	    pattern: '^\d{4}-\d{2}-\d{2}$'
type ParamSpec struct {
	Name        string   `yaml:"name"`
	Type        string   `yaml:"type"`
	Default     string   `yaml:"default"`
	Description string   `yaml:"description"`
	Enum        []string `yaml:"enum"`
	Pattern     string   `yaml:"pattern"`
}

func (p ParamSpec) dominio() dominio.Param {
	tipo := dominio.TipoParam(strings.TrimSpace(p.Type))
	if tipo == "" {
		// `string` como padrao: e o tipo mais comum e o unico que nao muda o
		// significado do valor. Exigir a chave em todo param seria ruido.
		tipo = dominio.ParamTexto
	}
	return dominio.Param{
		Nome: strings.TrimSpace(p.Name), Tipo: tipo,
		Padrao: p.Default, Descricao: p.Description,
		Enum: p.Enum, Pattern: p.Pattern,
	}
}

// StepSpec e um passo como escrito no arquivo.
type StepSpec struct {
	ID        string         `yaml:"id"`
	Run       string         `yaml:"run"`
	Action    string         `yaml:"action"`
	With      map[string]any `yaml:"with"`
	DependsOn []string       `yaml:"depends_on"`

	// Image e Resources sobrescrevem os do workflow. Ausentes = herda.
	Image     string       `yaml:"image"`
	Resources ResourceSpec `yaml:"resources"`

	// Shell: ponteiro para distinguir "nao declarou" de "declarou false". Sem o
	// ponteiro, todo passo sem a chave viraria `shell: false` e as imagens com
	// shell — a maioria — passariam a receber argv, quebrando qualquer comando
	// com pipe ou variavel.
	Shell *bool `yaml:"shell"`
}

// Parse le o YAML e devolve o workflow ja validado.
//
// `caminho` serve para dois fins: derivar o slug quando o arquivo nao traz
// `name`, e citar o arquivo nas mensagens de erro — um erro de grafo sem o nome
// do arquivo e inutil quando ha dezenas deles.
func Parse(caminho string, conteudo []byte) (dominio.Workflow, error) {
	var s Spec
	if err := yaml.Unmarshal(conteudo, &s); err != nil {
		return dominio.Workflow{}, fmt.Errorf("%s: yaml invalido: %w", caminho, err)
	}

	slug := s.Name
	if slug == "" {
		slug = strings.TrimSuffix(filepath.Base(caminho), filepath.Ext(caminho))
	}

	kind, err := parseKind(s.Type)
	if err != nil {
		return dominio.Workflow{}, fmt.Errorf("%s: %w", caminho, err)
	}

	w := dominio.Workflow{
		Slug:     slug,
		Name:     slug,
		Kind:     kind,
		Schedule: strings.TrimSpace(s.Schedule),
		Tags:     normalizarTags(s.Tags),

		// A imagem do workflow e o runtime padrao dos passos: em Kubernetes cada
		// passo vira um pod, e e ela que decide o que aquele pod sabe fazer.
		Image:     strings.TrimSpace(s.Image),
		Resources: s.Resources.dominio(),
		MaxAtivos: s.Concurrency,
	}
	for _, ps := range s.Params {
		w.Params = append(w.Params, ps.dominio())
	}
	for _, st := range s.Steps {
		w.Nodes = append(w.Nodes, dominio.Node{
			ID: st.ID, Run: st.Run, Action: st.Action, With: st.With,
			Image: strings.TrimSpace(st.Image), Resources: st.Resources.dominio(),
			Shell: st.Shell,
		})
	}

	w.Edges, err = arestas(kind, s.Steps)
	if err != nil {
		return dominio.Workflow{}, fmt.Errorf("%s: %w", caminho, err)
	}

	if err := w.Validate(); err != nil {
		return dominio.Workflow{}, fmt.Errorf("%s: %w", caminho, err)
	}
	return w, nil
}

// normalizarTags apara espacos, descarta vazias e deduplica preservando a ordem
// do arquivo. Sem isso, `tags: [dbt, dbt , ""]` viraria tres chips na tela, dois
// deles iguais e um em branco.
func normalizarTags(brutas []string) []string {
	if len(brutas) == 0 {
		return nil
	}
	vistas := make(map[string]struct{}, len(brutas))
	out := make([]string, 0, len(brutas))
	for _, t := range brutas {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if _, ja := vistas[t]; ja {
			continue
		}
		vistas[t] = struct{}{}
		out = append(out, t)
	}
	return out
}

// arestas transforma a declaracao em grafo.
//
// `chain` e acucar: cada passo depende do anterior. Convertendo aqui, o motor de
// execucao conhece apenas DAG — um formato a mais no arquivo, zero caminho a
// mais no runtime.
func arestas(kind dominio.Kind, steps []StepSpec) ([]dominio.Edge, error) {
	var out []dominio.Edge

	switch kind {
	case dominio.KindChain:
		for _, st := range steps {
			if len(st.DependsOn) > 0 {
				return nil, fmt.Errorf("step %q usa `depends_on` num workflow `chain`; "+
					"em chain a ordem e a do arquivo — use `type: dag` para declarar dependencias", st.ID)
			}
		}
		for i := 1; i < len(steps); i++ {
			out = append(out, dominio.Edge{From: steps[i-1].ID, To: steps[i].ID})
		}

	case dominio.KindDAG:
		for _, st := range steps {
			for _, dep := range st.DependsOn {
				out = append(out, dominio.Edge{From: dep, To: st.ID})
			}
		}
	}
	return out, nil
}

func parseKind(t string) (dominio.Kind, error) {
	switch strings.TrimSpace(t) {
	case "", string(dominio.KindDAG):
		// Sem `type`, assume DAG: e o modelo geral, e um arquivo sem dependencias
		// declaradas vira um grafo de nos soltos, que roda em paralelo. `chain`
		// precisa ser pedido, porque impoe ordem.
		return dominio.KindDAG, nil
	case string(dominio.KindChain):
		return dominio.KindChain, nil
	default:
		return "", fmt.Errorf("type %q desconhecido (use `chain` ou `dag`)", t)
	}
}
