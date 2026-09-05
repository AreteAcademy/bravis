---
title: Scheduler e fila
description: Dois laços independentes — quem cria os runs, quem os executa, e por que estão separados.
group: Conceitos
order: 5
slug: scheduler-and-queue
---

O brevis.sh separa **criar** uma execução de **executá-la**. São dois laços que
correm juntos e não dependem um do outro.

```
scheduler                          fila
─────────                          ────
lê a agenda
materializa o slot
cria o Run  ──────────────────►  (pendente no banco)
                                   reivindica
                                   percorre o grafo
                                   executa cada passo
                                   grava estado e log
```

## Por que separados

Um único laço que agenda e executa tem uma propriedade ruim: quando a execução
trava, o agendamento para junto. As doze horas seguintes de slots simplesmente
não existem, e ninguém percebe até alguém procurar um dado que nunca chegou.

Com os dois separados:

- o **scheduler** pode cair e a fila continua drenando o que já foi criado;
- a **fila** pode cair e os slots continuam sendo materializados, para serem
  executados quando ela voltar;
- **reprocessar não depende do relógio** — `backfill` cria runs passados pelo
  mesmo caminho que o cron usaria.

## O processo

Os dois laços vivem no mesmo comando:

```bash
brevis scheduler --interval 5s --concurrency 4 --max-pods 10
```

| flag | padrão | |
|---|---|---|
| `--interval` | `10s` | intervalo entre ciclos do scheduler |
| `--concurrency` | `5` | **runs** simultâneos |
| `--max-pods` | `5` | **passos** simultâneos no total |

`--concurrency` e `--max-pods` contam coisas diferentes, e isso é deliberado:
cinco runs com três passos paralelos cada dariam quinze pods se o único limite
fosse o de runs.

:::note `serve` não materializa agendas
O processo da API sobe o mesmo scheduler, mas **sem o laço** — ele existe ali só
para atender o disparo manual da interface, e assim a regra "o scheduler cria os
runs" continua com um dono só. Para as agendas rodarem, é preciso um
`brevis scheduler` ao lado.
:::

## O snapshot da definição

Quando o scheduler cria um run, ele grava **a definição do workflow dentro do
run**. A execução lê esse snapshot, não o YAML em disco.

A razão é concreta: entre o disparo e a execução, alguém pode ter editado o
arquivo e feito deploy. Sem o snapshot, um run criado às 5h com uma definição
seria executado às 5h02 com outra — e o log não teria como explicar a
diferença.

## Retries

A tentativa é persistida, não é estado de processo. Uma reinicialização do
worker não apaga o que já se sabia sobre aquele run.

A tentativa **do run** entra no nome do pod. Sem isso, o retry recomeçaria o run
do zero, encontraria o pod da tentativa anterior com o mesmo nome e ficaria
preso em `Pending` para sempre.

## Backfill

Materializa slots passados de um workflow já publicado:

```bash
brevis backfill diario --from 2026-01-01 --to 2026-01-31
brevis backfill diario --from 2026-01-01 --to 2026-01-31 --param load_full=true
```

```
  31 run(s) de backfill enfileirados para diario (2026-01-01 a 2026-01-31)
  rode `brevis scheduler` para executa-los
```

**Enfileira, não executa.** Quem executa é o `scheduler` — de novo, a mesma
separação. `--to` inclui o dia inteiro.

## Alertas

```bash
export BREVIS_SLACK_WEBHOOK='https://hooks.slack.com/services/...'
export BREVIS_UI_URL='https://brevis.exemplo.com'
```

Sem o webhook, o processo avisa no boot que falhas não serão comunicadas. Uma
instalação que falha em silêncio costuma ser descoberta pelo cliente, não pelo
time.

`BREVIS_UI_URL` monta o link da execução dentro do alerta — sem ela, o aviso diz
o que falhou mas obriga quem lê a procurar a run na mão.

## Próximos passos

- [Pod por passo](/docs/pod-per-step/) — onde cada passo realmente executa
- [CLI](/docs/cli/) — todas as flags de `scheduler` e `backfill`
