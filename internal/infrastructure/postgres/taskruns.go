package postgres

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"time"

	"github.com/google/uuid"

	dom "github.com/AreteAcademy/brevis/internal/domain/run"
)

// Este arquivo fecha uma divida aberta desde a PHASE 2: a tabela `task_runs`
// existia no schema mas nunca era populada, entao o retry era por Run e nao
// havia estado por passo. A visualizacao da DAG precisa exatamente disso.

// IniciarTask registra o inicio de um passo.
//
// `ON CONFLICT DO UPDATE` na chave (run, node, tentativa): reexecutar o mesmo
// passo na mesma tentativa e idempotente, o que importa quando o dispatcher
// recupera um item de worker morto e o refaz.
func (r *RunRepo) IniciarTask(ctx context.Context, runID uuid.UUID, nodeID string, tentativa int) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO task_runs (id, run_id, node_id, status, attempt, iniciado_em)
		VALUES ($1, $2, $3, $4, $5, now())
		ON CONFLICT (run_id, node_id, attempt) DO UPDATE
		SET status = EXCLUDED.status, iniciado_em = now(), terminado_em = NULL, erro = ''`,
		uuid.New(), runID, nodeID, dom.StatusRunning, tentativa)
	return err
}

// TerminarTask registra o desfecho.
func (r *RunRepo) TerminarTask(ctx context.Context, runID uuid.UUID, nodeID string,
	tentativa int, status dom.Status, exit *int, erro string, log string) error {

	_, err := r.pool.Exec(ctx, `
		UPDATE task_runs
		SET status = $4, exit_code = $5, erro = $6, log = $7, terminado_em = now()
		WHERE run_id = $1 AND node_id = $2 AND attempt = $3`,
		runID, nodeID, tentativa, status, exit, erro, log)
	return err
}

// PassoJaTeveSucesso responde se este passo, neste workflow, ja terminou bem
// antes — em qualquer run anterior.
//
// E o que decide se a execucao atual e a PRIMEIRA daquele passo, informacao
// que vai para o ambiente do passo e que o SDK usa para criar a tabela de
// destino. A alternativa seria o SDK inferir de "a tabela nao existe", e aí
// alguem apaga a tabela por engano e a proxima execucao se acha a primeira.
//
// Por (workflow, passo), nao por workflow: um workflow com tres fetchers
// escrevendo em tres tabelas criaria apenas a do primeiro passo se a resposta
// fosse do workflow inteiro.
//
// `exceto` e o run corrente, excluido para que a propria tentativa em curso
// nao conte como sucesso anterior.
func (r *RunRepo) PassoJaTeveSucesso(ctx context.Context, workflowSlug, nodeID string, exceto uuid.UUID) (bool, error) {
	var existe bool
	err := r.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM task_runs t
			JOIN runs rn ON rn.id = t.run_id
			WHERE rn.workflow_slug = $1
			  AND t.node_id = $2
			  AND t.run_id <> $3
			  AND t.status = $4
		)`, workflowSlug, nodeID, exceto, dom.StatusSuccess).Scan(&existe)
	return existe, err
}

// EstadoDosNos devolve o estado de cada no na ULTIMA tentativa de cada um.
//
// `DISTINCT ON` em vez de max(attempt) num subselect: a tentativa mais recente e
// a que interessa na tela, e uma tentativa antiga que falhou nao deve pintar o
// no de vermelho depois de o retry ter dado certo.
func (r *RunRepo) EstadoDosNos(ctx context.Context, runID uuid.UUID) (map[string]EstadoNo, error) {
	linhas, err := r.pool.Query(ctx, `
		SELECT DISTINCT ON (node_id)
		       node_id, status, attempt, exit_code, erro, iniciado_em, terminado_em
		FROM task_runs
		WHERE run_id = $1
		ORDER BY node_id, attempt DESC`, runID)
	if err != nil {
		return nil, err
	}
	defer linhas.Close()

	out := map[string]EstadoNo{}
	for linhas.Next() {
		var e EstadoNo
		var ini, fim *time.Time
		if err := linhas.Scan(&e.NodeID, &e.Status, &e.Tentativa, &e.ExitCode,
			&e.Erro, &ini, &fim); err != nil {
			return nil, err
		}
		if ini != nil && fim != nil {
			d := fim.Sub(*ini)
			e.DuracaoMs = d.Milliseconds()
		}
		out[e.NodeID] = e
	}
	return out, linhas.Err()
}

// EstadoNo e o estado de um passo, para a UI.
type EstadoNo struct {
	NodeID    string `json:"node_id"`
	Status    string `json:"status"`
	Tentativa int    `json:"attempt"`
	ExitCode  *int   `json:"exit_code,omitempty"`
	Erro      string `json:"erro,omitempty"`
	DuracaoMs int64  `json:"duracao_ms"`
}

// LogDoPasso e a saida de uma tentativa, para a tela da execucao.
type LogDoPasso struct {
	NodeID    string
	Tentativa int
	Status    string
	ExitCode  *int
	Erro      string
	Log       string
	DuracaoMs int64
}

// LogsDaRun devolve a saida de cada tentativa de cada passo, em ordem de
// execucao.
//
// TODAS as tentativas, nao so a ultima: quando um passo passa na segunda, o que
// explica a primeira falha esta justamente na tentativa que a tela descartaria.
func (r *RunRepo) LogsDaRun(ctx context.Context, runID uuid.UUID) ([]LogDoPasso, error) {
	linhas, err := r.pool.Query(ctx, `
		SELECT node_id, attempt, status, exit_code, erro, log,
		       COALESCE(EXTRACT(EPOCH FROM (terminado_em - iniciado_em)) * 1000, 0)::bigint
		FROM task_runs
		WHERE run_id = $1
		ORDER BY iniciado_em NULLS LAST, node_id, attempt`, runID)
	if err != nil {
		return nil, err
	}
	defer linhas.Close()

	var out []LogDoPasso
	for linhas.Next() {
		var p LogDoPasso
		if err := linhas.Scan(&p.NodeID, &p.Tentativa, &p.Status, &p.ExitCode,
			&p.Erro, &p.Log, &p.DuracaoMs); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, linhas.Err()
}

// PassoQueFalhou devolve o node e a saida da ultima tentativa que falhou.
//
// `ORDER BY iniciado_em DESC` e nao `attempt DESC`: num grafo com varios passos,
// a maior tentativa pode ser de um passo que ja tinha falhado e sido superado —
// o que interessa e o que falhou POR ULTIMO, que e onde a execucao parou.
//
// Ausencia nao e erro: um run que morreu antes de qualquer passo comecar (imagem
// inexistente, fila cancelada) nao tem task_run nenhuma, e o alerta sai sem esta
// parte em vez de nao sair.
func (r *RunRepo) PassoQueFalhou(ctx context.Context, runID uuid.UUID) (string, string, error) {
	var passo, log string
	err := r.pool.QueryRow(ctx, `
		SELECT node_id, log
		FROM task_runs
		WHERE run_id = $1 AND status = $2
		ORDER BY iniciado_em DESC NULLS LAST, attempt DESC
		LIMIT 1`, runID, dom.StatusFailed).Scan(&passo, &log)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", nil
	}
	return passo, log, err
}
