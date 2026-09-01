package scheduler_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	dom "github.com/zarvhq/bravis/internal/domain/run"
	"github.com/zarvhq/bravis/internal/infrastructure/postgres"
	"github.com/zarvhq/bravis/internal/notify"
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
		if err := fila.Enqueue(ctx, r.ID, 0, time.Time{}); err != nil {
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
		if err := fila.Enqueue(ctx, r.ID, 0, time.Time{}); err != nil {
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
		if err := fila.Enqueue(ctx, r.ID, 0, time.Time{}); err != nil {
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
	if err := fila.Enqueue(ctx, r.ID, 0, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if _, err := fila.Claim(ctx, "worker-que-vai-morrer", 1); err != nil {
		t.Fatal(err)
	}

	if _, reivindicados, _ := fila.Tamanho(ctx); reivindicados != 1 {
		t.Fatal("esperava 1 item reivindicado")
	}
	itens, err := fila.Recuperar(ctx, 0) // limite zero: tudo que esta reivindicado volta
	if err != nil {
		t.Fatal(err)
	}
	if len(itens) != 1 {
		t.Errorf("recuperou %d, queria 1", len(itens))
	}
	if len(itens) == 1 && itens[0].RunID != r.ID {
		t.Errorf("item recuperado aponta para %s, queria %s", itens[0].RunID, r.ID)
	}
	if pendentes, _, _ := fila.Tamanho(ctx); pendentes != 1 {
		t.Error("o item deveria estar livre de novo")
	}
}

// O bug que o usuario viu na tela: o worker morre no meio, o item volta para a
// fila mas o RUN fica "running" para sempre. A varredura precisa corrigir os
// dois lados.
func TestRecuperarOrfaosDevolveORunAFila(t *testing.T) {
	pool := banco(t)
	ctx := context.Background()
	repo := postgres.NewRunRepo(pool)
	fila := queue.New(pool.Pool)

	r, err := repo.Criar(ctx, dom.Run{WorkflowSlug: "w", IdempotencyKey: "orfa", Definicao: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if err := fila.Enqueue(ctx, r.ID, 0, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if _, err := fila.Claim(ctx, "worker-que-vai-morrer", 1); err != nil {
		t.Fatal(err)
	}
	// O worker chegou a marcar running antes de morrer — o estado exato em que
	// a run ficava pendurada.
	if err := repo.Transicionar(ctx, r.ID, dom.StatusQueued); err != nil {
		t.Fatal(err)
	}
	if err := repo.Transicionar(ctx, r.ID, dom.StatusRunning); err != nil {
		t.Fatal(err)
	}

	d := scheduler.New(scheduler.Config{
		Worker: "vivo", MaxTentativas: 3, Visibilidade: time.Nanosecond,
	}, fila, repo, func(context.Context, uuid.UUID) error { return nil }, semLog())

	n, err := d.RecuperarOrfaos(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("recuperou %d orfas, queria 1", n)
	}

	depois, err := repo.Buscar(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if depois.Status != dom.StatusQueued {
		t.Errorf("run ficou em %s; queria queued, pronta para outro worker", depois.Status)
	}
	if depois.Attempt != 1 {
		t.Errorf("tentativa = %d; a morte do worker consome uma tentativa", depois.Attempt)
	}
	if depois.Erro == "" {
		t.Error("o run precisa registrar POR QUE foi recuperado")
	}
	if pendentes, _, _ := fila.Tamanho(ctx); pendentes != 1 {
		t.Error("o item deveria estar livre para outro worker")
	}
}

// Esgotadas as tentativas, o orfao para em failed em vez de circular entre
// workers para sempre.
func TestOrfaoParaDeVoltarQuandoEsgotaTentativas(t *testing.T) {
	pool := banco(t)
	ctx := context.Background()
	repo := postgres.NewRunRepo(pool)
	fila := queue.New(pool.Pool)

	r, err := repo.Criar(ctx, dom.Run{WorkflowSlug: "w", IdempotencyKey: "orfa2", Definicao: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if err := fila.Enqueue(ctx, r.ID, 0, time.Time{}); err != nil {
		t.Fatal(err)
	}
	d := scheduler.New(scheduler.Config{
		Worker: "vivo", MaxTentativas: 1, Visibilidade: time.Nanosecond,
	}, fila, repo, func(context.Context, uuid.UUID) error { return nil }, semLog())

	if _, err := fila.Claim(ctx, "morto", 1); err != nil {
		t.Fatal(err)
	}
	if err := repo.Transicionar(ctx, r.ID, dom.StatusQueued); err != nil {
		t.Fatal(err)
	}
	if err := repo.Transicionar(ctx, r.ID, dom.StatusRunning); err != nil {
		t.Fatal(err)
	}
	if _, err := d.RecuperarOrfaos(ctx); err != nil {
		t.Fatal(err)
	}

	depois, _ := repo.Buscar(ctx, r.ID)
	if depois.Status != dom.StatusFailed {
		t.Errorf("run em %s; com tentativas esgotadas tem de parar em failed", depois.Status)
	}
	if pendentes, reivindicados, _ := fila.Tamanho(ctx); pendentes+reivindicados != 0 {
		t.Errorf("fila com %d itens; o orfao esgotado tem de sair dela", pendentes+reivindicados)
	}
}

type alertaFalso struct {
	mu       sync.Mutex
	recebido []notify.Alerta
	erro     error
}

func (a *alertaFalso) Falhou(_ context.Context, al notify.Alerta) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.recebido = append(a.recebido, al)
	return a.erro
}

func (a *alertaFalso) total() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.recebido)
}

// O alerta sai UMA vez, quando o run desiste — nao a cada tentativa. Avisar em
// toda falha transformaria um retry bem-sucedido em dois alertas e um silencio,
// e canal que grita a toa deixa de ser lido.
func TestAlertaSaiUmaVezQuandoEsgotamAsTentativas(t *testing.T) {
	pool := banco(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	repo := postgres.NewRunRepo(pool)
	fila := queue.New(pool.Pool)

	r, err := repo.Criar(ctx, dom.Run{
		WorkflowSlug: "id_verification", IdempotencyKey: "falha",
		TriggerType: "schedule", Definicao: []byte(`{"Tags":["zarv","id","dbt"]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Transicionar(ctx, r.ID, dom.StatusQueued); err != nil {
		t.Fatal(err)
	}
	if err := fila.Enqueue(ctx, r.ID, 0, time.Time{}); err != nil {
		t.Fatal(err)
	}

	avisos := &alertaFalso{}
	d := scheduler.New(scheduler.Config{
		Worker: "t", MaxConcorrente: 1, MaxTentativas: 3,
		Intervalo: 10 * time.Millisecond, BackoffBase: time.Millisecond,
	}, fila, repo, func(context.Context, uuid.UUID) error {
		return errors.New(`step "run": saiu com codigo 2`)
	}, semLog())
	d.Alertas = avisos
	d.URLBase = "https://bravis.zarv.net"

	go func() { _ = d.Run(ctx) }()

	// Espera o run esgotar as tentativas.
	prazo := time.Now().Add(15 * time.Second)
	for time.Now().Before(prazo) {
		atual, _ := repo.Buscar(ctx, r.ID)
		if atual.Status == dom.StatusFailed && atual.Attempt >= 3 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	time.Sleep(100 * time.Millisecond)

	if n := avisos.total(); n != 1 {
		t.Fatalf("%d alertas para um run que falhou uma vez; quero exatamente 1", n)
	}

	a := avisos.recebido[0]
	if a.Workflow != "id_verification" || a.Trigger != "schedule" {
		t.Errorf("alerta sem os detalhes do run: %+v", a)
	}
	if len(a.Tags) != 3 || a.Tags[1] != "id" {
		t.Errorf("tags do snapshot nao chegaram: %v", a.Tags)
	}
	if !strings.Contains(a.Erro, "codigo 2") {
		t.Errorf("alerta sem a causa: %q", a.Erro)
	}
	if a.URLBase == "" || a.RunID != r.ID.String() {
		t.Errorf("alerta sem o link da execucao: %+v", a)
	}
}

// Webhook fora do ar nao pode parar o dispatcher: o run tem de terminar em
// FAILED e a fila continuar sendo consumida.
func TestFalhaAoAvisarNaoDerrubaODispatcher(t *testing.T) {
	pool := banco(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	repo := postgres.NewRunRepo(pool)
	fila := queue.New(pool.Pool)

	r, _ := repo.Criar(ctx, dom.Run{
		WorkflowSlug: "w", IdempotencyKey: "x", Definicao: []byte(`{}`),
	})
	_ = repo.Transicionar(ctx, r.ID, dom.StatusQueued)
	_ = fila.Enqueue(ctx, r.ID, 0, time.Time{})

	d := scheduler.New(scheduler.Config{
		Worker: "t", MaxConcorrente: 1, MaxTentativas: 1,
		Intervalo: 10 * time.Millisecond, BackoffBase: time.Millisecond,
	}, fila, repo, func(context.Context, uuid.UUID) error {
		return errors.New("falhou")
	}, semLog())
	d.Alertas = &alertaFalso{erro: errors.New("slack respondeu 500")}

	go func() { _ = d.Run(ctx) }()

	prazo := time.Now().Add(10 * time.Second)
	for time.Now().Before(prazo) {
		atual, _ := repo.Buscar(ctx, r.ID)
		if atual.Status == dom.StatusFailed {
			cancel()
			if pendentes, reivindicados, _ := fila.Tamanho(ctx); pendentes+reivindicados != 0 {
				t.Errorf("item ficou preso na fila apos o alerta falhar")
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("o run nao terminou: o erro do alerta travou o dispatcher")
}

func enfileirar(t *testing.T, repo *postgres.RunRepo, fila *queue.Queue,
	slug string, quantos, maxAtivos int) {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < quantos; i++ {
		r, err := repo.Criar(ctx, dom.Run{
			WorkflowSlug: slug, IdempotencyKey: fmt.Sprintf("%s-%d", slug, i),
			Definicao: []byte(`{}`), MaxAtivos: maxAtivos,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := repo.Transicionar(ctx, r.ID, dom.StatusQueued); err != nil {
			t.Fatal(err)
		}
		if err := fila.Enqueue(ctx, r.ID, 0, time.Time{}); err != nil {
			t.Fatal(err)
		}
	}
}

// O caso que motivou tudo: um `*/15` que leva 20 minutos se sobrepoe a si
// mesmo, e dois `dbt build` no MESMO modelo disputam a mesma tabela.
//
// Cinco itens do mesmo workflow com limite 1: o claim entrega UM, por mais
// vagas globais que haja.
func TestLimitePorWorkflowSegurraOsDemais(t *testing.T) {
	pool := banco(t)
	ctx := context.Background()
	repo := postgres.NewRunRepo(pool)
	fila := queue.New(pool.Pool)

	enfileirar(t, repo, fila, "id_verification_today", 5, 1)

	itens, err := fila.Claim(ctx, "w", 10) // dez vagas globais
	if err != nil {
		t.Fatal(err)
	}
	if len(itens) != 1 {
		t.Fatalf("claim entregou %d itens; o limite do workflow e 1", len(itens))
	}

	// Enquanto o primeiro nao termina, ninguem mais entra.
	outros, err := fila.Claim(ctx, "w", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(outros) != 0 {
		t.Errorf("entregou mais %d com o primeiro ainda em voo", len(outros))
	}

	// Terminado o primeiro, o proximo entra.
	if err := fila.Done(ctx, itens[0].ID); err != nil {
		t.Fatal(err)
	}
	seguintes, err := fila.Claim(ctx, "w", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(seguintes) != 1 {
		t.Errorf("apos liberar a vaga, o claim entregou %d; queria 1", len(seguintes))
	}
}

// Limite maior que 1 entrega exatamente o limite — nem menos (seria
// serializacao), nem mais.
func TestLimiteDeTresEntregaTres(t *testing.T) {
	pool := banco(t)
	repo := postgres.NewRunRepo(pool)
	fila := queue.New(pool.Pool)

	enfileirar(t, repo, fila, "vendors_x", 8, 3)

	itens, err := fila.Claim(context.Background(), "w", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(itens) != 3 {
		t.Errorf("claim entregou %d; o limite e 3", len(itens))
	}
}

// Um workflow no limite nao pode bloquear os outros: a fila e compartilhada, e
// travar tudo por causa de um seria pior que nao ter limite.
func TestWorkflowNoLimiteNaoBloqueiaOsOutros(t *testing.T) {
	pool := banco(t)
	repo := postgres.NewRunRepo(pool)
	fila := queue.New(pool.Pool)

	enfileirar(t, repo, fila, "travado", 5, 1)
	enfileirar(t, repo, fila, "livre_a", 2, 0)
	enfileirar(t, repo, fila, "livre_b", 2, 0)

	itens, err := fila.Claim(context.Background(), "w", 10)
	if err != nil {
		t.Fatal(err)
	}
	// 1 do travado + 4 dos livres.
	if len(itens) != 5 {
		t.Errorf("claim entregou %d; queria 5 (1 limitado + 4 sem limite)", len(itens))
	}
}

// Sem limite declarado (0), nada muda — o comportamento antigo continua sendo o
// padrao.
func TestSemLimiteEntregaTudoQueCabe(t *testing.T) {
	pool := banco(t)
	repo := postgres.NewRunRepo(pool)
	fila := queue.New(pool.Pool)

	enfileirar(t, repo, fila, "sem_limite", 6, 0)

	itens, err := fila.Claim(context.Background(), "w", 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(itens) != 4 {
		t.Errorf("claim entregou %d; a vaga global era 4", len(itens))
	}
}
