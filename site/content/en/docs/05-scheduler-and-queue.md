---
title: Scheduler and queue
description: Two independent loops — who creates runs, who executes them, and why they are separate.
group: Concepts
order: 5
slug: scheduler-and-queue
---

brevis.sh separates **creating** a run from **executing** it. Two loops run
together and depend on neither.

```
scheduler                          queue
─────────                          ─────
reads the schedule
materialises the slot
creates the Run  ─────────────►  (pending in the database)
                                   claims it
                                   walks the graph
                                   executes each step
                                   records state and log
```

## Why separate

A single loop that both schedules and executes has one bad property: when
execution jams, scheduling stops with it. The next twelve hours of slots simply
do not exist, and nobody notices until someone looks for data that never
arrived.

With the two apart:

- the **scheduler** can go down and the queue keeps draining what was created;
- the **queue** can go down and slots keep being materialised, to be executed
  when it returns;
- **reprocessing does not depend on the clock** — `backfill` creates past runs
  through the same path cron would use.

## The process

Both loops live in the same command:

```bash
brevis scheduler --interval 5s --concurrency 4 --max-pods 10
```

| flag | default | |
|---|---|---|
| `--interval` | `10s` | interval between scheduler cycles |
| `--concurrency` | `5` | simultaneous **runs** |
| `--max-pods` | `5` | simultaneous **steps** in total |

`--concurrency` and `--max-pods` count different things, deliberately: five runs
with three parallel steps each would be fifteen pods if the only limit were on
runs.

:::note `serve` does not materialise schedules
The API process brings up the same scheduler, but **without the loop** — it is
there only to serve manual triggers from the interface, so the rule "the
scheduler creates runs" keeps a single owner. For schedules to fire, a
`brevis scheduler` must run alongside.
:::

## The definition snapshot

When the scheduler creates a run, it stores **the workflow definition inside the
run**. Execution reads that snapshot, not the YAML on disk.

The reason is concrete: between trigger and execution, someone may have edited
the file and deployed. Without the snapshot, a run created at 05:00 with one
definition would execute at 05:02 with another — and the log would have no way
of explaining the difference.

## Retries

The attempt is persisted, not process state. Restarting the worker does not
erase what was already known about that run.

The **run's** attempt number goes into the pod name. Without it, a retry would
restart the run from zero, meet the previous attempt's pod under the same name
and hang in `Pending` forever.

## Backfill

Materialises past slots of a workflow already published:

```bash
brevis backfill daily --from 2026-01-01 --to 2026-01-31
brevis backfill daily --from 2026-01-01 --to 2026-01-31 --param load_full=true
```

```
  31 run(s) de backfill enfileirados para daily (2026-01-01 a 2026-01-31)
  rode `brevis scheduler` para executa-los
```

It **enqueues, it does not execute.** Execution is the `scheduler`'s job — the
same separation again. `--to` includes the whole day.

## Alerts

```bash
export BREVIS_SLACK_WEBHOOK='https://hooks.slack.com/services/...'
export BREVIS_UI_URL='https://brevis.example.com'
```

Without the webhook, the process warns at boot that failures will not be
reported. An installation that fails silently tends to be discovered by the
customer, not by the team.

`BREVIS_UI_URL` builds the run link inside the alert — without it, the alert
says what failed but makes the reader hunt for the run by hand.

## Next steps

- [Pod per step](/docs/pod-per-step/) — where each step actually runs
- [CLI](/docs/cli/) — every flag of `scheduler` and `backfill`
