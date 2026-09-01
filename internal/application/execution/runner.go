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
	wf "github.com/zarvhq/bravis/internal/domain/workflow"
	"github.com/zarvhq/bravis/internal/execution"
	"github.com/zarvhq/bravis/internal/graph"
	"sync"
	"time"
)

// Reporter recebe os eventos da execucao. Interface pequena para que a CLI, os
// testes e (mais tarde) o persistidor de eventos possam observar o mesmo fluxo.
type Reporter interface {
	Evento(execution.Event)
}

// Runner executa um workflow inteiro.
//
// Guarda DOIS executores e escolhe por no: `run:` vai para o de processo,
// `action:` resolve no registry Go. A escolha e do runner, e nao do executor,
// para que cada executor continue ignorando a existencia do outro.
type Runner struct {
	Processo execution.Executor // atende `run:`; pode ser nil se so houver tasks Go
	Go       execution.Executor // atende `action:`; pode ser nil

	WorkDir string
	Env     map[string]string
	Report  Reporter

	// Timeout por no. Zero = sem limite.
	Timeout time.Duration

	// MaxTentativas por no. Zero ou 1 = tentativa unica.
	MaxTentativas int
	BackoffBase   time.Duration
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

// rodarNo executa um no, com retry.
//
// O retry e POR NO, e nao apenas por Run como no dispatcher: refazer o workflow
// inteiro porque um `notify.sh` falhou desperdicaria o trabalho ja concluido.
func (r Runner) rodarNo(ctx context.Context, w wf.Workflow, n wf.Node) error {
	tentativas := r.MaxTentativas
	if tentativas < 1 {
		tentativas = 1
	}

	var ultima error
	for t := 1; t <= tentativas; t++ {
		ultima = r.tentar(ctx, w, n)
		if ultima == nil {
			return nil
		}
		if t == tentativas {
			break
		}
		// Nao insiste se o contexto morreu: seria retry contra um cancelamento.
		if ctx.Err() != nil {
			break
		}

		espera := r.BackoffBase * time.Duration(1<<uint(t-1))
		if r.Report != nil {
			r.Report.Evento(execution.Event{
				Kind: execution.EventLog, NodeID: n.ID, Stream: "stderr",
				Message: fmt.Sprintf("tentativa %d/%d falhou, repetindo em %s", t, tentativas, espera),
			})
		}
		select {
		case <-time.After(espera):
		case <-ctx.Done():
			return ultima
		}
	}
	return ultima
}

func (r Runner) tentar(ctx context.Context, w wf.Workflow, n wf.Node) error {
	exec, tarefa, err := r.montar(w, n)
	if err != nil {
		return err
	}

	eventos, err := exec.Execute(ctx, tarefa)
	if err != nil {
		return fmt.Errorf("step %q: %w", n.ID, err)
	}

	var falha error
	for e := range eventos {
		if r.Report != nil {
			r.Report.Evento(e)
		}
		if e.Kind == execution.EventFailed {
			falha = fmt.Errorf("step %q: %s", n.ID, e.Message)
		}
	}
	return falha
}

// montar escolhe o executor e monta a task.
func (r Runner) montar(w wf.Workflow, n wf.Node) (execution.Executor, execution.TaskExec, error) {
	t := execution.TaskExec{
		ExecutionID: w.Slug + ":" + n.ID,
		NodeID:      n.ID,
		WorkDir:     r.WorkDir,
		Env:         r.Env,
		Timeout:     r.Timeout,
	}

	if n.Action != "" {
		if r.Go == nil {
			return nil, t, fmt.Errorf("step %q usa `action: %s`, mas nenhum executor Go foi configurado", n.ID, n.Action)
		}
		t.Action, t.With = n.Action, n.With
		return r.Go, t, nil
	}

	if r.Processo == nil {
		return nil, t, fmt.Errorf("step %q usa `run:`, mas nenhum executor de processo foi configurado", n.ID)
	}
	t.Command = n.Run
	return r.Processo, t, nil
}
