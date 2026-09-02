package local

import (
	"context"
	"fmt"
	"sync"

	"github.com/AreteAcademy/bravis/internal/execution"
)

// GoExecutor roda tasks Go registradas, dentro do proprio processo.
//
// E o executor da secao 14: sem container, sem pod, sem processo filho. O ganho
// e justamente esse — uma task Go simples nao paga startup de container, que foi
// a medida que motivou este projeto (cold start de 38s contra 5s no benchmark
// que originou o Bravis).
//
// Diferente do ProcessExecutor, NAO ha restricao de ambiente: o codigo aqui foi
// compilado junto com o binario, entao nao ha superficie de execucao arbitraria.
type GoExecutor struct {
	reg *execution.Registry

	mu      sync.Mutex
	rodando map[string]context.CancelFunc
}

func NewGoExecutor(reg *execution.Registry) *GoExecutor {
	return &GoExecutor{reg: reg, rodando: map[string]context.CancelFunc{}}
}

func (g *GoExecutor) Name() string { return "go" }

// Execute resolve a task no registry e a roda numa goroutine, transmitindo os
// eventos.
func (g *GoExecutor) Execute(ctx context.Context, t execution.TaskExec) (<-chan execution.Event, error) {
	task, ok := g.reg.Get(t.Action)
	if !ok {
		// Listar o que existe economiza uma ida a documentacao, e denuncia erro
		// de digitacao de imediato.
		disponiveis := g.reg.Nomes()
		if len(disponiveis) == 0 {
			// Registro vazio e o caso comum hoje: `docker.run` e
			// `kubernetes.run` estao no plano mas ainda nao existem. Dizer
			// "disponiveis: []" faz parecer erro de digitacao no nome.
			return nil, fmt.Errorf("task %q nao registrada: nenhuma acao foi registrada "+
				"neste worker — use `run:` com um comando, ou registre a acao no binario", t.Action)
		}
		return nil, fmt.Errorf("task %q nao registrada (disponiveis: %v)", t.Action, disponiveis)
	}

	ctx, cancel := context.WithCancel(ctx)
	if t.Timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, t.Timeout)
	}

	g.mu.Lock()
	g.rodando[t.ExecutionID] = cancel
	g.mu.Unlock()

	eventos := make(chan execution.Event, 64)

	go func() {
		defer close(eventos)
		defer func() {
			g.mu.Lock()
			delete(g.rodando, t.ExecutionID)
			g.mu.Unlock()
			cancel()
		}()

		eventos <- execution.Event{Kind: execution.EventStarted, NodeID: t.NodeID}

		// Uma task que entra em panico nao pode derrubar o orquestrador junto:
		// ela roda no MESMO processo, diferente de um pod. O panico vira falha
		// daquela task.
		var err error
		func() {
			defer func() {
				if r := recover(); r != nil {
					err = fmt.Errorf("task %q entrou em panico: %v", t.Action, r)
				}
			}()
			err = task.Run(ctx, execution.Input{
				NodeID: t.NodeID,
				With:   t.With,
				Log: func(msg string) {
					// nao bloqueia se ninguem esta lendo: uma task ruidosa nao
					// pode travar por causa do consumidor
					select {
					case eventos <- execution.Event{
						Kind: execution.EventLog, NodeID: t.NodeID,
						Stream: "stdout", Message: msg,
					}:
					default:
					}
				},
			})
		}()

		switch {
		case err == nil:
			eventos <- execution.Event{Kind: execution.EventSucceeded, NodeID: t.NodeID}
		case ctx.Err() == context.DeadlineExceeded:
			eventos <- execution.Event{
				Kind: execution.EventFailed, NodeID: t.NodeID, Err: err,
				Message: fmt.Sprintf("estourou o timeout de %s", t.Timeout),
			}
		default:
			eventos <- execution.Event{
				Kind: execution.EventFailed, NodeID: t.NodeID, Err: err,
				Message: err.Error(),
			}
		}
	}()

	return eventos, nil
}

func (g *GoExecutor) Cancel(_ context.Context, execID string) error {
	g.mu.Lock()
	cancel, ok := g.rodando[execID]
	g.mu.Unlock()
	if !ok {
		return fmt.Errorf("execucao %q nao esta rodando", execID)
	}
	cancel()
	return nil
}
