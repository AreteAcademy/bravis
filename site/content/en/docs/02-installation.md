---
title: Installation
description: How to get the binary, what it requires, and how to confirm the install is correct.
group: Getting started
order: 2
slug: installation
---

brevis.sh ships as a **single binary** and as a **Docker image**. There is no
runtime to install alongside it: migrations, interface assets and fonts all live
inside the binary.

## Requirements

| | |
|---|---|
| **PostgreSQL** | required for `serve`, `scheduler`, `migrate`, `publish` and `backfill` |
| **Go 1.25+** | only if you build from source |
| **Kubernetes** | optional — with no cluster, steps run as local processes |

`brevis run`, `validate`, `marca`, `hash` and `version` need **no** database.

## Building from source

This is the path that produces a binary with version and commit stamped in:

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

:::warning The engine has no tagged release yet
Every tag in the repository belongs to the SDK (`sdk/v*`); the root module has
none. A `go install github.com/AreteAcademy/brevis/cmd/brevis@latest` resolves
to the latest commit on the branch and produces a binary that identifies itself
as `brevis dev`, with no commit and no date — the `-ldflags` only come in
through `make build` and `docker build`. For a traceable version, use the image
or `make build`.
:::

## Docker

Two images of the same binary, because the two roles have opposite needs:

| tag | base | why |
|---|---|---|
| `:0.3.0` | distroless | the API only serves HTTP and executes nothing — no shell, minimal surface |
| `:0.3.0-worker` | alpine + tini | `run:` steps need a shell |

```bash
docker run --rm daniel3843/brevis:latest version
```

## A full local environment

The repository ships a `docker-compose.yml` that brings up Postgres, the API and
the scheduler:

```bash
make up      # Postgres + API + scheduler
make smoke   # checks /health and /ready
make logs
make down
```

The interface is at `http://localhost:8080`.

## Minimum configuration

A single variable is required:

```bash
export BREVIS_DATABASE_URL='postgres://brevis:brevis@localhost:5432/brevis?sslmode=disable'
```

Without it, the subcommands that use the database fail at boot — on purpose. A
process that comes up without credentials only discovers the problem on the
first request, and by then readiness has already lied to the orchestrator.

```
$ brevis migrate status
erro: BREVIS_DATABASE_URL e obrigatoria
```

The full list is in [Configuration](/docs/configuration/).

## Applying the schema

Migrations are embedded in the binary and applied by their own subcommand —
**`serve` never alters the schema**:

```bash
brevis migrate up
brevis migrate status
```

## Confirming

```bash
brevis version
brevis validate examples/
```

```
  ok    daily_analytics              dag  5 steps, 5 dependencias  (manual)
  ok    daily-report                 chain  3 steps, 2 dependencias  cron 0 2 * * *
  ok    hello                        dag  4 steps, 4 dependencias  (manual)
```

If both answer, the install is correct. Move on to the
[Quickstart](/docs/quickstart/).
