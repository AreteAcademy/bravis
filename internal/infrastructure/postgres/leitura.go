package postgres

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
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
	IniciadoEm   *time.Time
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
	TemAgenda    bool
	UltimoSlot   *time.Time
	UltimoStatus string
	TotalRuns    int

	// Da ultima execucao — a coluna "Latest Run" da lista.
	UltimaRunID *string
	UltimaRunEm *time.Time

	Tags []string

	// ProximaRun nao vem do banco: e calculada a partir do cron, no consumidor.
	// Guardar no banco exigiria recalcular a cada mudanca de agenda e conviver
	// com o valor obsoleto entre uma e outra.
	ProximaRun *time.Time
}

// Indicadores e o cabecalho do Overview.
type Indicadores struct {
	Total        int
	Sucesso      int
	Falha        int
	EmExecucao   int
	Pendentes    int
	DuracaoMedia time.Duration
}

// Razao devolve o percentual de `parte` sobre o total ja concluido.
//
// O denominador exclui o que ainda esta correndo: contar uma run em andamento
// como "nao-sucesso" faz a taxa despencar durante um pico de trabalho e subir
// sozinha depois, sem que nada tenha mudado.
func (i Indicadores) Razao(parte int) float64 {
	concluidas := i.Sucesso + i.Falha
	if concluidas == 0 {
		return 0
	}
	return float64(parte) * 100 / float64(concluidas)
}

// Balde e uma coluna do grafico de execucoes.
type Balde struct {
	Inicio       time.Time
	Sucesso      int
	Falha        int
	Executando   int
	Fila         int
	DuracaoMedia time.Duration
}

// Total soma o balde inteiro — a altura da coluna.
func (b Balde) Total() int { return b.Sucesso + b.Falha + b.Executando + b.Fila }

// AgendaResumo e o minimo para calcular o proximo disparo.
type AgendaResumo struct {
	WorkflowSlug string
	Cron         string
	Timezone     string
	Ativo        bool
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
	return varrerRuns(linhas)
}

// varrerRuns le as colunas que UltimasRuns e EmAndamento selecionam, na mesma
// ordem. Duas varreduras identicas divergiriam na primeira coluna nova.
func varrerRuns(linhas pgx.Rows) ([]ResumoRun, error) {
	var out []ResumoRun
	for linhas.Next() {
		var r ResumoRun
		var ini, fim *time.Time
		if err := linhas.Scan(&r.ID, &r.WorkflowSlug, &r.Status, &r.TriggerType, &r.Tentativa,
			&r.LogicalDate, &r.CriadoEm, &ini, &fim, &r.Erro); err != nil {
			return nil, err
		}
		r.IniciadoEm = ini
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
		       COALESCE(s.catchup, false), COALESCE(s.ativo, false),
		       s.workflow_slug IS NOT NULL, s.ultimo_slot,
		       COALESCE(u.status, ''), u.id::text, u.criado_em,
		       (SELECT count(*) FROM runs WHERE workflow_slug = w.slug),
		       -- A chave Tags pode nao existir (workflow publicado antes do
		       -- campo) ou vir como JSON null (sem tags). Nos dois casos a
		       -- expansao estoura com "cannot extract elements from a scalar" e
		       -- derruba a lista inteira por causa de UMA linha. O CASE
		       -- normaliza para array vazio antes de expandir.
		       COALESCE((
		           SELECT array_agg(t) FROM jsonb_array_elements_text(
		               CASE WHEN jsonb_typeof(w.definicao->'Tags') = 'array'
		                    THEN w.definicao->'Tags' ELSE '[]'::jsonb END) t
		       ), '{}')
		FROM workflows w
		JOIN projects p ON p.id = w.project_id
		LEFT JOIN schedules s ON s.workflow_slug = w.slug
		-- LATERAL em vez de subselect por coluna: assim id, status e instante da
		-- ultima execucao vem da MESMA linha. Tres subselects independentes
		-- poderiam misturar runs diferentes.
		LEFT JOIN LATERAL (
		    SELECT id, status, criado_em FROM runs
		    WHERE workflow_slug = w.slug ORDER BY criado_em DESC LIMIT 1
		) u ON true
		ORDER BY w.slug`)
	if err != nil {
		return nil, err
	}
	defer linhas.Close()

	var out []ResumoWorkflow
	for linhas.Next() {
		var w ResumoWorkflow
		if err := linhas.Scan(&w.Slug, &w.Nome, &w.Projeto, &w.Cron, &w.Timezone,
			&w.Catchup, &w.Ativo, &w.TemAgenda, &w.UltimoSlot, &w.UltimoStatus,
			&w.UltimaRunID, &w.UltimaRunEm, &w.TotalRuns, &w.Tags); err != nil {
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

// Indicadores agrega a janela recente para os quatro cartoes do topo.
//
// Uma consulta so, com FILTER, em vez de quatro: sao quatro varreduras da mesma
// tabela sobre o mesmo predicado de tempo.
func (r *LeituraRepo) Indicadores(ctx context.Context, janela time.Duration) (Indicadores, error) {
	var i Indicadores
	var mediaMs *float64
	err := r.pool.QueryRow(ctx, `
		SELECT count(*),
		       count(*) FILTER (WHERE status = 'success'),
		       count(*) FILTER (WHERE status = 'failed'),
		       count(*) FILTER (WHERE status IN ('running', 'retrying')),
		       count(*) FILTER (WHERE status = 'queued'),
		       avg(EXTRACT(EPOCH FROM (terminado_em - iniciado_em)) * 1000)
		         FILTER (WHERE terminado_em IS NOT NULL AND iniciado_em IS NOT NULL)
		FROM runs
		WHERE criado_em >= now() - $1::interval`, janela).
		Scan(&i.Total, &i.Sucesso, &i.Falha, &i.EmExecucao, &i.Pendentes, &mediaMs)
	if err != nil {
		return i, err
	}
	if mediaMs != nil {
		i.DuracaoMedia = time.Duration(*mediaMs) * time.Millisecond
	}
	return i, nil
}

// ExecucoesPorHora devolve uma coluna por hora, INCLUSIVE as vazias.
//
// O `generate_series` a esquerda e o ponto: sem ele, uma hora sem execucao
// simplesmente nao apareceria e o grafico comprimiria o tempo, dando a impressao
// de atividade continua onde houve um buraco.
func (r *LeituraRepo) ExecucoesPorHora(ctx context.Context, horas int) ([]Balde, error) {
	linhas, err := r.pool.Query(ctx, `
		WITH janela AS (
		    SELECT generate_series(
		        date_trunc('hour', now()) - make_interval(hours => $1 - 1),
		        date_trunc('hour', now()),
		        interval '1 hour') AS inicio
		)
		SELECT j.inicio,
		       count(r.id) FILTER (WHERE r.status = 'success'),
		       count(r.id) FILTER (WHERE r.status = 'failed'),
		       count(r.id) FILTER (WHERE r.status IN ('running', 'retrying')),
		       count(r.id) FILTER (WHERE r.status = 'queued'),
		       avg(EXTRACT(EPOCH FROM (r.terminado_em - r.iniciado_em)) * 1000)
		FROM janela j
		LEFT JOIN runs r
		       ON date_trunc('hour', r.criado_em) = j.inicio
		GROUP BY j.inicio
		ORDER BY j.inicio`, horas)
	if err != nil {
		return nil, err
	}
	defer linhas.Close()

	var out []Balde
	for linhas.Next() {
		var b Balde
		var mediaMs *float64
		if err := linhas.Scan(&b.Inicio, &b.Sucesso, &b.Falha, &b.Executando,
			&b.Fila, &mediaMs); err != nil {
			return nil, err
		}
		if mediaMs != nil {
			b.DuracaoMedia = time.Duration(*mediaMs) * time.Millisecond
		}
		out = append(out, b)
	}
	return out, linhas.Err()
}

// EmAndamento lista o que esta correndo ou esperando vez, mais antigo primeiro.
//
// A ordem e crescente de proposito: quem esta ha mais tempo na fila e o que
// merece atencao, e ordenar pelo mais recente esconderia exatamente isso.
func (r *LeituraRepo) EmAndamento(ctx context.Context, limite int) ([]ResumoRun, error) {
	linhas, err := r.pool.Query(ctx, `
		SELECT id::text, workflow_slug, status, trigger_type, attempt,
		       logical_date, criado_em, iniciado_em, terminado_em, erro
		FROM runs
		WHERE status IN ('queued', 'running', 'retrying')
		ORDER BY criado_em
		LIMIT $1`, limite)
	if err != nil {
		return nil, err
	}
	defer linhas.Close()
	return varrerRuns(linhas)
}

// Agendas devolve todas as agendas, ativas ou nao. A lista de DAGs mostra as
// pausadas tambem — some-las da tela seria esconder o motivo de nada rodar.
func (r *LeituraRepo) Agendas(ctx context.Context) ([]AgendaResumo, error) {
	linhas, err := r.pool.Query(ctx,
		`SELECT workflow_slug, cron, timezone, ativo FROM schedules ORDER BY workflow_slug`)
	if err != nil {
		return nil, err
	}
	defer linhas.Close()

	var out []AgendaResumo
	for linhas.Next() {
		var a AgendaResumo
		if err := linhas.Scan(&a.WorkflowSlug, &a.Cron, &a.Timezone, &a.Ativo); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, linhas.Err()
}

// RunsDoWorkflow lista as execucoes de um workflow so, para a tela dele.
func (r *LeituraRepo) RunsDoWorkflow(ctx context.Context, slug string, limite int) ([]ResumoRun, error) {
	linhas, err := r.pool.Query(ctx, `
		SELECT id::text, workflow_slug, status, trigger_type, attempt,
		       logical_date, criado_em, iniciado_em, terminado_em, erro
		FROM runs
		WHERE workflow_slug = $1
		ORDER BY criado_em DESC
		LIMIT $2`, slug, limite)
	if err != nil {
		return nil, err
	}
	defer linhas.Close()
	return varrerRuns(linhas)
}

// FiltroRuns e a consulta da tela de execucoes. Campos vazios nao filtram.
type FiltroRuns struct {
	Estado   string
	Workflow string
	De       *time.Time
	Ate      *time.Time
	Limite   int
	Offset   int
}

// where monta o predicado e os argumentos juntos, para que um nunca saia de
// sincronia com o outro — o jeito mais comum de errar SQL dinamico.
func (f FiltroRuns) where() (string, []any) {
	cond := []string{"true"}
	var args []any
	poe := func(sql string, valor any) {
		args = append(args, valor)
		cond = append(cond, fmt.Sprintf(sql, len(args)))
	}
	if f.Estado != "" {
		poe("status = $%d", f.Estado)
	}
	if f.Workflow != "" {
		poe("workflow_slug = $%d", f.Workflow)
	}
	if f.De != nil {
		poe("criado_em >= $%d", *f.De)
	}
	if f.Ate != nil {
		poe("criado_em < $%d", *f.Ate)
	}
	return strings.Join(cond, " AND "), args
}

// Runs lista execucoes com filtro e paginacao.
func (r *LeituraRepo) Runs(ctx context.Context, f FiltroRuns) ([]ResumoRun, error) {
	if f.Limite <= 0 {
		f.Limite = 50
	}
	predicado, args := f.where()
	args = append(args, f.Limite, f.Offset)

	linhas, err := r.pool.Query(ctx, fmt.Sprintf(`
		SELECT id::text, workflow_slug, status, trigger_type, attempt,
		       logical_date, criado_em, iniciado_em, terminado_em, erro
		FROM runs
		WHERE %s
		ORDER BY criado_em DESC
		LIMIT $%d OFFSET $%d`, predicado, len(args)-1, len(args)), args...)
	if err != nil {
		return nil, err
	}
	defer linhas.Close()
	return varrerRuns(linhas)
}

// ContarRuns devolve o total do MESMO filtro, para a paginacao saber quantas
// paginas existem.
func (r *LeituraRepo) ContarRuns(ctx context.Context, f FiltroRuns) (int, error) {
	predicado, args := f.where()
	var n int
	err := r.pool.QueryRow(ctx,
		fmt.Sprintf(`SELECT count(*) FROM runs WHERE %s`, predicado), args...).Scan(&n)
	return n, err
}
