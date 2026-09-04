package execution_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	app "github.com/AreteAcademy/brevis/internal/application/execution"
	wf "github.com/AreteAcademy/brevis/internal/domain/workflow"
	"github.com/AreteAcademy/brevis/internal/execution"
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

	if tarefa.Env["BREVIS_RUN_ID"] != id.String() {
		t.Errorf("BREVIS_RUN_ID = %q", tarefa.Env["BREVIS_RUN_ID"])
	}
	if tarefa.Env["BREVIS_RUN_FIRST"] != "true" {
		t.Errorf("sem sucesso anterior, o passo roda pela primeira vez: %q", tarefa.Env["BREVIS_RUN_FIRST"])
	}
	if tarefa.Env["BREVIS_RUN_TRIGGER"] != "backfill" {
		t.Errorf("BREVIS_RUN_TRIGGER = %q", tarefa.Env["BREVIS_RUN_TRIGGER"])
	}
	if tarefa.Env["BREVIS_RUN_LOGICAL_DATE"] != "2026-09-03T00:00:00Z" {
		t.Errorf("BREVIS_RUN_LOGICAL_DATE = %q", tarefa.Env["BREVIS_RUN_LOGICAL_DATE"])
	}

	var params map[string]string
	if err := json.Unmarshal([]byte(tarefa.Env["BREVIS_RUN_PARAMS"]), &params); err != nil {
		t.Fatalf("BREVIS_RUN_PARAMS nao e JSON: %v", err)
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

	if tarefa.Env["BREVIS_RUN_FIRST"] != "false" {
		t.Errorf("ja houve sucesso, entao nao e a primeira: %q", tarefa.Env["BREVIS_RUN_FIRST"])
	}
}

func TestSemHistoricoNaoInventaPrimeiraExecucao(t *testing.T) {
	// Criar tabela sem certeza e pior que nao criar: quem quiser pede
	// explicitamente no codigo do fetcher.
	tarefa := rodar(t, app.Runner{RunID: uuid.New()})

	if tarefa.Env["BREVIS_RUN_FIRST"] != "false" {
		t.Errorf("sem historico configurado a resposta e nao: %q", tarefa.Env["BREVIS_RUN_FIRST"])
	}
}

func TestFalhaNaConsultaNaoViraCriacaoDeTabela(t *testing.T) {
	h := &historico{err: context.DeadlineExceeded}
	tarefa := rodar(t, app.Runner{RunID: uuid.New(), Historico: h})

	if tarefa.Env["BREVIS_RUN_FIRST"] != "false" {
		t.Errorf("banco fora do ar nao pode virar DDL: %q", tarefa.Env["BREVIS_RUN_FIRST"])
	}
}

func TestAmbienteDoRunnerGanhaEmColisao(t *testing.T) {
	// Se alguem definiu a variavel na configuracao do runner, foi porque quis.
	tarefa := rodar(t, app.Runner{
		RunID:     uuid.New(),
		Historico: &historico{jaTeve: false},
		Env:       map[string]string{"BREVIS_RUN_FIRST": "false", "OUTRA": "coisa"},
	})

	if tarefa.Env["BREVIS_RUN_FIRST"] != "false" {
		t.Error("configuracao explicita do runner tem de vencer o valor calculado")
	}
	if tarefa.Env["OUTRA"] != "coisa" {
		t.Error("o resto do ambiente do runner tem de sobreviver")
	}
	if tarefa.Env["BREVIS_RUN_ID"] == "" {
		t.Error("as variaveis sem colisao continuam chegando")
	}
}

func TestSemParamsNaoInjetaVariavelVazia(t *testing.T) {
	tarefa := rodar(t, app.Runner{RunID: uuid.New(), Historico: &historico{}})

	if _, existe := tarefa.Env["BREVIS_RUN_PARAMS"]; existe {
		t.Error("sem params, a variavel nao deve existir em vez de existir vazia")
	}
	if _, existe := tarefa.Env["BREVIS_RUN_TRIGGER"]; existe {
		t.Error("sem trigger, idem")
	}
}

func TestSemRunIDNaoInventaExecucaoGerenciada(t *testing.T) {
	// `brevis run` executa um YAML na hora e nao pertence a historico nenhum.
	// O SDK decide que esta sob o engine pela PRESENCA do id, entao injetar o
	// UUID zero faria um fetcher rodado a mao logar "running under Brevis" com
	// um id inventado.
	tarefa := rodar(t, app.Runner{
		Params:    map[string]string{"create_table": "true"},
		Historico: &historico{jaTeve: false},
	})

	for _, v := range []string{"BREVIS_RUN_ID", "BREVIS_RUN_FIRST", "BREVIS_RUN_ATTEMPT"} {
		if _, existe := tarefa.Env[v]; existe {
			t.Errorf("%s nao devia existir sem um run de verdade: %q", v, tarefa.Env[v])
		}
	}

	// Os params continuam indo: `--param` e como se passa entrada nesse caminho.
	if tarefa.Env["BREVIS_RUN_PARAMS"] == "" {
		t.Error("os params tem de chegar ao passo mesmo sem run gerenciado")
	}
}

func TestTentativaComecaEmZeroComoNoBanco(t *testing.T) {
	// A coluna task_runs.attempt tem DEFAULT 0, e o nome do pod deriva dela.
	// Divergir aqui faria o passo reportar uma tentativa que nao existe.
	tarefa := rodar(t, app.Runner{RunID: uuid.New(), Historico: &historico{}})

	if tarefa.Env["BREVIS_RUN_ATTEMPT"] != "0" {
		t.Errorf("primeira tentativa = %q, esperado \"0\"", tarefa.Env["BREVIS_RUN_ATTEMPT"])
	}
}

// TestCaminhoDoDispatcher reproduz o que cmd/brevis monta no `executar` do
// dispatcher: um run de verdade, com id, params, trigger e historico.
//
// E o unico teste que cobre a forma como o Runner e realmente construido em
// producao — o resto do arquivo testa campos isolados.
func TestCaminhoDoDispatcher(t *testing.T) {
	id := uuid.New()
	quando := time.Date(2026, 9, 3, 4, 0, 0, 0, time.UTC)

	tarefa := rodar(t, app.Runner{
		RunID:          id,
		TentativaDoRun: 0,
		Params:         map[string]string{"load_full": "true"},
		Trigger:        "schedule",
		LogicalDate:    &quando,
		Historico:      &historico{jaTeve: false},
		Env:            map[string]string{"PATH": "/usr/bin", "HOME": "/root"},
	})

	// O ambiente das tasks sobrevive.
	if tarefa.Env["PATH"] == "" || tarefa.Env["HOME"] == "" {
		t.Error("o ambiente configurado das tasks tem de continuar chegando")
	}

	// E o contexto do run chega junto.
	esperado := map[string]string{
		"BREVIS_RUN_ID":           id.String(),
		"BREVIS_RUN_FIRST":        "true",
		"BREVIS_RUN_ATTEMPT":      "0",
		"BREVIS_RUN_TRIGGER":      "schedule",
		"BREVIS_RUN_LOGICAL_DATE": "2026-09-03T04:00:00Z",
		"BREVIS_RUN_PARAMS":       `{"load_full":"true"}`,
	}
	for k, v := range esperado {
		if tarefa.Env[k] != v {
			t.Errorf("%s = %q, esperado %q", k, tarefa.Env[k], v)
		}
	}
}
