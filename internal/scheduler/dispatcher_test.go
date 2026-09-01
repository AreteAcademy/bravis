package scheduler_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	dom "github.com/zarvhq/bravis/internal/domain/run"
	"github.com/zarvhq/bravis/internal/infrastructure/postgres"
	"github.com/zarvhq/bravis/internal/queue"
	"github.com/zarvhq/bravis/internal/scheduler"
)

// Estes testes exigem Postgres. Sem BRAVIS_TEST_DATABASE_URL eles pulam, para
// que `go test ./...` continue verde numa maquina sem docker.
func banco(t *testing.T) *postgres.Pool {
	t.Helper()
	url := os.Getenv("BRAVIS_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("defina BRAVIS_TEST_DATABASE_URL para rodar os testes de fila (make up)")
	}
	p, err := postgres.New(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)

	// Cada teste comeca do zero. `schedules` entra na lista mesmo sem FK para
	// workflows: ela referencia o slug como texto, entao o CASCADE nao a alcanca.
	if _, err := p.Exec(context.Background(),
		`TRUNCATE queue_items, task_runs, runs, schedules, workflows, projects CASCADE`); err != nil {
		t.Fatal(err)
	}
	return p
}

func semLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// CRITERIO DE ACEITE DA PHASE 2 (secao 37):
//
//	100 runs enfileiradas, concorrencia maxima 5
//	-> 5 RUNNING, 95 QUEUED, sem perda.
func TestCriterioDeAceite_100Runs_Concorrencia5(t *testing.T) {
	pool := banco(t)
	ctx := context.Background()
	repo := postgres.NewRunRepo(pool)
	fila := queue.New(pool.Pool)

	const total, maxConc = 100, 5

	for i := 0; i < total; i++ {
		r, err := repo.Criar(ctx, dom.Run{
			WorkflowSlug:   "teste",
			IdempotencyKey: fmt.Sprintf("aceite-%d", i),
			Definicao:      []byte(`{}`),
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := repo.Transicionar(ctx, r.ID, dom.StatusQueued); err != nil {
			t.Fatal(err)
		}
		if err := fila.Enqueue(ctx, r.ID, 0, time.Now()); err != nil {
			t.Fatal(err)
		}
	}

	// Segura toda execucao ate liberarmos, para poder observar o estado estavel.
	segurar := make(chan struct{})
	var rodando atomic.Int32
	var pico atomic.Int32

	executar := func(ctx context.Context, _ uuid.UUID) error {
		n := rodando.Add(1)
		for {
			p := pico.Load()
			if n <= p || pico.CompareAndSwap(p, n) {
				break
			}
		}
		defer rodando.Add(-1)
		<-segurar
		return nil
	}

	d := scheduler.New(scheduler.Config{
		Worker: "t", MaxConcorrente: maxConc, Intervalo: 20 * time.Millisecond,
	}, fila, repo, executar, semLog())

	ctxD, parar := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); _ = d.Run(ctxD) }()

	// Espera o estado estabilizar em maxConc em voo.
	prazo := time.After(10 * time.Second)
	for rodando.Load() < maxConc {
		select {
		case <-prazo:
			t.Fatalf("so %d em voo depois de 10s, queria %d", rodando.Load(), maxConc)
		case <-time.After(20 * time.Millisecond):
		}
	}
	time.Sleep(300 * time.Millisecond) // deixa o dispatcher tentar pegar mais

	contagem, err := repo.ContarPorStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	pendentes, reivindicados, err := fila.Tamanho(ctx)
	if err != nil {
		t.Fatal(err)
	}

	t.Logf("runs: %v | fila: %d pendentes, %d reivindicados | em voo: %d",
		contagem, pendentes, reivindicados, rodando.Load())

	if got := contagem[dom.StatusRunning]; got != maxConc {
		t.Errorf("RUNNING = %d, queria %d", got, maxConc)
	}
	if got := contagem[dom.StatusQueued]; got != total-maxConc {
		t.Errorf("QUEUED = %d, queria %d", got, total-maxConc)
	}
	if p := pico.Load(); p > maxConc {
		t.Errorf("pico de concorrencia = %d, excedeu o maximo de %d", p, maxConc)
	}
	if pendentes+reivindicados != total {
		t.Errorf("fila tem %d itens, queria %d — houve perda", pendentes+reivindicados, total)
	}

	// Libera e confirma que as 100 terminam, sem perda.
	close(segurar)
	prazo = time.After(30 * time.Second)
	for {
		c, err := repo.ContarPorStatus(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if c[dom.StatusSuccess] == total {
			break
		}
		select {
		case <-prazo:
			t.Fatalf("apos liberar: %v — queria %d em success", c, total)
		case <-time.After(50 * time.Millisecond):
		}
	}
	parar()
	wg.Wait()

	pendentes, reivindicados, _ = fila.Tamanho(ctx)
	if pendentes+reivindicados != 0 {
		t.Errorf("fila deveria estar vazia, tem %d pendentes e %d reivindicados", pendentes, reivindicados)
	}
	if p := pico.Load(); p > maxConc {
		t.Errorf("pico de concorrencia = %d durante toda a corrida", p)
	}
}

// A secao 29 pede que operacao critica tolere repeticao. O caso concreto: o
// scheduler cria o Run, morre antes de registrar e tenta de novo ao subir.
func TestIdempotenciaImpedeRunDuplicado(t *testing.T) {
	pool := banco(t)
	ctx := context.Background()
	repo := postgres.NewRunRepo(pool)

	r := dom.Run{WorkflowSlug: "w", IdempotencyKey: "mesma-chave", Definicao: []byte(`{}`)}
	if _, err := repo.Criar(ctx, r); err != nil {
		t.Fatal(err)
	}
	_, err := repo.Criar(ctx, dom.Run{
		WorkflowSlug: "w", IdempotencyKey: "mesma-chave", Definicao: []byte(`{}`),
	})
	if !errors.Is(err, postgres.ErrJaExiste) {
		t.Fatalf("erro = %v, queria ErrJaExiste", err)
	}
}

// Enfileirar o mesmo run duas vezes e no-op, nao duplicata.
func TestEnqueueEhIdempotente(t *testing.T) {
	pool := banco(t)
	ctx := context.Background()
	repo := postgres.NewRunRepo(pool)
	fila := queue.New(pool.Pool)

	r, err := repo.Criar(ctx, dom.Run{WorkflowSlug: "w", IdempotencyKey: "k", Definicao: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := fila.Enqueue(ctx, r.ID, 0, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	pendentes, _, err := fila.Tamanho(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if pendentes != 1 {
		t.Errorf("fila tem %d itens, queria 1", pendentes)
	}
}

// Dois dispatchers competindo nao podem receber o mesmo item — e o que o
// FOR UPDATE SKIP LOCKED garante.
func TestClaimNaoEntregaOMesmoItemDuasVezes(t *testing.T) {
	pool := banco(t)
	ctx := context.Background()
	repo := postgres.NewRunRepo(pool)
	fila := queue.New(pool.Pool)

	const n = 20
	for i := 0; i < n; i++ {
		r, err := repo.Criar(ctx, dom.Run{
			WorkflowSlug: "w", IdempotencyKey: fmt.Sprintf("c-%d", i), Definicao: []byte(`{}`),
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := fila.Enqueue(ctx, r.ID, 0, time.Now()); err != nil {
			t.Fatal(err)
		}
	}

	var mu sync.Mutex
	vistos := map[uuid.UUID]int{}
	var wg sync.WaitGroup

	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			itens, err := fila.Claim(ctx, fmt.Sprintf("worker-%d", w), n)
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			for _, it := range itens {
				vistos[it.RunID]++
			}
			mu.Unlock()
		}(w)
	}
	wg.Wait()

	if len(vistos) != n {
		t.Errorf("%d runs reivindicados, queria %d", len(vistos), n)
	}
	for id, c := range vistos {
		if c > 1 {
			t.Errorf("run %s entregue %d vezes", id, c)
		}
	}
}

// Item preso a um worker morto tem de voltar. Sem isso, e a execucao zumbi que
// travou pipelines por 33 dias no sistema anterior.
func TestRecuperarDevolveItemDeWorkerMorto(t *testing.T) {
	pool := banco(t)
	ctx := context.Background()
	repo := postgres.NewRunRepo(pool)
	fila := queue.New(pool.Pool)

	r, err := repo.Criar(ctx, dom.Run{WorkflowSlug: "w", IdempotencyKey: "z", Definicao: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if err := fila.Enqueue(ctx, r.ID, 0, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := fila.Claim(ctx, "worker-que-vai-morrer", 1); err != nil {
		t.Fatal(err)
	}

	if _, reivindicados, _ := fila.Tamanho(ctx); reivindicados != 1 {
		t.Fatal("esperava 1 item reivindicado")
	}
	n, err := fila.Recuperar(ctx, 0) // limite zero: tudo que esta reivindicado volta
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("recuperou %d, queria 1", n)
	}
	if pendentes, _, _ := fila.Tamanho(ctx); pendentes != 1 {
		t.Error("o item deveria estar livre de novo")
	}
}
