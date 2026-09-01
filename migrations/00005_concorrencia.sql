-- +goose Up
-- Limite de execucoes simultaneas POR WORKFLOW (o `concurrency.limit` do Kestra).
--
-- 36 dos 51 flows do repositorio de dados declaravam esse limite, cinco deles em
-- cadencia de 15 ou 30 minutos. Sem ele, um `*/15` que leva 20 minutos se
-- sobrepoe a si mesmo — dois `dbt build` no MESMO modelo, ao mesmo tempo.
--
-- A coluna vive no RUN, e nao so no workflow, pelo mesmo motivo de `definicao`:
-- e um snapshot. Baixar o limite de 3 para 1 nao pode mudar o significado de
-- runs que ja estavam na fila. E, na pratica, tira um JOIN com `workflows` do
-- caminho mais quente do sistema — a consulta de claim.
ALTER TABLE runs ADD COLUMN max_ativos INT NOT NULL DEFAULT 0;

COMMENT ON COLUMN runs.max_ativos IS
  'Execucoes simultaneas permitidas para este workflow. 0 = sem limite.';

-- O claim passa a agrupar itens reivindicados por workflow.
CREATE INDEX queue_items_reivindicados_idx ON queue_items (run_id)
  WHERE reivindicado_em IS NOT NULL;

-- +goose Down
DROP INDEX queue_items_reivindicados_idx;
ALTER TABLE runs DROP COLUMN max_ativos;
