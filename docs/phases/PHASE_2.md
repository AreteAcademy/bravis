# PHASE 2 — QUEUE + RUN ENGINE

Concluída em 2026-09-01.

## Critério de aceite

A §37 define:

> 100 runs queued, max concurrency = 5 → 5 RUNNING, 95 QUEUED. Sem perda.

Atingido:

```
runs: map[queued:95 running:5] | fila: 95 pendentes, 5 reivindicados | em voo: 5
```

E, ao liberar, as 100 chegam a `success` com a fila vazia. O teste também vigia o
**pico** de concorrência durante toda a corrida, não só no instante da medição —
um dispatcher que estourasse o limite por um momento passaria numa checagem
pontual.

## Implemented

| item | onde |
|---|---|
| Máquina de estados (§7) | `internal/domain/run` |
| Runs e TaskRuns | `internal/domain/run`, `internal/infrastructure/postgres` |
| Fila persistente (§8) | `internal/queue` + `migrations/00002_runs.sql` |
| Dispatcher (§27) | `internal/scheduler` |
| Limite de concorrência (§9) | idem |
| Retry com backoff | idem |
| Idempotência (§29) | chave única em `runs`, `ON CONFLICT` na fila |

## Architecture Decisions

**A fila é tabela, não canal.** A §8 é explícita: *"Nunca depender exclusivamente
de in-memory channel para jobs críticos"*. Um processo que morre com itens em
canal perde trabalho; com itens em tabela, não.

**`FOR UPDATE SKIP LOCKED` no claim.** É o padrão para fila em Postgres: vários
dispatchers competem sem bloquear uns aos outros e sem receber o mesmo item duas
vezes. A alternativa — `SELECT` seguido de `UPDATE` — tem corrida entre as duas
instruções. Testado com 4 workers concorrentes sobre 20 itens: cada um entregue
exatamente uma vez.

**A concorrência é imposta no pedido, não depois dele.** O dispatcher calcula as
vagas livres e pede à fila *apenas* essa quantidade. Um semáforo aplicado depois
do claim deixaria itens reivindicados e parados — invisíveis para outros workers,
que é como se perde trabalho sem perder registro.

**Estados são um tipo, transições são dados.** A §7 proíbe `running = true`. O
grafo de transições é um mapa, não uma cadeia de `if`, o que torna a máquina
inspecionável e o teste exaustivo trivial. `FAILED` **não** é terminal — quem
decide se há nova tentativa é a política de retry, não a máquina.

**A transição valida dentro da transação, com `FOR UPDATE`.** Ler o estado fora
dela permitiria que dois dispatchers lessem `queued` e ambos escrevessem
`running`.

**Reenfileirar limpa os carimbos de tempo.** A tentativa nova não herda o
`iniciado_em` da anterior; herdar reportaria a duração de outra execução.

**`Recuperar` devolve item de worker morto.** Sem isso, um item reivindicado por
um processo que caiu ficaria preso para sempre — exatamente o modo de falha das
execuções zumbis que travaram pipelines por 33 dias no sistema que este substitui.

**Backoff exponencial no retry.** Retry instantâneo contra dependência fora do ar
só gasta a fila. E o item de retry volta com prioridade menor que trabalho novo.

**A definição do grafo é copiada para o Run.** A §22 exige o snapshot: editar o
workflow depois não pode mudar o significado de uma execução passada.

## Tests

`go test ./...` passa. Os de integração exigem Postgres e **pulam** sem
`BREVIS_TEST_DATABASE_URL`, para que a suíte siga verde numa máquina sem docker.
`make test-int` roda tudo.

- **máquina de estados**: caminho feliz, ciclo de retry, limpeza de carimbos, sete
  transições inválidas (incluindo terminal que não volta e retry que não pula a
  fila), `FAILED` não terminal, cancelamento a partir de cada estado ativo
- **integração**: o critério de aceite; idempotência de `Criar` e de `Enqueue`;
  claim concorrente com 4 workers; recuperação de item de worker morto

## Benchmarks

Ainda não. O caminho quente é I/O de banco, e medir com `go test -bench` sem
carga realista produziria número enganoso. A §34 pede benchmark com baseline; o
lugar natural é junto ao scheduler da PHASE 4, com volume de fila de verdade.

Um dado observado, não medido rigorosamente: as 100 runs saem de `queued` a
`success` em menos de 1s com concorrência 5 e execução instantânea — o overhead
da fila não domina.

## Known Limitations

- **O dispatcher executa o Run inteiro como unidade.** `task_runs` existe no
  schema mas ainda não é populado: o retry é por Run, não por step. Retomar do
  passo que falhou é trabalho de uma fase posterior.
- **`Recuperar` não roda sozinho.** A função existe e é testada, mas ninguém a
  chama periodicamente. Precisa de um laço no processo — natural junto ao
  scheduler da PHASE 4.
- **Sem prioridade por projeto nem fairness** (§10). A fila ordena por prioridade
  e chegada; um projeto barulhento pode monopolizá-la.
- **Concorrência é global**, não por workflow nem por projeto, como a §9 prevê.
- **Sem eventos persistidos.** `execution_events` da §22 não existe ainda.

## Next Phase

**PHASE 3 — LOCAL GO EXECUTOR**: o registry de tasks Go in-process da §14. O
`ProcessExecutor` já foi feito na PHASE 1 por decisão registrada; falta a metade
Go, que é a que a §14 descreve.
