// Package load writes Envelopes to BigQuery as generic JSON.
//
// The SDK does NOT impose a schema — you define it. This makes load
// flexible for any data shape, not just opinionated landing tables.
//
// # Basic usage
//
//	loader := load.New(ctx, &sdk.LoadConfig{
//		ProjectID: "my-project",
//		Dataset:   "landing",
//		Table:     "raw_data",
//	})
//
//	result, err := loader.Load(ctx, envelopes...)
//	if err != nil {
//		log.Fatal(err)
//	}
//
//	log.Printf("Loaded %d rows in %v", result.RowsLoaded, result.Duration)
//
// # Schema design
//
// You create the table with whatever schema fits your use case.
// The loader writes the payload as JSON:
//
//	CREATE TABLE dataset.raw_data (
//	  payload JSON NOT NULL
//	);
//
// Or extract fields:
//
//	CREATE TABLE dataset.raw_data (
//	  id STRING,
//	  name STRING,
//	  amount FLOAT64,
//	  data JSON
//	);
//
// # Metadata (optional)
//
// Enable AddMetadata to inject fields into each payload:
//
//	loader := load.New(ctx, &sdk.LoadConfig{
//		ProjectID: "my-project",
//		Dataset:   "landing",
//		Table:     "raw_data",
//		AddMetadata: true,
//	})
//
// This adds to each payload:
//   - _bravis_ingestion_id (deterministic UUID v5 for idempotency)
//   - _bravis_ingestion_loaded_at (load timestamp)
//   - _bravis_provider (data source)
//   - _bravis_entity (entity type)
//   - _bravis_source_key (unique key from source)
//   - _bravis_record_ts (record timestamp at source)
//
// # Strategy selection
//
// The loader automatically chooses:
//   - Inline (NDJSON): up to ~5000 rows, no external staging
//   - GCS staging: larger volumes, deletes file after successful load
//
// The threshold is configurable via LoadConfig.ThresholdForGCS.
//
// # Idempotency
//
// The ingestion_id (when metadata is enabled) is deterministic UUID v5,
// derived from provider|entity|source_key|record_ts. Use it for deduplication
// downstream. The SDK does not deduplicate — that's a business-layer concern.
package load
