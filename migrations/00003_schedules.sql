-- +goose Up
-- Agendas e a origem de cada Run.
--
-- A secao 37 separa as responsabilidades: o scheduler CRIA runs, a fila as
-- EXECUTA. Por isso `schedules` nao tem nada de execucao — nem status de run,
-- nem contador de tentativa.

-- A definicao do grafo passa a viver no banco (secao 22: "Nunca depender
-- exclusivamente do arquivo YAML apos o workflow ser publicado").
ALTER TABLE workflows ADD COLUMN definicao JSONB NOT NULL DEFAULT '{}'::jsonb;

-- Por que o Run existe (secao 12). Sem isso nao da para distinguir um backfill
-- de uma execucao agendada ao investigar um incidente.
ALTER TABLE runs ADD COLUMN trigger_type TEXT NOT NULL DEFAULT 'manual';

-- O slot logico que este Run representa. Nulo para disparo manual, que nao
-- pertence a nenhum slot.
ALTER TABLE runs ADD COLUMN logical_date TIMESTAMPTZ;

CREATE TABLE schedules (
    id             UUID PRIMARY KEY,

    -- Uma agenda por workflow nesta fase. A secao 22 sugere N (um cron diario e
    -- outro de reconciliacao, por exemplo); quando isso for necessario, a unique
    -- sai e um nome de agenda entra.
    workflow_slug  TEXT        NOT NULL UNIQUE,

    cron           TEXT        NOT NULL,
    timezone       TEXT        NOT NULL DEFAULT 'UTC',
    catchup        BOOLEAN     NOT NULL DEFAULT false,
    ativo          BOOLEAN     NOT NULL DEFAULT true,

    -- Ultimo slot JA materializado em Run. E o que impede o scheduler de
    -- recriar a mesma lacuna a cada ciclo.
    ultimo_slot    TIMESTAMPTZ,

    criado_em      TIMESTAMPTZ NOT NULL DEFAULT now(),
    atualizado_em  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX schedules_ativos_idx ON schedules (ativo) WHERE ativo;

-- +goose Down
DROP TABLE schedules;
ALTER TABLE runs DROP COLUMN logical_date;
ALTER TABLE runs DROP COLUMN trigger_type;
ALTER TABLE workflows DROP COLUMN definicao;
