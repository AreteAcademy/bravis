# PHASE 1 — DOMAIN CORE (+ execução local)

Concluída em 2026-08-31.

## Desvio de escopo, declarado

A §37 põe o executor na PHASE 3 e a run engine na PHASE 2. Esta fase **alcança as
duas**, por decisão explícita: o pedido era que o `daily-report.yaml` rodasse na
própria instância, e isso não é possível só com o domínio.

O que foi feito das fases adiante, e o que **não** foi:

| | feito | não feito |
|---|---|---|
| Phase 2 | percurso do grafo por níveis | fila, persistência de run, task_runs, eventos no banco |
| Phase 3 | `ProcessExecutor` | registry de tasks Go in-process |

Sem fila e sem persistência, uma execução existe só enquanto o processo vive.
Isso é suficiente para `brevis run` local e insuficiente para qualquer coisa
agendada — que é o que a PHASE 2 resolve.

## Implemented

| item | onde |
|---|---|
| Modelo de domínio (Workflow, Node, Edge) | `internal/domain/workflow` |
| Validação do grafo (§5) | idem |
| Parser do YAML e desaçúcar de `chain` | `internal/application/workflow` |
| Ordenação topológica por níveis | `internal/graph` |
| Interface `Executor` (§13) | `internal/execution` |
| `ProcessExecutor` | `internal/execution/local` |
| Runner local do grafo | `internal/application/execution` |
| `brevis validate`, `brevis run` | `cmd/brevis` |

## Architecture Decisions

**`chain` é açúcar, não um segundo motor.** O parser converte a cadeia em arestas;
o runtime conhece apenas DAG. Um formato a mais no arquivo, zero caminho a mais
no código.

**Sem `type`, assume `dag`.** Um arquivo sem `depends_on` vira nós soltos, que
rodam em paralelo. `chain` impõe ordem e por isso precisa ser pedido.

**Níveis topológicos, não lista linear.** Uma ordenação topológica simples
serializaria `gold_metrics` e `gold_users`, que são independentes. Agrupar por
nível preserva o paralelismo declarado — medido: 4 steps com dois `sleep 1`
paralelos terminam em 1,9s, não em 2s+.

**O erro de ciclo mostra o caminho** (`a -> b -> c -> a`). Saber que existe ciclo
não ajuda; saber quais steps o fecham, sim.

**A fronteira do `ProcessExecutor` é código.** `New()` recusa-se a construir fora
de `BREVIS_ENV=local`, e a recusa é testada. Registrada como emenda à §3 do plano,
não como exceção silenciosa.

**O executor não herda o ambiente do pai.** O orquestrador carrega credenciais que
uma task não deve enxergar por acidente. Só `PATH` e `HOME` são repassados pelo
`brevis run`, e há teste provando que uma variável do processo pai não vaza.

**stdout e stderr chegam separados.** Juntá-los perde de onde a mensagem veio —
foi exatamente isso que fez o resumo final do dbt aparecer como erro no Leoflow.

**A falha para o nível seguinte.** Continuar depois de um erro produz resultado
parcial que parece completo. É o modo de falha que deixou uma pipeline 28 dias
atrasada sem ninguém ver, no sistema que este substitui.

**`action:` falha explicitamente.** Ainda não há executor para ela; pular em
silêncio seria pior que o erro, porque o workflow reportaria sucesso sem ter feito
o trabalho.

## Tests

`go test ./...` passa. 26 testes.

- **domínio**: ids duplicados, dependência inexistente, auto-dependência, ciclo com caminho, forma de execução (`run` xor `action`), workflow vazio
- **parser**: o `daily-report.yaml` do autor exatamente como escrito; DAG com fan-out/fan-in; `depends_on` recusado em `chain`; `type` desconhecido; ausência de `type`; erro citando o arquivo
- **grafo**: cadeia, paralelismo preservado, sem arestas, ciclo montado em código
- **executor**: recusa fora do local (tipada), sucesso, separação stdout/stderr, exit code, ambiente não herdado, cancelamento

Validação manual ponta a ponta:

```
brevis run pipeline.yaml   -> 4 steps, 2 em paralelo, 1,9s
brevis run falha.yaml      -> para no step 2, o 3 nao roda
brevis validate examples/  -> ok nos dois arquivos
```

## Benchmarks

Nenhum ainda. O caminho quente aqui é I/O de processo, não CPU. Benchmark passa a
fazer sentido na PHASE 2, com fila e concorrência — a §34 define o que medir.

## Known Limitations

- **Sem persistência.** A execução vive no processo; nada vai para o banco. Os
  `runs` e `task_runs` da §22 são PHASE 2.
- **Sem fila, sem retry, sem timeout.** Um step que trava trava o workflow.
- **`action:` não implementada** — `docker.run` e `kubernetes.run` falham com erro
  claro. Ver `docs/gaps-yaml-vs-plano.md`, itens 5 e 6.
- **`schedule` é lido e ignorado.** Não há scheduler (PHASE 4).
- **Sem `project`.** O domínio da §4 tem Project → Workflow; o YAML não o menciona.
- A falha para o nível, mas os steps já iniciados **no mesmo nível** seguem até o
  fim. Cancelá-los exigiria propagar cancelamento — decisão para a PHASE 2.

## Next Phase

**PHASE 2 — QUEUE + RUN ENGINE**: `runs` e `task_runs` persistidos, fila, máquina
de estados da §7, retries e idempotência. É o que transforma `brevis run` numa
execução observável e recuperável.
