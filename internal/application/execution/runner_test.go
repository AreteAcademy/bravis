package execution_test

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	app "github.com/zarvhq/bravis/internal/application/execution"
	wf "github.com/zarvhq/bravis/internal/domain/workflow"
	"github.com/zarvhq/bravis/internal/execution"
	"github.com/zarvhq/bravis/internal/execution/local"
)

type coletor struct {
	mu      sync.Mutex
	eventos []execution.Event
}

func (c *coletor) Evento(e execution.Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.eventos = append(c.eventos, e)
}

// CRITERIO DE ACEITE DA PHASE 3 (secao 37):
//
//	Task A -> (Task B + Task C) -> Task D, com paralelismo.
func TestCriterioDeAceite_DAGGoComParalelismo(t *testing.T) {
	reg := execution.NewRegistry()

	var (
		mu     sync.Mutex
		ordem  []string
		emVoo  atomic.Int32
		picoBC atomic.Int32
	)

	registrar := func(nome string, dura time.Duration) {
		reg.MustRegister(execution.FuncTask{Nome: nome, Fn: func(ctx context.Context, _ execution.Input) error {
			n := emVoo.Add(1)
			for {
				p := picoBC.Load()
				if n <= p || picoBC.CompareAndSwap(p, n) {
					break
				}
			}
			defer emVoo.Add(-1)

			mu.Lock()
			ordem = append(ordem, nome)
			mu.Unlock()

			select {
			case <-time.After(dura):
			case <-ctx.Done():
				return ctx.Err()
			}
			return nil
		}})
	}
	registrar("task_a", 10*time.Millisecond)
	registrar("task_b", 150*time.Millisecond)
	registrar("task_c", 150*time.Millisecond)
	registrar("task_d", 10*time.Millisecond)

	w := wf.Workflow{
		Slug: "dag_go", Kind: wf.KindDAG,
		Nodes: []wf.Node{
			{ID: "a", Action: "task_a"},
			{ID: "b", Action: "task_b"},
			{ID: "c", Action: "task_c"},
			{ID: "d", Action: "task_d"},
		},
		Edges: []wf.Edge{
			{From: "a", To: "b"}, {From: "a", To: "c"},
			{From: "b", To: "d"}, {From: "c", To: "d"},
		},
	}
	if err := w.Validate(); err != nil {
		t.Fatal(err)
	}

	c := &coletor{}
	r := app.Runner{Go: local.NewGoExecutor(reg), Report: c}

	inicio := time.Now()
	if err := r.Run(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	duracao := time.Since(inicio)

	// b e c rodaram JUNTAS: em serie o total passaria de 300ms
	if picoBC.Load() < 2 {
		t.Errorf("pico de concorrencia = %d; b e c deviam rodar em paralelo", picoBC.Load())
	}
	if duracao > 280*time.Millisecond {
		t.Errorf("levou %s; em paralelo deveria ficar perto de 170ms, nao de 320ms", duracao)
	}

	// a primeiro, d por ultimo — a ordem topologica foi respeitada
	if ordem[0] != "task_a" {
		t.Errorf("primeira = %q, queria task_a", ordem[0])
	}
	if ordem[len(ordem)-1] != "task_d" {
		t.Errorf("ultima = %q, queria task_d", ordem[len(ordem)-1])
	}
	t.Logf("ordem: %v | duracao: %s | pico: %d", ordem, duracao.Round(time.Millisecond), picoBC.Load())
}

// O retry e POR NO: refazer o workflow inteiro porque o ultimo step falhou
// desperdicaria o trabalho ja concluido.
func TestRetryPorNo(t *testing.T) {
	reg := execution.NewRegistry()
	var tentativas atomic.Int32

	reg.MustRegister(execution.FuncTask{Nome: "instavel", Fn: func(context.Context, execution.Input) error {
		if tentativas.Add(1) < 3 {
			return fmt.Errorf("falha transitoria")
		}
		return nil
	}})

	w := wf.Workflow{Slug: "w", Nodes: []wf.Node{{ID: "n", Action: "instavel"}}}
	r := app.Runner{
		Go: local.NewGoExecutor(reg), Report: &coletor{},
		MaxTentativas: 3, BackoffBase: time.Millisecond,
	}
	if err := r.Run(context.Background(), w); err != nil {
		t.Fatalf("devia ter sucesso na 3a tentativa: %v", err)
	}
	if n := tentativas.Load(); n != 3 {
		t.Errorf("tentativas = %d, queria 3", n)
	}
}

func TestRetryDesisteAposOLimite(t *testing.T) {
	reg := execution.NewRegistry()
	var tentativas atomic.Int32
	reg.MustRegister(execution.FuncTask{Nome: "sempre_falha", Fn: func(context.Context, execution.Input) error {
		tentativas.Add(1)
		return fmt.Errorf("falha permanente")
	}})

	w := wf.Workflow{Slug: "w", Nodes: []wf.Node{{ID: "n", Action: "sempre_falha"}}}
	r := app.Runner{
		Go: local.NewGoExecutor(reg), Report: &coletor{},
		MaxTentativas: 2, BackoffBase: time.Millisecond,
	}
	if err := r.Run(context.Background(), w); err == nil {
		t.Fatal("esperava falha")
	}
	if n := tentativas.Load(); n != 2 {
		t.Errorf("tentativas = %d, queria exatamente 2", n)
	}
}

// Cancelar o contexto interrompe a DAG e nao dispara retry — repetir contra um
// cancelamento e desperdicio.
func TestCancelamentoNaoDisparaRetry(t *testing.T) {
	reg := execution.NewRegistry()
	var tentativas atomic.Int32
	reg.MustRegister(execution.FuncTask{Nome: "lenta", Fn: func(ctx context.Context, _ execution.Input) error {
		tentativas.Add(1)
		<-ctx.Done()
		return ctx.Err()
	}})

	w := wf.Workflow{Slug: "w", Nodes: []wf.Node{{ID: "n", Action: "lenta"}}}
	r := app.Runner{
		Go: local.NewGoExecutor(reg), Report: &coletor{},
		MaxTentativas: 5, BackoffBase: time.Millisecond,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	_ = r.Run(ctx, w)

	if n := tentativas.Load(); n != 1 {
		t.Errorf("tentativas = %d; cancelamento nao deve repetir", n)
	}
}

// Um step sem executor configurado precisa falhar com mensagem clara, e nao
// ser pulado em silencio.
func TestStepSemExecutorFalhaExplicitamente(t *testing.T) {
	w := wf.Workflow{Slug: "w", Nodes: []wf.Node{{ID: "n", Action: "qualquer"}}}
	err := app.Runner{Report: &coletor{}}.Run(context.Background(), w)
	if err == nil {
		t.Fatal("esperava erro, nao silencio")
	}
}
