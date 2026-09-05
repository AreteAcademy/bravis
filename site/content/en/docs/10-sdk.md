---
title: Go SDK
description: Writing a fetcher — extract from a source, transform, and load into a destination.
group: SDK
order: 10
slug: sdk
---

When a step's job is to **fetch data and load it somewhere**, the Go SDK gives
you the whole fetcher in a few lines. It is a separate module, versioned
independently from the engine.

```bash
go get github.com/AreteAcademy/brevis/sdk@latest
```

Requires Go 1.23 or newer — the SDK streams rows as `iter.Seq2`.

:::warning Do not use `v0.1.0`
It shipped a `go.mod` pinning a revision that does not exist, and the Go module
proxy is immutable. Start at `v0.1.1`.
:::

## Three steps

```go
import (
	"github.com/AreteAcademy/brevis/sdk"
	"github.com/AreteAcademy/brevis/sdk/from"
	"github.com/AreteAcademy/brevis/sdk/to/bigquery"
)

rows, err := sdk.Extract(ctx, sdk.Source{
	From: from.HTTP{URL: "https://api.example.com/v1/events"},
})

rows = sdk.Transform(rows, sdk.Accept("id", "created_at", "amount"))

res, err := sdk.Load(ctx, rows, sdk.Target{
	To:      bigquery.Table{Dataset: "bronze", Name: "events"},
	Columns: []string{"id", "created_at", "amount"},
})
```

`Extract` reads, `Transform` reshapes, `Load` writes. Each takes and returns a
sequence — nothing is materialised in memory all at once.

## The driver is a value, not a setting

`from.HTTP` carries everything an HTTP source needs: URL, headers, retry,
pagination, and what a response means. `from.Files` carries a path and a format.
Neither has to make room for the other's fields, so there is no source struct
collecting forty options of which each driver reads six.

It also decides **what you compile**. Go prunes dependencies by package
imported, never by field used:

| what you import | packages | AWS | Google |
|---|---|---|---|
| `sdk` | 190 | no | no |
| `sdk` + `from` | 194 | no | no |
| `sdk` + `from` + `to` (files) | 195 | no | no |
| `sdk` + `to/bigquery` | 456 | no | **yes** |
| `sdk` + `from` + `store/s3` | 265 | **yes** | no |

A whole file pipeline — read and write — costs 195 packages and no cloud SDK at
all. **A driver with a vendor SDK behind it lives in its own package**, which is
why BigQuery is `to/bigquery` and the object stores are `store/s3` and
`store/gcs`.

## Sources and destinations

| source | package |
|---|---|
| HTTP | `from.HTTP` |
| files (local, S3, GCS) | `from.Files` |
| Postgres | `from/postgres` |
| MySQL | `from/mysql` |
| several sources | `from.Many` |

| destination | package |
|---|---|
| files | `to.Files` |
| BigQuery | `to/bigquery` |
| Postgres | `to/postgres` |
| MySQL | `to/mysql` |
| Redshift | `to/redshift` |

The path scheme decides the backend, and the `Store` is **passed in** rather
than chosen inside the driver:

```go
from.Files{Path: "s3://bucket/day=1/*.ndjson", Store: s3.New(client)}
to.Files{Path: "gs://bucket/landing/", Store: gcs.New(client)}
```

That is what keeps a local-files program from compiling a single line of AWS or
Google code.

## A whole fetcher

`sdk.Run` takes care of flags, `-dry-run`, logging, retry, pagination,
provenance, table creation and the exit code. What is left in the file is only
what is specific to that source:

```go
package main

import (
	"time"

	"github.com/AreteAcademy/brevis/sdk"
	"github.com/AreteAcademy/brevis/sdk/from"
)

func main() {
	sdk.Run(sdk.Pipeline{
		Source: sdk.Source{
			From: from.HTTP{
				URL:     "https://api.example.com/v1/events",
				Timeout: 15 * time.Second,
			},
			Guard:  sdk.RejectIf("error"),
			Expand: sdk.ArrayAt("results"),
		},
		Target: sdk.Target{
			Provider: "example",
			Entity:   "events",
			Key:      sdk.Key("id"),
			When:     sdk.Field("created_at"),
		},
	})
}
```

```bash
go run ./fetcher -dry-run   # extracts, counts rows and errors, writes nothing
go run ./fetcher
```

## The run context

When the fetcher runs **as a workflow step**, the engine injects into the
environment what it could not otherwise know: whether this is the first run,
which parameters it was dispatched with, which run it is. The SDK reads that in
`Pipeline.Run`:

```go
Before: func(ctx context.Context, p *sdk.Pipeline) error {
	if p.Run.Params["load_full"] == "true" {
		p.Source.From = from.HTTP{URL: base + "?full=1"}
	}
	return nil
},
```

Run by hand, `Run` comes back zeroed — reading it is optional, and ignoring it
costs nothing.

With no history, the answer to "is this the first run?" is always **no**:
creating a table without certainty is worse than not creating it.

## Reference

- [pkg.go.dev](https://pkg.go.dev/github.com/AreteAcademy/brevis/sdk) — the complete API
- [`examples/`](https://github.com/AreteAcademy/brevis/tree/master/examples) — twelve runnable examples
- [`CHANGELOG.md`](https://github.com/AreteAcademy/brevis/blob/master/CHANGELOG.md) — version-by-version history
