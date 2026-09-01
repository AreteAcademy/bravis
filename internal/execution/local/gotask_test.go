package local_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zarvhq/bravis/internal/execution"
	"github.com/zarvhq/bravis/internal/execution/local"
)

func coletarEventos(ev <-chan execution.Event) []execution.Event {
	var out []execution.Event
	for e := range ev {
		out = append(out, e)
	}
	return out
}

func ultimoKind(ev []execution.Event) execution.EventKind {
	return ev[len(ev)-1].Kind
}

func TestGoExecutorRodaTaskRegistrada(t *testing.T) {
	reg := execution.NewRegistry()
	var rodou atomic.Bool
	reg.MustRegister(execution.FuncTask{Nome: "sync", Fn: func(_ context.Context, in execution.Input) error {
		rodou.Store(true)
		in.Log("sincronizando")
		return nil
	}})

	ev, err := local.NewGoExecutor(reg).Execute(context.Background(),
		execution.TaskExec{ExecutionID: "1", NodeID: "n", Action: "sync"})
	if err != nil {
		t.Fatal(err)
	}
	eventos := coletarEventos(ev)

	if !rodou.Load() {
		t.Error("a task nao rodou")
	}
	if ultimoKind(eventos) != execution.EventSucceeded {
		t.Errorf("ultimo evento = %v", ultimoKind(eventos))
	}
	var temLog bool
	for _, e := range eventos {
		if e.Kind == execution.EventLog && e.Message == "sincronizando" {
			temLog = true
		}
	}
	if !temLog {
		t.Error("o Log da task nao virou evento")
	}
}

// O erro deve listar o que existe: economiza uma ida a documentacao e denuncia
// erro de digitacao de imediato.
func TestGoExecutorTaskDesconhecidaListaAsDisponiveis(t *testing.T) {
	reg := execution.NewRegistry()
	reg.MustRegister(execution.FuncTask{Nome: "daily_sync", Fn: func(context.Context, execution.Input) error { return nil }})

	_, err := local.NewGoExecutor(reg).Execute(context.Background(),
		execution.TaskExec{ExecutionID: "1", NodeID: "n", Action: "daly_sync"})
	if err == nil {
		t.Fatal("esperava erro")
	}
	if !strings.Contains(err.Error(), "daily_sync") {
		t.Errorf("erro = %q; devia listar as tasks disponiveis", err)
	}
}

// Uma task roda no MESMO processo, diferente de um pod: um panico nao pode
// derrubar o orquestrador junto.
func TestGoExecutorContemPanico(t *testing.T) {
	reg := execution.NewRegistry()
	reg.MustRegister(execution.FuncTask{Nome: "explode", Fn: func(context.Context, execution.Input) error {
		panic("boom")
	}})

	ev, err := local.NewGoExecutor(reg).Execute(context.Background(),
		execution.TaskExec{ExecutionID: "1", NodeID: "n", Action: "explode"})
	if err != nil {
		t.Fatal(err)
	}
	eventos := coletarEventos(ev)

	if ultimoKind(eventos) != execution.EventFailed {
		t.Fatalf("ultimo evento = %v, queria failed", ultimoKind(eventos))
	}
	if !strings.Contains(eventos[len(eventos)-1].Err.Error(), "panico") {
		t.Errorf("erro = %v; devia identificar o panico", eventos[len(eventos)-1].Err)
	}
}

func TestGoExecutorRespeitaTimeout(t *testing.T) {
	reg := execution.NewRegistry()
	reg.MustRegister(execution.FuncTask{Nome: "lenta", Fn: func(ctx context.Context, _ execution.Input) error {
		select {
		case <-time.After(5 * time.Second):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}})

	inicio := time.Now()
	ev, err := local.NewGoExecutor(reg).Execute(context.Background(), execution.TaskExec{
		ExecutionID: "1", NodeID: "n", Action: "lenta", Timeout: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	eventos := coletarEventos(ev)

	if d := time.Since(inicio); d > time.Second {
		t.Errorf("levou %s; o timeout nao interrompeu", d)
	}
	ultimo := eventos[len(eventos)-1]
	if ultimo.Kind != execution.EventFailed {
		t.Fatalf("ultimo evento = %v, queria failed", ultimo.Kind)
	}
	if !strings.Contains(ultimo.Message, "timeout") {
		t.Errorf("mensagem = %q; devia citar o timeout", ultimo.Message)
	}
}

func TestGoExecutorPropagaErroDaTask(t *testing.T) {
	reg := execution.NewRegistry()
	falha := errors.New("origem indisponivel")
	reg.MustRegister(execution.FuncTask{Nome: "falha", Fn: func(context.Context, execution.Input) error {
		return falha
	}})

	ev, _ := local.NewGoExecutor(reg).Execute(context.Background(),
		execution.TaskExec{ExecutionID: "1", NodeID: "n", Action: "falha"})
	eventos := coletarEventos(ev)

	if !errors.Is(eventos[len(eventos)-1].Err, falha) {
		t.Error("o erro da task nao chegou ao evento")
	}
}
