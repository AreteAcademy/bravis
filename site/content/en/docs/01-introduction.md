---
title: Introduction
description: What brevis.sh is, the problem it solves, and when it is not the right choice.
group: Getting started
order: 1
slug: introduction
---

brevis.sh is a **data orchestration runtime written in Go**. A single binary
brings together what normally takes four tools: declarative transformation,
workflow orchestration, a persistent queue, a scheduler, and an interface to
operate all of it.

It is open source under the MIT licence, and the project is born at
[Aretê Academy](https://areteacademy.com.br/).

## The problem

A common data stack combines a transformation tool, an orchestrator, a queue and
an interface. Each piece is good at what it does — and the integration between
them becomes permanent work: four versioning schemes, four ways to configure,
four places to look when something fails.

That cost never shows up in an architecture diagram. It shows up in the time a
team spends maintaining the glue instead of working on the domain only they
know.

## The proposal

One artefact, with the subcommand the role requires:

```bash
brevis serve        # API + interface
brevis scheduler    # materialises schedules and drains the queue
brevis run wf.yaml  # runs now, no database
```

A workflow is a versioned YAML file. Reviewing a pipeline becomes reviewing a
diff, not a clicking session.

```yaml
name: ingest
schedule: "0 5 * * *"

steps:
  - id: bronze
    image: registry.example/dbt:1.10.3
    run: dbt build --select bronze+

  - id: notify
    image: ghcr.io/example/notify:0.3
    run: /notify --channel data
    depends_on: [bronze]
```

## What makes it different

**Every step runs in its own pod, with its own image.** There is no generic
worker waiting for work: the work brings its own runtime. A `dbt` step comes up
with the dbt image; a Go fetcher next to it comes up with 5.8 MB and 32Mi of
memory, instead of inheriting the size of its largest neighbour.

**The scheduler creates runs; the queue executes them.** Two independent loops,
and that separation is deliberate: one can go down without interrupting the
other, and reprocessing a past month does not depend on the clock.

**Validation needs no database.** `brevis validate` runs in CI alongside the
tests, so a YAML error fails in the pull request and not in the cluster.

## When brevis.sh is not the choice

Documenting what a tool does not do saves more time than documenting what it
does:

- **You already run Airflow or Dagster and you are happy.** Migrating costs more
  than the saving of operating one fewer binary.
- **Your workflows are not about data.** brevis.sh assumes DAGs of
  transformation and load; a general-purpose orchestrator serves you better.
- **You need a catalogue of ready-made operators.** Here a step is an image and
  a command. That is simple, and it is limited — there are no hundreds of
  integrations to drag in.
- **Your team does not use Kubernetes and does not intend to.** Local mode
  works, but the design assumes pods.

## Where to go next

| if you want | go to |
|---|---|
| install and see it running | [Installation](/docs/installation/) and [Quickstart](/docs/quickstart/) |
| understand the workflow format | [Workflows](/docs/workflows/) |
| understand the architecture | [Scheduler and queue](/docs/scheduler-and-queue/) |
| the list of commands and flags | [CLI](/docs/cli/) |
| write a fetcher in Go | [SDK](/docs/sdk/) |

:::note About the name
*Brevis* is Latin for "short, brief" — the root of *brevity*. It comes from
Seneca's **De Brevitate Vitae**: it is not that we have little time, but that we
lose much of it.
:::
