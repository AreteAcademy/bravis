-- +goose Up
-- Phase 0 cria apenas as duas entidades que provam o caminho ponta a ponta.
-- A secao 22 do plano lista dez (workflow_versions, runs, task_runs,
-- queue_items, ...); elas nascem nas fases que as usam — a regra 2 proibe
-- antecipar, e schema sem caso de uso envelhece errado.

CREATE TABLE projects (
    id          UUID PRIMARY KEY,
    slug        TEXT        NOT NULL UNIQUE,
    name        TEXT        NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE workflows (
    id          UUID PRIMARY KEY,
    project_id  UUID        NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    slug        TEXT        NOT NULL,
    name        TEXT        NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- slug e unico DENTRO do projeto, nao globalmente: dois projetos podem ter
    -- um workflow `daily_ingest` sem colidir.
    UNIQUE (project_id, slug)
);

-- +goose Down
DROP TABLE workflows;
DROP TABLE projects;
