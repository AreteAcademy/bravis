# Brevis SDK

High-performance data extraction and loading library for Go.

Extract HTTP data with retry, timeout, and format handling. Load to BigQuery with automatic strategy selection.

## Installation

```bash
go get github.com/AreteAcademy/brevis/sdk@latest
go mod tidy
```

Requires Go 1.23 or newer (the SDK streams rows as `iter.Seq2`).

> **Do not use `v0.1.0`.** It shipped a `go.mod` pinning a revision that does
> not exist, so it fails to build for everyone. The Go module proxy is
> immutable and cannot be corrected after the fact. Start at `v0.1.1`.

## Three lines

```go
import (
	"github.com/AreteAcademy/brevis/sdk"
	"github.com/AreteAcademy/brevis/sdk/from"
	"github.com/AreteAcademy/brevis/sdk/to/bigquery"
)

dados, err := sdk.Extract(ctx, sdk.Source{
	From: from.HTTP{
		URL: "https://api.open-meteo.com/v1/forecast?...",
		Records: func(r sdk.Response) ([]any, error) {
			doc, err := r.Object()
			if err != nil {
				return nil, err
			}
			if bad, _ := doc["error"].(bool); bad {
				return nil, sdk.Reject("open-meteo refused: %v", doc["reason"])
			}
			return sdk.ParallelArrays("hourly", "time", "temperature_2m")(doc)
		},
	},
})

// What we take from the source.
dados = sdk.Transform(dados, sdk.Accept("time", "temperature_2m", "latitude", "longitude"))

// Where it goes, and the columns it has.
res, err := sdk.Load(ctx, dados, sdk.Target{
	To:      bigquery.Table{Dataset: "bronze", Name: "hourly_temperatures"},
	Columns: []string{"time", "temperature_2m", "latitude", "longitude"},
})
```

**The driver is a value, not a setting.** `from.HTTP` carries everything an
HTTP source needs — URL, headers, retry, pagination, and what a response
means. `from.Files` carries a path and a format. Neither has to make room for
the other's fields, so no source struct collects forty options of which any one
driver reads six.

It also decides what you compile. Go prunes dependencies by package imported,
never by field used:

| what you import | packages | AWS | Google |
|---|---|---|---|
| `sdk` | 190 | no | no |
| `sdk` + `from` | 194 | no | no |
| `sdk` + `from` + `to` (files) | 195 | no | no |
| `sdk` + `to/bigquery` | 456 | no | yes |
| `sdk` + `from` + `store/s3` | 265 | yes | no |
| `sdk` + `from` + `store/gcs` | 392 | no | yes |

A whole file pipeline — read and write — costs 195 packages and no cloud SDK
at all. **A driver with a vendor SDK behind it lives in its own package**, which
is why BigQuery is `to/bigquery` and the object stores are `store/s3` and
`store/gcs`.

`Source` and `Target` hold what every driver honours: the preview and counters
on one side, the declared columns, metadata and deduplication on the other.

**The columns come from `Transform` and are declared in `Target.Columns`.**
Whatever shape your transformers compose is exactly what is written; the SDK
adds nothing on its own — the two columns it knows how to write, `ingestion_id`
and `ingestion_loaded_at`, are transformers you put in the chain like any
other.

Everything between the two calls that is not specific to the vendor lives in
the SDK: config, retry, pagination, table creation, deduplication and the
result you log.

## Sources and destinations

| read from | |
|---|---|
| `from.HTTP` | an API: retry, rate limiting, three pagination strategies, and `Records` |
| `from.Files` | files on disk, S3 or GCS: NDJSON, CSV, JSON, XML, `.gz` |

| write to | |
|---|---|
| `bigquery.Table` | a table: GCS staging, `MERGE`, typed creation, partitioning, clustering |
| `to.Files` | files on disk, S3 or GCS: NDJSON or CSV, partitioned, compressed |

### Files, and the three backends

One driver, three backends. The scheme in `Path` says which:

```go
from.Files{Path: "./entrada/*.csv", Format: sdk.FormatCSV}
from.Files{Path: "s3://bucket/dia=2026-09-04/*.ndjson.gz", Store: s3.New(client)}
to.Files{Path: "gs://bucket/landing/", PartitionBy: "ingestion_loaded_at", Store: gcs.New(client)}
```

**The backend is passed in, not chosen inside `Files`** — that is what keeps a
fetcher reading local CSV from compiling the AWS SDK and the Google one. A path
whose scheme the `Store` does not serve is an error naming both, not a
confusing 404.

Files are read in **sorted order, always**. Two runs over the same prefix
produce the same sequence, which a positional `Key` depends on: without it the
`ingestion_id` of a record would change between runs. `.gz` is handled by
extension, and a `.gz` that is not gzip fails naming the file rather than as an
"invalid JSON" three layers down.

Writing is **atomic**: a temporary file and a rename on disk, a single PUT in
object storage. Nobody ever reads half a file. A batch becomes one object, so a
second load does not overwrite the first — a directory has no notion of "the
same rows again", and what to do about duplicates is decided downstream.

`to.Files` refuses what a directory cannot do, naming the option:
`Dedup` has no key to match on, and Parquet would bring Arrow along for a
fetcher that only wanted a file.

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

**Order matters inside the chain.** `sdk.IngestionID` reads the record after
every Transformer before it, so a rename has to be reflected in the field names
you give it:

```go
sdk.Rename(map[string]string{"time": "observed_at"}),
sdk.Compute("source_key", func(r map[string]any) (any, error) {
	return sdk.Key("latitude", "longitude", "observed_at")(r)
}),
sdk.IngestionID("provider", "entity", "source_key", "observed_at"),
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
		Source: sdk.Source{From: from.HTTP{
			URL: "...",
			Records: func(r sdk.Response) ([]any, error) {
				doc, err := r.Object()
				if err != nil {
					return nil, err
				}
				return sdk.ArrayAt("results")(doc)
			},
		}},
		Transform: []sdk.Transformer{
			sdk.Compute("provider", func(map[string]any) (any, error) { return "example", nil }),
			sdk.Compute("entity", func(map[string]any) (any, error) { return "events", nil }),
			sdk.Compute("source_key", func(r map[string]any) (any, error) { return sdk.Key("id")(r) }),
			sdk.IngestionID("provider", "entity", "source_key", "created_at"),
			sdk.IngestionLoadedAt(),
		},
		Target: sdk.Target{To: bigquery.Table{Name: "events"}},
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
| `BREVIS_SDK_DATASET` | `landing` |
| `BREVIS_SDK_STAGING_BUCKET` | `<projeto>-brevis-staging` |
| `BREVIS_SDK_LOG_LEVEL` | `info` |

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

	"github.com/AreteAcademy/brevis/sdk"
	"github.com/AreteAcademy/brevis/sdk/extract"
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
	"github.com/AreteAcademy/brevis/sdk"
	"github.com/AreteAcademy/brevis/sdk/load"
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

Four strategies, picked by which field you set. Setting two is an error, not a
precedence rule: the loser would be a field you wrote that does nothing. All of
them cap out at `MaxPages` (1000 by default) so a server that always advertises
a next page cannot spin forever.

```go
// Link: <...>; rel="next"
extract.NDJSON(ctx, sdk.Source{URL: url, FollowLinks: true})

// {"results": [...], "next_page": "abc"} -- the cursor is sent back as the
// query parameter of the same name, and DataKey says where the rows live.
extract.JSON(ctx, sdk.Source{URL: url, CursorKey: "next_page", DataKey: "results"})

// ?page=1, ?page=2, ... until a page comes back empty
extract.JSON(ctx, sdk.Source{URL: url, PageKey: "page", DataKey: "results"})

// ?offset=0, ?offset=100, ... until a page comes back empty
extract.NDJSON(ctx, sdk.Source{URL: url, OffsetKey: "offset", PageSize: 100})
```

`PageKey` counts pages; `OffsetKey` counts rows, and `PageSize` is how many
rows it skips ahead each time. Before `PageKey` existed the way to paginate by
page number was `OffsetKey: "page"` with `PageSize: 1`, which worked by
accident: the "offset" was counting pages because the step happened to be one.
That still runs — the SDK cannot tell it from a genuine offset of one row — but
it breaks the moment someone touches the page size. Use `PageKey`.

The first request always carries the page number, so the server never picks a
default the SDK would then guess wrong from. `FirstPage` moves the start, and a
number already in the URL wins over it — which is how a zero-indexed API says
so: `…?page=0`.

## Authentication

`Auth` is optional. A static key belongs in `Header` and needs none of this:

```go
from.HTTP{URL: url, Header: http.Header{"Authorization": {"Bearer " + key}}}
```

What `Auth` buys is the two things consumers kept writing by hand.

**A login that is cached**, for an API that rate-limits authentication attempts
rather than requests:

```go
Auth: &from.Credential{
    Value: func(ctx context.Context) (string, error) { return login(ctx) },
    Apply: from.AsBearer,
    TTL:   time.Hour,
}
```

`TTL` keeps the value in memory for the process, behind a lock, so concurrent
callers produce one login and not one each. It never reaches disk.

**A session that would otherwise expire in silence.** Some vendors have no
programmatic login at all: a human pastes a session cookie, it has a sliding
expiry, and only the renewal endpoint pushes the window forward.

```go
Auth: &from.Credential{
    Value: from.FromEnv("APP_SESSION_COOKIE"),
    Apply: from.AsCookie,
    Refresh: &from.Refresh{
        URL:       "https://api.example.com/auth/session",
        ExpiresAt: from.JSONField("expires"),
        WarnAfter: 7 * 24 * time.Hour,
    },
}
```

`Refresh` runs once, before the first page. A `Set-Cookie` in its response
lands in the same jar the pages use, so the renewed credential applies to this
run — and to this run only. **The SDK stores nothing.** A rotated token does
not invalidate the previous one, so the cost is that somebody re-pastes the
credential once per window; `ExpiresAt` and `WarnAfter` are what make sure they
know before it lapses, on the log line *and* in `Stats.CredentialExpiry`.

### Keeping the rotated credential between runs

Without a store, the renewed value lives for this run only — and somebody
re-pastes the credential once per expiry window, forever.

```go
import "github.com/AreteAcademy/brevis/sdk/store/gcs"

Refresh: &from.Refresh{
    URL:       "https://api.example.com/auth/session",
    ExpiresAt: from.JSONField("expires"),
    Store:     gcs.Credential{Bucket: "myproject-credentials", Object: "app-session"},
}
```

The trade this makes is the point: the environment variable stops holding the
**rotating** value and starts holding the **seed**, pasted once.

The read order is: the store, then `Value` as the seed, then renew, then save.

**Two stores, and `Store` is optional** — without it, nothing changes.

| | where | concurrency |
|---|---|---|
| `gcs.Credential{Bucket, Object}` | an object in GCS | conditional write on the generation read; a concurrent rotation loses the write, not the run |
| `from.FileStore{Dir, Name}` | a file in a directory | last writer wins |

`gcs.Credential` writes with `ifGenerationMatch` on the generation `Load` saw.
If another process rotated in between, the write is refused and **this run keeps
theirs** — that is compare-and-swap, not a lock, and it is why an object beats a
file on a shared mount, where `rename` is not atomic.

`FileStore` resolves its directory from `Dir`, then `BREVIS_CREDENTIAL_DIR`, then
nowhere — and nowhere turns the store **off**, saying so once. The directory can
be `./.brevis`, a compose volume, or a mount: same `Store`, and it is what makes
this work without GCS. File `0600`, directory `0700`, temp file then rename, and
a directory with looser permissions is refused.

**Encryption is optional.** `Key`, then `BREVIS_CREDENTIAL_KEY`; without either,
the value is written in the clear and the log says so once. What protects it is
then whatever guards the store — bucket IAM, directory permissions. A key that
lives in the same secret as whoever can read the store protects against nobody,
and calling that security is worse than not having it. For a **directory**, use
a key: a directory is easier to end up shared than a bucket with IAM.

```bash
head -c 32 /dev/urandom | base64
```

With a key it is AES-256-GCM, a fresh nonce per write. Either way the stored
value carries a version line, and a version this build does not read is treated
as absent — the run falls back to the seed rather than failing, because during a
rollout the same store holds both.

Failing to save does **not** stop the run — the extract already happened; what
was lost is the rotation. It goes out at `ERROR` and in
`Result.CredentialStoreError`, because the effect is deferred (the next run falls
back to a seed that one day expires) and a deferred effect that only exists in a
log is the one nobody sees in time.

Importing `store/gcs` costs you the Google storage client. A fetcher that uses
`FileStore` never compiles it.

A refresh that fails stops the run. Continuing would send every page out with a
credential the API has just refused, and the failure would come back blaming
the data endpoint.

`Value` errors name the environment variable. `Apply` is `AsBearer`,
`AsCookie` (the whole `name=value`, as copied from a browser),
`AsCookieNamed(name)` or `AsHeader(name)`.

## Cookies

A session cookie survives the whole walk. Hand the first one over in `Header`
and the SDK keeps it in a jar from there, so a `Set-Cookie` that refreshes the
session mid-pagination replaces it by name and page two goes out with the new
value.

```go
sdk.Source{
    URL:    url,
    Header: http.Header{"Cookie": {"session-token=" + os.Getenv("APP_SESSION")}},
}
```

The `Cookie` header is read once and then dropped from the requests, so the
same name is never sent twice with two different values. It is parsed with
`http.ParseCookie`, which splits on the first `=` — a JWT session cookie ends
in `=` padding, and cutting it produces a `401` rather than a parse error.

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
- **Records** — you decide what a response means, per response (see below)
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

### Deciding what a response means

`Pipeline.Records` receives every successful response and returns the records
it carries — or refuses it, saying why. It replaces `Guard` and `Expand`, which
were the same question ("what does this response mean?") split in two.

It sits on `Pipeline`, next to `Transform`, and not inside `Source`: `Source` is
configuration — URL, headers, timeouts, retry, pagination — and this is the one
decision in a fetcher that is about the data. On the two-call API it is
`Extract`'s optional second argument.

```go
Records: func(r sdk.Response) ([]any, error) {
	if r.Status == http.StatusNoContent {
		return nil, nil // an empty window is a result, not a failure
	}

	doc, err := r.Object()
	if err != nil {
		return nil, err
	}
	if bad, _ := doc["error"].(bool); bad {
		return nil, sdk.Reject("open-meteo refused: %v", doc["reason"])
	}

	return sdk.ParallelArrays("hourly", "time", "temperature_2m")(doc)
},
```

**Per response, not per record**, and that is the point. A response that is an
error carries zero records, so a per-record check is never called on it — the
failure would arrive as "0 rows", which says nothing about what the vendor
actually answered.

Every **2xx** reaches it, `204` and `206` included, because what those mean is
the vendor's convention and only the fetcher knows it. A non-2xx never does:
that is a transport failure, retried where retrying makes sense and reported
with its body otherwise.

| on `Response` | |
|---|---|
| `Status` | the code, always 2xx |
| `Header` | the response's headers |
| `Bytes()` | the body, undecoded — looking for a marker costs no parse |
| `Object()` | the body as a JSON object, which is what the helpers take |
| `JSON(&v)` | the body into your own type |

`ParallelArrays`, `ArrayAt`, `RejectIf` and `RequireFields` are ordinary
functions you call from in here. They are shortcuts, not the interface: when
the vendor's shape does not fit one, write the function yourself.

Leaving `Records` nil keeps the default — decode the body, one record per
document — and that path stays **streaming**, which matters for a large NDJSON
or CSV. Setting `Records` buffers the response, because a function that decides
what a response means has to see all of it.

### Refusing, and being told apart

```go
return nil, sdk.Reject("open-meteo refused: %v", doc["reason"])
```

A plain `fmt.Errorf` also fails the run, but it cannot be told apart from a nil
map or a typo in the fetcher — and those two want different things from
whoever is on call. A rejection means the vendor sent something that is not
data: the fetcher is fine, the source is not, and retrying the same window will
do the same thing.

```go
if errors.Is(err, sdk.ErrRejected) { ... }
```

Per record, `sdk.SkipRecord` from a `Transformer` drops that record without
failing the run. See `examples/09-transform`.

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

**The columns you declared.** `Target.Columns` is the destination's shape, in
the order of its DDL, and it names every column — the `Transform` chain
composes all of them:

```go
Target: sdk.Target{
	To: bigquery.Table{
		Dataset: "bronze",
		Name:    "vendors_open_meteo_hourly_temperatures",
	},
	Columns: []string{
		"ingestion_id",
		"ingestion_loaded_at",
		"provider",
		"entity",
		"source_key",
		"payload",
	},
},
```

Put that next to the table's `CREATE TABLE` and the question *"do these
describe the same table?"* is answered by reading, not by tracing.

It is checked three ways:

| | |
|---|---|
| a declared column the `Transform` chain did not deliver | error naming the column |
| a field the row carries that the list does not declare | error naming the field |
| a declared column the real table does not have | error naming the column and the table's own |

The row that reaches the check is exactly what the chain composed, so the check
needs no special case: `ingestion_id` is a column like the others.

Nil declares nothing and checks nothing. There is no fallback: this list is the
only place the destination's columns are declared.

### Accept is not Columns

Two checks, two names, and they catch different things:

```go
Transform: []sdk.Transformer{
	sdk.Accept("time", "temperature_2m", "latitude", "longitude"),  // from the source
},
Target: sdk.Target{Columns: []string{...}},                         // of the table
```

`Accept` asks *"does the source still send what I read?"* — the vendor drops
`temperature_2m` and you get an error naming it, instead of a payload that is
quietly one field short. `Columns` asks *"does the row have the table's
columns?"*. Losing either one to have a single list would trade clarity for a
detection hole.

### As duas colunas que o SDK conhece

`sdk.IngestionID()` e `sdk.IngestionLoadedAt()` são transformers, usados como
qualquer outro:

```go
Transform: []sdk.Transformer{
	sdk.Accept("time", "temperature_2m", "latitude", "longitude"),
	sdk.Compute("provider", ...),
	sdk.Compute("entity", ...),
	sdk.Compute("source_key", func(r map[string]any) (any, error) {
		return sdk.Key("latitude", "longitude", "time")(r)
	}),
	sdk.IngestionID("provider", "entity", "source_key", "time"),
	sdk.IngestionLoadedAt(),
},
Target: sdk.Target{
	To:      bigquery.Table{Dataset: "bronze", Name: "hourly"},
	Columns: []string{"ingestion_id", "ingestion_loaded_at", "provider", "entity", "source_key", "payload"},
},
```

Ler a cadeia dá a resposta inteira: **seis helpers, seis colunas.** Nada
acontece fora dela.

`ingestion_id` é um UUID v5 determinístico sobre
`provider|entity|source_key|record_ts`, então o mesmo registro sempre recebe o
mesmo id e uma reexecução é segura. A fórmula, o namespace e o separador são
**congelados** — uma linha escrita aqui tem de casar com a que um fetcher
Python escreve para o mesmo registro.

É por isso que ele é um transformer do SDK e não algo que você escreve: um
`fmt.Sprintf` no fetcher pareceria idêntico e daria outro id no primeiro float
formatado diferente, e toda carga anterior deixaria de casar.

Sem argumentos lê `provider`, `entity`, `source_key`, `record_ts`. Nomeie os
campos quando os seus diferirem. Campo nomeado e ausente é erro nomeando-o —
o que costuma significar que a cadeia está fora de ordem.

`sdk.IngestionLoadedAt()` escreve o instante da carga em UTC, RFC 3339. Não
recebe argumentos: um valor de fora transformaria "quando esta linha foi
escrita" em outra coisa com o mesmo nome.

### NOT NULL, quando você declara

Quando `Target.Columns` nomeia uma dessas duas, o SDK cria a tabela ele mesmo
para poder declarar aquela coluna `NOT NULL`:

```sql
ingestion_id        STRING    NOT NULL,
ingestion_loaded_at TIMESTAMP NOT NULL
```

O autodetect as infere nullable e o BigQuery não aperta uma coluna depois, então
a garantia tem de ser posta na criação. **Declare a coluna, tenha a garantia**;
não declare nada e tudo é inferido nullable. O gatilho é a sua própria lista,
então nada decide a forma da tabela pelas suas costas.

`DedupMerge` precisa de `ingestion_id`, e as opções de partição precisam de
`ingestion_loaded_at` — as duas conferidas contra `Columns` quando ele é
declarado.

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
Target: sdk.Target{..., Columns: []string{"ingestion_loaded_at", ...}},
```

See [`examples/07-own-shape`](../examples/07-own-shape/).

## Running inside Brevis

A fetcher does not change to run under the engine. The engine injects
`BREVIS_RUN_*` into the step, `sdk.Run` picks it up, and `Pipeline.Run` holds
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

| `CreateTable` | outside Brevis | inside Brevis |
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

- **Partitioning** by day on `ingestion_loaded_at`, whenever `Metadata`
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
-- With a Metadata block these two sit alongside your own columns:
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
	Records: func(r sdk.Response) ([]any, error) {
		if !bytes.Contains(r.Bytes(), []byte(`"status":"ok"`)) {
			return nil, sdk.Reject("the API answered %d without status:ok", r.Status)
		}
		var docs []any
		return docs, r.JSON(&docs)
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
