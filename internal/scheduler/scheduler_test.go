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

	n, err := s.Backfill(ctx, "diario", emUTC("2026-01-01T00:00:00Z"), emUTC("2026-01-05T23:59:00Z"), nil)
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

	if _, err := s.Backfill(ctx, "diario", emUTC("2026-01-01T00:00:00Z"), emUTC("2026-01-03T23:59:00Z"), nil); err != nil {
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
		emUTC("2026-01-01T00:00:00Z"), emUTC("2026-01-01T23:59:59Z"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if n != 24 {
		t.Errorf("backfill criou %d slots, queria 24 (00:00 a 23:00)", n)
	}
}

// A pasta e a fonte da verdade — mas publicar so ADICIONAVA. Tirar um arquivo
// dali nao tirava nada do banco, e o scheduler seguia materializando runs de um
// workflow que ninguem enxergava mais. Com cron de 15 minutos, isso e trabalho
// invisivel rodando para sempre.
func TestPodarRemoveOQueSaiuDaPasta(t *testing.T) {
	pool := banco(t)
	ctx := context.Background()
	repo := postgres.NewWorkflowRepo(pool)
	agendas := postgres.NewScheduleRepo(pool)

	var projeto uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO projects (id, slug, name) VALUES ($1,'zarv','zarv') RETURNING id`,
		uuid.New()).Scan(&projeto); err != nil {
		t.Fatal(err)
	}

	fica := wf.Workflow{Slug: "id_verification", Name: "id", Schedule: "0 4 * * *",
		Nodes: []wf.Node{{ID: "run", Run: "dbt build"}}}
	sai := wf.Workflow{Slug: "vendors_ana_telemetry", Name: "ana", Schedule: "*/15 * * * *",
		Nodes: []wf.Node{{ID: "run", Run: "echo x"}}}
	for _, w := range []wf.Workflow{fica, sai} {
		if err := repo.Publicar(ctx, w, projeto); err != nil {
			t.Fatal(err)
		}
	}

	// Uma execucao antiga do que vai sair: o historico tem de sobreviver.
	if _, err := postgres.NewRunRepo(pool).Criar(ctx, dom.Run{
		WorkflowSlug: sai.Slug, IdempotencyKey: "antiga", Definicao: []byte(`{}`),
	}); err != nil {
		t.Fatal(err)
	}

	removidos, err := repo.Podar(ctx, projeto, []string{fica.Slug})
	if err != nil {
		t.Fatal(err)
	}
	if len(removidos) != 1 || removidos[0] != sai.Slug {
		t.Fatalf("removidos = %v, quero apenas %s", removidos, sai.Slug)
	}

	if _, err := repo.Definicao(ctx, sai.Slug); err == nil {
		t.Error("o workflow removido ainda tem definicao no banco")
	}
	if _, err := repo.Definicao(ctx, fica.Slug); err != nil {
		t.Errorf("o workflow que ficou sumiu: %v", err)
	}

	// A agenda vive numa tabela separada, ligada por slug em texto — o CASCADE
	// nao a alcanca, e uma agenda orfa continuaria criando runs.
	ativas, err := agendas.Ativas(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range ativas {
		if a.WorkflowSlug == sai.Slug {
			t.Error("a agenda do workflow removido sobreviveu e continuaria criando runs")
		}
	}

	var historico int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM runs WHERE workflow_slug = $1`, sai.Slug).Scan(&historico); err != nil {
		t.Fatal(err)
	}
	if historico != 1 {
		t.Errorf("historico apagado junto (%d runs); apagar a execucao seria apagar a evidencia", historico)
	}
}

// Sem nada para podar, nao mexe em nada.
func TestPodarSemDiferencaNaoRemoveNada(t *testing.T) {
	pool := banco(t)
	ctx := context.Background()
	repo := postgres.NewWorkflowRepo(pool)

	var projeto uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO projects (id, slug, name) VALUES ($1,'zarv','zarv') RETURNING id`,
		uuid.New()).Scan(&projeto); err != nil {
		t.Fatal(err)
	}
	w := wf.Workflow{Slug: "so_esse", Name: "x", Nodes: []wf.Node{{ID: "a", Run: "echo"}}}
	if err := repo.Publicar(ctx, w, projeto); err != nil {
		t.Fatal(err)
	}

	removidos, err := repo.Podar(ctx, projeto, []string{"so_esse"})
	if err != nil {
		t.Fatal(err)
	}
	if len(removidos) != 0 {
		t.Errorf("removeu %v sem motivo", removidos)
	}
}

// Uma agenda recem-publicada (`ultimo_slot` NULL) precisa comecar a disparar.
//
// Este e o teste que faltava, e a lacuna tinha forma: TODOS os casos acima
// chamam `fixarUltimoSlot` antes do ciclo, entao o caminho do marcador nulo
// nunca era exercitado. Em dev, 18 workflows ficaram registrados por horas sem
// uma unica execucao automatica — inclusive um `*/30` — porque `Slots` partia do
// proprio `agora`, o proximo horario do cron era sempre futuro, e o marcador
// nunca saia de NULL para quebrar o circulo.
func TestAgendaNovaComecaADisparar(t *testing.T) {
	s, runs, _, _ := montar(t, "*/30 * * * *", false)
	ctx := context.Background()

	// Primeiro ciclo: so planta o marco, sem executar o passado.
	n, err := s.Ciclo(ctx, emUTC("2026-01-01T10:05:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("o ciclo de estreia criou %d runs; o slot anterior ao registro nao e nosso", n)
	}

	// Segundo ciclo, depois que o relogio passou das 10:30: agora dispara.
	n, err = s.Ciclo(ctx, emUTC("2026-01-01T10:31:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("criou %d runs apos o horario do cron; queria 1", n)
	}
	porTrigger, _ := runs.ContarPorTrigger(ctx)
	if porTrigger["schedule"] != 1 {
		t.Errorf("trigger = %v; queria schedule", porTrigger)
	}
}

// O marco de estreia nao pode ser replantado a cada ciclo: se fosse, `de`
// avancaria junto com o relogio e a agenda voltaria a nunca disparar.
func TestMarcoDeEstreiaEPlantadoUmaVezSo(t *testing.T) {
	s, _, _, pool := montar(t, "*/30 * * * *", false)
	ctx := context.Background()

	if _, err := s.Ciclo(ctx, emUTC("2026-01-01T10:05:00Z")); err != nil {
		t.Fatal(err)
	}
	var primeiro time.Time
	if err := pool.QueryRow(ctx,
		`SELECT ultimo_slot FROM schedules WHERE workflow_slug = 'diario'`).Scan(&primeiro); err != nil {
		t.Fatal(err)
	}

	if _, err := s.Ciclo(ctx, emUTC("2026-01-01T10:10:00Z")); err != nil {
		t.Fatal(err)
	}
	var depois time.Time
	if err := pool.QueryRow(ctx,
		`SELECT ultimo_slot FROM schedules WHERE workflow_slug = 'diario'`).Scan(&depois); err != nil {
		t.Fatal(err)
	}
	if !depois.Equal(primeiro) {
		t.Errorf("o marco andou de %s para %s — a agenda nunca alcancaria um horario", primeiro, depois)
	}
}
