// Package workflow e o modelo de dominio de um fluxo e seu grafo.
//
// O dominio nao sabe o que e YAML. A traducao do arquivo para estas structs
// vive em internal/application/workflow — assim o formato de arquivo pode mudar
// sem tocar as invariantes.
package workflow

import "fmt"

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
	Nodes    []Node
	Edges    []Edge
}

// Node e uma unidade de trabalho. Exatamente uma forma de execucao deve estar
// preenchida — `Run` (comando) ou `Action` (acao tipada com parametros).
type Node struct {
	ID     string
	Run    string
	Action string
	With   map[string]any
}

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
