---
title: Pod per step
description: Why each step runs in its own pod, and what that changes in cost and isolation.
group: Concepts
order: 6
slug: pod-per-step
---

On Kubernetes, **each workflow step becomes a pod** with the image declared in
that step. There is no generic worker waiting for work: the work brings its own
runtime.

```
scheduler                          cluster
─────────                          ───────
walks the graph
  for each ready step:
    builds the Pod  ───────────►  Pod: the step's image, the step's command
    follows status      ◄───────  Pending → Running → Succeeded/Failed
    follows the log     ◄───────  container stdout
    reads the exit code ◄───────  containerStatuses[0].terminated
    deletes the pod ───────────►  (gone)
```

## What this changes

A monolithic worker forces the image to contain everything any step might need.
In practice:

| | single image | pod per step |
|---|---|---|
| dbt step | 1.9 GB · 1Gi RAM | 1.9 GB · 1Gi RAM |
| Go fetcher next to it | **1.9 GB · 1Gi** | **12 MB · 32Mi** |
| bumping the dbt version | rebuild everything | one line in that workflow's YAML |
| a step that leaks memory | takes the worker and its neighbours down | dies alone |

The third row is the least obvious and the most expensive day to day: with a
single image, moving dbt from 1.10 to 1.11 in one pipeline forces the move in
all of them.

## Images by role

Images are per **role**, not per project. Everything left over in an image is
pulled by every node that runs that step:

| role | size | cold start |
|---|---|---|
| **Go** | **5.8 MB** | 0.18 s |
| **Python** | **118 MB** | 0.54 s |
| **dbt** | **620 MB** | 3.3 s |

Before: a single 1.87 GB image for everything.

For dbt, `dbt parse` runs at build time and `target/partial_parse.msgpack`
travels in the image — **2.68 s less per pod**, on every invocation.

## Where each step runs

The decision is made once, at boot, and depends on `BREVIS_PODS` and on whether
there is a cluster:

| `BREVIS_PODS` | with cluster | without cluster |
|---|---|---|
| `auto` (default) | a step with `image:` becomes a pod | everything as local processes, with a log warning |
| `on` | a step with `image:` becomes a pod | **boot error** |
| `off` | everything as local processes | everything as local processes |

`auto` is the default because the same binary runs in both places: on a laptop
there is no service account mounted and it falls back to local processes; in a
cluster there is, and it starts creating pods.

`on` exists for the deployment that **must not** silently become local
execution. There, having no cluster has to be a boot error.

:::warning `brevis run` creates no pods
The `run` command builds only the process executor. A step with `image:` runs on
the instance itself — and says so. Staying silent would make it look like the
step ran on the declared image, which is the kind of mistake that only surfaces
once the result is already wrong.
:::

## Identity and credentials

Which identity pods come up with is an **installation** decision, not a workflow
one — a pipeline must not be able to choose the service account it runs as. So
it comes from the environment:

```bash
BREVIS_POD_NAMESPACE=data
BREVIS_POD_SERVICE_ACCOUNT=brevis-runner
BREVIS_POD_PULL_SECRETS=registry-cred
BREVIS_POD_ENV_FROM_SECRETS=bigquery-cred,api-tokens
```

In pod mode the task environment comes from cluster Secrets — not from
`BREVIS_TASK_ENV`.

## Node selector and tolerations

```bash
BREVIS_POD_NODE_SELECTOR='kubernetes.io/arch=arm64'
BREVIS_POD_TOLERATIONS='dedicated=data:NoSchedule'
```

Only the `Equal` operator is accepted in tolerations: `Exists` would tolerate
**any** taint with that key, too broad for a decision coming from an environment
variable.

A malformed toleration is **ignored** rather than made a boot error — it leaves
the pod `Pending`, which is visible, whereas refusing to boot would also stop
the workflows that do not need that pool.

## Debugging a failure

```bash
BREVIS_POD_MANTER_EM_FALHA=true
```

Keeps the failed pod around for `kubectl logs` and `kubectl describe`. Leave it
off in production: stopped pods consume cluster quota.

## Next steps

- [Kubernetes](/docs/kubernetes/) — the full deployment
- [Configuration](/docs/configuration/) — every `BREVIS_POD_*` variable
