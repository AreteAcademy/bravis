package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/zarvhq/bravis/internal/api"
	"github.com/zarvhq/bravis/internal/branding"
	dom "github.com/zarvhq/bravis/internal/domain/run"
	wf "github.com/zarvhq/bravis/internal/domain/workflow"
	"github.com/zarvhq/bravis/internal/infrastructure/postgres"
)

// Grafo em diamante: b e c dependem de a, d depende dos dois. O formato importa
// porque e o menor caso em que layout errado aparece — b e c TEM que sair na
// mesma coluna, ou o desenho contradiz o que o executor faz.
func diamante() wf.Workflow {
	return wf.Workflow{
		Slug: "diamante",
		Nodes: []wf.Node{
			{ID: "a", Run: "echo a"}, {ID: "b", Run: "echo b"},
			{ID: "c", Action: "docker.run"}, {ID: "d", Run: "echo d"},
		},
		Edges: []wf.Edge{{From: "a", To: "b"}, {From: "a", To: "c"},
			{From: "b", To: "d"}, {From: "c", To: "d"}},
	}
}

type defsFake struct {
	w   wf.Workflow
	err error
}

func (d defsFake) Definicao(context.Context, string) (wf.Workflow, error) { return d.w, d.err }

type execsFake struct {
	run     dom.Run
	estados map[string]postgres.EstadoNo
	err     error
}

func (e execsFake) Buscar(context.Context, uuid.UUID) (dom.Run, error) { return e.run, e.err }
func (e execsFake) LogsDaRun(context.Context, uuid.UUID) ([]postgres.LogDoPasso, error) {
	return nil, nil
}

func (e execsFake) EstadoDosNos(context.Context, uuid.UUID) (map[string]postgres.EstadoNo, error) {
	return e.estados, nil
}

type grafo struct {
	Slug     string `json:"slug"`
	RunID    string `json:"run_id"`
	Status   string `json:"status"`
	Terminal bool   `json:"terminal"`
	Nodes    []struct {
		ID       string             `json:"id"`
		Type     string             `json:"type"`
		Position struct{ X, Y int } `json:"position"`
		Data     map[string]any     `json:"data"`
	} `json:"nodes"`
	Edges []struct {
		ID       string `json:"id"`
		Source   string `json:"source"`
		Target   string `json:"target"`
		Animated bool   `json:"animated"`
	} `json:"edges"`
}

func pedir(t *testing.T, ui *api.UI, caminho string) (*http.Response, grafo) {
	t.Helper()
	mux := http.NewServeMux()
	ui.Registrar(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, caminho, nil))

	res := rec.Result()
	var g grafo
	corpo, _ := io.ReadAll(res.Body)
	if res.StatusCode == http.StatusOK {
		if err := json.Unmarshal(corpo, &g); err != nil {
			t.Fatalf("json invalido: %v — %s", err, corpo)
		}
	}
	return res, g
}

func novaUI(d api.Definicoes, e api.Execucoes) *api.UI {
	return api.NewUI(nil, d, e, nil, branding.Padrao(), slog.New(slog.DiscardHandler))
}

func TestGrafoDoWorkflowPoeNiveisEmColunas(t *testing.T) {
	ui := novaUI(defsFake{w: diamante()}, execsFake{})
	res, g := pedir(t, ui, "/api/workflows/diamante/graph")

	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, quero 200", res.StatusCode)
	}
	if len(g.Nodes) != 4 || len(g.Edges) != 4 {
		t.Fatalf("nodes=%d edges=%d, quero 4 e 4", len(g.Nodes), len(g.Edges))
	}

	x := map[string]int{}
	y := map[string]int{}
	for _, n := range g.Nodes {
		x[n.ID], y[n.ID] = n.Position.X, n.Position.Y
		if n.Type != "bravis" {
			t.Errorf("no %s com type %q, quero bravis (o custom node)", n.ID, n.Type)
		}
	}
	if !(x["a"] < x["b"] && x["b"] < x["d"]) {
		t.Errorf("colunas fora de ordem: a=%d b=%d d=%d", x["a"], x["b"], x["d"])
	}
	if x["b"] != x["c"] {
		t.Errorf("b e c rodam em paralelo mas sairam em colunas diferentes: %d e %d", x["b"], x["c"])
	}
	if y["b"] == y["c"] {
		t.Errorf("b e c sairam sobrepostos em y=%d", y["b"])
	}
	if y["a"] != y["d"] {
		t.Errorf("niveis de um no so deviam ficar centrados: a=%d d=%d", y["a"], y["d"])
	}
}

// Sem execucao, todo no e "pending" — a tela de um workflow que nunca rodou nao
// pode herdar estado de lugar nenhum.
func TestGrafoDoWorkflowNaoTemEstado(t *testing.T) {
	ui := novaUI(defsFake{w: diamante()}, execsFake{})
	_, g := pedir(t, ui, "/api/workflows/diamante/graph")

	for _, n := range g.Nodes {
		if n.Data["status"] != "pending" {
			t.Errorf("no %s com status %v, quero pending", n.ID, n.Data["status"])
		}
	}
	if g.RunID != "" {
		t.Errorf("run_id = %q num grafo sem execucao", g.RunID)
	}
	if g.Nodes[0].Data["acao"] == nil {
		t.Error("o card perdeu o rotulo da acao")
	}
}

func TestGrafoDaRunAplicaEstadoPorNo(t *testing.T) {
	id := uuid.New()
	def, _ := json.Marshal(diamante())
	saida := 2
	ui := novaUI(defsFake{err: errors.New("nao deve consultar a definicao publicada")}, execsFake{
		run: dom.Run{ID: id, WorkflowSlug: "diamante", Status: dom.StatusRunning, Definicao: def},
		estados: map[string]postgres.EstadoNo{
			"a": {NodeID: "a", Status: "success", DuracaoMs: 1200},
			"b": {NodeID: "b", Status: "running"},
			"c": {NodeID: "c", Status: "failed", Tentativa: 2, ExitCode: &saida, Erro: "boom"},
		},
	})

	res, g := pedir(t, ui, "/api/runs/"+id.String()+"/graph")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, quero 200", res.StatusCode)
	}
	if g.RunID != id.String() || g.Status != "running" {
		t.Fatalf("cabecalho errado: run=%q status=%q", g.RunID, g.Status)
	}
	if g.Terminal {
		t.Error("run em execucao marcada como terminal: a UI pararia de atualizar")
	}

	porID := map[string]map[string]any{}
	for _, n := range g.Nodes {
		porID[n.ID] = n.Data
	}
	if porID["a"]["status"] != "success" || porID["a"]["duracao_ms"] != float64(1200) {
		t.Errorf("no a: %v", porID["a"])
	}
	if porID["c"]["erro"] != "boom" || porID["c"]["exit_code"] != float64(2) {
		t.Errorf("no c perdeu erro/exit code: %v", porID["c"])
	}
	// d nunca rodou; tem que continuar cinza em vez de herdar o estado do pai.
	if porID["d"]["status"] != "pending" {
		t.Errorf("no d: %v", porID["d"])
	}

	for _, e := range g.Edges {
		if e.Target == "b" && !e.Animated {
			t.Error("aresta que chega no no em execucao devia estar animada")
		}
		if e.Target == "d" && e.Animated {
			t.Error("aresta para no parado nao deve animar")
		}
	}
}

// Grafo terminal precisa dizer isso no JSON: e o sinal que faz a ilha parar de
// consultar. Sem ele, cada run concluida deixa um poll eterno de 2 em 2s.
func TestGrafoDaRunMarcaTerminal(t *testing.T) {
	id := uuid.New()
	def, _ := json.Marshal(diamante())
	ui := novaUI(defsFake{}, execsFake{
		run: dom.Run{ID: id, Status: dom.StatusSuccess, Definicao: def},
	})
	_, g := pedir(t, ui, "/api/runs/"+id.String()+"/graph")
	if !g.Terminal {
		t.Error("run em success nao foi marcada como terminal")
	}
}

func TestGrafoRecusaEntradasInvalidas(t *testing.T) {
	casos := []struct {
		nome     string
		ui       *api.UI
		caminho  string
		esperado int
	}{
		{"workflow inexistente", novaUI(defsFake{err: errors.New("sem linhas")}, execsFake{}),
			"/api/workflows/fantasma/graph", http.StatusNotFound},
		{"uuid malformado", novaUI(defsFake{}, execsFake{}),
			"/api/runs/nao-e-uuid/graph", http.StatusBadRequest},
		{"run inexistente", novaUI(defsFake{}, execsFake{err: errors.New("sem linhas")}),
			"/api/runs/" + uuid.New().String() + "/graph", http.StatusNotFound},
		{"ciclo gravado no banco", novaUI(defsFake{w: wf.Workflow{
			Slug:  "ciclo",
			Nodes: []wf.Node{{ID: "a", Run: "x"}, {ID: "b", Run: "y"}},
			Edges: []wf.Edge{{From: "a", To: "b"}, {From: "b", To: "a"}},
		}}, execsFake{}), "/api/workflows/ciclo/graph", http.StatusUnprocessableEntity},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			res, _ := pedir(t, c.ui, c.caminho)
			if res.StatusCode != c.esperado {
				t.Errorf("status = %d, quero %d", res.StatusCode, c.esperado)
			}
		})
	}
}
