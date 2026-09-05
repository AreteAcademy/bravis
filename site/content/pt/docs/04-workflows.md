---
title: Workflows
description: O formato YAML completo — passos, dependências, imagens, recursos e agenda.
group: Conceitos
order: 4
slug: workflows
---

Um workflow é um arquivo YAML. Ele declara **o que executar**, **em que ordem**
e **com que runtime** — e nada além disso.

## Estrutura mínima

```yaml
name: hello
steps:
  - id: unico
    run: echo pronto
```

`name` é o identificador do workflow no banco e na interface. `steps` é a lista
de passos, cada um com um `id` único e um `run`.

## Ordem: chain ou dag

```yaml
type: chain   # a ordem é a do arquivo
type: dag     # a ordem vem de depends_on
```

`chain` é açúcar: o parser converte a sequência em arestas, e **o motor conhece
apenas DAG**. Use `dag` quando a ordem não for linear:

```yaml
type: dag

steps:
  - id: preparar
    run: ./preparar.sh

  # Irmãos: dependem do mesmo passo, então rodam em paralelo.
  - id: extrair
    run: ./extrair.sh
    depends_on: [preparar]

  - id: validar
    run: ./validar.sh
    depends_on: [preparar]

  - id: publicar
    run: ./publicar.sh
    depends_on: [extrair, validar]
```

O runner percorre o grafo **por níveis**: tudo dentro de um nível roda em
paralelo, e o nível seguinte só começa quando o anterior fecha inteiro.

:::warning Falha para o nível inteiro
O runner para na **primeira** falha do nível, sem iniciar o próximo. Continuar
depois de um erro produziria resultado parcial que parece completo.
:::

## Agenda

```yaml
schedule: "0 5 * * *"   # cron de cinco campos
concurrency: 1
```

Sem `schedule`, o workflow é manual: só roda por disparo na interface, por
`brevis run` ou por `backfill`.

`concurrency: 1` limita execuções simultâneas do mesmo workflow — é o que
impede um `*/15` de se sobrepor a si mesmo quando uma execução passa dos quinze
minutos.

## Imagem e recursos

```yaml
image: us-central1-docker.pkg.dev/exemplo/apps/dbt:1.10.3
resources:
  cpu: 200m
  memory: 1Gi
  limits: {memory: 2Gi}
```

Declarados no topo, valem como **padrão de todos os passos**. Cada passo pode
sobrescrever os seus:

```yaml
steps:
  - id: bronze
    run: dbt build --select bronze+          # herda a imagem do topo

  - id: notificar
    image: ghcr.io/exemplo/notify:0.3        # outro runtime
    shell: false                             # distroless não tem shell
    run: /notify --canal dados
    resources: {cpu: 25m, memory: 32Mi, limits: {memory: 64Mi}}
    depends_on: [bronze]
```

É isto que faz um fetcher em Go custar 12 MB e 32Mi ao lado de um `dbt build` de
1,9 GB, em vez de os dois pagarem o tamanho do maior. Ver
[Pod por passo](/docs/pod-per-step/).

## Campos do passo

| campo | | |
|---|---|---|
| `id` | **obrigatório** | único no workflow; é o nome que aparece no grafo e nos logs |
| `run` | | o comando |
| `image` | | a imagem do passo; sem ela, herda a do topo |
| `shell` | `true` | `false` executa sem shell — necessário em distroless |
| `depends_on` | | lista de `id` que precisam terminar antes |
| `resources` | | `cpu`, `memory` e `limits` daquele passo |

## Tags

```yaml
tags: [analytics, dbt, diario]
```

Servem para filtrar na interface. Não afetam a execução.

## Validação

A validação não precisa de banco, então roda na CI junto com os testes:

```bash
brevis validate workflows/
```

```
  ok    daily_analytics              dag  5 steps, 5 dependencias  (manual)
  ok    daily-report                 chain  3 steps, 2 dependencias  cron 0 2 * * *
```

Aceita arquivo ou diretório. Num diretório, casa `*.y*ml` e ordena — dois runs
produzem o mesmo log, e a diferença entre dois deploys não vira ruído.

## Publicando

```bash
brevis publish workflows/
brevis publish workflows/ --project acme --prune
```

`--prune` remove do projeto os workflows ausentes da lista publicada,
preservando o histórico. **Não é o padrão**, e a razão é prática: `publish
um-arquivo.yaml` não pode apagar os outros quarenta e oito só porque não foram
citados na linha de comando.

## Próximos passos

- [Parâmetros](/docs/parameters/) — o que muda entre dois disparos
- [Scheduler e fila](/docs/scheduler-and-queue/) — como o workflow vira execução
