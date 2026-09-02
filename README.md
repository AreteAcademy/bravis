# Bravis

Engine de transformação e orquestração de dados, em Go. Substitui o par
Kestra/Leoflow (orquestração) e o dbt (transformação) por um binário único, com
execução em pod no Kubernetes.

Arquitetura e faseamento: [`docs/plan.md`](docs/plan.md).
Relatórios por fase: [`docs/phases/`](docs/phases/).

SDK público (`extract` e `load`, em construção): [`docs/SDK.md`](docs/SDK.md).

**Estado: PHASE 6 concluída.** Workflows em YAML, fila persistente, scheduler com
cron, backfill, UI server-rendered (Overview com métricas e gráficos, lista de
workflows com busca/filtros/pausar/executar) e visualização da DAG com o estado
de cada passo ao vivo — ver `docs/phases/`.

A interface segue a identidade da [Aretê Academy](https://areteacademy.com.br):
pergaminho, ouro e serifa. Fontes e bundles são servidos do próprio binário — a
UI funciona sem saída para a internet.

**Marca branca**: título, subtítulo, frase e paleta saem de um YAML
(`brand.example.yaml` → `brand.yaml`, ou `BRAVIS_BRAND_FILE`). As cores
sobrescrevem as variáveis CSS em tempo de execução, então trocar o tema não
recompila nada. O rodapé "Powered by Bravis" não vem da configuração — vem do
código.

```bash
bravis validate examples/            # valida sem banco, serve na CI
bravis run examples/hello.yaml       # executa agora, na propria instancia

bravis publish examples/hello.yaml   # grava workflow e agenda no banco
bravis scheduler --concurrency 5     # materializa slots e executa
bravis backfill diario --from 2026-01-01 --to 2026-01-31
```

O scheduler **cria** runs; a fila os **executa**. Os dois laços são independentes:
um pode cair sem afetar o outro.

Em Kubernetes, **cada passo vira um pod** com a imagem declarada no YAML — não há
worker genérico esperando trabalho, é o trabalho que traz o seu runtime. O mesmo
arquivo roda local como processo. Ver [`docs/KUBERNETES.md`](docs/KUBERNETES.md).

As imagens são por papel, não por projeto: **5,8 MB** para um passo em Go,
118 MB para Python, 620 MB para dbt (com o parse já embutido, 2,7 s a menos por
pod). Ver [`docs/IMAGENS.md`](docs/IMAGENS.md).

Um workflow pode declarar **parâmetros de execução** — o que muda entre dois
disparos sem editar o arquivo:

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
    run: dbt build --vars '{"load_full":"{{ .load_full }}"}' --select bronze_x+
```

```bash
bravis run wf.yaml --param load_full=true
bravis backfill diario --from 2026-01-01 --to 2026-01-31 --param load_full=true
```

Na UI, um workflow com params ganha formulário no lugar do botão simples.

`concurrency: 1` limita execuções simultâneas do mesmo workflow — o que impede
um `*/15` de se sobrepor a si mesmo.

O YAML aceita `type: chain` (ordem do arquivo) ou `type: dag` com `depends_on`.
`chain` é açúcar: vira arestas no parser, e o motor conhece apenas DAG.

`examples/hello.yaml` é o único que **roda** em qualquer lugar — os outros dois
vieram do plano e mostram o formato, chamando `python`, `docker.run` e
`./notify.sh`, que não existem na imagem do worker.

## Imagem

```bash
docker login -u daniel3843
make image-push            # daniel3843/bravis:<VERSION> e :<VERSION>-worker
```

Duas imagens do mesmo binário: `:<versao>` é a API em distroless (não executa
nada, então não precisa de shell) e `:<versao>-worker` é alpine com shell, para
os passos `run:` dos workflows. Detalhes em [`docs/PUBLICAR.md`](docs/PUBLICAR.md).

## Local

```bash
make dev     # hot reload: templ + tailwind + go build a cada mudanca
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
