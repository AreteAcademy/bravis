---
title: Kubernetes
description: How to deploy the engine, with what permissions, and what to check afterwards.
group: Operations
order: 11
slug: kubernetes
---

A typical installation has **two Deployments** of the same binary — the API and
the scheduler — plus a Postgres.

## The two roles

| role | command | image | replicas |
|---|---|---|---|
| API + interface | `serve` | `:0.3.0` (distroless) | as many as you like |
| scheduler + queue | `scheduler` | `:0.3.0-worker` (alpine) | **one** |

The API is distroless because it only serves HTTP: it executes nothing, so it
needs no shell. The worker is alpine because `run:` steps need one.

:::warning One scheduler replica
Two processes materialising the same schedule create duplicate runs. If you need
availability, use `replicas: 1` with `strategy: Recreate`.
:::

## Migrations

Run them as a Job before the deploy, never at `serve` boot:

```yaml
apiVersion: batch/v1
kind: Job
metadata:
  name: brevis-migrate
spec:
  template:
    spec:
      restartPolicy: Never
      containers:
        - name: migrate
          image: daniel3843/brevis:0.3.0
          args: ["migrate", "up"]
          envFrom:
            - secretRef: {name: brevis-db}
```

## API Deployment

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: brevis-api
spec:
  replicas: 2
  selector:
    matchLabels: {app: brevis-api}
  template:
    metadata:
      labels: {app: brevis-api}
    spec:
      serviceAccountName: brevis
      containers:
        - name: api
          image: daniel3843/brevis:0.3.0
          args: ["serve"]
          ports: [{containerPort: 8080}]
          envFrom:
            - secretRef: {name: brevis-db}
            - secretRef: {name: brevis-auth}
          env:
            - name: BREVIS_ENV
              value: production
          livenessProbe:
            httpGet: {path: /health, port: 8080}
          readinessProbe:
            httpGet: {path: /ready, port: 8080}
          resources:
            requests: {cpu: 100m, memory: 128Mi}
            limits: {memory: 256Mi}
```

:::note Why the two probes differ
`/health` does **not** query the database; `/ready` does, and names the
dependency that failed. If liveness depended on Postgres, a database wobble
would make Kubernetes **kill** the API pods instead of merely removing them from
the load balancer — and recovery would get slower exactly when the system is
already under stress.
:::

## Scheduler Deployment

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: brevis-scheduler
spec:
  replicas: 1
  strategy: {type: Recreate}
  selector:
    matchLabels: {app: brevis-scheduler}
  template:
    metadata:
      labels: {app: brevis-scheduler}
    spec:
      serviceAccountName: brevis
      containers:
        - name: scheduler
          image: daniel3843/brevis:0.3.0-worker
          args: ["scheduler", "--interval", "10s", "--concurrency", "5", "--max-pods", "10"]
          envFrom:
            - secretRef: {name: brevis-db}
            - secretRef: {name: brevis-auth}
          env:
            - name: BREVIS_ENV
              value: production
            - name: BREVIS_PODS
              value: "on"
            - name: BREVIS_POD_NAMESPACE
              value: data
            - name: BREVIS_POD_SERVICE_ACCOUNT
              value: brevis-runner
            - name: BREVIS_POD_ENV_FROM_SECRETS
              value: bigquery-cred
```

`BREVIS_PODS=on` **requires** a cluster: with no service account mounted, boot
fails instead of silently falling back to local execution.

## Permissions

The scheduler creates, watches and deletes pods in the execution namespace.
Nothing beyond that:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: brevis-runner
  namespace: data
rules:
  - apiGroups: [""]
    resources: ["pods"]
    verbs: ["create", "get", "list", "watch", "delete"]
  - apiGroups: [""]
    resources: ["pods/log"]
    verbs: ["get"]
```

`pods/log` is what allows following a step's output into the interface. Without
it, the run works and the log stays empty.

## After the deploy

```bash
kubectl -n data get pods -l app=brevis-api
kubectl -n data exec deploy/brevis-api -- brevis version
curl -fsS https://brevis.example.com/ready
```

Trigger a simple workflow from the interface and confirm that the step's pod
appears and disappears:

```bash
kubectl -n data get pods -w
```

## When a step stays Pending

It is almost always one of these three:

| cause | how to confirm |
|---|---|
| taint without toleration | `kubectl describe pod` shows `node(s) had untolerated taint` |
| `nodeSelector` with no node | `describe` shows `didn't match Pod's node affinity` |
| resource unavailable | `describe` shows `Insufficient cpu` or `memory` |

To inspect a failed step's pod rather than watch it disappear:

```bash
BREVIS_POD_MANTER_EM_FALHA=true
```

Leave it off in production — stopped pods consume quota.

## Next steps

- [Pod per step](/docs/pod-per-step/) — the execution model
- [Configuration](/docs/configuration/) — every `BREVIS_POD_*` variable
