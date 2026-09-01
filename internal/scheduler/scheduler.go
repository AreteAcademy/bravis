package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	dom "github.com/zarvhq/bravis/internal/domain/run"
	sch "github.com/zarvhq/bravis/internal/domain/schedule"
	"github.com/zarvhq/bravis/internal/infrastructure/postgres"
	"github.com/zarvhq/bravis/internal/queue"
)

// Scheduler materializa slots em Runs e os enfileira.
//
// Ele CRIA runs e nada mais. Quem executa e o Dispatcher, consumindo a fila —
// a §37 e explicita em nao misturar as duas responsabilidades. Na pratica isso
// significa que o Scheduler pode cair sem interromper nenhuma execucao em voo, e
// o Dispatcher pode cair sem perder nenhum slot.
type Scheduler struct {
	agendas   *postgres.ScheduleRepo
	workflows *postgres.WorkflowRepo
	runs      *postgres.RunRepo
	fila      *queue.Queue
	log       *slog.Logger

	intervalo   time.Duration
	maxPorCiclo int

	// Prioridade menor que a de trabalho novo: um backfill grande nao pode
	// atrasar a operacao corrente.
	prioridadeBackfill int
}

// OpcoesScheduler parametriza o laco.
type OpcoesScheduler struct {
	Intervalo   time.Duration
	MaxPorCiclo int
}

func NewScheduler(a *postgres.ScheduleRepo, w *postgres.WorkflowRepo, r *postgres.RunRepo,
	f *queue.Queue, log *slog.Logger, o OpcoesScheduler) *Scheduler {

	if o.Intervalo <= 0 {
		o.Intervalo = 10 * time.Second
	}
	if o.MaxPorCiclo <= 0 {
		// Teto por agenda e por ciclo: um workflow parado por meses com
		// catchup=true criaria milhares de runs de uma vez e afogaria a fila.
		o.MaxPorCiclo = 100
	}
	return &Scheduler{
		agendas: a, workflows: w, runs: r, fila: f, log: log,
		intervalo: o.Intervalo, maxPorCiclo: o.MaxPorCiclo, prioridadeBackfill: -10,
	}
}

// Run avalia as agendas periodicamente ate o contexto ser cancelado.
func (s *Scheduler) Run(ctx context.Context) error {
	tick := time.NewTicker(s.intervalo)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
			if n, err := s.Ciclo(ctx, time.Now()); err != nil {
				s.log.Error("ciclo do scheduler", "erro", err)
			} else if n > 0 {
				s.log.Info("runs criados", "quantidade", n)
			}
		}
	}
}

// Ciclo avalia todas as agendas uma vez. Exportado para ser testavel com um
// instante fixo, sem esperar o relogio.
func (s *Scheduler) Ciclo(ctx context.Context, agora time.Time) (int, error) {
	agendas, err := s.agendas.Ativas(ctx)
	if err != nil {
		return 0, err
	}

	var criados int
	for _, a := range agendas {
		n, err := s.materializar(ctx, a, agora)
		if err != nil {
			// Uma agenda com cron invalido nao pode impedir as outras de rodar.
			s.log.Error("materializando agenda", "workflow", a.WorkflowSlug, "erro", err)
			continue
		}
		criados += n
	}
	return criados, nil
}

func (s *Scheduler) materializar(ctx context.Context, a sch.Schedule, agora time.Time) (int, error) {
	slots, truncado, err := a.Slots(agora, s.maxPorCiclo)
	if err != nil {
		return 0, err
	}
	if truncado {
		// Visivel, nao silencioso: truncar sem avisar faz parecer que a lacuna
		// foi coberta.
		s.log.Warn("slots truncados no ciclo", "workflow", a.WorkflowSlug,
			"limite", s.maxPorCiclo, "obs", "o restante entra nos proximos ciclos")
	}
	if len(slots) == 0 {
		return 0, nil
	}

	def, err := s.workflows.Definicao(ctx, a.WorkflowSlug)
	if err != nil {
		return 0, err
	}
	bruto, err := json.Marshal(def)
	if err != nil {
		return 0, err
	}

	var criados int
	for _, slot := range slots {
		if err := s.criarEEnfileirar(ctx, a.WorkflowSlug, bruto, slot,
			sch.TriggerSchedule, 0); err != nil {
			return criados, err
		}
		criados++

		// Avanca o marcador a CADA slot, e nao ao fim do laco: se o processo
		// cair no meio, os slots ja materializados nao sao recriados.
		if err := s.agendas.AvancarSlot(ctx, a.WorkflowSlug, slot); err != nil {
			return criados, err
		}
	}
	return criados, nil
}

// criarEEnfileirar cria o Run e o coloca na fila.
//
// A chave de idempotencia e `slug:trigger:slot`. E o que torna o scheduler
// seguro sob reinicio: se ele cair depois de criar o Run e antes de avancar o
// marcador, a tentativa seguinte colide na unique em vez de duplicar — o caso
// exato da secao 29.
func (s *Scheduler) criarEEnfileirar(ctx context.Context, slug string, def []byte,
	slot time.Time, trigger sch.TriggerType, prioridade int) error {

	chave := fmt.Sprintf("%s:%s:%s", slug, trigger, slot.UTC().Format(time.RFC3339))

	r, err := s.runs.Criar(ctx, dom.Run{
		WorkflowSlug:   slug,
		IdempotencyKey: chave,
		Definicao:      def,
		TriggerType:    string(trigger),
		LogicalDate:    &slot,
	})
	if err != nil {
		if errors.Is(err, postgres.ErrJaExiste) {
			return nil // ja materializado: nada a fazer
		}
		return err
	}

	if err := s.runs.Transicionar(ctx, r.ID, dom.StatusQueued); err != nil {
		return err
	}
	return s.fila.Enqueue(ctx, r.ID, prioridade, time.Now())
}

// Backfill materializa os slots de um intervalo passado.
//
// Entra na fila como qualquer outro run, respeitando concorrencia e prioridade —
// a secao 12 e explicita nisso. A prioridade negativa faz o backfill ceder a vez
// para trabalho corrente em vez de competir com ele.
func (s *Scheduler) Backfill(ctx context.Context, slug string, de, ate time.Time) (int, error) {
	agendas, err := s.agendas.Ativas(ctx)
	if err != nil {
		return 0, err
	}

	var alvo *sch.Schedule
	for i := range agendas {
		if agendas[i].WorkflowSlug == slug {
			alvo = &agendas[i]
			break
		}
	}
	if alvo == nil {
		return 0, fmt.Errorf("workflow %q nao tem agenda ativa", slug)
	}

	cronSched, loc, err := alvo.Parse()
	if err != nil {
		return 0, err
	}

	def, err := s.workflows.Definicao(ctx, slug)
	if err != nil {
		return 0, err
	}
	bruto, err := json.Marshal(def)
	if err != nil {
		return 0, err
	}

	// Um instante ANTES de `de`, para que um slot exatamente em `de` entre:
	// `Next(t)` devolve o proximo estritamente depois de `t`, entao comecar em
	// `de` excluiria o slot das 00:00 num backfill de um dia inteiro.
	var criados int
	for cursor := de.In(loc).Add(-time.Nanosecond); ; {
		prox := cronSched.Next(cursor)
		if prox.After(ate) {
			break
		}
		if err := s.criarEEnfileirar(ctx, slug, bruto, prox,
			sch.TriggerBackfill, s.prioridadeBackfill); err != nil {
			return criados, err
		}
		criados++
		cursor = prox
	}

	// O backfill NAO mexe em ultimo_slot: ele preenche o passado, e avancar o
	// marcador faria o scheduler pular slots futuros que ainda nao aconteceram.
	return criados, nil
}
