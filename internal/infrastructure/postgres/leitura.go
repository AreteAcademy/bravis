package postgres

import (
	"context"
	"time"
)

// Este arquivo concentra as consultas de LEITURA que a UI faz.
//
// Separado dos repositorios de escrita de proposito: a UI precisa de projecoes
// achatadas e agregados, que nao correspondem as entidades de dominio. Misturar
// as duas coisas faria o dominio carregar campos que so existem para a tela.

// ResumoRun e uma linha da lista de execucoes.
type ResumoRun struct {
	ID           string
	WorkflowSlug string
	Status       string
	TriggerType  string
	Tentativa    int
	LogicalDate  *time.Time
	CriadoEm     time.Time
	Duracao      *time.Duration
	Erro         string
}

// ResumoWorkflow junta o workflow, sua agenda e o estado da ultima execucao.
type ResumoWorkflow struct {
	Slug         string
	Nome         string
	Projeto      string
	Cron         string
	Timezone     string
	Catchup      bool
	Ativo        bool
	UltimoSlot   *time.Time
	UltimoStatus string
	TotalRuns    int
}

// ResumoProjeto conta o que existe sob um projeto.
type ResumoProjeto struct {
	Slug      string
	Nome      string
	Workflows int
	Runs      int
	CriadoEm  time.Time
}

// LeituraRepo serve a UI.
type LeituraRepo struct{ pool *Pool }

func NewLeituraRepo(p *Pool) *LeituraRepo { return &LeituraRepo{pool: p} }

// ContagemPorStatus alimenta os cartoes do dashboard.
func (r *LeituraRepo) ContagemPorStatus(ctx context.Context) (map[string]int, error) {
	linhas, err := r.pool.Query(ctx, `SELECT status, count(*) FROM runs GROUP BY status`)
	if err != nil {
		return nil, err
	}
	defer linhas.Close()

	out := map[string]int{}
	for linhas.Next() {
		var s string
		var n int
		if err := linhas.Scan(&s, &n); err != nil {
			return nil, err
		}
		out[s] = n
	}
	return out, linhas.Err()
}

// UltimasRuns lista as execucoes mais recentes.
func (r *LeituraRepo) UltimasRuns(ctx context.Context, limite int) ([]ResumoRun, error) {
	linhas, err := r.pool.Query(ctx, `
		SELECT id::text, workflow_slug, status, trigger_type, attempt,
		       logical_date, criado_em, iniciado_em, terminado_em, erro
		FROM runs
		ORDER BY criado_em DESC
		LIMIT $1`, limite)
	if err != nil {
		return nil, err
	}
	defer linhas.Close()

	var out []ResumoRun
	for linhas.Next() {
		var r ResumoRun
		var ini, fim *time.Time
		if err := linhas.Scan(&r.ID, &r.WorkflowSlug, &r.Status, &r.TriggerType, &r.Tentativa,
			&r.LogicalDate, &r.CriadoEm, &ini, &fim, &r.Erro); err != nil {
			return nil, err
		}
		// Duracao so existe quando a execucao de fato comecou E terminou;
		// calcular com um dos lados nulo produziria numero sem significado.
		if ini != nil && fim != nil {
			d := fim.Sub(*ini)
			r.Duracao = &d
		}
		out = append(out, r)
	}
	return out, linhas.Err()
}

// Workflows lista os workflows publicados com sua agenda e ultimo estado.
func (r *LeituraRepo) Workflows(ctx context.Context) ([]ResumoWorkflow, error) {
	linhas, err := r.pool.Query(ctx, `
		SELECT w.slug, w.name, p.slug,
		       COALESCE(s.cron, ''), COALESCE(s.timezone, ''),
		       COALESCE(s.catchup, false), COALESCE(s.ativo, false), s.ultimo_slot,
		       COALESCE((SELECT status FROM runs WHERE workflow_slug = w.slug
		                 ORDER BY criado_em DESC LIMIT 1), ''),
		       (SELECT count(*) FROM runs WHERE workflow_slug = w.slug)
		FROM workflows w
		JOIN projects p ON p.id = w.project_id
		LEFT JOIN schedules s ON s.workflow_slug = w.slug
		ORDER BY w.slug`)
	if err != nil {
		return nil, err
	}
	defer linhas.Close()

	var out []ResumoWorkflow
	for linhas.Next() {
		var w ResumoWorkflow
		if err := linhas.Scan(&w.Slug, &w.Nome, &w.Projeto, &w.Cron, &w.Timezone,
			&w.Catchup, &w.Ativo, &w.UltimoSlot, &w.UltimoStatus, &w.TotalRuns); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, linhas.Err()
}

// Projetos lista os projetos com seus totais.
func (r *LeituraRepo) Projetos(ctx context.Context) ([]ResumoProjeto, error) {
	linhas, err := r.pool.Query(ctx, `
		SELECT p.slug, p.name, p.created_at,
		       (SELECT count(*) FROM workflows w WHERE w.project_id = p.id),
		       (SELECT count(*) FROM runs r
		         JOIN workflows w2 ON w2.slug = r.workflow_slug AND w2.project_id = p.id)
		FROM projects p
		ORDER BY p.slug`)
	if err != nil {
		return nil, err
	}
	defer linhas.Close()

	var out []ResumoProjeto
	for linhas.Next() {
		var p ResumoProjeto
		if err := linhas.Scan(&p.Slug, &p.Nome, &p.CriadoEm, &p.Workflows, &p.Runs); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, linhas.Err()
}

// ProfundidadeDaFila mostra a fila no dashboard.
func (r *LeituraRepo) ProfundidadeDaFila(ctx context.Context) (pendentes, reivindicados int, err error) {
	err = r.pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE reivindicado_em IS NULL),
		       count(*) FILTER (WHERE reivindicado_em IS NOT NULL)
		FROM queue_items`).Scan(&pendentes, &reivindicados)
	return
}
