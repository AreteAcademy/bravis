---
title: CLI
description: Os dez subcomandos do engine, com flags, argumentos e o que cada um exige.
group: Referência
order: 7
slug: cli
---

O engine é um binário com subcomandos, não vários binários: API, scheduler e
workers são papéis do mesmo sistema, e um binário só mantém uma versão, uma
imagem e um caminho de build.

```
brevis [comando] [flags]
```

| comando | banco | credencial fora de `local` | papel |
|---|---|---|---|
| [`serve`](#serve) | **sim** | **exigida** | API + interface |
| [`scheduler`](#scheduler) | **sim** | **exigida** | os dois laços |
| [`migrate`](#migrate) | **sim** | **exigida** | schema |
| [`publish`](#publish) | **sim** | **exigida** | grava workflow e agenda |
| [`backfill`](#backfill) | **sim** | **exigida** | reprocessa um intervalo |
| [`run`](#run) | não | — | executa agora, na própria instância |
| [`validate`](#validate) | não | — | valida YAML de workflow |
| [`marca`](#marca) | não | — | valida YAML de marca |
| [`hash`](#hash) | não | — | gera o hash da senha |
| [`version`](#version) | não | — | versão, commit, build |

Os cinco primeiros chamam a carga de configuração, que **falha no boot** sem
`BREVIS_DATABASE_URL`. Os outros cinco não a chamam — e é por isso que
`validate` serve na CI, onde não há Postgres.

Os mesmos cinco herdam a regra de credencial: com `BREVIS_ENV` diferente de
`local`, subir sem as três variáveis de autenticação é **erro de boot**.

---

## serve

Sobe a API HTTP e a interface. Sem flags — tudo vem do ambiente.

```bash
brevis serve
```

Encerra em `SIGINT`/`SIGTERM` com shutdown graceful, para que um deploy não
corte requisições em voo. Este processo **não materializa agendas**: para isso é
preciso um `brevis scheduler` ao lado.

`CMD` da imagem `api` (distroless, sem shell).

---

## scheduler

Os dois laços: o scheduler materializa agendas em runs, o dispatcher tira da
fila e executa.

```bash
brevis scheduler --interval 5s --concurrency 4 --max-pods 10
```

| flag | tipo | padrão | |
|---|---|---|---|
| `--interval` | duration | `10s` | intervalo entre ciclos |
| `--concurrency` | int | `5` | **runs** simultâneos |
| `--max-pods` | int | `5` | **passos** simultâneos no total |

`CMD` da imagem `worker` (alpine com shell, porque os passos `run:` precisam).

---

## migrate

```bash
brevis migrate up      # aplica o que falta
brevis migrate down    # desfaz a última
brevis migrate status  # mostra o estado
```

Aceita exatamente um argumento. As migrations estão embutidas no binário e são
aplicadas por subcomando próprio — **`serve` nunca altera schema**.

---

## validate

Valida um ou mais arquivos de workflow. Aceita arquivo **ou diretório**.

```bash
brevis validate examples/
```

```
  ok    daily_analytics              dag  5 steps, 5 dependencias  (manual)
  ok    hello                        dag  4 steps, 4 dependencias  (manual)
```

Não toca no banco e não sobe servidor, então roda no editor e na CI. Sai com
código diferente de zero e conta as falhas.

---

## run

Executa um workflow **agora, na própria instância**: sem fila, sem banco, sem
scheduler.

```bash
brevis run examples/hello.yaml
brevis run wf.yaml --param load_full=true --retries 3 --timeout 5m
```

| flag | tipo | padrão | |
|---|---|---|---|
| `--param` | repetível | — | `chave=valor` de um parâmetro declarado |
| `--workdir` | string | o diretório do arquivo | diretório de trabalho dos passos |
| `--retries` | int | `1` | tentativas por passo (`1` = sem retry) |
| `--timeout` | duration | `0` | timeout por passo (`0` = sem limite) |

`--param` sem `=` é **erro**, não aviso: `--param load_full` rodaria com o padrão
e o operador acharia que o valor foi aplicado.

:::warning Três limites
`run` só opera com `BREVIS_ENV=local`; um passo com `image:` executa na própria
instância e avisa; e não há registry de tasks Go, então um `action:` de task não
registrada falha citando as disponíveis.
:::

---

## publish

Grava os workflows e as agendas no banco. Aceita arquivo ou diretório.

```bash
brevis publish examples/hello.yaml
brevis publish workflows/ --project acme --prune
```

| flag | tipo | padrão | |
|---|---|---|---|
| `--project` | string | `default` | slug do projeto |
| `--prune` | bool | `false` | remove do projeto os workflows ausentes da lista |

Com `--prune`, o histórico dos removidos é preservado.

---

## backfill

Materializa slots passados de um workflow já publicado. **Enfileira, não
executa.**

```bash
brevis backfill diario --from 2026-01-01 --to 2026-01-31 --param load_full=true
```

| flag | | |
|---|---|---|
| `--from` | **obrigatória** | data inicial, `AAAA-MM-DD` |
| `--to` | **obrigatória** | data final, `AAAA-MM-DD` |
| `--param` | | vale para **todos** os slots do intervalo |

`--to` inclui o dia inteiro: internamente o fim vira `23:59:59` daquela data.

---

## marca

Valida um arquivo de identidade visual sem subir o servidor.

```bash
brevis marca brand.yaml
```

```
  ok    Brevis · Orquestração
        logo      /assets/logo.svg  (simbolo embutido)
        destaque  #aa8450
        Powered by Brevis
```

Diferença em relação ao boot: **arquivo ausente é erro**. No `serve`, ausência
significa "usa a identidade padrão"; quem pediu para validar um caminho espera
saber que ele não existe.

---

## hash

Gera o hash de `BREVIS_AUTH_SENHA_HASH`.

```bash
brevis hash
```

A senha é lida do terminal **sem eco**, nunca de argumento: argumento aparece no
`ps` de qualquer processo da máquina e fica no histórico do shell. Recusa senha
com menos de 12 caracteres.

O hash vai sozinho para **stdout** e os rótulos para **stderr**, então
`brevis hash > hash.txt` grava só o que interessa.

---

## version

```bash
brevis version
```

```
brevis 0.3.0
  commit  bb832ff
  build   2026-09-05T12:00:00Z
  go      go1.25.7 darwin/arm64
```

Compilado direto com `go build`, sai `brevis dev` sem commit nem data — e
distinguir isso de um artefato de release importa quando alguém reporta
comportamento estranho.
