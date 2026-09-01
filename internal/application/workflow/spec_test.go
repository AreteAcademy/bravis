package workflow

import (
	"os"
	"testing"

	dominio "github.com/zarvhq/bravis/internal/domain/workflow"
)

// O exemplo que o autor pediu tem de passar exatamente como escrito.
func TestParseExemploDailyReport(t *testing.T) {
	b, err := os.ReadFile("../../../examples/daily-report.yaml")
	if err != nil {
		t.Fatal(err)
	}
	w, err := Parse("daily-report.yaml", b)
	if err != nil {
		t.Fatal(err)
	}

	if w.Slug != "daily-report" {
		t.Errorf("slug = %q; sem `name`, deve vir do nome do arquivo", w.Slug)
	}
	if w.Schedule != "0 2 * * *" {
		t.Errorf("schedule = %q", w.Schedule)
	}
	if len(w.Nodes) != 3 {
		t.Fatalf("nodes = %d, queria 3", len(w.Nodes))
	}
	// chain vira arestas: o motor so ve DAG
	if len(w.Edges) != 2 {
		t.Fatalf("edges = %d, queria 2 (chain de 3 passos)", len(w.Edges))
	}
	if w.Edges[0] != (dominio.Edge{From: "fetch_data", To: "build_report"}) {
		t.Errorf("primeira aresta = %+v", w.Edges[0])
	}

	docker := w.Nodes[1]
	if docker.Action != "docker.run" {
		t.Errorf("action = %q", docker.Action)
	}
	if docker.With["image"] != "ghcr.io/acme/reporting:1.4.2" {
		t.Errorf("with.image = %v", docker.With["image"])
	}
}

// Fan-out e fan-in: o mesmo motor, com dependencias declaradas.
func TestParseDAGComParalelismo(t *testing.T) {
	b, err := os.ReadFile("../../../examples/analytics-dag.yaml")
	if err != nil {
		t.Fatal(err)
	}
	w, err := Parse("analytics-dag.yaml", b)
	if err != nil {
		t.Fatal(err)
	}
	if w.Slug != "daily_analytics" {
		t.Errorf("slug = %q; deve vir do campo `name`", w.Slug)
	}
	if len(w.Edges) != 5 {
		t.Errorf("edges = %d, queria 5", len(w.Edges))
	}
}

func TestParseRecusaDependsOnEmChain(t *testing.T) {
	y := []byte("type: chain\nsteps:\n  - id: a\n    run: echo\n  - id: b\n    run: echo\n    depends_on: [a]\n")
	_, err := Parse("x.yaml", y)
	if err == nil {
		t.Fatal("esperava erro: em chain a ordem e a do arquivo")
	}
}

func TestParseRecusaTypeDesconhecido(t *testing.T) {
	y := []byte("type: pipeline\nsteps:\n  - id: a\n    run: echo\n")
	if _, err := Parse("x.yaml", y); err == nil {
		t.Fatal("esperava erro de type desconhecido")
	}
}

// Sem `type`, assume DAG: sem depends_on os passos ficam soltos e rodam em
// paralelo. `chain` impoe ordem e por isso precisa ser pedido.
func TestParseSemTypeAssumeDAG(t *testing.T) {
	y := []byte("steps:\n  - id: a\n    run: echo\n  - id: b\n    run: echo\n")
	w, err := Parse("x.yaml", y)
	if err != nil {
		t.Fatal(err)
	}
	if w.Kind != dominio.KindDAG {
		t.Errorf("kind = %q, queria dag", w.Kind)
	}
	if len(w.Edges) != 0 {
		t.Errorf("edges = %d; sem depends_on os passos sao independentes", len(w.Edges))
	}
}

func TestParseCitaOArquivoNoErro(t *testing.T) {
	y := []byte("type: chain\nsteps:\n  - id: a\n")
	_, err := Parse("relatorio.yaml", y)
	if err == nil {
		t.Fatal("esperava erro")
	}
	if got := err.Error(); len(got) < 15 || got[:14] != "relatorio.yaml" {
		t.Errorf("erro = %q; deve comecar pelo arquivo", got)
	}
}
