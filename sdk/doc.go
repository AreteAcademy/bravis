// Package bravis/sdk provides high-performance data extraction and loading to BigQuery.
//
// # Extract
//
// Package extract abstracts HTTP collection with retry, timeout, pagination, and format handling.
// It supports JSON, NDJSON, CSV, and XML with stream decoding and automatic retry on 5xx and 429.
//
// # Load
//
// Package load writes data to BigQuery using strategy selection: inline for small batches,
// GCS staging for large volumes. It generates idempotent ingestion IDs using UUID v5.
//
// # When NOT to use this SDK
//
//   - Streaming scenarios where sub-second latency matters (use Storage Write API instead).
//   - Destinations other than BigQuery (consider wrapping a generic Envelope writer).
//   - Transformation logic beyond format conversion (use dbt for that).
//
// # Module path
//
// Published at github.com/AreteAcademy/bravis/sdk
package sdk
