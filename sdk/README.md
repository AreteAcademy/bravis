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

## Two calls

```go
dados, err := sdk.Extract(ctx, sdk.Source{
	URL:      "https://api.open-meteo.com/v1/forecast?...",
	Guard:   sdk.RejectIf("error"),
	Expand: sdk.ParallelArrays("hourly", "time", "temperature_2m"),
})

res, err := sdk.Load(ctx, dados, sdk.Target{Table: "hourly_temperature"})
```

**The payload is yours.** The SDK writes the record as `Transform` left it and
adds nothing — no wrapper column, no provenance columns, no shape it decided
for you.

The one thing it will add, on request, is two metadata fields. `ingestion_id`
is built out of provenance, which is why asking for it is what makes
`Provider`, `Entity` and `Key` necessary:

```go
res, err := sdk.Load(ctx, dados, sdk.Target{
	Provider:      "open_meteo",
	Entity:        "hourly_temperature",
	Key:           sdk.Key("latitude", "longitude", "time"),
	When:          sdk.Field("time"),
	ExtraMetadata: true,
})
```

With the flag off, `Key` and `When` are never called: the SDK does not read a
field out of your payload to build a column it is not writing.

Everything between those two calls that is not specific to the vendor lives in
the SDK: config, retry, pagination, expansion, table creation, deduplication
and the result you log.

## Transform

The step between, where your own function reshapes each record before it is
written:

```go
data, err := sdk.Extract(ctx, source)

data = sdk.Transform(data,
	sdk.Without("generationtime_ms"),                            // request metadata
	sdk.Rename(map[string]string{"temperature_2m": "temp_c"}),   // name it what it is
	sdk.Compute("temp_f", func(r map[string]any) (any, error) {  // derive
		return r["temp_c"].(float64)*9/5 + 32, nil
	}),
	func(payload any) (any, error) {                             // or anything of yours
		r := payload.(map[string]any)
		if r["temp_c"] == nil {
			return nil, sdk.SkipRecord                           // drop the record
		}
		return r, nil
	},
)

res, err := sdk.Load(ctx, data, target)
```

`Transformer` is `func(payload any) (any, error)`. The helpers are the four
things every fetcher ends up writing by hand:

| helper | does |
|---|---|
| `Only(fields...)` | keep just these |
| `Without(fields...)` | keep everything except these |
| `Rename(map)` | source's name → yours |
| `Compute(name, fn)` | add a derived field |

`Rename` and `Compute` refuse to overwrite an existing field: which value
survived would otherwise depend on map iteration order, and a silently
replaced value is the kind of thing nobody notices until the numbers are
wrong.

It stays lazy, so a paginated source still does not have to fit in memory.

**Order matters against `Target.Key`.** Provenance is stamped after every
Transformer has run, so a rename here has to be reflected there:

```go
sdk.Transform(data, sdk.Rename(map[string]string{"time": "observed_at"}))
sdk.Target{Key: sdk.Key("latitude", "longitude", "observed_at")}  // the new name
```

Naming the old one is an error listing what the record actually has — not a
short key, which would silently change every `ingestion_id`.

This is a seam, not a transformation engine. Heavy reshaping belongs
downstream in dbt; what belongs here is the shaping a row needs before it is
worth storing at all.

`Driver` selects the implementation on each side — `DriverHTTP` for a Source,
`DriverBigQuery` for a Target. One of each exists today, and an empty Driver
takes the default, so nothing has to be set for the common case.

> **`Driver` is not `Provider`.** Driver is which system carries the rows;
> Provider is which vendor the data came from. Only Provider feeds
> `ingestion_id`.

Or the whole fetcher as a value, which gets flags, `-dry-run`, logging and the
exit code for free:

```go
func main() {
	sdk.Run(sdk.Pipeline{
		Source:   sdk.Source{URL: "...", Expand: sdk.ArrayAt("results")},
		Target: sdk.Target{
			Provider: "example", Entity: "events",
			Key:  sdk.Key("id"),
			When: sdk.Field("created_at"),
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

| variable | default |
|---|---|
| `GOOGLE_PROJECT_ID` | — (error) |
| `BRAVIS_SDK_DATASET` | `landing` |
| `BRAVIS_SDK_STAGING_BUCKET` | `<projeto>-bravis-staging` |
| `BRAVIS_SDK_LOG_LEVEL` | `info` |

Every run logs where each value came from — `projeto=x (de GOOGLE_PROJECT_ID)`.
Reading the environment silently is how a job works on your machine and writes
to the wrong dataset in the pod.

### Deduplication

`Target.Dedup` defaults to appending, which costs nothing. `sdk.DedupMerge`
stages into a temporary table and MERGEs on `ingestion_id`, so re-running the
same window is a no-op — at the cost of one scan of the destination per load,
which is why it is never enabled on your behalf.

The merge names its columns, reconciling the destination's schema against the
rows before it runs. The rule is asymmetric on purpose:

| situation | what happens |
|---|---|
| the rows carry a column the destination lacks | **refused**, naming the column |
| the destination has a column the rows omit    | fine, it stays NULL |
| the same name with incompatible types         | **refused**, naming both types |

Dropping a column in silence is the worst way to fail — it disappears and
nothing says so — so that case stops the load. A destination column the rows do
not fill is normal, and does not.

### The table

`CreateTable` lets the load job create it on the first run. Off by default, and
it never alters a table that already exists. See **Creating the table** below.

> **`Key` is frozen.** The field order you give it and the `|` separator both
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
	lines, err := extract.CSV(ctx, sdk.Source{
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
lines, _ := extract.CSV(ctx, sdk.Source{URL: url})
// -> {name: Alice, age: 30}
// -> {name: Bob,   age: 25}
```

Set `NoHeader: true` when the file has no header row. No row is then treated as
special and every line is keyed positionally:

```go
lines, _ := extract.CSV(ctx, sdk.Source{URL: url, NoHeader: true})
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
extract.NDJSON(ctx, sdk.Source{URL: url, FollowLinks: true})

// {"results": [...], "next_page": "abc"} -- the cursor is sent back as the
// query parameter of the same name, and DataKey says where the rows live.
extract.JSON(ctx, sdk.Source{URL: url, CursorKey: "next_page", DataKey: "results"})

// ?offset=0, ?offset=100, ... until a page comes back empty
extract.NDJSON(ctx, sdk.Source{URL: url, OffsetKey: "offset", PageSize: 100})
```

## Rate limiting

`Source.RateLimiter` is any type with `Wait(ctx) error`, which
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
- **Preview** — print the first records as a table (see below)
- **Stream decoding** — no materializing entire response

### Seeing what you pulled

`Preview` prints the first N records once the extract finishes, the way a
dataframe's `head()` shows the top of a frame. Off by default.

```go
sdk.Source{
	URL:     "https://api.open-meteo.com/v1/forecast?...",
	Preview: 4,
}
```

```
   relative_humidity_2m  station           temperature_2m  time              weather_code  wind_speed_10m
0                    78  Berlin-Tempelhof             3.4  2026-01-01T00:00             3            11.2
1                    81  Berlin-Tempelhof           -1.15  2026-01-01T01:00            61             9.7
2                    78  Berlin-Tempelhof             6.4  2026-01-02T00:00             3            11.2
3                    81  Berlin-Tempelhof           -2.15  2026-01-02T01:00            61             9.7

[4 of 6 rows · 6 columns · 960 B · 3 pages in 262µs · 87µs/page]
```

It answers "what did I actually just pull?" without a debugger and without
draining the stream into a variable to look at it. The sample is taken as the
records stream past, so it costs N records of memory and never changes what
the consumer receives. It still prints when the source dies halfway or you
break out of the loop — which is exactly when you want to see what did arrive.

A pipeline gets it from the command line too, so you can look without
recompiling:

```bash
./my-fetcher -preview 5
```

| field | default | what it does |
|---|---|---|
| `Preview` | `0` (off) | how many records to print |
| `PreviewBytes` | `4096` | caps the block; rows are dropped from the bottom and the footer says how many |
| `PreviewWriter` | `os.Stderr` | where the table goes |

The table goes to a writer rather than through `slog` because slog's
`TextHandler` escapes newlines, so a table logged as an attribute arrives as
one unreadable line of `\n`. The counters do go through slog, where a
structured number belongs:

```
INFO extract complete format=json pages=3 rows=6 bytes=960 duration=262µs per_page=87µs
```

`bytes` is what came off the wire, before `Transform` — the number that
explains a slow extract. It is also on `Data.Stats().Bytes` and
`Result.ExtractBytes`.

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

### What the Result tells you

Every field describes what happened, including `Pages` and `Attempts` — the
two that make a flaky source visible:

```go
res, err := sdk.Load(ctx, data, target)
slog.Info("done", res.Args()...)
// records=24 rows=24 ignored=0 pages=3 attempts=4 ...
```

`Attempts` above `Pages` means requests were retried. There is a test locking
each counter to reality: a number in a result that is always zero is worse
than no number, because nobody doubts it.

For a pipeline that does not load — a dry run, a validation pass, an extract
feeding somewhere else — the same counters are on `Data`:

```go
data, _ := sdk.Extract(ctx, source)
for range data.Records { }
stats := data.Stats()   // read after the stream is drained
```

> The `ingestion_id` namespace is **not** configurable. `WithMetadataNamespace`
> used to accept one and then ignore it — the namespace is a `const`, checked
> byte-for-byte against Python's `uuid.uuid5`. A configurable contract is not a
> contract, so the option is gone rather than wired up.

## What gets written

**Your payload, as Transform left it.** The SDK imposes no columns: what a row
looks like is your decision.

```go
sdk.Target{Table: "hourly"}
// writes exactly the payload -- nothing asked for, nothing added
```

`ExtraMetadata` adds two fields, and nothing else:

| field | |
|---|---|
| `ingestion_id` | deterministic UUID v5 over `provider\|entity\|source_key\|record_ts` |
| `ingestion_loaded_at` | when the row was written, RFC 3339 |

`Provider`, `Entity` and `SourceKey` stay provenance — they build the id, they
do not become columns. A payload that already owns one of the two names is an
error naming the field, never a silent overwrite.

Turning the flag on is also what makes `Provider`, `Entity` and `Key`
necessary, and the only reason the SDK reads your payload at all. With it off
they are optional and never called.

`ExtraMetadata` is required by `DedupMerge`, which matches on `ingestion_id`,
and by the partition options, which partition on `ingestion_loaded_at`.

### A row shape of your own

When the warehouse has a contract, you build it — in one Transformer:

```go
Transform: []sdk.Transformer{
	func(payload any) (any, error) {
		return map[string]any{
			"provider":   "open_meteo",
			"entity":     "hourly_temperature",
			"source_key": payload.(map[string]any)["time"],
			"payload":    payload,
		}, nil
	},
},
Target: sdk.Target{..., ExtraMetadata: true},
```

See [`examples/07-own-shape`](../examples/07-own-shape/).

## Running inside Bravis

A fetcher does not change to run under the engine. The engine injects
`BRAVIS_RUN_*` into the step, `sdk.Run` picks it up, and `Pipeline.Run` holds
what is useful:

| what | from |
|---|---|
| `Run.First` | no earlier attempt of this step has succeeded |
| `Run.Params` | the values this execution was dispatched with; never nil |
| `Run.ID`, `Attempt`, `Trigger`, `LogicalDate` | which run this is |

Reading it is optional:

```go
Before: func(ctx context.Context, p *sdk.Pipeline) error {
	if p.Run.Params["load_full"] == "true" {
		p.Source.URL += "&full=1"
	}
	return nil
},
```

Run by hand, every field is zero and nothing behaves differently.

> **This is not a private channel.** The step's process can read its own
> environment, and someone will. What is promised is that a fetcher does not
> *have* to — not that it cannot. Secrets do not travel this way; they go
> through `envFrom.secretRef`, as they always did.

## Creating the table

Off by default: nothing runs DDL against your warehouse without being asked.

```go
sdk.Target{
	CreateTable: sdk.Bool(true),   // always, when the table is absent
	ClusterBy:   []string{"provider"},
}
```

Three states, because two are not enough:

| `CreateTable` | outside Bravis | inside Bravis |
|---|---|---|
| `nil` | nothing created | created on the step's first run, or when dispatched with `create_table=true` |
| `sdk.Bool(true)` | created | created |
| `sdk.Bool(false)` | nothing created | **nothing created** — an explicit refusal wins |

A plain `bool` cannot carry this: its zero value would mean both "I do not want
a table" and "I said nothing", and the engine would have no way to tell them
apart. The log says which of the three answered, and why:

```
create_table=true (from the engine: first run of this step)
```

The schema is inferred from the data, because nothing else knows it — the
payload is yours. Two knobs the SDK can still set:

- **Partitioning** by day on `ingestion_loaded_at`, whenever `ExtraMetadata`
  gives it that column. Not optional: an unpartitioned landing table costs a
  full scan on every MERGE the bronze layer runs.
- **Clustering**, on the columns you name. The SDK cannot guess them.

With `PartitionExpiration` old partitions are dropped; zero keeps them, which
is the default, because deleting data is not something a library starts doing
on its own. `RequirePartitionFilter` blocks queries that would scan
everything — and is refused alongside `DedupMerge`, because the merge matches
on `ingestion_id` across every partition and cannot be scoped.

To keep the schema under your control, pass the DDL:

```go
sdk.Target{CreateTable: true, CreateSQL: "CREATE TABLE landing.x (...)"}
```

The SDK runs it once, then checks it produced the table being written to. It
never alters a table that already exists, in either mode.

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
-- With ExtraMetadata these two sit alongside your payload's own fields:
-- - ingestion_id (deterministic UUID v5)
-- - ingestion_loaded_at (load timestamp)
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
fonte := sdk.Source{
	URL:          "https://api.example.com/data",
	Method:       "GET",           // default
	Timeout:      30 * time.Second, // per attempt
	TotalTimeout: 5 * time.Minute,  // total
	NoHeader:     false,            // CSV only; false = first row is the header
	Preview:      5,                // print the first 5 records as a table; 0 is off
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
