// Package workflow e o modelo de dominio de um fluxo e seu grafo.
//
// O dominio nao sabe o que e YAML. A traducao do arquivo para estas structs
// vive em internal/application/workflow — assim o formato de arquivo pode mudar
// sem tocar as invariantes.
package workflow

import (
	"fmt"
	"regexp"
)

// Kind distingue como o grafo foi declarado. `chain` e acucar sintatico: o
// parser o converte em arestas antes de chegar aqui, entao o motor de execucao
// so conhece DAG. Um motor so, dois jeitos de escrever.
type Kind string

const (
	KindChain Kind = "chain"
	KindDAG   Kind = "dag"
)

// Workflow e a definicao de um fluxo. Imutavel depois de publicado: a secao 22
// do plano exige que uma Run guarde o snapshot da versao que a originou.
type Workflow struct {
	Slug     string
	Name     string
	Kind     Kind
	Schedule string // cron; vazio = so disparo manual

	// Tags classificam o workflow para busca e filtro na UI. Sao rotulos livres
	// do autor do YAML, nao dominio: nada no motor depende delas.
	Tags []string

	// Image e o runtime padrao dos passos. Em Kubernetes CADA passo vira um pod,
	// e a imagem e o que define o que aquele pod sabe fazer: um passo de dbt
	// sobe a imagem de dbt, um binario Go sobe uma imagem de 10 MB. Declarar
	// aqui evita repetir a mesma linha em dez passos; o passo sobrescreve quando
	// precisa de outro runtime.
	Image string

	// Resources e o pedido padrao de CPU e memoria, pela mesma razao.
	Resources Resources

	// Params sao os valores que mudam entre dois disparos do mesmo workflow —
	// `load_full`, uma janela de datas, um limite. Ver param.go.
	Params []Param

	// MaxAtivos limita execucoes simultaneas DESTE workflow. Zero = sem limite.
	//
	// E diferente do teto global de passos: aquele protege o CLUSTER, este
	// protege o DADO. Um `*/15` que leva 20 minutos se sobrepoe a si mesmo, e
	// dois `dbt build` no mesmo modelo ao mesmo tempo disputam a mesma tabela.
	MaxAtivos int

	Nodes []Node
	Edges []Edge
}

// Node e uma unidade de trabalho. Exatamente uma forma de execucao deve estar
// preenchida — `Run` (comando) ou `Action` (acao tipada com parametros).
type Node struct {
	ID     string
	Run    string
	Action string
	With   map[string]any

	// Image sobrescreve a do workflow. Vazio = herda.
	Image string

	// Resources dimensiona o pod deste passo. O ganho de separar por passo e
	// concreto: um fetcher em Go cabe em 64Mi enquanto o dbt ao lado pede 1Gi, e
	// numa imagem unica os dois pagariam o maior dos dois.
	Resources Resources

	// Shell decide como o comando entra no container. Nulo = com shell, que e o
	// que `run:` sugere ("python fetch.py"). Falso passa o argv direto, para
	// imagem distroless — onde `sh -c` falharia com "no such file or directory",
	// erro que nao diz nada sobre a causa.
	Shell *bool
}

// Resources sao pedidos e limites de um pod, no formato do Kubernetes
// ("200m", "1Gi"). Texto e nao numero de proposito: o formato e do Kubernetes, e
// converter para uma unidade nossa so criaria um segundo vocabulario para a
// mesma coisa.
type Resources struct {
	CPU         string
	Memory      string
	CPULimit    string
	MemoryLimit string
}

// Vazio diz se nada foi declarado — o pod entao sobe sem `resources`, herdando
// o LimitRange do namespace.
func (r Resources) Vazio() bool {
	return r.CPU == "" && r.Memory == "" && r.CPULimit == "" && r.MemoryLimit == ""
}

// ComPadrao preenche o que o passo nao declarou com o do workflow. Herdar campo
// a campo, e nao o bloco inteiro, permite um passo pedir so mais memoria sem
// perder a CPU do padrao.
func (r Resources) ComPadrao(p Resources) Resources {
	if r.CPU == "" {
		r.CPU = p.CPU
	}
	if r.Memory == "" {
		r.Memory = p.Memory
	}
	if r.CPULimit == "" {
		r.CPULimit = p.CPULimit
	}
	if r.MemoryLimit == "" {
		r.MemoryLimit = p.MemoryLimit
	}
	return r
}

// ImagemDe resolve a imagem efetiva de um passo.
func (w Workflow) ImagemDe(n Node) string {
	if n.Image != "" {
		return n.Image
	}
	return w.Image
}

// RecursosDe resolve os recursos efetivos de um passo.
func (w Workflow) RecursosDe(n Node) Resources {
	return n.Resources.ComPadrao(w.Resources)
}

// UsaShell diz se o comando entra por `sh -c`.
func (n Node) UsaShell() bool { return n.Shell == nil || *n.Shell }

// Edge liga dois nos: From roda antes de To.
type Edge struct {
	From string
	To   string
}

// Validate aplica as invariantes que a secao 5 do plano exige antes de salvar.
// A ordem importa: IDs duplicados e dependencias ausentes sao verificados antes
// do ciclo, porque um grafo com aresta pendurada nao pode ser percorrido.
func (w Workflow) Validate() error {
	if w.Slug == "" {
		return fmt.Errorf("workflow sem slug")
	}
	if len(w.Nodes) == 0 {
		return fmt.Errorf("workflow %q nao tem nenhum step", w.Slug)
	}

	vistos := make(map[string]struct{}, len(w.Nodes))
	for _, n := range w.Nodes {
		if n.ID == "" {
			return fmt.Errorf("workflow %q tem step sem id", w.Slug)
		}
		if _, dup := vistos[n.ID]; dup {
			return fmt.Errorf("workflow %q: id de step duplicado: %q", w.Slug, n.ID)
		}
		vistos[n.ID] = struct{}{}

		temRun, temAction := n.Run != "", n.Action != ""
		switch {
		case temRun && temAction:
			return fmt.Errorf("step %q declara `run` e `action`; use um dos dois", n.ID)
		case !temRun && !temAction:
			return fmt.Errorf("step %q nao declara nem `run` nem `action`", n.ID)
		case temRun && len(n.With) > 0:
			return fmt.Errorf("step %q usa `with`, que so vale com `action`", n.ID)
		}
	}

	if w.MaxAtivos < 0 {
		return fmt.Errorf("workflow %q: concurrency negativa (%d); use 0 para sem limite", w.Slug, w.MaxAtivos)
	}
	if err := validarRecursos(w.Slug, "workflow", w.Resources); err != nil {
		return err
	}
	for _, n := range w.Nodes {
		if err := validarRecursos(w.Slug, "step "+n.ID, n.Resources); err != nil {
			return err
		}
	}

	for _, e := range w.Edges {
		if _, ok := vistos[e.From]; !ok {
			return fmt.Errorf("workflow %q: dependencia inexistente %q", w.Slug, e.From)
		}
		if _, ok := vistos[e.To]; !ok {
			return fmt.Errorf("workflow %q: dependencia inexistente %q", w.Slug, e.To)
		}
		if e.From == e.To {
			return fmt.Errorf("step %q depende de si mesmo", e.From)
		}
	}

	if w.MaxAtivos < 0 {
		return fmt.Errorf("workflow %q: concurrency negativa (%d); use 0 para sem limite", w.Slug, w.MaxAtivos)
	}
	if err := validarRecursos(w.Slug, "workflow", w.Resources); err != nil {
		return err
	}
	vistosParams := make(map[string]struct{}, len(w.Params))
	for _, p := range w.Params {
		if err := p.Validar(); err != nil {
			return fmt.Errorf("workflow %q: %w", w.Slug, err)
		}
		if _, ja := vistosParams[p.Nome]; ja {
			return fmt.Errorf("workflow %q: param duplicado: %q", w.Slug, p.Nome)
		}
		vistosParams[p.Nome] = struct{}{}
	}
	for _, n := range w.Nodes {
		if err := validarRecursos(w.Slug, "step "+n.ID, n.Resources); err != nil {
			return err
		}
	}

	if ciclo := w.encontrarCiclo(); ciclo != "" {
		return fmt.Errorf("workflow %q tem ciclo: %s", w.Slug, ciclo)
	}
	return nil
}

// encontrarCiclo devolve o caminho do ciclo, ou vazio se o grafo for aciclico.
//
// Devolve o CAMINHO, nao apenas um booleano: quem escreveu a DAG precisa saber
// quais steps fecham o laco para corrigi-lo.
func (w Workflow) encontrarCiclo() string {
	saida := make(map[string][]string, len(w.Nodes))
	for _, e := range w.Edges {
		saida[e.From] = append(saida[e.From], e.To)
	}

	const (
		novo = iota // sem iota, emUso e pronto repetiriam o valor de novo
		emUso
		pronto
	)
	estado := make(map[string]int, len(w.Nodes))
	var caminho []string
	var achado string

	var visitar func(string) bool
	visitar = func(id string) bool {
		estado[id] = emUso
		caminho = append(caminho, id)

		for _, prox := range saida[id] {
			switch estado[prox] {
			case emUso:
				// fecha o laco: recorta o caminho a partir de onde `prox` entrou
				for i, v := range caminho {
					if v == prox {
						achado = formatarCiclo(append(append([]string{}, caminho[i:]...), prox))
						return true
					}
				}
			case novo:
				if visitar(prox) {
					return true
				}
			}
		}

		caminho = caminho[:len(caminho)-1]
		estado[id] = pronto
		return false
	}

	for _, n := range w.Nodes {
		if estado[n.ID] == novo && visitar(n.ID) {
			return achado
		}
	}
	return ""
}

func formatarCiclo(ids []string) string {
	s := ""
	for i, id := range ids {
		if i > 0 {
			s += " -> "
		}
		s += id
	}
	return s
}

// quantidade e o formato do Kubernetes: inteiro ou decimal com sufixo opcional
// (m para CPU; Ki/Mi/Gi/K/M/G para memoria).
var quantidade = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?(m|[KMGTPE]i?)?$`)

// validarRecursos recusa quantidade malformada na PUBLICACAO.
//
// Sem isto o erro so aparece quando o pod e criado — horas depois, no meio da
// madrugada, como um 422 do servidor de API que nao cita o arquivo nem o passo.
func validarRecursos(slug, onde string, r Resources) error {
	for campo, valor := range map[string]string{
		"cpu": r.CPU, "memory": r.Memory,
		"cpu_limit": r.CPULimit, "memory_limit": r.MemoryLimit,
	} {
		if valor == "" {
			continue
		}
		if !quantidade.MatchString(valor) {
			return fmt.Errorf("workflow %q, %s: %s=%q nao e uma quantidade valida (ex.: 200m, 1, 512Mi, 2Gi)",
				slug, onde, campo, valor)
		}
	}
	return nil
}
