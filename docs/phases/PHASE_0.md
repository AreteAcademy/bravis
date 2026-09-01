# PHASE 0 — FOUNDATION

Concluída em 2026-08-31. Validada 100% local.

## Implemented

| item | onde |
|---|---|
| Go module (`github.com/zarvhq/bravis`, Go 1.25) | `go.mod` |
| Configuration | `internal/config` |
| Logging estruturado | `internal/observability` |
| Health checks (`/health`, `/ready`) | `internal/api` |
| PostgreSQL (pool + check) | `internal/infrastructure/postgres` |
| Migrations embutidas | `migrations/` |
| Project structure (§36) | árvore criada, pacotes vazios até sua fase |
| CLI (`serve`, `migrate up/down/status`) | `cmd/bravis` |
| Docker development environment | `docker-compose.yml`, `Dockerfile` |

Fora de escopo por decisão: scheduler, fila, executores, transform, UI. A §37
não os inclui na Phase 0 e a regra 2 proíbe antecipar.

## Architecture Decisions

**stdlib em vez de GoFr.** A §37 lista GoFr, mas a regra 6 pede evitar framework
quando a stdlib resolve. O `ServeMux` do Go 1.22+ casa por método e path, que é
tudo que health e readiness precisam. O trabalho difícil deste sistema é fila,
scheduler e máquina de estados — GoFr não ajuda ali e opinaria sobre a estrutura
da §36. Custo: ~150 linhas de servidor, shutdown e log de acesso. Reversível.

**Cinco dependências, três delas stdlib.**

| dependência | por quê |
|---|---|
| `net/http`, `log/slog`, `embed` | stdlib |
| `jackc/pgx/v5` | driver e pool de referência; `database/sql` perde tipos do Postgres |
| `pressly/goose/v3` | migrations em SQL, embutidas no binário |
| `spf13/cobra` | a CLI vai crescer (`serve`, `migrate`, depois `scheduler`, `worker`) |

Medido: 68 pacotes no build, binário de 15 MB. O goose declara `modernc.org/sqlite`
para outros dialetos, mas **nenhum pacote `modernc.org` entra no binário** — o
linker não os alcança pelo caminho Postgres.

**`/health` não toca o banco; `/ready` toca.** Liveness que depende de dependência
externa faz o Kubernetes *matar* o pod quando o banco oscila, trocando
indisponibilidade parcial por crashloop. Readiness apenas tira do balanceador.
Provado empiricamente (ver Tests).

**`migrate` é subcomando, não parte do `serve`.** Subir aplicação e alterar schema
têm blast radius diferente; juntar as duas faz um restart casual virar um DDL. O
compose roda `migrate up` como etapa própria, com `service_completed_successfully`.

**Migrations embutidas via `embed.FS`.** Imagem e schema andam juntos — não existe
versão do binário aplicável a um schema que ela não carrega. O embed vive em
`migrations/embed.go` porque `//go:embed` não alcança diretório pai.

**Schema mínimo.** A §22 lista dez entidades; a Phase 0 cria duas (`projects`,
`workflows`). Schema sem caso de uso envelhece errado. `slug` é único *dentro* do
projeto, não global.

**`internal/config` não está na §36.** Desvio consciente: a alternativa seria
espalhar `os.Getenv` por `cmd/` e `infrastructure/`. Registrado aqui em vez de
silencioso (regra 7).

## Tests

`go test ./...` passa. Sete testes, sem dependência de banco:

- `config`: falha sem `BRAVIS_DATABASE_URL`; aplica padrões; rejeita timeout não numérico
- `api`: `/health` responde 200 **mesmo com a dependência quebrada**; `/ready` responde 200 quando tudo responde e 503 **nomeando** a dependência que falhou; método errado devolve 405

Validação de integração, executada contra o ambiente local:

```
docker compose up          -> postgres healthy, migrate exited 0, api up
GET /health                -> 200 {"status":"ok"}
GET /ready                 -> 200 {"status":"ok","checks":{"postgres":"ok"}}
psql \dt                   -> goose_db_version, projects, workflows

docker compose stop postgres
GET /health                -> 200  (pod segue vivo)
GET /ready                 -> 503  {"status":"unavailable","checks":{"postgres":"..."}}

docker compose start postgres
GET /ready                 -> 200  (recupera sozinho)
```

## Benchmarks

Nenhum. A Phase 0 não tem caminho quente: health check e boot. Benchmarks passam
a fazer sentido na Phase 2 (fila) e na Phase 3 (executor local), onde a §34 define
o que medir. Criar benchmark de handler agora seria ruído.

## Known Limitations

- Sem autenticação na API. A §35 trata segurança; nenhum endpoint da Phase 0 expõe dado.
- Sem métricas nem tracing — só logging. A §32 os detalha para fases posteriores.
- `migrate down` reverte **uma** migration por vez (padrão do goose), não tudo.
- Nenhum teste de integração automatizado contra Postgres real; a validação acima
  foi manual. Um `testcontainers` entra quando houver query de domínio a testar.
- O `docker-compose.yml` usa credenciais fixas, adequadas só para local.

## Next Phase

**PHASE 1 — DOMAIN CORE**: Projects, Workflows, Workflow Nodes e Edges como
entidades de domínio, com repositórios e a API que as expõe. Nada de execução
ainda.

Não avançar sem revisão desta fase.
