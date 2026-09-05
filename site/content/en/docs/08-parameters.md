---
title: Parameters
description: What changes between two runs of the same workflow, without editing the file.
group: Reference
order: 8
slug: parameters
---

A workflow can declare **run parameters**: values that change between two
triggers without the file changing.

## Declaring

```yaml
params:
  - name: load_full
    type: boolean
    default: "false"

  - name: start_date
    type: string
    pattern: '^\d{4}-\d{2}-\d{2}$'

steps:
  - id: run
    run: dbt build --vars '{"load_full":"{{ .load_full }}"}' --select bronze+
```

| field | | |
|---|---|---|
| `name` | **required** | the name used in the template and in `--param` |
| `type` | | `string` or `boolean` |
| `default` | | used when the trigger provides no value |
| `pattern` | | regular expression the value must match |

A parameter **without a `default`** is required: a trigger that omits it fails
before executing any step.

## Using it in the command

The value enters `run` through a template, in double braces with a dot:

```yaml
run: ./load.sh --since {{ .start_date }}
```

## Passing it on the command line

```bash
brevis run wf.yaml --param load_full=true
brevis run wf.yaml --param load_full=true --param start_date=2026-01-01
```

`--param` is repeatable. Input without `=` is an **error**:

```
erro: --param "load_full": use chave=valor
```

That is deliberate. `--param load_full`, forgetting the value, would run with
the default — and the operator would believe the backfill happened with the
parameter they intended.

## In a backfill

The values apply to **every** slot in the range. This is the central use case:
"reprocess all of January with `load_full=true`".

```bash
brevis backfill daily --from 2026-01-01 --to 2026-01-31 --param load_full=true
```

## In the interface

A workflow with `params` gets a **form** in place of the simple trigger button.
Fields come from the declaration, and `pattern` is validated before submission.

## In the step environment

Run parameters also enter each step's **environment**, prefixed. This exists so
that a fetcher written with the [SDK](/docs/sdk/) can see them without receiving
anything as an argument:

```go
Before: func(ctx context.Context, p *sdk.Pipeline) error {
    if p.Run.Params["load_full"] == "true" {
        p.Source.URL += "&full=1"
    }
    return nil
},
```

## The snapshot

Parameters are stored **in the run**, not read from the workflow at execution
time. The run carries the input it was triggered with, and the log of a January
execution keeps showing January's values even after the default changes.

## Next steps

- [Configuration](/docs/configuration/) — process environment variables
- [SDK](/docs/sdk/) — how a fetcher reads the run context
