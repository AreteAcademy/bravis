// Package execution (application) percorre o grafo e executa os nos.
//
// Esta e a versao LOCAL: sem fila, sem persistencia, sem scheduler — essas
// pecas tem fase propria no plano (§37, fases 2 e 4). O que existe aqui e o
// suficiente para `bravis run arquivo.yaml` rodar na propria instancia, que foi
// o pedido.
package execution

import (
	"context"
	"fmt"
	"sync"

	wf "github.com/zarvhq/bravis/internal/domain/workflow"
	"github.com/zarvhq/bravis/internal/execution"
	"github.com/zarvhq/bravis/internal/graph"
)

// Reporter recebe os eventos da execucao. Interface pequena para que a CLI, os
// testes e (mais tarde) o persistidor de eventos possam observar o mesmo fluxo.
type Reporter interface {
	Evento(execution.Event)
}

// Runner executa um workflow inteiro.
type Runner struct {
	Exec    execution.Executor
	WorkDir string
	Env     map[string]string
	Report  Reporter
}

// Run percorre o grafo por niveis: tudo dentro de um nivel roda em paralelo, e o
// nivel seguinte so comeca quando o anterior fecha inteiro.
//
// Para na PRIMEIRA falha do nivel, sem iniciar o proximo. Continuar depois de um
// erro produziria resultado parcial que parece completo — foi assim que uma
// pipeline ficou 28 dias atrasada sem ninguem ver, no sistema que este substitui.
func (r Runner) Run(ctx context.Context, w wf.Workflow) error {
	niveis, err := graph.Niveis(w)
	if err != nil {
		return err
	}
	porID := make(map[string]wf.Node, len(w.Nodes))
	for _, n := range w.Nodes {
		porID[n.ID] = n
	}

	for i, nivel := range niveis {
		if err := r.rodarNivel(ctx, w, nivel, porID); err != nil {
			return fmt.Errorf("nivel %d: %w", i+1, err)
		}
	}
	return nil
}

func (r Runner) rodarNivel(ctx context.Context, w wf.Workflow, nivel []string, porID map[string]wf.Node) error {
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		erros []error
	)

	for _, id := range nivel {
		n := porID[id]

		// `action:` ainda nao tem executor. Falhar explicitamente e melhor que
		// pular em silencio: um step que nao roda e o workflow reporta sucesso e
		// pior que um erro.
		if n.Action != "" {
			mu.Lock()
			erros = append(erros, fmt.Errorf("step %q usa `action: %s`, ainda nao implementada "+
				"(so `run:` funciona hoje)", n.ID, n.Action))
			mu.Unlock()
			continue
		}

		wg.Add(1)
		go func(n wf.Node) {
			defer wg.Done()
			if err := r.rodarNo(ctx, w, n); err != nil {
				mu.Lock()
				erros = append(erros, err)
				mu.Unlock()
			}
		}(n)
	}
	wg.Wait()

	if len(erros) > 0 {
		return erros[0]
	}
	return nil
}

func (r Runner) rodarNo(ctx context.Context, w wf.Workflow, n wf.Node) error {
	eventos, err := r.Exec.Execute(ctx, execution.Task{
		ExecutionID: w.Slug + ":" + n.ID,
		NodeID:      n.ID,
		Command:     n.Run,
		WorkDir:     r.WorkDir,
		Env:         r.Env,
	})
	if err != nil {
		return fmt.Errorf("step %q: %w", n.ID, err)
	}

	var falha error
	for e := range eventos {
		if r.Report != nil {
			r.Report.Evento(e)
		}
		if e.Kind == execution.EventFailed {
			falha = fmt.Errorf("step %q falhou (exit %d)", n.ID, e.ExitCode)
		}
	}
	return falha
}
