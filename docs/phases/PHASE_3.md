# PHASE 3 — LOCAL GO EXECUTOR

Concluída em 2026-09-01.

## Critério de aceite

A §37 define: uma DAG Go executando `A → (B + C) → D`, com paralelismo.

```
ordem: [task_a task_b task_c task_d] | duracao: 173ms | pico: 2
```

Ordem topológica respeitada, e **pico de concorrência 2** — `b` e `c` rodaram
juntas. Em série o total seria 320ms (10+150+150+10); em paralelo, 173ms. O teste
falha se passar de 280ms, então uma regressão para execução serial é detectada.

## Implemented

| item | onde |
|---|---|
| Task Registry (§14) | `internal/execution/registry.go` |
| Local Go Executor | `internal/execution/local/gotask.go` |
| Context cancellation | ambos os executores |
| Timeout | `TaskExec.Timeout`, nos dois executores |
| Retry por nó | `internal/application/execution` |
| Event streaming | `Input.Log` → canal de eventos |

## Architecture Decisions

**O registry é a fronteira da §14.** O plano exige *"não executar código
arbitrário recebido pela API; tasks locais devem ser compiladas e registradas no
runtime"*. O registry torna isso estrutural: o YAML só pode citar o **nome** de
algo que já está no binário, nunca fornecer o código. É a diferença entre
`action:` (nome registrado) e `run:` (comando arbitrário, restrito ao modo local
pela emenda à §3).

**Registro duplicado é recusado, não sobrescrito.** Substituição silenciosa é um
bug que só aparece em produção, quando a task errada roda.

**Task desconhecida lista as disponíveis.** `task "daly_sync" não registrada
(disponíveis: [daily_sync])` denuncia o erro de digitação de imediato.

**Pânico é contido.** Uma task Go roda no **mesmo processo**, diferente de um pod:
sem `recover`, um pânico derrubaria o orquestrador e todas as outras execuções
junto. O pânico vira falha daquela task.

**`Input.Log` não bloqueia.** Se ninguém consome os eventos, a task segue — uma
task ruidosa não pode travar por causa do consumidor.

**O runner escolhe o executor, não o executor.** `run:` vai para o de processo,
`action:` para o registry Go. Cada executor continua ignorando a existência do
outro.

**Retry é por nó, não só por Run.** O dispatcher da PHASE 2 repete o Run inteiro;
aqui a repetição é do passo. Refazer o workflow porque um `notify.sh` falhou
desperdiçaria o trabalho já concluído. E cancelamento **não** dispara retry —
repetir contra um cancelamento é desperdício.

**Timeout padrão é zero (sem limite).** Impor um valor arbitrário mataria tasks
legitimamente longas; quem conhece a task declara o limite.

## Tests

`go test ./...` passa. Os de integração pulam sem `BREVIS_TEST_DATABASE_URL`.

- **registry**: duplicado recusado, nome vazio recusado, `Nomes()` ordenado,
  `Input.Texto` valida ausência e tipo
- **executor Go**: roda task registrada e emite o `Log`; task desconhecida lista
  as disponíveis; pânico contido; timeout interrompe; erro da task propagado
- **runner**: o critério de aceite; retry por nó com sucesso na 3ª tentativa;
  desistência exata no limite; cancelamento sem retry; step sem executor falha
  explicitamente

## Benchmarks

Ainda não formalizados. Um dado do critério de aceite serve de referência: a DAG
de 4 tasks fecha em 173ms contra 320ms em série — o overhead do runner sobre o
trabalho útil é de poucos milissegundos.

O que justifica benchmark de verdade é a comparação que motivou o projeto: task
Go in-process contra pod. Os números do lado pod já existem, medidos no Leoflow
(cold start 5s na imagem enxuta, 38s na completa). Falta medir o lado Go sob
carga — natural na PHASE 4, com o scheduler disparando volume real.

## Known Limitations

- **Nenhuma task registrada de fábrica.** `docker.run` e `kubernetes.run` do
  YAML de exemplo continuam sem implementação; falham citando as disponíveis.
  São os itens 5 e 6 de `docs/gaps-yaml-vs-plano.md`.
- **O `brevis run` usa registry vazio.** Tasks Go são registradas por quem compila
  o binário; o CLI genérico não conhece nenhuma. Falta um ponto de extensão
  documentado para quem embute o Brevis.
- **Timeout e retry não vêm do YAML.** São flags de `brevis run`
  (`--timeout`, `--retries`), não campos por step — item 9 dos gaps.
- **O runner não persiste nada.** Ele e o dispatcher da PHASE 2 ainda não se
  falam: `task_runs` segue sem ser populado.
- Steps já iniciados no mesmo nível seguem até o fim quando outro falha.

## Next Phase

**PHASE 4 — SCHEDULER**: cron, missed runs e backfill (§11 e §12). É o que
transforma o `schedule: "0 2 * * *"` do YAML — hoje lido e ignorado — em execução.
