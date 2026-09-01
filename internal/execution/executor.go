// Package execution define o contrato de execucao de tasks.
//
// A interface e a da secao 13 do plano, com uma diferenca: `Execute` devolve um
// canal de eventos em vez de bloquear. Um pod que roda por vinte minutos precisa
// reportar progresso antes de terminar, e o mesmo vale para um processo local
// que escreve em stdout.
package execution

import (
	"context"
	"time"
)

// Executor roda uma task e reporta o que acontece.
type Executor interface {
	Name() string
	Execute(ctx context.Context, t TaskExec) (<-chan Event, error)
	Cancel(ctx context.Context, execID string) error
}

// TaskExec e o que se pede para executar. Deliberadamente pobre: o executor nao
// conhece workflow, dependencia nem agenda.
//
// `Command` e `Action` sao exclusivos: o primeiro vai para o ProcessExecutor, o
// segundo resolve no registry de tasks Go. Quem escolhe o executor e o runner,
// nao o executor.
type TaskExec struct {
	ExecutionID string
	NodeID      string

	Command string // shell, para o ProcessExecutor
	Action  string // nome no registry, para o GoExecutor
	With    map[string]any

	WorkDir string
	Env     map[string]string

	// Timeout zero significa sem limite. A secao 37 pede timeout na PHASE 3;
	// deixar o padrao aberto e deliberado — impor um limite arbitrario mataria
	// tasks legitimamente longas.
	Timeout time.Duration
}

// EventKind classifica o que o executor reporta.
type EventKind string

const (
	EventStarted   EventKind = "started"
	EventLog       EventKind = "log"
	EventSucceeded EventKind = "succeeded"
	EventFailed    EventKind = "failed"
)

// Event e uma ocorrencia durante a execucao. `Stream` distingue stdout de
// stderr: juntar os dois perde a informacao de onde a mensagem veio, e foi
// exatamente isso que fez o resumo final do dbt aparecer como erro no Leoflow.
type Event struct {
	Kind     EventKind
	NodeID   string
	Message  string
	Stream   string // "stdout" | "stderr"
	ExitCode int
	Err      error
}
