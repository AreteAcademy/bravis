// Package scheduler contem o dispatcher da secao 27 do plano.
//
// O desenho e o da secao 8: fila PERSISTENTE (Postgres) mais dispatcher EM
// MEMORIA. O dispatcher nao guarda trabalho — ele reivindica da fila, executa e
// devolve o resultado. Se o processo morrer, os itens reivindicados voltam a
// ficar livres pelo `Recuperar`, e nada se perde.
package scheduler

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	dom "github.com/zarvhq/bravis/internal/domain/run"
	"github.com/zarvhq/bravis/internal/queue"
)

// Executar roda um Run. Injetado para que o dispatcher nao conheca executor,
// grafo nem YAML — o que torna a concorrencia testavel sem processo de verdade.
type Executar func(ctx context.Context, runID uuid.UUID) error

// Repo e o que o dispatcher precisa da persistencia. Interface pequena e
// declarada aqui, no consumidor, e nao no pacote que a implementa.
type Repo interface {
	Transicionar(ctx context.Context, id uuid.UUID, para dom.Status) error
	IncrementarTentativa(ctx context.Context, id uuid.UUID) (int, error)
	RegistrarErro(ctx context.Context, id uuid.UUID, msg string) error
}

// Config parametriza o dispatcher.
type Config struct {
	Worker         string
	MaxConcorrente int
	Intervalo      time.Duration
	MaxTentativas  int
	BackoffBase    time.Duration
}

func (c *Config) padroes() {
	if c.Worker == "" {
		c.Worker = "dispatcher"
	}
	if c.MaxConcorrente <= 0 {
		c.MaxConcorrente = 5
	}
	if c.Intervalo <= 0 {
		c.Intervalo = 200 * time.Millisecond
	}
	if c.MaxTentativas <= 0 {
		c.MaxTentativas = 3
	}
	if c.BackoffBase <= 0 {
		c.BackoffBase = time.Second
	}
}

// Dispatcher consome a fila respeitando a concorrencia maxima.
type Dispatcher struct {
	cfg      Config
	fila     *queue.Queue
	repo     Repo
	executar Executar
	log      *slog.Logger

	mu    sync.Mutex
	emVoo int
	wg    sync.WaitGroup
}

func New(cfg Config, f *queue.Queue, r Repo, e Executar, log *slog.Logger) *Dispatcher {
	cfg.padroes()
	return &Dispatcher{cfg: cfg, fila: f, repo: r, executar: e, log: log}
}

// Run consome a fila ate o contexto ser cancelado, e entao espera o trabalho em
// voo terminar antes de retornar.
func (d *Dispatcher) Run(ctx context.Context) error {
	tick := time.NewTicker(d.cfg.Intervalo)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			d.wg.Wait() // shutdown gracioso: nao abandona execucao em voo
			return nil
		case <-tick.C:
			if err := d.cicloDeClaim(ctx); err != nil {
				d.log.Error("ciclo de claim", "erro", err)
			}
		}
	}
}

// cicloDeClaim pede a fila APENAS as vagas livres.
//
// E aqui que a concorrencia e imposta, e o motivo de ser confiavel: nao existe
// caminho em que mais itens saiam da fila do que o limite permite, porque quem
// conta as vagas e quem faz o pedido. Um semaforo depois do claim deixaria itens
// reivindicados e parados, invisiveis para outros workers.
func (d *Dispatcher) cicloDeClaim(ctx context.Context) error {
	d.mu.Lock()
	vagas := d.cfg.MaxConcorrente - d.emVoo
	d.mu.Unlock()

	if vagas <= 0 {
		return nil
	}

	itens, err := d.fila.Claim(ctx, d.cfg.Worker, vagas)
	if err != nil {
		return err
	}

	for _, it := range itens {
		d.mu.Lock()
		d.emVoo++
		d.mu.Unlock()

		d.wg.Add(1)
		go func(it queue.Item) {
			defer d.wg.Done()
			defer func() {
				d.mu.Lock()
				d.emVoo--
				d.mu.Unlock()
			}()
			d.processar(ctx, it)
		}(it)
	}
	return nil
}

func (d *Dispatcher) processar(ctx context.Context, it queue.Item) {
	if err := d.repo.Transicionar(ctx, it.RunID, dom.StatusRunning); err != nil {
		// Transicao invalida aqui significa que outro dispatcher pegou o mesmo
		// run, ou que ele foi cancelado. Nao e erro nosso: solta e segue.
		d.log.Warn("nao pude marcar running", "run", it.RunID, "erro", err)
		_ = d.fila.Release(ctx, it.ID, 0)
		return
	}

	err := d.executar(ctx, it.RunID)
	if err == nil {
		if err := d.repo.Transicionar(ctx, it.RunID, dom.StatusSuccess); err != nil {
			d.log.Error("marcando success", "run", it.RunID, "erro", err)
		}
		_ = d.fila.Done(ctx, it.ID)
		return
	}

	d.falhar(ctx, it, err)
}

// falhar decide entre retry e desistencia.
func (d *Dispatcher) falhar(ctx context.Context, it queue.Item, causa error) {
	_ = d.repo.RegistrarErro(ctx, it.RunID, causa.Error())
	if err := d.repo.Transicionar(ctx, it.RunID, dom.StatusFailed); err != nil {
		d.log.Error("marcando failed", "run", it.RunID, "erro", err)
		_ = d.fila.Done(ctx, it.ID)
		return
	}

	tentativa, err := d.repo.IncrementarTentativa(ctx, it.RunID)
	if err != nil {
		d.log.Error("incrementando tentativa", "run", it.RunID, "erro", err)
		_ = d.fila.Done(ctx, it.ID)
		return
	}

	if tentativa >= d.cfg.MaxTentativas {
		// Esgotou: sai da fila e fica em FAILED, que nao e terminal na maquina
		// de estados mas e o fim desta execucao.
		d.log.Warn("tentativas esgotadas", "run", it.RunID, "tentativas", tentativa)
		_ = d.fila.Done(ctx, it.ID)
		return
	}

	if err := d.repo.Transicionar(ctx, it.RunID, dom.StatusRetrying); err != nil {
		d.log.Error("marcando retrying", "run", it.RunID, "erro", err)
		_ = d.fila.Done(ctx, it.ID)
		return
	}
	if err := d.repo.Transicionar(ctx, it.RunID, dom.StatusQueued); err != nil {
		d.log.Error("reenfileirando", "run", it.RunID, "erro", err)
		_ = d.fila.Done(ctx, it.ID)
		return
	}

	// Backoff exponencial. O item volta a fila com atraso, e nao imediatamente:
	// retry instantaneo contra dependencia fora do ar so gasta a fila.
	atraso := d.cfg.BackoffBase * time.Duration(1<<uint(tentativa-1))
	d.log.Info("reenfileirado", "run", it.RunID, "tentativa", tentativa, "atraso", atraso)
	if err := d.fila.Release(ctx, it.ID, atraso); err != nil {
		d.log.Error("devolvendo a fila", "run", it.RunID, "erro", err)
	}
}

// EmVoo devolve quantas execucoes estao correndo agora.
func (d *Dispatcher) EmVoo() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.emVoo
}
