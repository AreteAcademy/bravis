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

	dominio "github.com/zarvhq/bravis/internal/domain/workflow"
)

// Spec espelha o YAML, e so isso. Campos frouxos aqui, invariantes no dominio.
type Spec struct {
	Name     string     `yaml:"name"`
	Schedule string     `yaml:"schedule"`
	Type     string     `yaml:"type"`
	Steps    []StepSpec `yaml:"steps"`
}

// StepSpec e um passo como escrito no arquivo.
type StepSpec struct {
	ID        string         `yaml:"id"`
	Run       string         `yaml:"run"`
	Action    string         `yaml:"action"`
	With      map[string]any `yaml:"with"`
	DependsOn []string       `yaml:"depends_on"`
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
	}
	for _, st := range s.Steps {
		w.Nodes = append(w.Nodes, dominio.Node{
			ID: st.ID, Run: st.Run, Action: st.Action, With: st.With,
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
