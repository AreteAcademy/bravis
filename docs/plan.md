# Bravis

## High-Performance Data Transformation &amp; Workflow Engine

# SUPER PROMPT DE ARQUITETURA E IMPLEMENTAÇÃO

Você é o principal arquiteto e engenheiro responsável pela construção do **Bravis**.

O Bravis é uma plataforma de Data Engineering fortemente baseada em Go, criada para unificar:

- transformação de dados inspirada no dbt;
- execução de DAGs;
- agendamento de workflows;
- gerenciamento de filas;
- controle de concorrência;
- execução local de workloads Go;
- execução em Kubernetes;
- observabilidade;
- visualização de DAGs;
- gerenciamento de runs;
- integração com BigQuery.

O objetivo não é criar um clone literal do dbt, Airflow ou LeoFlow.

O objetivo é construir uma arquitetura moderna que combine os melhores conceitos dessas ferramentas, eliminando complexidades desnecessárias e priorizando:

```text
Performance
↓
Correctness
↓
Reliability
↓
Observability
↓
Simplicity
↓
Extensibility

```

---

# 1. VISÃO DO PRODUTO

O Bravis possui três responsabilidades principais.

## Bravis TRANSFORM

Responsável por transformação de dados.

Inspirado em:

```text
dbt
SQLMesh
Airflow
Leoflow
```

Responsabilidades:

```text
Models
Macros
Seeds
Sources
Tests
ref()
source()
Materializations
Incremental Models
MERGE
INSERT
INSERT OVERWRITE
UPSERT abstraction
BigQuery execution
State
Lineage
Artifacts

```

---

# Bravis ORCHESTRATOR

Responsável por:

```text
DAGs
Schedules
Queue
Concurrency
Retries
Backfills
Execution
Workers
Run State
Kubernetes Pods
Local Execution
Events

```

Ele substitui a responsabilidade que seria desempenhada por Airflow ou LeoFlow.

---

# Bravis UI

Responsável por operação e visualização.

Tecnologias:

```text
Go
Go templ
templUI
Tailwind
React Flow
JavaScript somente quando necessário

```

A UI deve ser majoritariamente server-rendered.

React Flow será utilizado especificamente para:

```text
DAG visualization
DAG editor
Workflow builder
Node inspection
Edge inspection
Execution flow

```

A interface não deve ser uma SPA completa.

---

# 2. ARQUITETURA GERAL

```text
                         ┌───────────────────────┐
                         │       Bravis UI        │
                         │                       │
                         │ templ + templUI       │
                         │ React Flow Islands    │
                         └───────────┬───────────┘
                                     │
                          HTTP / SSE / WebSocket
                                     │
                         ┌───────────▼───────────┐
                         │       Bravis API       │
                         │                       │
                         │         GoFr          │
                         └───────────┬───────────┘
                                     │
                 ┌───────────────────┼───────────────────┐
                 │                   │                   │
                 ▼                   ▼                   ▼
        ┌────────────────┐ ┌────────────────┐ ┌────────────────┐
        │ PROJECT ENGINE │ │ SCHEDULER      │ │ RUN MANAGER    │
        │                │ │                │ │                │
        │ Models         │ │ Cron           │ │ State Machine  │
        │ Macros         │ │ Queue          │ │ Events         │
        │ DAG            │ │ Backfill       │ │ Retry          │
        └────────┬───────┘ └───────┬────────┘ └────────┬───────┘
                 │                 │                    │
                 └─────────────────┼────────────────────┘
                                   │
                          ┌────────▼────────┐
                          │ EXECUTION ENGINE│
                          └────────┬────────┘
                                   │
                    ┌──────────────┼──────────────┐
                    │              │              │
                    ▼              ▼              ▼
              Local Go       Kubernetes Job    Future Executors
                    │              │
                    └──────────────┘
                           │
                      User Workload

```

---

# 3. REGRA FUNDAMENTAL DE EXECUÇÃO

O Bravis deve possuir diferentes tipos de runtime.

## GO LOCAL EXECUTOR

Jobs escritos em Go podem executar localmente dentro de um Worker Bravis.

Exemplo:

```text
Bravis Worker
     │
     ▼
Go Runtime
     │
     ├── Task A
     ├── Task B
     └── Task C

```

Isso evita:

```text
Container startup
Pod scheduling
Image pull
Kubernetes overhead

```

Para workloads simples ou de baixa latência.

---

## KUBERNETES EXECUTOR

Qualquer workload externo ao runtime Go deve executar em Kubernetes.

Exemplos:

```text
Python
Node.js
Java
Shell
Spark
Custom Docker Image

```

Fluxo:

```text
DAG Task
    │
    ▼
Execution Planner
    │
    ▼
Kubernetes Executor
    │
    ▼
Create Job / Pod
    │
    ▼
Kubernetes Scheduler
    │
    ▼
Pod Running
    │
    ▼
Stream Events
    │
    ▼
Bravis Run Updated

```

A regra inicial é:

```text
GO
↓
LOCAL ou KUBERNETES

OUTRAS LINGUAGENS
↓
KUBERNETES OBRIGATÓRIO

```

A decisão deve ser explícita.

Exemplo:

```yaml
executor: local

```

ou:

```yaml
executor: kubernetes

```

O Bravis nunca deve tentar executar arbitrariamente código de outras linguagens diretamente dentro do processo principal.

---

# 4. CONCEITOS DE DOMÍNIO

Precisamos separar claramente:

```text
Project
    │
    ├── Transform Models
    │
    └── Workflows
            │
            ▼
           DAG
            │
            ▼
        Schedule
            │
            ▼
           Run
            │
            ▼
         Task Run

```

Definições:

## Project

Unidade organizacional.

```text
analytics-platform

```

## Workflow / DAG

Definição lógica do fluxo.

```text
daily_analytics

```

## Schedule

Define quando uma DAG deve ser disparada.

```text
0 8 * * *

```

## Run

Uma execução específica de uma DAG.

```text
run_id
workflow_id
scheduled_at
started_at
finished_at
status

```

## Task Run

Execução específica de um node.

```text
task_run_id
run_id
task_id
status
attempt
executor
started_at
finished_at

```

---

# 5. DAG COMO ENTIDADE DE PRIMEIRA CLASSE

Uma DAG deve poder existir independentemente de um projeto dbt-like.

Exemplo:

```yaml
name: daily_analytics

tasks:

  - id: ingest_users

    type: kubernetes

    image: company/ingestion:latest

  - id: transform_silver

    type: Bravis

    command: Bravis run --select silver.*

    depends_on:
      - ingest_users

  - id: transform_gold

    type: Bravis

    command: Bravis run --select gold.*

    depends_on:
      - transform_silver

```

Representação:

```text
             ingest_users
                   │
                   ▼
           transform_silver
                   │
          ┌────────┴────────┐
          ▼                 ▼
     gold_metrics      gold_users
          │                 │
          └────────┬────────┘
                   ▼
                publish

```

A DAG deve ser validada antes de salvar.

Validar:

```text
Duplicate IDs
Cycles
Missing Dependencies
Invalid Executor
Invalid Image
Invalid Configuration

```

---

# 6. DAG ENGINE

Implementar um DAG Engine próprio em Go.

Estrutura conceitual:

```go
type DAG struct {
    ID       string
    Name     string
    Nodes    map[string]*Node
    Edges    []Edge
}

type Node struct {
    ID           string
    Name         string
    Type         NodeType
    Dependencies []string

    Executor     ExecutorType

    Config       map[string]any
}

```

O Engine deve:

```text
Build Graph
↓
Validate Graph
↓
Detect Cycles
↓
Calculate Dependencies
↓
Identify Ready Nodes
↓
Dispatch Execution
↓
Wait for Results
↓
Unlock Downstream Nodes

```

Estados possíveis:

```text
CREATED
QUEUED
SCHEDULED
RUNNING
SUCCESS
FAILED
RETRYING
CANCELLED
SKIPPED
BLOCKED

```

---

# 7. EXECUTION STATE MACHINE

Não utilizar simples booleans como:

```text
running = true

```

Criar uma máquina de estados formal.

Exemplo:

```text
CREATED
   │
   ▼
QUEUED
   │
   ▼
RUNNING
   │
   ├───────────┐
   ▼           ▼
SUCCESS      FAILED
                │
                ▼
             RETRYING
                │
                └──────► QUEUED

```

Cada transição deve ser validada.

Exemplo:

```go
func (r *Run) Transition(to RunStatus) error

```

Transições inválidas devem retornar erro.

---

# 8. QUEUE ENGINE

A fila é um componente crítico.

Não perder DAGs quando a concorrência máxima for atingida.

Fluxo:

```text
SCHEDULE
   │
   ▼
RUN CREATED
   │
   ▼
QUEUE
   │
   ├── waiting
   ├── priority
   ├── retry
   └── delayed
   │
   ▼
DISPATCHER
   │
   ▼
EXECUTOR

```

A fila deve ser persistente.

Nunca depender exclusivamente de:

```text
in-memory channel

```

para jobs críticos.

Utilizar uma arquitetura com:

```text
Persistent Queue
+
In-Memory Dispatcher

```

A implementação inicial deve suportar uma única estratégia persistente bem testada.

Não introduzir Kafka, RabbitMQ ou múltiplos brokers no MVP sem necessidade real.

---

# 9. CONCORRÊNCIA

Precisamos de diferentes limites.

## Global

```yaml
max_concurrency: 50

```

## Por Workflow

```yaml
workflow:
  max_concurrency: 3

```

## Por Projeto

```yaml
project:
  max_concurrency: 10

```

## Por Executor

```yaml
executors:

  local:
    max_concurrency: 20

  kubernetes:
    max_concurrency: 30

```

O sistema deve garantir:

```text
Max concurrency reached
        │
        ▼
Run remains QUEUED
        │
        ▼
Slot becomes available
        │
        ▼
Dispatcher picks next eligible run

```

Nunca:

```text
Max concurrency reached
        │
        ▼
Run lost

```

---

# 10. FAIRNESS DA FILA

A fila não deve permitir starvation.

Exemplo problemático:

```text
Project A
Project A
Project A
Project A
Project A

Project B
Project B

```

O Project B nunca pode ficar esperando indefinidamente.

Implementar uma estratégia inicial simples e explícita.

Avaliar:

```text
FIFO
Priority Queue
Round Robin por Project
Weighted Fair Queue

```

Para MVP:

```text
FIFO
+
Priority

```

Posteriormente:

```text
Fair Scheduling

```

Toda decisão deve ser configurável e observável.

---

# 11. SCHEDULER

Implementar:

```text
Cron Schedules
Interval Schedules
Manual Trigger
API Trigger
Event Trigger - futuro

```

Exemplo:

```yaml
schedule:

  cron: "0 8 * * *"

  timezone: "America/Sao_Paulo"

```

Cada schedule deve gerar uma intenção de execução.

Fluxo:

```text
08:00
 │
 ▼
Scheduler Tick
 │
 ▼
Create DAG Run
 │
 ▼
Persist Run
 │
 ▼
Queue Run
 │
 ▼
Dispatcher

```

Nunca executar diretamente dentro do scheduler.

O Scheduler apenas cria runs.

---

# 12. MISSED RUNS E BACKFILL

Precisamos definir comportamento explícito.

```yaml
schedule:

  cron: "0 * * * *"

  catchup: false

```

Ou:

```yaml
catchup: true

```

Backfill:

```bash
Bravis backfill \
  --dag daily_analytics \
  --from 2026-01-01 \
  --to 2026-01-31

```

Backfills devem entrar na fila.

Não devem ignorar:

```text
Concurrency
Priority
Project Limits
Executor Limits

```

Cada run deve possuir:

```text
trigger_type

SCHEDULE
MANUAL
BACKFILL
API
RETRY

```

---

# 13. EXECUTORS

Criar uma interface única.

```go
type Executor interface {
    Name() string

    Execute(
        ctx context.Context,
        task TaskExecution,
    ) (<-chan Event, error)

    Cancel(
        ctx context.Context,
        executionID string,
    ) error
}

```

Implementações:

```text
LocalExecutor
KubernetesExecutor

```

Futuro:

```text
DockerExecutor
RemoteExecutor
ServerlessExecutor

```

---

# 14. LOCAL GO EXECUTOR

O executor local deve ser utilizado somente para workloads confiáveis e registrados.

Exemplo:

```go
type Task interface {
    Name() string

    Run(ctx context.Context) error
}

```

Registro:

```go
registry.Register(
    "daily_sync",
    DailySyncTask{},
)

```

A execução:

```text
Worker
  │
  ▼
Task Registry
  │
  ▼
Go Task
  │
  ▼
Events

```

Não executar código arbitrário recebido pela API.

Tasks locais devem ser compiladas e registradas no runtime.

---

# 15. KUBERNETES EXECUTOR

Para workloads externos:

```yaml
executor: kubernetes

image: python:3.13

command:
  - python

args:
  - main.py

```

O executor deve criar recursos Kubernetes e acompanhar o ciclo de vida.

Responsabilidades:

```text
Create Job
Wait
Watch Status
Stream Logs
Detect Failure
Handle Cancellation
Cleanup

```

O Bravis deve armazenar:

```text
cluster
namespace
job_name
pod_name
container

```

A UI deve permitir navegar da Task Run para os detalhes da execução Kubernetes.

---

# 16. EVENT SYSTEM

Todo o sistema deve ser orientado a eventos internos.

Exemplos:

```text
dag.created

schedule.triggered

run.created
run.queued
run.started
run.completed
run.failed

task.ready
task.started
task.completed
task.failed
task.retrying

pod.created
pod.running
pod.completed

```

Interface:

```go
type EventBus interface {

    Publish(
        ctx context.Context,
        event Event,
    ) error

    Subscribe(
        topic string,
        handler EventHandler,
    )
}

```

Inicialmente:

```text
In-process Event Bus

```

Mas eventos importantes também devem ser persistidos.

Separar:

```text
Command State

```

de:

```text
Observability Events

```

---

# 17. UI ARCHITECTURE

Tecnologias obrigatórias:

```text
Go
Go templ
templUI
Tailwind
React Flow

```

A UI deve ser dividida em:

```text
SSR Pages
+
Interactive Islands

```

Exemplo:

```text
Dashboard
↓
Server Rendered

Projects
↓
Server Rendered

Runs
↓
Server Rendered + Live Updates

DAG Editor
↓
React Flow

DAG Visualization
↓
React Flow

Logs
↓
Streaming Component

```

O templUI deve ser utilizado como base para:

```text
Sidebar
Cards
Tables
Dialogs
Forms
Inputs
Selects
Badges
Progress
Toasts
Calendar
Time Picker

```

A arquitetura segue o modelo de componentes Go/templ e Tailwind documentado pelo templUI, evitando uma SPA completa quando não há necessidade.

---

# 18. DASHBOARD

Criar uma página inicial operacional.

Mostrar:

```text
Active Runs

Queued Runs

Running Tasks

Failed Runs - 24h

Success Rate

Average Duration

Queue Latency

Available Concurrency

```

Também:

```text
Recent Runs

```

Tabela:

```text
Workflow
Run ID
Status
Trigger
Started
Duration
Tasks

```

---

# 19. LIVE EXECUTION DASHBOARD

Durante uma execução:

```text
Run: daily_analytics

Status: RUNNING

```

Mostrar a DAG:

```text
      INGEST
         │
         ▼
      SILVER
       /   \
      ▼     ▼
   USERS   ORDERS
      \     /
       ▼   ▼
       GOLD

```

Cada node recebe updates em tempo real:

```text
QUEUED
RUNNING
SUCCESS
FAILED
RETRYING

```

Dados:

```text
Started At
Duration
Attempt
Executor
Pod
Worker
Logs
Error

```

Utilizar:

```text
SSE inicialmente

```

WebSocket somente quando existir uma necessidade real de comunicação bidirecional.

---

# 20. REACT FLOW

React Flow deve ser uma camada de visualização e edição.

Não deve ser a fonte da verdade.

Fluxo:

```text
Database DAG Definition
       │
       ▼
Bravis API
       │
       ▼
React Flow JSON
       │
       ▼
UI

```

Ao editar:

```text
React Flow
    │
    ▼
Validate Client Side
    │
    ▼
API
    │
    ▼
Server Validation
    │
    ▼
Persist DAG

```

O backend sempre valida:

```text
Cycles
Dependencies
Node Types
Configuration

```

---

# 21. DAG EDITOR

Permitir criar nodes:

```text
Bravis Transform

Go Local Task

Kubernetes Task

HTTP Task - futuro

Dependency Group - futuro

```

Cada node deve possuir:

```text
ID
Name
Type
Executor
Dependencies
Retry Policy
Timeout
Priority
Configuration

```

Exemplo de node:

```yaml
id: transform_gold

type: Bravis

executor: local

command:

  Bravis run --select gold.*

retry:

  max_attempts: 3

timeout: 30m

```

---

# 22. DATABASE E PERSISTÊNCIA

O banco deve ser a fonte da verdade operacional.

Entidades iniciais:

```text
projects

workflows

workflow_versions

workflow_nodes

workflow_edges

schedules

runs

task_runs

queue_items

execution_events

```

Nunca depender exclusivamente do arquivo YAML após o workflow ser publicado.

Precisamos suportar:

```text
Draft
Published Version
Historical Runs
Immutable Run Definition

```

Quando uma DAG for executada:

```text
Workflow Version
        │
        ▼
Run Snapshot

```

Mudanças futuras na DAG não podem alterar uma Run já criada.

---

# 23. VERSIONAMENTO DE DAG

Cada publicação cria:

```text
Workflow Version

```

Exemplo:

```text
daily_analytics

v1
v2
v3

```

Uma Run deve referenciar:

```text
workflow_version_id

```

Isso permite reproduzir:

```text
exact workflow definition

```

para qualquer execução histórica.

---

# 24. API

Utilizar GoFr.

Estrutura:

```text
internal/

  api/
    handlers/
    middleware/

  domain/

    workflow/
    run/
    schedule/
    queue/
    execution/

  application/

  infrastructure/

    postgres/
    kubernetes/
    bigquery/

  ui/

```

Não criar handlers que contenham regras complexas.

Fluxo:

```text
HTTP Handler
      │
      ▼
Application Service
      │
      ▼
Domain
      │
      ▼
Repository / Infrastructure

```

GoFr deve ser utilizado para:

```text
HTTP
Logging
Metrics
Tracing
Health Checks
Configuration
Graceful Shutdown

```

---

# 25. API PRINCIPAL

Projetos:

```text
POST   /projects
GET    /projects
GET    /projects/{id}

```

Workflows:

```text
POST   /workflows
GET    /workflows
GET    /workflows/{id}

POST   /workflows/{id}/publish

POST   /workflows/{id}/validate

```

Runs:

```text
POST /workflows/{id}/runs

GET  /runs

GET  /runs/{id}

POST /runs/{id}/cancel

POST /runs/{id}/retry

```

Schedules:

```text
POST /workflows/{id}/schedules

GET /schedules

PUT /schedules/{id}

DELETE /schedules/{id}

```

Queue:

```text
GET /queue

GET /queue/stats

```

---

# 26. SCHEDULER ARCHITECTURE

Não fazer:

```text
Cron
 │
 ▼
Execute Workflow

```

Fazer:

```text
Cron Scheduler
      │
      ▼
Create Run
      │
      ▼
Persist
      │
      ▼
Enqueue
      │
      ▼
Dispatcher
      │
      ▼
Acquire Concurrency Slot
      │
      ▼
Execution

```

Isso garante:

```text
Durability
Retries
Observability
Queue Control
Concurrency Control

```

---

# 27. DISPATCHER

O Dispatcher deve operar continuamente.

Pseudo fluxo:

```text
loop:

    get available capacity

    fetch eligible queue items

    apply priority

    apply concurrency limits

    reserve execution slot

    atomically mark RUNNING

    dispatch executor

```

Evitar race conditions.

A aquisição do job deve ser atômica.

Nunca permitir:

```text
Worker A executes Run X

Worker B executes Run X

```

---

# 28. HIGH AVAILABILITY

Não assumir que apenas uma instância do Bravis existe.

A arquitetura deve permitir futuramente:

```text
Bravis API 1
Bravis API 2
Bravis Worker 1
Bravis Worker 2
Bravis Scheduler

```

O design deve evitar:

```text
global process memory as source of truth

```

Utilizar:

```text
database state
distributed-safe locks when necessary
atomic state transitions
idempotency keys

```

O MVP pode rodar em uma instância, mas não deve impedir evolução para múltiplos workers.

---

# 29. IDEMPOTÊNCIA

Toda operação crítica deve considerar repetição.

Exemplo:

```text
Scheduler crashes
after creating Run

```

Ao reiniciar:

```text
não criar duplicatas indevidas

```

Utilizar:

```text
idempotency key

unique constraints

transactional operations

```

---

# 30. Bravis TRANSFORM

O módulo de transformação continua sendo parte central.

Estrutura:

```text
models/

macros/

seeds/

tests/

sources/

snapshots/

```

Suportar:

```text
ref()

source()

config()

var()

env_var()

is_incremental()

```

Materializations:

```text
view

table

incremental

ephemeral

```

BigQuery inicialmente:

```text
CREATE OR REPLACE VIEW

CREATE OR REPLACE TABLE

INSERT

MERGE

INSERT OVERWRITE

MICROBATCH

```

---

# 31. TRANSFORM COMO TASK

Um workflow pode executar Bravis Transform.

Exemplo:

```yaml
id: silver

type: Bravis_transform

executor: local

select:

  - silver.*

```

O runtime:

```text
Bravis Orchestrator
        │
        ▼
Bravis Transform Engine
        │
        ▼
Model DAG
        │
        ▼
BigQuery

```

Portanto teremos dois níveis de DAG.

```text
WORKFLOW DAG

Load
 │
 ▼
Transform Silver
 │
 ▼
Transform Gold

```

E internamente:

```text
MODEL DAG

silver.users
silver.orders
silver.payments

```

Não misturar os dois conceitos.

---

# 32. OBSERVABILITY

Todo Run deve possuir:

```text
run_id
workflow_id
workflow_version
trigger_type

created_at
queued_at
started_at
finished_at

```

Calcular:

```text
Queue Duration

Execution Duration

Retry Count

Success Rate

```

Task:

```text
task_run_id

executor

worker_id

pod_name

attempt

started_at

finished_at

```

GoFr deve expor métricas e tracing, e o Bravis deve acrescentar métricas específicas do domínio de execução.

---

# 33. LOGGING

Logs precisam ser associados a:

```text
Project
Workflow
Workflow Version
Run
Task Run
Attempt

```

Fluxo:

```text
Executor
   │
   ▼
Log Event
   │
   ├── stdout
   ├── stderr
   └── structured event
   │
   ▼
Log Store
   │
   ▼
UI Streaming

```

Nunca misturar logs de execuções diferentes.

---

# 34. PERFORMANCE

Performance deve ser medida.

Criar:

```text
benchmarks/

  dag/
  scheduler/
  queue/
  state_machine/
  transform/

```

Cenários:

```text
100 DAG nodes

1,000 DAG nodes

10,000 DAG nodes

100,000 queued runs

```

Medir:

```text
DAG validation

DAG traversal

Ready node calculation

Queue polling

Dispatch latency

Memory

CPU

```

Não otimizar com base em intuição.

Fluxo obrigatório:

```text
Benchmark
↓
Profile
↓
Identify Bottleneck
↓
Optimize
↓
Benchmark Again

```

Ferramentas:

```text
go test -bench

pprof

trace

```

---

# 35. SEGURANÇA

O Bravis não deve permitir execução arbitrária sem controle.

Local Go:

```text
registered tasks only

```

Kubernetes:

```text
approved configuration

namespace control

resource limits

service accounts

```

Nunca aceitar diretamente:

```text
arbitrary shell command

```

como funcionalidade padrão da API pública.

---

# 36. ESTRUTURA DO REPOSITÓRIO

```text
Bravis/

├── cmd/
│   └── Bravis/
│
├── internal/
│
│   ├── api/
│   │
│   ├── domain/
│   │   ├── project/
│   │   ├── workflow/
│   │   ├── run/
│   │   ├── task/
│   │   ├── schedule/
│   │   └── queue/
│   │
│   ├── application/
│   │   ├── workflow/
│   │   ├── scheduling/
│   │   ├── execution/
│   │   └── transform/
│   │
│   ├── execution/
│   │   ├── executor.go
│   │   ├── local/
│   │   └── kubernetes/
│   │
│   ├── scheduler/
│   │
│   ├── queue/
│   │
│   ├── graph/
│   │
│   ├── transform/
│   │   ├── models/
│   │   ├── macros/
│   │   ├── compiler/
│   │   ├── materializations/
│   │   ├── seeds/
│   │   └── adapters/
│   │       └── bigquery/
│   │
│   ├── events/
│   │
│   ├── observability/
│   │
│   └── infrastructure/
│       ├── postgres/
│       ├── kubernetes/
│       └── bigquery/
│
├── web/
│
│   ├── pages/
│   ├── components/
│   ├── layouts/
│   ├── assets/
│   │
│   └── react/
│       └── flow/
│
├── migrations/
│
├── benchmarks/
│
├── examples/
│
├── docs/
│
└── deployments/
    └── kubernetes/

```

---

# 37. FASEAMENTO

## PHASE 0 — FOUNDATION

Implementar:

```text
Go module

GoFr

Configuration

Logging

Health checks

PostgreSQL

Migrations

Project structure

Basic CLI

Docker development environment

```

Não implementar scheduler ainda.

---

## PHASE 1 — DOMAIN CORE

Implementar:

```text
Projects

Workflows

Workflow Nodes

Workflow Edges

Validation

Cycle Detection

Workflow Versioning

```

Critério:

Uma DAG pode ser:

```text
Created
Validated
Saved
Published
Versioned

```

---

## PHASE 2 — QUEUE + RUN ENGINE

Implementar:

```text
Runs

Task Runs

State Machine

Persistent Queue

Dispatcher

Concurrency Limits

Retry

```

Critério:

```text
100 runs queued

```

Com:

```text
max concurrency = 5

```

Resultado:

```text
5 RUNNING

95 QUEUED

```

Sem perda.

---

## PHASE 3 — LOCAL GO EXECUTOR

Implementar:

```text
Task Registry

Local Executor

Context Cancellation

Timeout

Retry

Event Streaming

```

Critério:

Uma DAG Go pode executar:

```text
Task A
   │
   ▼
Task B + Task C
   │
   ▼
Task D

```

Com paralelismo.

---

## PHASE 4 — SCHEDULER

Implementar:

```text
Cron

Timezone

Manual Trigger

Catchup Policy

Missed Runs

Backfill

```

Importante:

Scheduler:

```text
creates runs

```

Queue:

```text
executes runs

```

Nunca misturar responsabilidades.

---

## PHASE 5 — UI FOUNDATION

Implementar:

```text
templ

templUI

Tailwind

Layout

Sidebar

Dashboard

Projects

Workflows

Runs

```

Páginas SSR primeiro.

---

## PHASE 6 — REACT FLOW

Implementar:

```text
DAG Visualization

Custom Nodes

Execution States

Live Updates

Node Inspector

```

Depois:

```text
DAG Editor

```

A visualização vem antes da edição.

---

## PHASE 7 — KUBERNETES EXECUTOR

Implementar:

```text
Kubernetes Client

Job Creation

Pod Monitoring

Logs

Cancellation

Resource Limits

Cleanup

```

Critério:

```text
Python Job

```

executado via Kubernetes e acompanhado pela UI.

---

## PHASE 8 — Bravis TRANSFORM CORE

Implementar:

```text
Models

ref()

source()

SQL Compilation

Model DAG

BigQuery Adapter

```

---

## PHASE 9 — MATERIALIZATIONS

Implementar:

```text
view

table

incremental

ephemeral

```

Incremental:

```text
append

merge

insert_overwrite

microbatch

```

---

## PHASE 10 — MACROS, SEEDS E TESTS

Implementar:

```text
Macros

config()

var()

env_var()

is_incremental()

Seeds

Schema YAML

Tests

```

---

## PHASE 11 — ADVANCED OPERATIONS

Implementar:

```text
Priority Queue

Fair Scheduling

Worker Pools

Multiple Bravis Instances

Leader Election

Distributed Scheduling

```

Somente após o sistema single-instance estar completamente testado.

---

# 38. REGRAS PARA O AGENTE

O agente deve obrigatoriamente:

1. Trabalhar apenas na fase atual.
2. Nunca implementar uma fase futura antecipadamente.
3. Antes de codificar:

```text
analisar
↓
propor
↓
identificar riscos
↓
definir interfaces
↓
implementar
↓
testar
↓
benchmarkar quando aplicável

```

4. Não criar abstrações genéricas sem necessidade.
5. Preferir interfaces pequenas.
6. Evitar frameworks adicionais quando a biblioteca padrão Go resolver o problema.
7. Não esconder decisões arquiteturais.
8. Ao existir mais de uma alternativa:

```text
Option A

Pros
Cons

Option B

Pros
Cons

Recommendation

```

9. Após cada fase gerar:

```text
docs/phases/PHASE_X.md

```

Contendo:

```text
Implemented

Architecture Decisions

Tests

Benchmarks

Known Limitations

Next Phase

```

---

# 39. PRIMEIRA EXECUÇÃO

Começar exclusivamente pela:

# PHASE 0 — FOUNDATION

Primeiro apresentar:

```text
1. Repository Structure

2. Proposed Dependencies

3. Reason for every dependency

4. Database choice

5. Migration strategy

6. Local development architecture

7. Main interfaces that will exist

```

Depois implementar.

Ao finalizar:

```text
go test ./...

```

Deve executar sem falhas.

Também garantir:

```text
docker compose up

```

com:

```text
PostgreSQL

Bravis API

```

E endpoints:

```text
GET /health

GET /ready

```

Apenas depois apresentar o resultado da PHASE 0.

Não avançar automaticamente para a PHASE 1.

---

# PRINCÍPIO FINAL

O Bravis deve ser pensado como:

```text
               Bravis

     ┌────────────────────────┐
     │                        │
     │   DATA WORKFLOW OS     │
     │                        │
     └───────────┬────────────┘
                 │
       ┌─────────┼─────────┐
       │         │         │
       ▼         ▼         ▼

   SCHEDULE    EXECUTE   TRANSFORM

       │         │         │

       └─────────┼─────────┘
                 │
                 ▼

             OBSERVE

                 │
                 ▼

                UI

```

O sistema deve ser:

```text
GO-FIRST

KUBERNETES-NATIVE

BIGQUERY-FIRST

EVENT-DRIVEN

QUEUE-BASED

STATEFUL

OBSERVABLE

PERFORMANCE-ORIENTED

```

Mas a regra mais importante é:

> Não sacrificar confiabilidade em nome de performance.

Uma DAG perdida, uma execução duplicada ou um estado inconsistente é um problema muito maior do que alguns milissegundos adicionais de overhead.  
  
```text
Brevis
│
├── Brevis Core
│
├── Brevis Flow
│   └── DAGs
│
├── Brevis Transform
│   └── SQL / Models
│
├── Brevis Load
│   └── Ingestion
│
└── Brevis Observe
    └── UI / Metrics / Lineage
```