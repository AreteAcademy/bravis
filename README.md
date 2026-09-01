# Bravis

Engine de transformação e orquestração de dados, em Go. Substitui o par
Kestra/Leoflow (orquestração) e o dbt (transformação) por um binário único, com
execução em pod no Kubernetes.

Arquitetura e faseamento: [`docs/plan.md`](docs/plan.md).
Relatórios por fase: [`docs/phases/`](docs/phases/).

**Estado: PHASE 0 concluída.** Fundação — configuração, logging, Postgres,
migrations, health checks e CLI. Sem scheduler, fila ou executor ainda.

## Local

```bash
make up      # Postgres + API
make smoke   # confere /health e /ready
make logs
make down
```

```bash
make check   # gofmt + vet + testes
make build   # binario em bin/
```

## Configuração

| variável | padrão | |
|---|---|---|
| `BRAVIS_DATABASE_URL` | — | **obrigatória** |
| `BRAVIS_ENV` | `local` | `local` usa log em texto; o resto, JSON |
| `BRAVIS_HTTP_ADDR` | `:8080` | |
| `BRAVIS_LOG_LEVEL` | `info` | |
| `BRAVIS_SHUTDOWN_TIMEOUT_SECONDS` | `15` | |

## Endpoints

| | |
|---|---|
| `GET /health` | liveness — **não** consulta o banco |
| `GET /ready` | readiness — consulta, e nomeia a dependência que falhou |

A separação é deliberada: liveness que depende de dependência externa faz o
Kubernetes matar o pod quando o banco oscila, em vez de apenas tirá-lo do
balanceador.

## Migrations

```bash
bravis migrate up|down|status
```

Embutidas no binário e aplicadas por subcomando próprio — o `serve` nunca altera
schema.
