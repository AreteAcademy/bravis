// Package load writes Envelopes to BigQuery.
//
// By default the SDK imposes no schema: it writes your payload as-is, and the
// destination table must already exist. You decide what the columns are.
//
// Dataset and Table address the destination directly, so an existing table is
// written to as it stands. Only CreateTable, alongside WriteEnvelopeColumns,
// makes the SDK create one -- and it never alters a table that is already
// there.
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
// # Three ways to shape a row
//
// Default: the payload, exactly as given.
//
//	CREATE TABLE dataset.raw_data (payload JSON NOT NULL);
//
// WithMetadata(true): provenance folded into the payload as a flat object,
// under these keys:
//
//   - ingestion_id          deterministic UUID v5
//   - ingestion_loaded_at   load timestamp
//   - provider              data source
//   - entity                entity type
//   - source_key            unique key at the source
//   - record_ts             record timestamp at the source
//
// These are the same names the envelope contract uses as columns, so a flat
// row and a wrapped row describe a record identically and downstream SQL
// reads one spelling. They carry no prefix, so a payload that already has one
// of them is an error naming the field rather than a silent overwrite. When
// your source genuinely owns those names, use WriteEnvelopeColumns: it nests
// the payload and cannot collide.
//
// WithEnvelopeColumns(true): the six-column landing contract, with your
// payload nested rather than merged.
//
//	CREATE TABLE dataset.raw_data (
//	  ingestion_id        STRING    NOT NULL,
//	  ingestion_loaded_at TIMESTAMP NOT NULL,
//	  provider            STRING    NOT NULL,
//	  entity              STRING    NOT NULL,
//	  source_key          STRING,
//	  payload             JSON      NOT NULL
//	)
//	PARTITION BY DATE(ingestion_loaded_at)
//	CLUSTER BY provider, entity;
//
// Envelope mode exists so ingestion_id keeps a single owner: a row written
// here matches the row any other producer writes for the same record. Rebuild
// those columns per consumer and the ids drift apart.
//
// The last two modes are mutually exclusive; New refuses both at once.
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
