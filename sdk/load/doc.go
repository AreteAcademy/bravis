// Package load writes Envelopes to BigQuery.
//
// By default the SDK imposes no schema: it writes your payload as-is, and the
// destination table must already exist. You decide what the columns are.
//
// Dataset and Table address the destination directly, so an existing table is
// written to as it stands. Only CreateTable makes the SDK create one -- and it
// never alters a table that is already there.
//
// # Basic usage
//
//	loader, err := load.New(ctx, nil,
//		core.WithProjectID("my-project"),
//		core.WithDataset("landing"),
//		core.WithTable("raw_data"),
//	)
//	if err != nil {
//		log.Fatal(err)
//	}
//
//	result, err := loader.Load(ctx, envelopes...)
//	if err != nil {
//		log.Printf("load failed: %v", err)
//		for _, e := range result.ErrorRows {
//			log.Printf("  %s", e)
//		}
//		return
//	}
//
//	log.Printf("loaded %d rows in %v", result.RowsLoaded, result.Duration)
//
// Load always returns a LoadResult, failures included, so ErrorRows is safe to
// read after a non-nil error.
//
// New takes a *LoadConfig, a list of options, or both, and never mutates the
// config you hand it.
//
// # What gets written
//
// Your payload, as Transform left it. The SDK imposes no columns: what a row
// looks like is your decision.
//
// Metadata adds two columns and nothing else, declared NOT NULL:
//
//	ingestion_id        STRING    NOT NULL
//	ingestion_loaded_at TIMESTAMP NOT NULL
//
// The id is a deterministic UUID v5 over provider|entity|source_key|record_ts,
// so a re-run writes the same id for the same record. AutoID makes it a fresh
// random UUID instead, which gives up that guarantee -- see LoadConfig.AutoID.
//
// Provider, Entity and SourceKey stay provenance: they build the id, they do
// not become columns. A record that already owns one of those two names is an
// error naming the field, never a silent overwrite.
//
// Metadata is required by DedupMerge, which matches on ingestion_id, and
// by the partition options, which partition on ingestion_loaded_at.
//
// # Creating the table
//
// Off by default. With CreateTable the table is created on the first run and
// the columns come from the data, because nothing else knows them.
//
// With Metadata the SDK creates the table itself, so it can declare its own
// two columns NOT NULL -- autodetect infers them nullable, and BigQuery will
// not tighten a column afterwards. Your columns are still typed by BigQuery,
// from a schema it infers over the batch: the SDK infers no type of its own.
// That costs one extra load job, on the run that creates the table and never
// again.
//
// Either way the SDK sets what it can: day partitioning on
// ingestion_loaded_at when Metadata provides it, and clustering on the
// columns you name in ClusterBy.
//
// CreateSQL runs your DDL instead, once, and the SDK then checks it produced
// the table being written to.
//
// It never alters a table that already exists, in either mode: a loader that
// can ALTER or DROP is a loader that can erase history.
//
// # Strategy selection
//
// Both strategies are batch load jobs and differ only in where BigQuery reads
// from:
//
//   - inline: up to ThresholdForGCS rows (5000 by default), NDJSON embedded in
//     the job request
//   - gcs: above it, staged as an object and deleted after a successful load
//
// The SDK never uses streaming inserts, so rows are visible to DML as soon as
// the job finishes.
//
// # Format
//
// Only "ndjson" is written today. LoadConfig.Format refuses "csv" and
// "parquet" rather than accepting them and writing NDJSON anyway, and
// LoadResult.Format reports what was actually written.
//
// # Idempotency
//
// ingestion_id is a deterministic UUID v5 over
// provider|entity|source_key|record_ts. Deduplicate on it downstream: the same
// batch loaded twice produces duplicate rows here, by design. WRITE_APPEND is
// the only disposition the SDK uses; it never truncates.
package load
