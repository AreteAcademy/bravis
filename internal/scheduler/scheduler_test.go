package scheduler_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	dom "github.com/zarvhq/bravis/internal/domain/run"
	wf "github.com/zarvhq/bravis/internal/domain/workflow"
	"github.com/zarvhq/bravis/internal/infrastructure/postgres"
	"github.com/zarvhq/bravis/internal/queue"
	"github.com/zarvhq/bravis/internal/scheduler"
)

func emUTC(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

// montar prepara projeto, workflow publicado e o scheduler.
func montar(t *testing.T, cron string, catchup bool) (*scheduler.Scheduler, *postgres.RunRepo, *queue.Queue, *postgres.Pool) {
	t.Helper()
	pool := banco(t)
	ctx := context.Background()

	projeto := uuid.New()
	if _, err := pool.Exec(ctx,
		`INSERT INTO projects (id, slug, name) VALUES ($1, 'p', 'Projeto')`, projeto); err != nil {
		t.Fatal(err)
	}

	wRepo := postgres.NewWorkflowRepo(pool)
	w := wf.Workflow{
		Slug: "diario", Name: "diario", Kind: wf.KindChain, Schedule: cron,
		Nodes: []wf.Node{{ID: "a", Run: "echo ok"}},
	}
	if err := wRepo.Publicar(ctx, w, projeto); err != nil {
		t.Fatal(err)
	}
	if catchup {
		if _, err := pool.Exec(ctx,
			`UPDATE schedules SET catchup = true WHERE workflow_slug = 'diario'`); err != nil {
			t.Fatal(err)
		}
	}

	runs := postgres.NewRunRepo(pool)
	fila := queue.New(pool.Pool)
	s := scheduler.NewScheduler(postgres.NewScheduleRepo(pool), wRepo, runs, fila,
		semLog(), scheduler.OpcoesScheduler{})
	return s, runs, fila, pool
}

func fixarUltimoSlot(t *testing.T, pool *postgres.Pool, quando time.Time) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`UPDATE schedules SET ultimo_slot = $1 WHERE workflow_slug = 'diario'`, quando); err != nil {
		t.Fatal(err)
	}
}

// Publicar grava grafo e agenda juntos, e o scheduler materializa o slot.
func TestSchedulerCriaRunEEnfileira(t *testing.T) {
	s, runs, fila, pool := montar(t, "0 2 * * *", false)
	ctx := context.Background()
	fixarUltimoSlot(t, pool, emUTC("2026-01-01T02:00:00Z"))

	n, err := s.Ciclo(ctx, emUTC("2026-01-02T03:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("criou %d runs, queria 1", n)
	}

	contagem, _ := runs.ContarPorStatus(ctx)
	if contagem[dom.StatusQueued] != 1 {
		t.Errorf("queued = %d, queria 1", contagem[dom.StatusQueued])
	}
	porTrigger, _ := runs.ContarPorTrigger(ctx)
	if porTrigger["schedule"] != 1 {
		t.Errorf("trigger_type = %v, queria schedule", porTrigger)
	}
	pendentes, _, _ := fila.Tamanho(ctx)
	if pendentes != 1 {
		t.Errorf("fila tem %d, queria 1 — o scheduler cria E enfileira", pendentes)
	}
}

// O ponto da idempotencia: reexecutar o ciclo no mesmo instante nao pode
// duplicar. E o caso da secao 29 — o scheduler cai e sobe de novo.
func TestCicloRepetidoNaoDuplica(t *testing.T) {
	s, runs, _, pool := montar(t, "0 2 * * *", true)
	ctx := context.Background()
	fixarUltimoSlot(t, pool, emUTC("2026-01-01T02:00:00Z"))
	agora := emUTC("2026-01-04T03:00:00Z")

	primeiro, err := s.Ciclo(ctx, agora)
	if err != nil {
		t.Fatal(err)
	}
	segundo, err := s.Ciclo(ctx, agora)
	if err != nil {
		t.Fatal(err)
	}

	if primeiro != 3 { // 02, 03, 04
		t.Errorf("primeiro ciclo criou %d, queria 3", primeiro)
	}
	if segundo != 0 {
		t.Errorf("segundo ciclo criou %d, queria 0", segundo)
	}
	c, _ := runs.ContarPorStatus(ctx)
	if total := c[dom.StatusQueued]; total != 3 {
		t.Errorf("total de runs = %d, queria 3", total)
	}
}

// catchup=false so materializa o slot mais recente, mesmo com dias de lacuna.
func TestCatchupFalseNaoRefazOPassado(t *testing.T) {
	s, runs, _, pool := montar(t, "0 2 * * *", false)
	ctx := context.Background()
	fixarUltimoSlot(t, pool, emUTC("2026-01-01T02:00:00Z"))

	n, err := s.Ciclo(ctx, emUTC("2026-01-10T03:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("criou %d runs, queria 1 — catchup=false ignora a lacuna", n)
	}
	c, _ := runs.ContarPorStatus(ctx)
	if c[dom.StatusQueued] != 1 {
		t.Errorf("queued = %d, queria 1", c[dom.StatusQueued])
	}
}

// Backfill entra na fila como qualquer run, com trigger proprio e prioridade
// menor — a secao 12 exige que ele respeite concorrencia e prioridade.
func TestBackfillEntraNaFilaComPrioridadeMenor(t *testing.T) {
	s, runs, fila, pool := montar(t, "0 2 * * *", false)
	ctx := context.Background()
	fixarUltimoSlot(t, pool, emUTC("2026-03-01T02:00:00Z"))

	n, err := s.Backfill(ctx, "diario", emUTC("2026-01-01T00:00:00Z"), emUTC("2026-01-05T23:59:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	if n != 5 {
		t.Fatalf("backfill criou %d runs, queria 5", n)
	}

	porTrigger, _ := runs.ContarPorTrigger(ctx)
	if porTrigger["backfill"] != 5 {
		t.Errorf("trigger = %v, queria 5 backfill", porTrigger)
	}
	pendentes, _, _ := fila.Tamanho(ctx)
	if pendentes != 5 {
		t.Errorf("fila tem %d, queria 5", pendentes)
	}

	var prio int
	if err := pool.QueryRow(ctx, `SELECT min(prioridade) FROM queue_items`).Scan(&prio); err != nil {
		t.Fatal(err)
	}
	if prio >= 0 {
		t.Errorf("prioridade = %d; backfill deve ceder a vez ao trabalho corrente", prio)
	}
}

// O backfill preenche o passado e NAO pode avancar o marcador, senao o scheduler
// pularia slots futuros que ainda nao aconteceram.
func TestBackfillNaoAvancaOMarcador(t *testing.T) {
	s, _, _, pool := montar(t, "0 2 * * *", false)
	ctx := context.Background()
	marcador := emUTC("2026-03-01T02:00:00Z")
	fixarUltimoSlot(t, pool, marcador)

	if _, err := s.Backfill(ctx, "diario", emUTC("2026-01-01T00:00:00Z"), emUTC("2026-01-03T23:59:00Z")); err != nil {
		t.Fatal(err)
	}

	var depois time.Time
	if err := pool.QueryRow(ctx,
		`SELECT ultimo_slot FROM schedules WHERE workflow_slug = 'diario'`).Scan(&depois); err != nil {
		t.Fatal(err)
	}
	if !depois.Equal(marcador) {
		t.Errorf("ultimo_slot = %v, devia continuar %v", depois, marcador)
	}
}

// Republicar um workflow nao pode fazer o scheduler recriar slots ja
// materializados.
func TestRepublicarPreservaOMarcador(t *testing.T) {
	_, _, _, pool := montar(t, "0 2 * * *", false)
	ctx := context.Background()
	marcador := emUTC("2026-05-01T02:00:00Z")
	fixarUltimoSlot(t, pool, marcador)

	var projeto uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM projects LIMIT 1`).Scan(&projeto); err != nil {
		t.Fatal(err)
	}
	w := wf.Workflow{
		Slug: "diario", Name: "diario v2", Kind: wf.KindChain, Schedule: "0 3 * * *",
		Nodes: []wf.Node{{ID: "a", Run: "echo v2"}},
	}
	if err := postgres.NewWorkflowRepo(pool).Publicar(ctx, w, projeto); err != nil {
		t.Fatal(err)
	}

	var depois time.Time
	var cron string
	if err := pool.QueryRow(ctx,
		`SELECT ultimo_slot, cron FROM schedules WHERE workflow_slug = 'diario'`).Scan(&depois, &cron); err != nil {
		t.Fatal(err)
	}
	if !depois.Equal(marcador) {
		t.Errorf("ultimo_slot = %v; republicar nao pode recriar o passado", depois)
	}
	if cron != "0 3 * * *" {
		t.Errorf("cron = %q; o novo devia valer", cron)
	}
}

// Tirar o `schedule` do YAML deve desagendar, e nao deixar a agenda antiga viva.
func TestPublicarSemCronRemoveAAgenda(t *testing.T) {
	_, _, _, pool := montar(t, "0 2 * * *", false)
	ctx := context.Background()

	var projeto uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM projects LIMIT 1`).Scan(&projeto); err != nil {
		t.Fatal(err)
	}
	w := wf.Workflow{Slug: "diario", Name: "diario", Kind: wf.KindChain,
		Nodes: []wf.Node{{ID: "a", Run: "echo"}}}
	if err := postgres.NewWorkflowRepo(pool).Publicar(ctx, w, projeto); err != nil {
		t.Fatal(err)
	}

	var n int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM schedules WHERE workflow_slug = 'diario'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Error("a agenda deveria ter sido removida")
	}
}

// Um backfill de um dia com cron horario tem de dar 24 slots, nao 23: `Next(t)`
// devolve o proximo estritamente depois de `t`, entao comecar exatamente em
// `de` excluiria o slot da meia-noite.
func TestBackfillIncluiOSlotDaBorda(t *testing.T) {
	s, _, _, pool := montar(t, "0 * * * *", false)
	ctx := context.Background()
	fixarUltimoSlot(t, pool, emUTC("2026-06-01T00:00:00Z"))

	n, err := s.Backfill(ctx, "diario",
		emUTC("2026-01-01T00:00:00Z"), emUTC("2026-01-01T23:59:59Z"))
	if err != nil {
		t.Fatal(err)
	}
	if n != 24 {
		t.Errorf("backfill criou %d slots, queria 24 (00:00 a 23:00)", n)
	}
}
