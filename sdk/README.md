# Bravis SDK

High-performance data extraction and loading library for Go.

Extract HTTP data with retry, timeout, and format handling. Load to BigQuery with automatic strategy selection.

## Installation

```bash
go get github.com/AreteAcademy/bravis/sdk@latest
go mod tidy
```

Requires Go 1.23 or newer (the SDK streams rows as `iter.Seq2`).

> **Do not use `v0.1.0`.** It shipped a `go.mod` pinning a revision that does
> not exist, so it fails to build for everyone. The Go module proxy is
> immutable and cannot be corrected after the fact. Start at `v0.1.1`.

## Duas chamadas

```go
dados, err := sdk.Extract(ctx, sdk.Fonte{
	URL:      "https://api.open-meteo.com/v1/forecast?...",
	Guarda:   sdk.RecusarSe("error"),
	Expandir: sdk.ArraysParalelos("hourly", "time", "temperature_2m"),
})

res, err := sdk.Load(ctx, dados, sdk.Destino{
	Provider: "open_meteo",
	Entity:   "hourly_temperature",
	Chave:    sdk.Chave("latitude", "longitude", "time"),
	Quando:   sdk.Campo("time"),
})
```

Everything between those two calls that is not specific to the vendor lives in
the SDK: config, retry, pagination, expansion, provenance, table creation,
deduplication and the result you log.

Or the whole fetcher as a value, which gets flags, `-dry-run`, logging and the
exit code for free:

```go
func main() {
	sdk.Rodar(sdk.Pipeline{
		Fonte:   sdk.Fonte{URL: "...", Expandir: sdk.ArrayEm("results")},
		Destino: sdk.Destino{
			Provider: "example", Entity: "events",
			Chave:  sdk.Chave("id"),
			Quando: sdk.Campo("created_at"),
		},
	})
}
```

The first real consumer went from **156 lines to 44** on this API.

`sdk/extract` and `sdk/load` stay available and unchanged: reach for them
directly when you need a shape these two calls do not cover. The hard case has
to stay possible.

### Configuration and where it came from

Resolved in this order: what you set explicitly, then the environment, then the
SDK default, then an error when there is no sensible default.

| variável | default |
|---|---|
| `GOOGLE_PROJECT_ID` | — (erro) |
| `BRAVIS_SDK_DATASET` | `landing` |
| `BRAVIS_SDK_STAGING_BUCKET` | `<projeto>-bravis-staging` |
| `BRAVIS_SDK_LOG_LEVEL` | `info` |

Every run logs where each value came from — `projeto=x (de GOOGLE_PROJECT_ID)`.
Reading the environment silently is how a job works on your machine and writes
to the wrong dataset in the pod.

### Deduplication

`Destino.Dedup` defaults to appending, which costs nothing. `sdk.DedupMerge`
stages into a temporary table and MERGEs on `ingestion_id`, so re-running the
same window is a no-op — at the cost of one scan of the destination per load,
which is why it is never enabled on your behalf.

### The table

By default the SDK creates the landing table with the six-column contract,
partitioned by `ingestion_loaded_at` and clustered by `provider, entity`. An
unpartitioned landing table costs a full scan on every MERGE the bronze layer
runs.

It never alters an existing table: one that does not match the contract is an
error naming the differing column. A loader that can ALTER is a loader that can
erase history.

> **`Chave` is frozen.** The field order you give it and the `|` separator both
> feed `source_key`, which feeds `ingestion_id`. Change either and the same
> reading lands twice and stops matching the row a Python fetcher writes for it.

## Quick Start

### Extract CSV

```go
package main

import (
	"context"
	"log"

	"github.com/AreteAcademy/bravis/sdk"
	"github.com/AreteAcademy/bravis/sdk/extract"
)

func main() {
	ctx := context.Background()
	lines, err := extract.CSV(ctx, sdk.Fonte{
		URL: "https://example.gov/api/data.csv",
	})
	if err != nil {
		log.Fatal(err)
	}

	for env, err := range lines {
		if err != nil {
			log.Printf("error: %v", err)
			continue
		}
		// Process env
		log.Printf("Payload: %+v", env.Payload)
	}
}
```

### Load to BigQuery

```go
package main

import (
	"context"
	"log"
	"github.com/AreteAcademy/bravis/sdk"
	"github.com/AreteAcademy/bravis/sdk/load"
)

func main() {
	ctx := context.Background()
	
	loader, err := load.New(ctx, nil,
		sdk.WithProjectID("my-project"),
		sdk.WithDataset("landing"),
		sdk.WithTable("raw_data"),
	)
	if err != nil {
		log.Fatal(err)
	}

	// Prepare envelopes (e.g., from extract)
	envelopes := []sdk.Envelope{
		{
			Provider:  "example_api",
			Entity:    "transactions",
			SourceKey: "tx-123",
			RecordTS:  "2026-01-01T10:00:00Z",
			Payload:   map[string]any{"amount": 100},
		},
	}

	result, err := loader.Load(ctx, envelopes...)
	if err != nil {
		log.Fatal(err)
	}

	log.Printf("Loaded %d rows in %v using %s strategy", 
		result.RowsLoaded, result.Duration, result.Strategy)
}
```

## CSV headers

By default the first CSV row is consumed as the column names, so a file with a
header and N data rows yields N Envelopes keyed by those names:

```go
// name,age
// Alice,30
// Bob,25
lines, _ := extract.CSV(ctx, sdk.Fonte{URL: url})
// -> {name: Alice, age: 30}
// -> {name: Bob,   age: 25}
```

Set `NoHeader: true` when the file has no header row. No row is then treated as
special and every line is keyed positionally:

```go
lines, _ := extract.CSV(ctx, sdk.Fonte{URL: url, NoHeader: true})
// -> {field_0: name,  field_1: age}
// -> {field_0: Alice, field_1: 30}
// -> {field_0: Bob,   field_1: 25}
```

## Pagination

Three strategies, picked by which field you set. All of them cap out at
`MaxPages` (1000 by default) so a server that always advertises a next page
cannot spin forever.

```go
// Link: <...>; rel="next"
extract.NDJSON(ctx, sdk.Fonte{URL: url, FollowLinks: true})

// {"results": [...], "next_page": "abc"} -- the cursor is sent back as the
// query parameter of the same name, and DataKey says where the rows live.
extract.JSON(ctx, sdk.Fonte{URL: url, CursorKey: "next_page", DataKey: "results"})

// ?offset=0, ?offset=100, ... until a page comes back empty
extract.NDJSON(ctx, sdk.Fonte{URL: url, OffsetKey: "offset", PageSize: 100})
```

## Rate limiting

`Fonte.RateLimiter` is any type with `Wait(ctx) error`, which
`*golang.org/x/time/rate.Limiter` satisfies as-is — so you get it without the
SDK taking on the dependency:

```go
fonte.RateLimiter = rate.NewLimiter(rate.Every(time.Second), 1)
```

It is consulted before every attempt, retries included.

## Extract Formats

- **CSV** — tabular data; the first row names the columns (set `NoHeader: true` to key rows positionally instead)
- **NDJSON** — newline-delimited JSON; streaming friendly
- **JSON** — array or single object
- **XML** — the repeated element under the root becomes one Envelope each

## Extract Features

- **Retry with exponential backoff** — 429, 5xx, and network errors only
- **Timeout** — per-attempt and total, separate
- **Guard function** — validate 200-OK-but-wrong-content
- **Pagination** — Link headers, body cursor, or offset (see below)
- **Rate limiting** — any `Wait(ctx) error`, including `*rate.Limiter`
- **Observability** — structured logs with redacted URLs
- **Stream decoding** — no materializing entire response

## Load Strategy

The loader automatically chooses:

| Rows | Strategy | Benefit |
|------|----------|---------|
| <= 5000 | Inline | One request, no external staging |
| > 5000 | GCS staging | No memory limit, deletes after load |

Both are batch load jobs; they differ only in where BigQuery reads from. The
SDK never uses streaming inserts, so rows are visible to DML as soon as the job
finishes.

Configure the threshold with `sdk.WithThresholdForGCS(n)`, or set
`LoadConfig.ThresholdForGCS` directly. `load.New` accepts either a
`*LoadConfig`, a list of options, or both — and never mutates the config you
pass it.

## Three ways to shape a row

| mode | writes | use when |
|---|---|---|
| default | the payload, as-is | you own the schema and want nothing imposed |
| `WithMetadata(true)` | payload with `_bravis_*` fields folded in | you want provenance beside the data, in one flat object |
| `WithEnvelopeColumns(true)` | the six-column landing contract, payload nested | you need rows to match a bronze layer keyed on `ingestion_id` |

The last two are mutually exclusive and `New` refuses both at once — they are
two different answers to the same question.

Envelope mode writes exactly this:

```sql
CREATE TABLE <dataset>.<table> (
  ingestion_id        STRING    NOT NULL,
  ingestion_loaded_at TIMESTAMP NOT NULL,
  provider            STRING    NOT NULL,
  entity              STRING    NOT NULL,
  source_key          STRING,
  payload             JSON      NOT NULL
)
PARTITION BY DATE(ingestion_loaded_at)
CLUSTER BY provider, entity;
```

It exists so `ingestion_id` keeps a single owner. Rebuild those columns in each
consumer and the ids drift apart, which is the duplication the contract exists
to prevent.

## BigQuery Schema

**You define the schema.** The SDK writes raw JSON payloads.

Create your table with whatever schema makes sense for your data:

```sql
-- Simple: just store the payload
CREATE TABLE {dataset}.{table} (
  payload JSON NOT NULL
)
```

Or with metadata:

```sql
-- Rich: store payload + metadata
CREATE TABLE {dataset}.{table} (
  payload JSON NOT NULL
)
-- Metadata fields (if AddMetadata=true) will be inside payload:
-- - _bravis_ingestion_id (deterministic UUID v5)
-- - _bravis_ingestion_loaded_at (load timestamp)
-- - _bravis_provider (data source)
-- - _bravis_entity (entity type)
-- - _bravis_source_key (unique key from source)
-- - _bravis_record_ts (record timestamp at source)
```

Or structured:

```sql
-- Structured: extract fields from JSON
CREATE TABLE {dataset}.{table} (
  ingestion_id STRING NOT NULL,
  loaded_at TIMESTAMP NOT NULL,
  data JSON NOT NULL
)
PARTITION BY DATE(loaded_at)
CLUSTER BY ingestion_id
```

## Idempotency

The `ingestion_id` is a deterministic UUID v5 derived from:
- Namespace: fixed (`e3a4f8c0-1b9d-4ea0-9c2e-77f6a6c4a4d7`)
- Key: `{provider}|{entity}|{source_key}|{record_ts}`

**Idempotency is a downstream responsibility** (bronze layer deduplication), not the SDK's. The same envelope loaded twice produces duplicate rows; use `ingestion_id` to deduplicate at bronze.

## Error Handling

Extract errors include URL, attempt number, and duration. Load errors include table name, row count, and per-row BigQuery errors (truncated).

`Load` always returns a `LoadResult`, including on failure, so the per-row
diagnostics BigQuery reported are readable after an error:

```go
result, err := loader.Load(ctx, envelopes...)
if err != nil {
	log.Printf("load failed: %v", err)
	for _, e := range result.ErrorRows { // never nil-derefs
		log.Printf("  %s", e)
	}
}
```

## Configuration

### Extract
```go
fonte := sdk.Fonte{
	URL:          "https://api.example.com/data",
	Method:       "GET",           // default
	Timeout:      30 * time.Second, // per attempt
	TotalTimeout: 5 * time.Minute,  // total
	NoHeader:     false,            // CSV only; false = first row is the header
	RetryConfig: &sdk.RetryConfig{
		MaxAttempts:    3,
		InitialBackoff: 1 * time.Second,
		MaxBackoff:     60 * time.Second,
		JitterFraction: 0.1,
	},
	Guard: func(status int, body []byte) error {
		if !bytes.Contains(body, []byte(`"status":"ok"`)) {
			return fmt.Errorf("API returned error")
		}
		return nil
	},
}
```

### Load
```go
cfg := &sdk.LoadConfig{
	ProjectID:       "my-project",
	Dataset:         "landing",
	StagingBucket:   "my-staging-bucket",
	ThresholdForGCS: 5000,
	Format:          "ndjson", // the only format written today; csv and parquet are refused
	DeleteAfterLoad: true,
}
```

## Testing

Run tests with:

```bash
go test ./sdk/...
```

Tests for extract use `httptest`; no network access required.

Tests for load use a mock BigQuery client. For integration testing against real BigQuery:

```bash
go test ./sdk/... -short=false
```

The reference test compares `ingestion_id` values against Python implementation to ensure exact idempotency.

## When NOT to use

- **Streaming scenarios** — use Storage Write API for sub-second latency
- **Non-BigQuery destinations** — extend with custom loaders
- **Complex transformations** — use dbt instead
- **Batches > 1GB** — consider splitting into multiple loads

## Performance

- **Binary size** — ~2.4 MB (vs. 144 MB for Python + dependencies)
- **Startup** — < 10ms
- **Throughput** — ~50K rows/sec (limited by BigQuery load job, not SDK)
- **Memory** — O(batch size); stream-based extraction uses minimal memory

## Developing

Build and test locally:

```bash
cd sdk
go build ./...
go test ./...
go vet ./...
```

The SDK has its own `go.mod` to keep dependencies minimal. Import only:

- `cloud.google.com/go/bigquery` — official client
- `cloud.google.com/go/storage` — GCS staging
- `github.com/google/uuid` — deterministic IDs
- Stdlib only otherwise

## License

MIT — see [LICENSE](../LICENSE).
