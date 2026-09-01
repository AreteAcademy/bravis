# Bravis

Engine de transformação e orquestração de dados, em Go. Substitui o par
Kestra/Leoflow (orquestração) e o dbt (transformação) por um binário único, com
execução em pod no Kubernetes.

Arquitetura e faseamento: [`docs/plan.md`](docs/plan.md).
Relatórios por fase: [`docs/phases/`](docs/phases/).

**Estado: PHASE 4 concluída.** Workflows em YAML, fila persistente, scheduler com
cron e backfill. Sem UI ainda — ver `docs/phases/`.

```bash
bravis validate examples/            # valida sem banco, serve na CI
bravis run examples/pipeline.yaml    # executa agora, na propria instancia

bravis publish examples/*.yaml       # grava workflow e agenda no banco
bravis scheduler --concurrency 5     # materializa slots e executa
bravis backfill diario --from 2026-01-01 --to 2026-01-31
```

O scheduler **cria** runs; a fila os **executa**. Os dois laços são independentes:
um pode cair sem afetar o outro.

O YAML aceita `type: chain` (ordem do arquivo) ou `type: dag` com `depends_on`.
`chain` é açúcar: vira arestas no parser, e o motor conhece apenas DAG.

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
