-- +goose Up
-- Runs, task_runs e a fila persistente da PHASE 2.
--
-- A secao 8 do plano e explicita: "Nunca depender exclusivamente de in-memory
-- channel para jobs criticos". A fila mora no Postgres; o dispatcher em memoria
-- apenas a consome.

CREATE TABLE runs (
    id               UUID PRIMARY KEY,
    workflow_slug    TEXT        NOT NULL,

    -- Chave de idempotencia (secao 29). O caso que ela resolve: o scheduler cria
    -- o Run, morre antes de registrar, reinicia e tentaria criar de novo. Com a
    -- unique, a segunda tentativa colide em vez de duplicar.
    idempotency_key  TEXT        NOT NULL UNIQUE,

    status           TEXT        NOT NULL,
    attempt          INT         NOT NULL DEFAULT 0,

    -- Snapshot do grafo no instante do disparo (secao 22): editar o workflow
    -- depois nao pode mudar o significado de uma execucao passada.
    definicao        JSONB       NOT NULL,

    erro             TEXT        NOT NULL DEFAULT '',
    criado_em        TIMESTAMPTZ NOT NULL DEFAULT now(),
    iniciado_em      TIMESTAMPTZ,
    terminado_em     TIMESTAMPTZ
);

CREATE INDEX runs_status_idx ON runs (status) WHERE status NOT IN ('success', 'canceled');

CREATE TABLE task_runs (
    id            UUID PRIMARY KEY,
    run_id        UUID        NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    node_id       TEXT        NOT NULL,
    status        TEXT        NOT NULL,
    attempt       INT         NOT NULL DEFAULT 0,
    exit_code     INT,
    erro          TEXT        NOT NULL DEFAULT '',
    iniciado_em   TIMESTAMPTZ,
    terminado_em  TIMESTAMPTZ,

    UNIQUE (run_id, node_id, attempt)
);

CREATE TABLE queue_items (
    id            BIGSERIAL PRIMARY KEY,
    run_id        UUID        NOT NULL REFERENCES runs(id) ON DELETE CASCADE,

    -- Maior roda antes. Retry entra com prioridade menor que trabalho novo para
    -- nao monopolizar a fila.
    prioridade    INT         NOT NULL DEFAULT 0,

    -- Backoff de retry: o item existe mas so e visivel depois deste instante.
    disponivel_em TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- Marca de posse. NULL = livre. Preenchido = algum dispatcher reivindicou.
    reivindicado_em  TIMESTAMPTZ,
    reivindicado_por TEXT,

    criado_em     TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- Um run nao pode estar duas vezes na fila.
    UNIQUE (run_id)
);

-- Indice do caminho quente: o claim busca itens livres, disponiveis, na ordem de
-- prioridade. Parcial para nao indexar o que ja foi reivindicado.
CREATE INDEX queue_pendentes_idx
    ON queue_items (prioridade DESC, disponivel_em, id)
    WHERE reivindicado_em IS NULL;

-- +goose Down
DROP TABLE queue_items;
DROP TABLE task_runs;
DROP TABLE runs;
