package local

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/zarvhq/bravis/internal/execution"
)

func executor(t *testing.T) *ProcessExecutor {
	t.Helper()
	p, err := New("local")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func coletar(t *testing.T, ev <-chan execution.Event) []execution.Event {
	t.Helper()
	var out []execution.Event
	for e := range ev {
		out = append(out, e)
	}
	return out
}

// A fronteira do executor e codigo, nao convencao. Se este teste passar a
// falhar, a emenda a secao 3 do plano foi violada.
func TestRecusaConstrucaoForaDoLocal(t *testing.T) {
	for _, env := range []string{"prod", "staging", "dev", ""} {
		if _, err := New(env); err == nil {
			t.Fatalf("New(%q) devia recusar: fora do local, `run:` vai para o Kubernetes", env)
		} else if !errors.As(err, &ErrForaDoLocal{}) {
			t.Errorf("New(%q) devolveu %T, queria ErrForaDoLocal", env, err)
		}
	}
}

func TestExecutaEReportaSucesso(t *testing.T) {
	ev, err := executor(t).Execute(context.Background(), execution.TaskExec{
		ExecutionID: "1", NodeID: "hello", Command: "echo ola",
	})
	if err != nil {
		t.Fatal(err)
	}
	eventos := coletar(t, ev)

	if eventos[0].Kind != execution.EventStarted {
		t.Errorf("primeiro evento = %v, queria started", eventos[0].Kind)
	}
	ultimo := eventos[len(eventos)-1]
	if ultimo.Kind != execution.EventSucceeded {
		t.Errorf("ultimo evento = %v, queria succeeded", ultimo.Kind)
	}
	if !temLog(eventos, "ola", "stdout") {
		t.Error("nao capturou a saida do comando")
	}
}

// stdout e stderr precisam chegar separados: juntar os dois perde de onde a
// mensagem veio, que foi o que fez o resumo do dbt aparecer como erro no Leoflow.
func TestSeparaStdoutDeStderr(t *testing.T) {
	ev, err := executor(t).Execute(context.Background(), execution.TaskExec{
		ExecutionID: "2", NodeID: "n", Command: "echo saida; echo erro >&2",
	})
	if err != nil {
		t.Fatal(err)
	}
	eventos := coletar(t, ev)

	if !temLog(eventos, "saida", "stdout") {
		t.Error("stdout nao classificado")
	}
	if !temLog(eventos, "erro", "stderr") {
		t.Error("stderr nao classificado")
	}
}

func TestReportaFalhaComExitCode(t *testing.T) {
	ev, err := executor(t).Execute(context.Background(), execution.TaskExec{
		ExecutionID: "3", NodeID: "n", Command: "exit 3",
	})
	if err != nil {
		t.Fatal(err)
	}
	eventos := coletar(t, ev)

	ultimo := eventos[len(eventos)-1]
	if ultimo.Kind != execution.EventFailed {
		t.Fatalf("ultimo evento = %v, queria failed", ultimo.Kind)
	}
	if ultimo.ExitCode != 3 {
		t.Errorf("exit = %d, queria 3", ultimo.ExitCode)
	}
}

// O ambiente e explicito: o orquestrador carrega credenciais que uma task nao
// deve enxergar por acidente.
func TestNaoHerdaAmbienteDoPai(t *testing.T) {
	t.Setenv("SEGREDO_DO_ORQUESTRADOR", "nao-vazar")

	ev, err := executor(t).Execute(context.Background(), execution.TaskExec{
		ExecutionID: "4", NodeID: "n",
		Command: "echo [$SEGREDO_DO_ORQUESTRADOR]",
		Env:     map[string]string{"PERMITIDA": "sim"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range coletar(t, ev) {
		if strings.Contains(e.Message, "nao-vazar") {
			t.Fatal("a task enxergou uma variavel do processo pai")
		}
	}
}

func TestCancelInterrompe(t *testing.T) {
	p := executor(t)
	ev, err := p.Execute(context.Background(), execution.TaskExec{
		ExecutionID: "5", NodeID: "n", Command: "sleep 30",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Cancel(context.Background(), "5"); err != nil {
		t.Fatal(err)
	}
	ultimo := coletar(t, ev)
	if k := ultimo[len(ultimo)-1].Kind; k != execution.EventFailed {
		t.Errorf("ultimo evento = %v, queria failed apos cancelamento", k)
	}
}

func temLog(eventos []execution.Event, msg, stream string) bool {
	for _, e := range eventos {
		if e.Kind == execution.EventLog && e.Stream == stream && strings.Contains(e.Message, msg) {
			return true
		}
	}
	return false
}
