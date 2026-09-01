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
// Enqueue poe o run na fila. `disponivelEm` zero significa AGORA, medido pelo
// relogio do BANCO.
//
// A diferenca importa: o relogio do processo pode estar alguns milissegundos a
// frente do relogio do Postgres, e um item gravado com `time.Now()` do
// aplicativo fica invisivel ate o banco alcanca-lo. Nao e perda — o proximo
// ciclo pega —, mas e latencia inexplicavel, e foi o que fez um teste de
// concorrencia entregar 4 itens onde 5 estavam prontos.
func (q *Queue) Enqueue(ctx context.Context, runID uuid.UUID, prioridade int, disponivelEm time.Time) error {
	var quando any = disponivelEm
	if disponivelEm.IsZero() {
		quando = nil // COALESCE resolve para now() do banco
	}
	_, err := q.pool.Exec(ctx, `
		INSERT INTO queue_items (run_id, prioridade, disponivel_em)
		VALUES ($1, $2, COALESCE($3::timestamptz, now()))
		ON CONFLICT (run_id) DO NOTHING`,
		runID, prioridade, quando)
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

	// O limite por workflow e imposto AQUI, na propria consulta de claim, pelo
	// mesmo motivo que a concorrencia global e imposta no pedido: nao existe
	// caminho em que mais itens saiam da fila do que o permitido. Reivindicar e
	// depois devolver seria uma janela em que dois dispatchers ja teriam pegado
	// o mesmo workflow.
	//
	// `em_voo` conta itens RE IVINDICADOS, e nao runs em `running`: entre o
	// claim e a transicao de estado ha um instante em que o run ainda esta
	// `queued`, e contar por status abriria exatamente essa fresta.
	//
	// `posicao` e o que impede o segundo problema: sem ele, tres itens do mesmo
	// workflow com limite 1 sairiam TODOS no mesmo lote, porque a contagem nao
	// muda no meio da consulta. Com a numeracao por workflow, o item so passa se
	// `em_voo + sua posicao` couber no limite.
	linhas, err := q.pool.Query(ctx, `
		WITH em_voo AS (
			SELECT r.workflow_slug, count(*) AS n
			FROM queue_items q
			JOIN runs r ON r.id = q.run_id
			WHERE q.reivindicado_em IS NOT NULL
			GROUP BY r.workflow_slug
		),
		elegiveis AS (
			SELECT q.id,
			       r.max_ativos,
			       COALESCE(v.n, 0) AS ja_em_voo,
			       row_number() OVER (
			           PARTITION BY r.workflow_slug
			           ORDER BY q.prioridade DESC, q.disponivel_em, q.id
			       ) AS posicao
			FROM queue_items q
			JOIN runs r ON r.id = q.run_id
			LEFT JOIN em_voo v ON v.workflow_slug = r.workflow_slug
			WHERE q.reivindicado_em IS NULL
			  AND q.disponivel_em <= now()
		)
		UPDATE queue_items
		SET reivindicado_em = now(), reivindicado_por = $1
		WHERE id IN (
			SELECT q.id
			FROM queue_items q
			JOIN elegiveis e ON e.id = q.id
			WHERE q.reivindicado_em IS NULL
			  AND q.disponivel_em <= now()
			  AND (e.max_ativos = 0 OR e.ja_em_voo + e.posicao <= e.max_ativos)
			ORDER BY q.prioridade DESC, q.disponivel_em, q.id
			LIMIT $2
			FOR UPDATE OF q SKIP LOCKED
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
func (q *Queue) Recuperar(ctx context.Context, limite time.Duration) ([]Item, error) {
	// Devolve os itens, e nao apenas a contagem: quem recupera precisa saber
	// QUAIS runs ficaram penduradas para corrigir tambem o estado delas. Com a
	// contagem sozinha, o item voltava para a fila mas o Run seguia "running"
	// para sempre — a metade do bug que isto conserta.
	linhas, err := q.pool.Query(ctx, `
		UPDATE queue_items
		SET reivindicado_em = NULL, reivindicado_por = NULL
		WHERE reivindicado_em IS NOT NULL
		  AND reivindicado_em < now() - $1::interval
		RETURNING id, run_id, prioridade`,
		fmt.Sprintf("%d milliseconds", limite.Milliseconds()))
	if err != nil {
		return nil, err
	}
	defer linhas.Close()

	var out []Item
	for linhas.Next() {
		var it Item
		if err := linhas.Scan(&it.ID, &it.RunID, &it.Prioridade); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, linhas.Err()
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
