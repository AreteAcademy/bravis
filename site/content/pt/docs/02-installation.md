---
title: Instalação
description: Como obter o binário, o que ele exige e como confirmar que a instalação está correta.
group: Começar
order: 2
slug: installation
---

O brevis.sh é distribuído como **binário único** e como **imagem Docker**. Não
há runtime para instalar ao lado: as migrations, os assets da interface e as
fontes vão dentro do binário.

## Requisitos

| | |
|---|---|
| **PostgreSQL** | obrigatório para `serve`, `scheduler`, `migrate`, `publish` e `backfill` |
| **Go 1.25+** | apenas se você for compilar do código |
| **Kubernetes** | opcional — sem cluster, os passos rodam como processos locais |

`brevis run`, `validate`, `marca`, `hash` e `version` **não** precisam de banco.

## Compilando do código

É o caminho que produz um binário com versão e commit carimbados:

```bash
git clone https://github.com/AreteAcademy/brevis.git
cd brevis
make build
./bin/brevis version
```

```
brevis 0.3.0
  commit  bb832ff
  build   2026-09-05T12:00:00Z
  go      go1.25.7 darwin/arm64
```

:::warning O engine ainda não tem release taggeada
Todas as tags do repositório são do SDK (`sdk/v*`); o módulo raiz não tem
nenhuma. Um `go install github.com/AreteAcademy/brevis/cmd/brevis@latest`
resolve para o último commit do branch e produz um binário que se identifica
como `brevis dev`, sem commit nem data — os `-ldflags` só entram pelo
`make build` e pelo `docker build`. Para uma versão rastreável, use a imagem ou
o `make build`.
:::

## Docker

Duas imagens do mesmo binário, porque os dois papéis têm exigências opostas:

| tag | base | por quê |
|---|---|---|
| `:0.3.0` | distroless | a API só serve HTTP e não executa nada — sem shell, superfície mínima |
| `:0.3.0-worker` | alpine + tini | os passos `run:` precisam de shell |

```bash
docker run --rm daniel3843/brevis:latest version
```

## Ambiente local completo

O repositório traz um `docker-compose.yml` que sobe Postgres, API e scheduler:

```bash
make up      # Postgres + API + scheduler
make smoke   # confere /health e /ready
make logs
make down
```

A interface fica em `http://localhost:8080`.

## Configuração mínima

Uma única variável é obrigatória:

```bash
export BREVIS_DATABASE_URL='postgres://brevis:brevis@localhost:5432/brevis?sslmode=disable'
```

Sem ela, os subcomandos que usam banco falham no boot — de propósito. Um
processo que sobe sem credencial só descobre o problema no primeiro request, e
aí o readiness já mentiu para o orquestrador.

```
$ brevis migrate status
erro: BREVIS_DATABASE_URL e obrigatoria
```

A lista completa está em [Configuração](/docs/configuration/).

## Aplicando o schema

As migrations são embutidas no binário e aplicadas por subcomando próprio —
**`serve` nunca altera schema**:

```bash
brevis migrate up
brevis migrate status
```

## Confirmando

```bash
brevis version
brevis validate examples/
```

```
  ok    daily_analytics              dag  5 steps, 5 dependencias  (manual)
  ok    daily-report                 chain  3 steps, 2 dependencias  cron 0 2 * * *
  ok    hello                        dag  4 steps, 4 dependencias  (manual)
```

Se os dois respondem, a instalação está correta. Siga para o
[Quickstart](/docs/quickstart/).
