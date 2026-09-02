// Package load writes Envelopes to BigQuery with automatic strategy selection.
//
// # Basic usage
//
//	loader := load.New(ctx, load.LoadConfig{
//		ProjectID: "my-project",
//		Dataset:   "landing",
//	})
//
//	result, err := loader.Load(ctx, provider, entity, envelopes...)
//	if err != nil {
//		log.Fatal(err)
//	}
//
//	log.Printf("Loaded %d rows in %v", result.RowsLoaded, result.Duration)
//
// # Strategy selection
//
// The loader automatically chooses:
//   - Inline (NDJSON): up to ~5000 rows, no external staging
//   - GCS staging: larger volumes, deletes file after successful load
//
// The threshold is configurable via LoadConfig.ThresholdForGCS.
//
// # Schema
//
// The loader creates or verifies this schema in your dataset:
//
//	CREATE TABLE {dataset}.vendors_{provider}_{entity}s (
//	  ingestion_id        STRING NOT NULL,
//	  ingestion_loaded_at TIMESTAMP NOT NULL,
//	  provider            STRING NOT NULL,
//	  entity              STRING NOT NULL,
//	  source_key          STRING,
//	  payload             JSON   NOT NULL
//	)
//	PARTITION BY DATE(ingestion_loaded_at)
//	CLUSTER BY provider, entity
//
// # Idempotency
//
// Idempotency is the responsibility of downstream (bronze layer).
// The ingestion_id is deterministic UUID v5; loading the same envelope twice
// produces two identical rows. Deduplication happens at bronze, not in load.
//
// # Error reporting
//
// If BigQuery reports row-level errors, LoadResult.ErrorRows contains
// descriptions (truncated to 500 chars each). Use this to diagnose data quality issues.
package load