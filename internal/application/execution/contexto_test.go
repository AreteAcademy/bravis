package execution_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	app "github.com/AreteAcademy/bravis/internal/application/execution"
	wf "github.com/AreteAcademy/bravis/internal/domain/workflow"
	"github.com/AreteAcademy/bravis/internal/execution"
)

// capturador guarda a TaskExec que o runner montou, para conferir o ambiente
// que chega ao passo.
type capturador struct {
	tarefa execution.TaskExec
}

func (c *capturador) Name() string { return "capturador" }

func (c *capturador) Cancel(ctx context.Context, execID string) error { return nil }

func (c *capturador) Execute(ctx context.Context, t execution.TaskExec) (<-chan execution.Event, error) {
	c.tarefa = t
	ch := make(chan execution.Event, 2)
	ch <- execution.Event{Kind: execution.EventStarted}
	ch <- execution.Event{Kind: execution.EventSucceeded}
	close(ch)
	return ch, nil
}

// historico responde o que o teste mandar.
type historico struct {
	jaTeve bool
	err    error
	slug   string
	nodeID string
	exceto uuid.UUID
}

func (h *historico) PassoJaTeveSucesso(ctx context.Context, slug, nodeID string, exceto uuid.UUID) (bool, error) {
	h.slug, h.nodeID, h.exceto = slug, nodeID, exceto
	return h.jaTeve, h.err
}

func workflowDeUmPasso() wf.Workflow {
	return wf.Workflow{
		Slug:  "clima",
		Nodes: []wf.Node{{ID: "coletar", Run: "true"}},
	}
}

func rodar(t *testing.T, r app.Runner) execution.TaskExec {
	t.Helper()
	cap := &capturador{}
	r.Processo = cap
	if err := r.Run(context.Background(), workflowDeUmPasso()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	return cap.tarefa
}

func TestAmbienteDoPassoCarregaOContextoDoRun(t *testing.T) {
	id := uuid.New()
	quando := time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)

	tarefa := rodar(t, app.Runner{
		RunID:       id,
		Params:      map[string]string{"load_full": "true"},
		Trigger:     "backfill",
		LogicalDate: &quando,
		Historico:   &historico{jaTeve: false},
	})

	if tarefa.Env["BRAVIS_RUN_ID"] != id.String() {
		t.Errorf("BRAVIS_RUN_ID = %q", tarefa.Env["BRAVIS_RUN_ID"])
	}
	if tarefa.Env["BRAVIS_RUN_FIRST"] != "true" {
		t.Errorf("sem sucesso anterior, o passo roda pela primeira vez: %q", tarefa.Env["BRAVIS_RUN_FIRST"])
	}
	if tarefa.Env["BRAVIS_RUN_TRIGGER"] != "backfill" {
		t.Errorf("BRAVIS_RUN_TRIGGER = %q", tarefa.Env["BRAVIS_RUN_TRIGGER"])
	}
	if tarefa.Env["BRAVIS_RUN_LOGICAL_DATE"] != "2026-09-03T00:00:00Z" {
		t.Errorf("BRAVIS_RUN_LOGICAL_DATE = %q", tarefa.Env["BRAVIS_RUN_LOGICAL_DATE"])
	}

	var params map[string]string
	if err := json.Unmarshal([]byte(tarefa.Env["BRAVIS_RUN_PARAMS"]), &params); err != nil {
		t.Fatalf("BRAVIS_RUN_PARAMS nao e JSON: %v", err)
	}
	if params["load_full"] != "true" {
		t.Errorf("params = %v", params)
	}
}

func TestPrimeiraExecucaoEPorPassoNaoPorWorkflow(t *testing.T) {
	// Um workflow com tres fetchers escrevendo em tres tabelas criaria apenas
	// a do primeiro passo se a pergunta fosse do workflow inteiro.
	h := &historico{jaTeve: false}
	id := uuid.New()

	rodar(t, app.Runner{RunID: id, Historico: h})

	if h.slug != "clima" || h.nodeID != "coletar" {
		t.Errorf("a pergunta tem de ser por (workflow, passo): %q / %q", h.slug, h.nodeID)
	}
	// O proprio run corrente nao pode contar como sucesso anterior.
	if h.exceto != id {
		t.Errorf("o run corrente tem de ser excluido da consulta: %v", h.exceto)
	}
}

func TestPassoComSucessoAnteriorNaoEPrimeiro(t *testing.T) {
	tarefa := rodar(t, app.Runner{RunID: uuid.New(), Historico: &historico{jaTeve: true}})

	if tarefa.Env["BRAVIS_RUN_FIRST"] != "false" {
		t.Errorf("ja houve sucesso, entao nao e a primeira: %q", tarefa.Env["BRAVIS_RUN_FIRST"])
	}
}

func TestSemHistoricoNaoInventaPrimeiraExecucao(t *testing.T) {
	// Criar tabela sem certeza e pior que nao criar: quem quiser pede
	// explicitamente no codigo do fetcher.
	tarefa := rodar(t, app.Runner{RunID: uuid.New()})

	if tarefa.Env["BRAVIS_RUN_FIRST"] != "false" {
		t.Errorf("sem historico configurado a resposta e nao: %q", tarefa.Env["BRAVIS_RUN_FIRST"])
	}
}

func TestFalhaNaConsultaNaoViraCriacaoDeTabela(t *testing.T) {
	h := &historico{err: context.DeadlineExceeded}
	tarefa := rodar(t, app.Runner{RunID: uuid.New(), Historico: h})

	if tarefa.Env["BRAVIS_RUN_FIRST"] != "false" {
		t.Errorf("banco fora do ar nao pode virar DDL: %q", tarefa.Env["BRAVIS_RUN_FIRST"])
	}
}

func TestAmbienteDoRunnerGanhaEmColisao(t *testing.T) {
	// Se alguem definiu a variavel na configuracao do runner, foi porque quis.
	tarefa := rodar(t, app.Runner{
		RunID:     uuid.New(),
		Historico: &historico{jaTeve: false},
		Env:       map[string]string{"BRAVIS_RUN_FIRST": "false", "OUTRA": "coisa"},
	})

	if tarefa.Env["BRAVIS_RUN_FIRST"] != "false" {
		t.Error("configuracao explicita do runner tem de vencer o valor calculado")
	}
	if tarefa.Env["OUTRA"] != "coisa" {
		t.Error("o resto do ambiente do runner tem de sobreviver")
	}
	if tarefa.Env["BRAVIS_RUN_ID"] == "" {
		t.Error("as variaveis sem colisao continuam chegando")
	}
}

func TestSemParamsNaoInjetaVariavelVazia(t *testing.T) {
	tarefa := rodar(t, app.Runner{RunID: uuid.New(), Historico: &historico{}})

	if _, existe := tarefa.Env["BRAVIS_RUN_PARAMS"]; existe {
		t.Error("sem params, a variavel nao deve existir em vez de existir vazia")
	}
	if _, existe := tarefa.Env["BRAVIS_RUN_TRIGGER"]; existe {
		t.Error("sem trigger, idem")
	}
}
