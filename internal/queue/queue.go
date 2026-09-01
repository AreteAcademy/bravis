// Package queue e a fila persistente da secao 8 do plano.
//
// A fila mora no Postgres, nao em canal de memoria: "Nunca depender
// exclusivamente de in-memory channel para jobs criticos". Um processo que morre
// com itens em canal perde trabalho; um que morre com itens em tabela nao.
//
// O claim usa `FOR UPDATE SKIP LOCKED`, que e o padrao para fila em Postgres:
// varios dispatchers competem pela mesma tabela sem bloquear uns aos outros e
// sem entregar o mesmo item duas vezes. A alternativa — SELECT seguido de UPDATE
// — tem corrida entre as duas instrucoes.
package queue

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Item e uma entrada da fila.
type Item struct {
	ID           int64
	RunID        uuid.UUID
	Prioridade   int
	DisponivelEm time.Time
}

// Queue opera sobre queue_items.
type Queue struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Queue { return &Queue{pool: pool} }

// Enqueue coloca um run na fila.
//
// `ON CONFLICT DO NOTHING` na unique de run_id: enfileirar duas vezes o mesmo
// run e no-op, nao erro. E o comportamento que a secao 29 pede — a operacao
// tolera repeticao.
func (q *Queue) Enqueue(ctx context.Context, runID uuid.UUID, prioridade int, disponivelEm time.Time) error {
	_, err := q.pool.Exec(ctx, `
		INSERT INTO queue_items (run_id, prioridade, disponivel_em)
		VALUES ($1, $2, $3)
		ON CONFLICT (run_id) DO NOTHING`,
		runID, prioridade, disponivelEm)
	if err != nil {
		return fmt.Errorf("enfileirando run %s: %w", runID, err)
	}
	return nil
}

// Claim reivindica ate `limite` itens para este worker.
//
// O limite e como a concorrencia e imposta: o dispatcher pede apenas as vagas
// que tem livres. Nao existe caminho em que mais itens saiam da fila do que a
// concorrencia permite, porque quem conta as vagas e quem pede.
func (q *Queue) Claim(ctx context.Context, worker string, limite int) ([]Item, error) {
	if limite <= 0 {
		return nil, nil
	}

	linhas, err := q.pool.Query(ctx, `
		UPDATE queue_items
		SET reivindicado_em = now(), reivindicado_por = $1
		WHERE id IN (
			SELECT id FROM queue_items
			WHERE reivindicado_em IS NULL
			  AND disponivel_em <= now()
			ORDER BY prioridade DESC, disponivel_em, id
			LIMIT $2
			FOR UPDATE SKIP LOCKED
		)
		RETURNING id, run_id, prioridade, disponivel_em`,
		worker, limite)
	if err != nil {
		return nil, fmt.Errorf("reivindicando itens: %w", err)
	}
	defer linhas.Close()

	var itens []Item
	for linhas.Next() {
		var it Item
		if err := linhas.Scan(&it.ID, &it.RunID, &it.Prioridade, &it.DisponivelEm); err != nil {
			return nil, err
		}
		itens = append(itens, it)
	}
	return itens, linhas.Err()
}

// Done remove o item: o trabalho terminou e nao volta.
func (q *Queue) Done(ctx context.Context, id int64) error {
	_, err := q.pool.Exec(ctx, `DELETE FROM queue_items WHERE id = $1`, id)
	return err
}

// Release devolve o item a fila, disponivel apos `atraso`.
//
// Usado no retry e quando um dispatcher e interrompido antes de concluir: o item
// volta a ficar livre em vez de ficar preso a um worker que morreu.
func (q *Queue) Release(ctx context.Context, id int64, atraso time.Duration) error {
	_, err := q.pool.Exec(ctx, `
		UPDATE queue_items
		SET reivindicado_em = NULL, reivindicado_por = NULL, disponivel_em = now() + $2::interval
		WHERE id = $1`,
		id, fmt.Sprintf("%d milliseconds", atraso.Milliseconds()))
	return err
}

// Recuperar devolve a fila os itens reivindicados ha mais tempo que `limite`.
//
// E a rede de seguranca contra worker morto: sem isso, um item reivindicado por
// um processo que caiu ficaria preso para sempre. Era exatamente o modo de falha
// das execucoes zumbis que travaram pipelines por 33 dias no sistema anterior.
func (q *Queue) Recuperar(ctx context.Context, limite time.Duration) (int64, error) {
	tag, err := q.pool.Exec(ctx, `
		UPDATE queue_items
		SET reivindicado_em = NULL, reivindicado_por = NULL
		WHERE reivindicado_em IS NOT NULL
		  AND reivindicado_em < now() - $1::interval`,
		fmt.Sprintf("%d milliseconds", limite.Milliseconds()))
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// Tamanho conta os itens pendentes e os reivindicados, para observabilidade.
func (q *Queue) Tamanho(ctx context.Context) (pendentes, reivindicados int, err error) {
	err = q.pool.QueryRow(ctx, `
		SELECT
			count(*) FILTER (WHERE reivindicado_em IS NULL),
			count(*) FILTER (WHERE reivindicado_em IS NOT NULL)
		FROM queue_items`).Scan(&pendentes, &reivindicados)
	return
}
