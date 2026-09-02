# Bravis SDK

High-performance data extraction and loading library for Go.

Extract HTTP data with retry, timeout, and format handling. Load to BigQuery with automatic strategy selection.

## Installation

The SDK is published at `pkg.go.dev` once the repository is public.

For now, use local `replace` in your `go.mod`:

```go
module github.com/yourorg/yourproject

go 1.25.7

require github.com/AreteAcademy/bravis/sdk v0.1.0
```

## Quick Start

### Extract CSV

```go
package main

import (
	"context"
	"log"
	"github.com/zarvhq/bravis/sdk/extract"
)

func main() {
	ctx := context.Background()
	lines, err := extract.CSV(ctx, extract.Fonte{
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
	"github.com/zarvhq/bravis/sdk"
	"github.com/zarvhq/bravis/sdk/load"
)

func main() {
	ctx := context.Background()
	
	loader, err := load.New(ctx, &sdk.LoadConfig{
		ProjectID: "my-project",
		Dataset:   "landing",
	})
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

	result, err := loader.Load(ctx, "example_api", "transactions", envelopes...)
	if err != nil {
		log.Fatal(err)
	}

	log.Printf("Loaded %d rows in %v using %s strategy", 
		result.RowsLoaded, result.Duration, result.Strategy)
}
```

## Extract Formats

- **CSV** — tabular data; auto-detects headers
- **NDJSON** — newline-delimited JSON; streaming friendly
- **JSON** — array or single object
- **XML** — structured data (partial support)

## Extract Features

- **Retry with exponential backoff** — 429, 5xx, and network errors only
- **Timeout** — per-attempt and total, separate
- **Guard function** — validate 200-OK-but-wrong-content
- **Pagination** — cursor, offset, or Link headers
- **Rate limiting** — optional `rate.Limiter`
- **Observability** — structured logs with redacted URLs
- **Stream decoding** — no materializing entire response

## Load Strategy

The loader automatically chooses:

| Rows | Strategy | Benefit |
|------|----------|---------|
| < 5000 | Inline | One request, no external staging |
| >= 5000 | GCS staging | No memory limit, deletes after load |

Configure threshold with `WithThresholdForGCS()`.

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

```go
result, err := loader.Load(ctx, provider, entity, envelopes...)
if err != nil {
	log.Printf("Load failed: %v", err)
	log.Printf("Errors by row: %v", result.ErrorRows)
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
	Format:          "ndjson",
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

See LICENSE in the Bravis repository.