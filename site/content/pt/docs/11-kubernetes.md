---
title: Kubernetes
description: Como implantar o engine, com que permissões, e o que verificar depois.
group: Operação
order: 11
slug: kubernetes
---

Uma instalação típica tem **dois Deployments** do mesmo binário — a API e o
scheduler — e um Postgres.

## Os dois papéis

| papel | comando | imagem | réplicas |
|---|---|---|---|
| API + interface | `serve` | `:0.3.0` (distroless) | quantas quiser |
| scheduler + fila | `scheduler` | `:0.3.0-worker` (alpine) | **uma** |

A API é distroless porque só serve HTTP: não executa nada, então não precisa de
shell. O worker é alpine porque os passos `run:` precisam de um.

:::warning Uma réplica de scheduler
Dois processos materializando a mesma agenda criam runs duplicados. Se precisar
de disponibilidade, use `replicas: 1` com `strategy: Recreate`.
:::

## Migrations

Rode como Job antes do deploy, nunca no boot do `serve`:

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

## Deployment da API

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

:::note Por que as duas probes diferem
`/health` **não** consulta o banco; `/ready` consulta e nomeia a dependência que
falhou. Se a liveness dependesse do Postgres, uma oscilação do banco faria o
Kubernetes **matar** os pods da API em vez de apenas tirá-los do balanceador — e
a recuperação ficaria mais lenta justamente quando o sistema já está sob
estresse.
:::

## Deployment do scheduler

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
              value: dados
            - name: BREVIS_POD_SERVICE_ACCOUNT
              value: brevis-runner
            - name: BREVIS_POD_ENV_FROM_SECRETS
              value: bigquery-cred
```

`BREVIS_PODS=on` **exige** cluster: sem service account montada, o boot falha em
vez de cair silenciosamente para execução local.

## Permissões

O scheduler cria, observa e apaga pods no namespace de execução. Nada além
disso:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: brevis-runner
  namespace: dados
rules:
  - apiGroups: [""]
    resources: ["pods"]
    verbs: ["create", "get", "list", "watch", "delete"]
  - apiGroups: [""]
    resources: ["pods/log"]
    verbs: ["get"]
```

`pods/log` é o que permite seguir a saída do passo até a interface. Sem ele, o
run funciona e o log fica vazio.

## Depois do deploy

```bash
kubectl -n dados get pods -l app=brevis-api
kubectl -n dados exec deploy/brevis-api -- brevis version
curl -fsS https://brevis.exemplo.com/ready
```

Dispare um workflow simples pela interface e confirme que o pod do passo aparece
e desaparece:

```bash
kubectl -n dados get pods -w
```

## Quando um passo fica Pending

Quase sempre é uma destas três:

| causa | como confirmar |
|---|---|
| taint sem toleração | `kubectl describe pod` mostra `node(s) had untolerated taint` |
| `nodeSelector` sem nó | `describe` mostra `didn't match Pod's node affinity` |
| recurso indisponível | `describe` mostra `Insufficient cpu` ou `memory` |

Para inspecionar o pod de um passo que falhou, em vez de vê-lo desaparecer:

```bash
BREVIS_POD_MANTER_EM_FALHA=true
```

Deixe desligado em produção — pods parados consomem cota.

## Próximos passos

- [Pod por passo](/docs/pod-per-step/) — o modelo de execução
- [Configuração](/docs/configuration/) — todas as variáveis `BREVIS_POD_*`
