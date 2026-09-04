# Comandos

Referência da linha de comando. As saídas abaixo foram capturadas do binário
compilado deste commit, não escritas à mão.

## Dois binários, nomes parecidos

|  | `bravis` | `bravis-sdk` |
|---|---|---|
| **o que é** | o engine: orquestra, agenda, executa e serve a UI | o CLI do SDK: extrai de HTTP e carrega no BigQuery |
| **fonte** | [`cmd/bravis/`](../cmd/bravis/) | [`cmd/bravis-sdk/`](../cmd/bravis-sdk/) |
| **módulo** | o do repositório | próprio (`go.mod` separado, SDK fixado por versão) |
| **instalar** | `make build` → `bin/bravis` | `go install github.com/AreteAcademy/bravis/cmd/bravis-sdk@latest` |
| **precisa de Postgres** | na maioria dos subcomandos | nunca |

> O `Use:` do cobra em `cmd/bravis-sdk` está como `"bravis"`, então o help dele
> se anuncia com o nome do engine. O binário instalado chama-se `bravis-sdk`.
> Ver [Defeitos conhecidos](#defeitos-conhecidos).

## Instalar

```bash
make build                    # engine → bin/bravis, com versão e commit carimbados
docker run daniel3843/bravis:latest version
go install github.com/AreteAcademy/bravis/cmd/bravis-sdk@latest
```

**O engine não tem release taggeada.** As 30 tags do repositório são todas do
SDK (`sdk/v0.23.0` e anteriores); o módulo raiz não tem nenhuma. Consequências
práticas:

- `go install github.com/AreteAcademy/bravis/cmd/bravis@latest` resolve para
  uma pseudo-versão do último commit do branch, não para a `0.3.0` do arquivo
  `VERSION`. Compila — `web/assets/app.css` e os `_templ.go` estão versionados,
  então não é preciso rodar `make generate` —, mas o binário resultante reporta
  `bravis dev`, sem commit nem data, porque os `-ldflags` só entram pelo
  `make build` e pelo `docker build`.
- Para uma versão rastreável, o caminho é a **imagem** (`:0.3.0`) ou o
  `make build` local.

Enquanto não houver uma tag `v0.3.0` na raiz, o `go install` do engine não é o
caminho a recomendar em documentação de primeiro uso.

---

# `bravis` — o engine

```
Bravis — engine de transformacao e orquestracao de dados

Available Commands:
  backfill    Materializa slots passados de um workflow
  hash        Gera o hash de BRAVIS_AUTH_SENHA_HASH (le a senha do terminal)
  marca       Valida um arquivo de marca (nao precisa de banco)
  migrate     Aplica as migrations de schema
  publish     Publica workflows e suas agendas no banco
  run         Executa um workflow localmente
  scheduler   Materializa agendas em runs e as executa
  serve       Sobe a API HTTP
  validate    Valida arquivos de workflow (nao precisa de banco)
  version     Mostra a versao do binario
```

| comando | banco | credencial fora de `local` | papel |
|---|---|---|---|
| [`serve`](#bravis-serve) | **sim** | **exigida** | API + UI |
| [`scheduler`](#bravis-scheduler) | **sim** | **exigida** | os dois laços: cria e executa |
| [`migrate`](#bravis-migrate) | **sim** | **exigida** | schema |
| [`publish`](#bravis-publish) | **sim** | **exigida** | grava workflow e agenda |
| [`backfill`](#bravis-backfill) | **sim** | **exigida** | reprocessa um intervalo |
| [`run`](#bravis-run) | não | — | executa agora, na própria instância |
| [`validate`](#bravis-validate) | não | — | valida YAML de workflow |
| [`marca`](#bravis-marca) | não | — | valida YAML de marca |
| [`hash`](#bravis-hash) | não | — | gera o hash da senha |
| [`version`](#bravis-version) | não | — | versão, commit, build |

A coluna do banco vem de um fato só: os cinco primeiros chamam `config.Load()`,
que **falha no boot** sem `BRAVIS_DATABASE_URL`. Os outros cinco não a chamam —
e é por isso que `validate` serve na CI, onde não há Postgres.

Os mesmos cinco herdam a regra de credencial: com `BRAVIS_ENV` diferente de
`local`, subir sem `BRAVIS_AUTH_USUARIO` + `BRAVIS_AUTH_SENHA_HASH` +
`BRAVIS_AUTH_SEGREDO` é **erro de boot**, não aviso. A UI dispara `dbt build`
contra o warehouse; aberta na internet, ela é um controle remoto do warehouse.

---

## `bravis serve`

Sobe a API HTTP e a interface. Sem flags — tudo vem do ambiente.

```bash
bravis serve
```

Encerra em `SIGINT`/`SIGTERM` com shutdown graceful de
`BRAVIS_SHUTDOWN_TIMEOUT_SECONDS` (padrão 15 s), para que um deploy não corte
requisições em voo.

Este processo **não materializa agendas**. O disparo manual da tela chama o
mesmo scheduler, sem o laço — a regra "o scheduler cria os runs" continua com
um dono só. Para as agendas rodarem, é preciso um `bravis scheduler` ao lado.

`CMD` da imagem `api` (distroless, sem shell).

---

## `bravis scheduler`

Os dois laços do sistema, no mesmo processo e independentes: o **scheduler**
materializa agendas em runs, o **dispatcher** tira da fila e executa. Um pode
cair sem interromper o outro.

```bash
bravis scheduler --interval 5s --concurrency 4 --max-pods 10
```

| flag | tipo | padrão | |
|---|---|---|---|
| `--interval` | duration | `10s` | intervalo entre ciclos do scheduler |
| `--concurrency` | int | `5` | **runs** simultâneos |
| `--max-pods` | int | `5` | **passos** simultâneos no total |

`--concurrency` e `--max-pods` contam coisas diferentes, e isso é deliberado:
cinco runs com três passos paralelos cada dariam quinze pods se o único limite
fosse o de runs.

Onde cada passo roda depende de `BRAVIS_PODS` e de haver cluster:

| `BRAVIS_PODS` | com cluster | sem cluster |
|---|---|---|
| `auto` (padrão) | passo com `image:` vira pod | tudo em processo local, com aviso no log |
| `on` | passo com `image:` vira pod | **erro de boot** |
| `off` | tudo em processo local | tudo em processo local |

`on` existe para o deploy que não pode silenciosamente virar execução local.

Sem `BRAVIS_SLACK_WEBHOOK` o processo avisa no boot que falhas não serão
comunicadas — uma instalação que falha em silêncio é descoberta pelo cliente,
não pelo time.

`CMD` da imagem `worker` (alpine com shell, porque os passos `run:` precisam).

---

## `bravis migrate`

```bash
bravis migrate up      # aplica o que falta
bravis migrate down    # desfaz a última
bravis migrate status  # mostra o estado
```

Aceita exatamente um argumento, entre `up`, `down` e `status`. As migrations
estão embutidas no binário (`migrations/`, seis arquivos até aqui) e são
aplicadas por subcomando próprio — **`serve` nunca altera schema**.

Sem a variável do banco, falha na hora:

```
$ bravis migrate status
erro: BRAVIS_DATABASE_URL e obrigatoria
```

---

## `bravis validate`

Valida um ou mais arquivos de workflow. Aceita arquivo **ou diretório** — num
diretório, casa `*.y*ml` e ordena, para que dois runs produzam o mesmo log.

```bash
$ bravis validate examples/
  ok    daily_analytics              dag  5 steps, 5 dependencias  (manual)
  ok    daily-report                 chain  3 steps, 2 dependencias  cron 0 2 * * *
  ok    hello                        dag  4 steps, 4 dependencias  (manual)
```

Não toca no banco e não sobe servidor, então roda no editor e na CI. Sai com
código diferente de zero e conta as falhas:

```
$ bravis validate quebrado.yaml
  ERRO  quebrado.yaml: ...
erro: 1 de 1 arquivo(s) com erro
```

---

## `bravis run`

Executa um workflow **agora, na própria instância**: sem fila, sem banco, sem
scheduler.

```bash
bravis run examples/hello.yaml
bravis run wf.yaml --param load_full=true --retries 3 --timeout 5m
```

| flag | tipo | padrão | |
|---|---|---|---|
| `--param` | repetível | — | `chave=valor` de um parâmetro declarado no workflow |
| `--workdir` | string | o diretório do arquivo | diretório de trabalho dos passos |
| `--retries` | int | `1` | tentativas por passo (`1` = sem retry) |
| `--timeout` | duration | `0` | timeout por passo (`0` = sem limite) |

`--param` sem `=` é **erro**, não aviso: `--param load_full` rodaria com o
padrão e o operador acharia que o valor foi aplicado.

Três limites que valem saber antes de usar em produção:

- **Só opera com `BRAVIS_ENV=local`** (vazio conta como local). Fora disso o
  executor de processo recusa construir.
- **Um passo com `image:` roda na própria instância**, não em pod — e avisa. O
  `run` não monta executor de Kubernetes; quem faz isso é o `scheduler`.
- **Não há registry de tasks Go.** Um `action:` de task não registrada falha
  citando as disponíveis, porque tasks são registradas por quem compila o
  binário e o CLI genérico não conhece nenhuma.

A saída é prefixada pelo passo, que é o que a torna legível quando vários
correm em paralelo no mesmo nível:

```
workflow hello (dag, 4 steps) em examples
  ▶ preparar
    preparar | preparando
  ✓ preparar
  ▶ extrair
  ▶ validar
```

---

## `bravis publish`

Grava os workflows e as agendas no banco. Aceita arquivo ou diretório, como o
`validate`.

```bash
bravis publish examples/hello.yaml
bravis publish workflows/ --project acme --prune
```

| flag | tipo | padrão | |
|---|---|---|---|
| `--project` | string | `default` | slug do projeto |
| `--prune` | bool | `false` | remove do projeto os workflows ausentes da lista publicada |

```
$ bravis publish examples/
  publicado  daily_analytics          (manual)
  publicado  daily-report             cron 0 2 * * *
  publicado  hello                    (manual)
```

**`--prune` é opcional e não padrão** por um motivo prático: `publish
um-arquivo.yaml` não pode apagar os outros 48 do projeto só porque não foram
citados na linha de comando. Com `--prune`, o histórico dos removidos é
preservado.

O projeto é criado se não existir (`ON CONFLICT DO UPDATE`), o que mantém a FK
honesta enquanto não há gestão de projetos.

---

## `bravis backfill`

Materializa slots passados de um workflow já publicado. **Enfileira, não
executa** — quem executa é o `scheduler`.

```bash
bravis backfill diario --from 2026-01-01 --to 2026-01-31
bravis backfill diario --from 2026-01-01 --to 2026-01-31 --param load_full=true
```

| flag | tipo | | |
|---|---|---|---|
| `--from` | string | **obrigatória** | data inicial, `AAAA-MM-DD` |
| `--to` | string | **obrigatória** | data final, `AAAA-MM-DD` |
| `--param` | repetível | — | vale para **todos** os slots do intervalo |

`--to` inclui o dia inteiro: internamente o fim vira `23:59:59` daquela data.
Data em outro formato é erro com a dica embutida (`use AAAA-MM-DD`).

```
  31 run(s) de backfill enfileirados para diario (2026-01-01 a 2026-01-31)
  rode `bravis scheduler` para executa-los
```

O caso de uso central é exatamente "reprocessa janeiro inteiro com
`load_full=true`".

---

## `bravis marca`

Valida um arquivo de identidade visual sem subir o servidor.

```bash
$ bravis marca brand.example.yaml
  ok    Bravis · Orquestração
        logo      /assets/logo.svg  (simbolo embutido)
        destaque  #aa8450
        Powered by Bravis
```

Existe pelo mesmo motivo do `validate`: um hexadecimal errado no `brand.yaml`
só apareceria quando o container subisse, e a mensagem chegaria pelo log do pod
— longe de quem editou o arquivo. Aqui o erro volta no pull request.

Diferença de comportamento em relação ao boot: **arquivo ausente é erro**. No
`serve`, ausência significa "usa a identidade padrão"; quem pediu para validar
um caminho espera saber que ele não existe.

---

## `bravis hash`

Gera o hash de `BRAVIS_AUTH_SENHA_HASH`.

```bash
$ bravis hash
senha:
BRAVIS_AUTH_SENHA_HASH:
pbkdf2-sha256$...

Falta ainda BRAVIS_AUTH_USUARIO e um BRAVIS_AUTH_SEGREDO de 32+ bytes
(openssl rand -base64 48).
```

A senha é lida do terminal **sem eco**, nunca de argumento: argumento aparece
no `ps` de qualquer processo da máquina e fica no histórico do shell. Com a
entrada redirecionada (script de provisionamento), lê da entrada padrão.

Recusa senha com menos de 12 caracteres — este é o único acesso ao painel.

O hash vai sozinho para **stdout** e os rótulos para **stderr**, então
`bravis hash > hash.txt` grava só o que interessa.

---

## `bravis version`

```bash
$ bravis version
bravis 0.3.0
  commit  bb832ff
  build   2026-09-04T12:00:00Z
  go      go1.25.7 darwin/arm64
```

Versão, commit e data são carimbados no build por `-ldflags`. Compilado direto
com `go build`, sai `bravis dev` sem commit nem data — e distinguir isso de um
artefato de release importa quando alguém reporta comportamento estranho. O
`make image` acrescenta `-dirty` ao commit quando há mudança não commitada.

---

# `bravis-sdk` — o CLI do SDK

Extrai de HTTP e carrega no BigQuery sem escrever Go. Módulo próprio, com o SDK
fixado por versão (hoje `sdk v0.23.0`).

```bash
go install github.com/AreteAcademy/bravis/cmd/bravis-sdk@latest
```

| comando | |
|---|---|
| [`extract`](#bravis-sdk-extract) | extrai de uma URL e imprime |
| [`load`](#bravis-sdk-load) | carrega NDJSON da entrada padrão no BigQuery — **não implementado** |
| [`run`](#bravis-sdk-run) | extrai e carrega num comando |
| `version` | versão e commit |

## `bravis-sdk extract`

```bash
bravis-sdk extract https://api.example.com/data.csv
bravis-sdk extract https://api.example.com/data.json --format json --output json
bravis-sdk extract https://api.example.com/data --retries 5 --timeout 60s
```

| flag | curta | tipo | padrão | |
|---|---|---|---|---|
| `--format` | `-f` | string | vazio | `csv`, `json`, `ndjson`, `xml`; vazio tenta autodetectar |
| `--timeout` | `-t` | duration | `30s` | timeout por tentativa |
| `--total-timeout` | | duration | `5m` | timeout somando as tentativas |
| `--retries` | `-r` | int | `3` | tentativas máximas |
| `--output` | `-o` | string | `table` | `table` ou `json` |

Com `--output json`, cada linha sai como um objeto JSON — é o formato que
alimentaria um `bravis-sdk load` por pipe. Formato não reconhecido cai em CSV
em silêncio.

## `bravis-sdk load`

> **Não faz nada ainda.** O código tem `// TODO: read from stdin and parse
> NDJSON` e carrega uma lista vazia, então reporta `Rows: 0` sem ler a entrada
> padrão. Os exemplos do `--help` descrevem o comportamento pretendido, não o
> atual. **Não use, e não anuncie.**

| flag | curta | padrão | |
|---|---|---|---|
| `--project` | `-p` | — | **obrigatória** |
| `--dataset` | `-d` | `landing` | |
| `--table` | `-t` | `raw_data` | |
| `--metadata` | `-m` | `false` | acrescenta `ingestion_id` e `ingestion_loaded_at` |

## `bravis-sdk run`

Extrai de uma URL e carrega no BigQuery em um comando.

```bash
bravis-sdk run https://api.example.com/data.csv --project meu-projeto
bravis-sdk run https://api.example.com/data.csv --project meu-projeto --dry-run
```

| flag | curta | padrão | |
|---|---|---|---|
| `--project` | `-p` | — | **obrigatória** |
| `--dataset` | `-d` | `landing` | |
| `--table` | `-t` | `raw_data` | |
| `--metadata` | `-m` | `false` | |
| `--dry-run` | | `false` | extrai e para antes de carregar |

**Só lê CSV.** O formato está fixo em `sdk.FormatCSV` no código, e não há flag
`--format` aqui. Uma URL JSON é lida como CSV e o resultado não presta.

`--dry-run` é o caminho útil hoje: extrai, conta linhas e erros, e não escreve
nada.

Para qualquer coisa além disso, o SDK em Go é o caminho — um fetcher inteiro
cabe em vinte linhas, com flags, retry, paginação, procedência e código de
saída vindos de `sdk.Run`. Ver [`examples/08-fetcher-minimo`](../examples/08-fetcher-minimo/).

---

# Variáveis de ambiente

Lidas uma vez, no boot, por `config.Load()` — nada consulta o ambiente depois.

## Obrigatórias

| variável | |
|---|---|
| `BRAVIS_DATABASE_URL` | Postgres. Ausente = **erro de boot** nos cinco subcomandos que usam banco |

## Processo

| variável | padrão | |
|---|---|---|
| `BRAVIS_ENV` | `local` | `local` usa log em texto e libera a UI sem senha; o resto exige credencial e loga JSON |
| `BRAVIS_HTTP_ADDR` | `:8080` | endereço de escuta |
| `BRAVIS_LOG_LEVEL` | `info` | |
| `BRAVIS_SHUTDOWN_TIMEOUT_SECONDS` | `15` | inteiro; valor não numérico é erro de boot |
| `BRAVIS_BRAND_FILE` | `brand.yaml` | identidade visual; ausente = padrão |
| `BRAVIS_UI_URL` | — | base do link da execução no alerta |
| `BRAVIS_SLACK_WEBHOOK` | — | destino do alerta de falha definitiva. Vazio = ninguém é avisado |

## Autenticação

| variável | |
|---|---|
| `BRAVIS_AUTH_USUARIO` | usuário do painel |
| `BRAVIS_AUTH_SENHA_HASH` | hash `pbkdf2-sha256$...`, gerado por `bravis hash` |
| `BRAVIS_AUTH_SEGREDO` | 32+ bytes para assinar a sessão (`openssl rand -base64 48`) |

As três vêm juntas ou nenhuma vem. **Metade configurada é erro de boot**: quem
preencheu o usuário acredita que fechou a porta, e um aviso no log não desfaz
essa crença.

## Ambiente das tasks

| variável | padrão | |
|---|---|---|
| `BRAVIS_TASK_ENV` | vazio | o que cada passo recebe |

A task **não herda** o ambiente do orquestrador — herdar entregaria a
credencial do Postgres a todo passo de todo pipeline. O que ela precisa é
declarado:

```bash
BRAVIS_TASK_ENV=GOOGLE_PROJECT_ID,STAGE   # repassa essas duas do processo
BRAVIS_TASK_ENV=STAGE=prod                # define um literal
BRAVIS_TASK_ENV='*'                       # repassa tudo MENOS as BRAVIS_*
```

`PATH` e `HOME` entram sempre. Um nome ausente **não** vira string vazia:
`GOOGLE_PROJECT_ID=""` faria o dbt falhar com uma mensagem pior que a de
variável ausente.

Com só `PATH` e `HOME` e sem pods, o `scheduler` avisa no boot — um `dbt` ali
falharia com "Env var required but not provided", que não aponta para a causa.

## Kubernetes

Decisões da **instalação**, não do workflow: um pipeline não deve poder
escolher a service account com que roda.

| variável | padrão | formato | |
|---|---|---|---|
| `BRAVIS_PODS` | `auto` | `auto`, `on` ou `off` | valor inválido é erro de boot |
| `BRAVIS_POD_NAMESPACE` | o do cluster | | |
| `BRAVIS_POD_SERVICE_ACCOUNT` | — | | |
| `BRAVIS_POD_PULL_SECRETS` | — | lista por vírgula | |
| `BRAVIS_POD_ENV_FROM_SECRETS` | — | lista por vírgula | em modo pod, é daqui que vem o ambiente da task |
| `BRAVIS_POD_ENV_FROM_CONFIGMAPS` | — | lista por vírgula | |
| `BRAVIS_POD_NODE_SELECTOR` | — | `chave=valor,outra=valor` | |
| `BRAVIS_POD_TOLERATIONS` | — | `chave=valor:efeito,...` | só o operador `Equal` |
| `BRAVIS_POD_MANTER_EM_FALHA` | `false` | `true` mantém o pod para inspeção | |

Em listas, vazios são ignorados: `a,,b` é erro de digitação, e um nome de
secret vazio faria o servidor recusar o pod inteiro. Uma **toleração
malformada é ignorada** em vez de virar erro de boot — ela deixa o pod
`Pending`, que é visível, enquanto recusar o boot pararia também os workflows
que não precisam daquele pool.

`Exists` não é aceito como operador: toleraria qualquer taint com aquela chave,
amplo demais para uma decisão vinda de variável de ambiente.

## Testes

| variável | |
|---|---|
| `BRAVIS_TEST_DATABASE_URL` | banco dos testes de integração; sem ela, eles pulam |

---

# Endpoints HTTP

## Saúde

| | |
|---|---|
| `GET /health` | liveness — **não** consulta o banco |
| `GET /ready` | readiness — consulta, e nomeia a dependência que falhou |

A separação é deliberada: liveness que depende de serviço externo faz o
Kubernetes **matar o pod** quando o banco oscila, em vez de apenas tirá-lo do
balanceador.

## Interface

| | |
|---|---|
| `GET /` | overview com métricas e gráficos |
| `GET /runs` | execuções |
| `GET /runs/{id}` | uma execução, com a DAG e o estado de cada passo |
| `GET /workflows` | lista com busca e filtros |
| `GET /workflows/{slug}` | um workflow |
| `GET /projects` | projetos |
| `POST /workflows/{slug}/toggle` | pausa e retoma a agenda |
| `POST /workflows/{slug}/trigger` | dispara agora (formulário, se houver `params`) |
| `GET /login` · `POST /login` · `POST /logout` | sessão |
| `GET /assets/` | fontes e bundles, servidos do binário |

## JSON

| | |
|---|---|
| `GET /api/workflows/{slug}/graph` | a DAG declarada |
| `GET /api/runs/{id}/graph` | a DAG com o estado de cada passo |

`POST /workflows/{slug}/trigger` executa `dbt build` contra o warehouse. É por
isso que `BRAVIS_ENV` diferente de `local` exige credencial.

---

# Makefile

```
$ make help
  build        Compila o binario em bin/ (gera templ e css antes)
  test         Roda os testes (os de integracao pulam sem Postgres)
  test-int     Roda tudo, inclusive integracao (exige `make up`)
  test-db      Cria e migra o banco de testes (idempotente)
  check        gofmt + vet + testes (portao antes de commitar)
  dev          Hot reload: recompila e reinicia a cada mudanca
  generate     Gera os _templ.go e o CSS
  image        Constroi as imagens para a arquitetura local (nao publica)
  image-push   Publica multi-arch no registry (exige `docker login`)
  image-smoke  Confere que as imagens locais sobem e reportam a versao
  up           Sobe Postgres + API + scheduler localmente
  down         Derruba o ambiente local
  logs         Acompanha os logs da API
  smoke        Verifica /health e /ready contra o ambiente local
```

O ciclo curto:

```bash
make up      # Postgres + API + scheduler
make smoke   # health e ready
make logs
make down
```

`make dev` exige `air` e o Tailwind standalone (`make tailwind-install` baixa,
sem Node). `make generate` exige `templ`.

Os testes de integração usam um banco **separado** (`bravis_test`): desde que a
stack local passou a subir um scheduler de verdade, rodar contra `bravis` era
uma corrida — o scheduler do compose reivindicava os itens que o teste acabara
de enfileirar, e o critério de aceite falhava sem nada estar errado.

`make image` produz duas imagens do mesmo binário:

| tag | base | por quê |
|---|---|---|
| `:<versao>` | distroless | a API só serve HTTP, não executa nada — sem shell |
| `:<versao>-worker` | alpine + tini | os passos `run:` precisam de shell |

`REGISTRY`, `NAMESPACE` e `VERSAO` são variáveis, então um fork publica no
próprio espaço com `make image-push NAMESPACE=outro`.

---

# Defeitos conhecidos

Levantados ao escrever este documento, todos em `cmd/bravis-sdk`. Nada aqui
está no engine.

| | onde | |
|---|---|---|
| **`load` não lê a entrada padrão** | `commands.go` | `// TODO` explícito; carrega lista vazia e reporta `Rows: 0`. O `--help` descreve o que ainda não existe |
| **`run` ignora o formato** | `commands.go` | fixo em `sdk.FormatCSV`, sem flag `--format`; uma URL JSON é lida como CSV |
| **o help se anuncia como `bravis`** | `main.go` | `Use: "bravis"` no binário `bravis-sdk` — colide com o nome do engine |
| **versão fixa no código** | `main.go` | `version = "0.1.0"` em minúscula, enquanto o `VERSION` do repo está em `0.3.0` |
| **`make build` erra o nome** | `Makefile` | grava `bravis-sdk-sdk` e anuncia `./bravis-sdk`; o `install` diz "Installed: bravis" |
| **README aponta para si mesmo** | `README.md` | o link para `cmd/bravis` vai para `../bravis-sdk/`, e o `go work init` sugerido cita `./cmd/bravis` em vez de `./cmd/bravis-sdk` |

Fora do `cmd/bravis-sdk`, um item de empacotamento: **o módulo raiz não tem
tag**, então `go install .../cmd/bravis@latest` pega o último commit e produz um
binário que se identifica como `dev`. Ver [Instalar](#instalar).

**Para o site:** dos três comandos do `bravis-sdk`, só `extract` e
`run --dry-run` fazem o que dizem. Ao levar comandos para a landing page, use
os dez do engine e o `extract`; para carga no BigQuery, aponte o SDK em Go, não
o `load` do CLI. E prefira `make build` ou a imagem ao `go install` do engine,
enquanto não houver tag na raiz.
