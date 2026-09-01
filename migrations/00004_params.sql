-- +goose Up
-- Parametros de execucao (secao 12: um Run precisa registrar POR QUE e COM QUE
-- valores rodou).
--
-- Coluna propria, e nao dentro de `definicao`: a definicao e o snapshot do
-- GRAFO, imutavel; os params sao a entrada daquela execucao. Misturar os dois
-- faria dois disparos do mesmo workflow terem snapshots diferentes sem que nada
-- no workflow tivesse mudado.
ALTER TABLE runs ADD COLUMN params JSONB NOT NULL DEFAULT '{}'::jsonb;

-- +goose Down
ALTER TABLE runs DROP COLUMN params;
