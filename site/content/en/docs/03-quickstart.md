---
title: Quickstart
description: From nothing to a live DAG in the interface, in four steps.
group: Getting started
order: 3
slug: quickstart
---

This guide brings brevis.sh up, publishes a workflow and runs it. At the end you
see the graph with the live state of every step.

Requirement: a reachable PostgreSQL and the `BREVIS_DATABASE_URL` variable.

## 1. Apply the schema

```bash
export BREVIS_DATABASE_URL='postgres://brevis:brevis@localhost:5432/brevis?sslmode=disable'
brevis migrate up
```

## 2. Write a workflow

Create `hello.yaml`. This example uses only shell, so it runs anywhere:

```yaml
name: hello
type: dag
tags: [example]

steps:
  - id: prepare
    run: sh -c 'echo preparing; sleep 1'

  # The next two depend on the same step, so they run IN PARALLEL.
  - id: extract
    run: sh -c 'sleep 2; echo 42 extracted'
    depends_on: [prepare]

  - id: validate
    run: sh -c 'sleep 1; echo validated'
    depends_on: [prepare]

  - id: publish
    run: echo published
    depends_on: [extract, validate]
```

Check it before publishing — this does not touch the database:

```bash
brevis validate hello.yaml
```

```
  ok    hello                        dag  4 steps, 4 dependencias  (manual)
```

## 3. Run it now, without the queue

The shortest path to seeing the graph work:

```bash
brevis run hello.yaml
```

```
workflow hello (dag, 4 steps) em .
  ▶ prepare
    prepare | preparing
  ✓ prepare
  ▶ extract
  ▶ validate
    validate | validated
  ✓ validate
    extract | 42 extracted
  ✓ extract
  ▶ publish
    publish | published
  ✓ publish

workflow hello concluido
```

`extract` and `validate` start together because they depend on the same step.
The runner walks the graph **by levels**: everything within a level runs in
parallel, and the next level starts only when the previous one closes entirely.

:::note `run` is local by nature
`brevis run` only operates with `BREVIS_ENV=local` (empty counts as local) and
executes steps as processes on the instance itself — even those that declare
`image:`. Pods are created by the `scheduler`.
:::

## 4. Publish and operate

So the workflow exists in the database, appears in the interface and follows its
schedule:

```bash
brevis publish hello.yaml
```

```
  publicado  hello                    (manual)
```

Bring up the interface and the scheduler in two terminals:

```bash
brevis serve
```

```bash
brevis scheduler --interval 5s --concurrency 4
```

Open `http://localhost:8080`, find `hello` in the workflow list and click ▶. The
graph shows every step changing state live.

## What happened

| | |
|---|---|
| `migrate up` | created the tables |
| `validate` | checked the YAML without touching the database |
| `run` | executed on the instance itself, no queue |
| `publish` | stored the workflow and its schedule |
| `serve` | brought up API and interface |
| `scheduler` | materialised the schedule and drained the queue |

## Next steps

- [Workflows](/docs/workflows/) — the full YAML format
- [Parameters](/docs/parameters/) — what changes between two runs
- [Pod per step](/docs/pod-per-step/) — how this becomes Kubernetes
