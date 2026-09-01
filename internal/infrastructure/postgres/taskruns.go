package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"

	dom "github.com/zarvhq/bravis/internal/domain/run"
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
	tentativa int, status dom.Status, exit *int, erro string) error {

	_, err := r.pool.Exec(ctx, `
		UPDATE task_runs
		SET status = $4, exit_code = $5, erro = $6, terminado_em = now()
		WHERE run_id = $1 AND node_id = $2 AND attempt = $3`,
		runID, nodeID, tentativa, status, exit, erro)
	return err
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
