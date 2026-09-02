package load

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"cloud.google.com/go/bigquery"
	"cloud.google.com/go/storage"
	"github.com/AreteAcademy/bravis/sdk"
)

// Loader writes Envelopes to BigQuery as generic JSON.
// The SDK does NOT impose a table schema — you define it.
// Metadata can be optionally added to the payload itself.
type Loader struct {
	cfg *sdk.LoadConfig
	bq  *bigquery.Client
	gcs *storage.Client
}

// New creates a new Loader.
//
// cfg may be nil, in which case the configuration is built entirely from
// opts. cfg is never mutated: defaults and options are applied to a copy, so
// the caller can reuse the same LoadConfig for several Loaders.
//
//	l, err := load.New(ctx, nil,
//		sdk.WithProjectID("my-project"),
//		sdk.WithDataset("landing"),
//		sdk.WithTable("raw_data"),
//	)
func New(ctx context.Context, cfg *sdk.LoadConfig, opts ...sdk.LoadOption) (*Loader, error) {
	cfg, err := resolveConfig(cfg, opts...)
	if err != nil {
		return nil, err
	}

	bq, err := bigquery.NewClient(ctx, cfg.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("create bigquery client: %w", err)
	}

	gcs, err := storage.NewClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("create storage client: %w", err)
	}

	return &Loader{
		cfg: cfg,
		bq:  bq,
		gcs: gcs,
	}, nil
}

// resolveConfig merges cfg with opts and fills in defaults. It is separate
// from New so the whole configuration contract is testable without GCP
// credentials -- New itself cannot run without them.
//
// The caller's cfg is never modified.
func resolveConfig(cfg *sdk.LoadConfig, opts ...sdk.LoadOption) (*sdk.LoadConfig, error) {
	var c sdk.LoadConfig
	if cfg != nil {
		c = *cfg
	}
	for _, opt := range opts {
		if opt != nil {
			opt(&c)
		}
	}

	if c.ProjectID == "" {
		return nil, fmt.Errorf("projectID is required")
	}
	if c.Dataset == "" {
		return nil, fmt.Errorf("dataset is required")
	}
	if c.Table == "" {
		return nil, fmt.Errorf("table is required")
	}

	if c.StagingBucket == "" {
		c.StagingBucket = fmt.Sprintf("%s-bravis-staging", c.ProjectID)
	}
	if c.StagingPrefix == "" {
		c.StagingPrefix = "extracts/"
	}
	if c.ThresholdForGCS == 0 {
		c.ThresholdForGCS = defaultThresholdForGCS
	}
	if c.Format == "" {
		c.Format = "ndjson"
	}
	if c.MetadataNamespace == "" {
		c.MetadataNamespace = defaultMetadataNamespace
	}

	return &c, nil
}

const (
	defaultThresholdForGCS   = 5000
	defaultMetadataNamespace = "e3a4f8c0-1b9d-4ea0-9c2e-77f6a6c4a4d7"
)

// strategyFor picks how a batch of n rows reaches BigQuery. Small batches go
// inline in one request; large ones stage through GCS so memory stays flat.
func strategyFor(n, threshold int) string {
	if n > threshold {
		return "gcs"
	}
	return "inline"
}

// Load writes envelopes to BigQuery.
// The table must already exist with the schema you define.
// Metadata can be optionally added to each payload.
func (l *Loader) Load(ctx context.Context, envelopes ...sdk.Envelope) (*sdk.LoadResult, error) {
	start := time.Now()

	if len(envelopes) == 0 {
		return &sdk.LoadResult{
			Duration: time.Since(start),
			Strategy: "inline",
			Format:   l.cfg.Format,
		}, nil
	}

	// Add metadata if requested
	if l.cfg.AddMetadata {
		for i := range envelopes {
			if err := l.addMetadataToEnvelope(&envelopes[i]); err != nil {
				return nil, fmt.Errorf("add metadata: %w", err)
			}
		}
	}

	table := l.bq.Dataset(l.cfg.Dataset).Table(l.cfg.Table)

	// Verify table exists (don't create it)
	_, err := table.Metadata(ctx)
	if err != nil {
		return nil, fmt.Errorf("table %s.%s not found: %w. Create it manually with your desired schema",
			l.cfg.Dataset, l.cfg.Table, err)
	}

	// Choose strategy based on envelope count
	var bytesStaged int64
	strategy := strategyFor(len(envelopes), l.cfg.ThresholdForGCS)

	var loadErr error
	if strategy == "gcs" {
		bytesStaged, loadErr = l.loadViaGCS(ctx, table, envelopes)
	} else {
		bytesStaged, loadErr = l.loadInline(ctx, table, envelopes)
	}
	if loadErr != nil {
		return nil, loadErr
	}

	result := &sdk.LoadResult{
		RowsLoaded:  int64(len(envelopes)),
		BytesStaged: bytesStaged,
		Duration:    time.Since(start),
		Strategy:    strategy,
		Format:      l.cfg.Format,
	}

	slog.InfoContext(ctx, "load complete",
		"table", fmt.Sprintf("%s.%s", l.cfg.Dataset, l.cfg.Table),
		"rows", result.RowsLoaded,
		"bytes", result.BytesStaged,
		"strategy", strategy,
		"duration", result.Duration)

	return result, nil
}

// addMetadataToEnvelope adds metadata fields to the envelope's payload.
func (l *Loader) addMetadataToEnvelope(env *sdk.Envelope) error {
	// Calculate ingestion ID
	id, err := env.IngestionID()
	if err != nil {
		return err
	}

	// Convert payload to map if it isn't already
	var payloadMap map[string]interface{}
	switch p := env.Payload.(type) {
	case map[string]interface{}:
		payloadMap = p
	default:
		// Try to marshal and unmarshal to convert to map
		data, err := json.Marshal(env.Payload)
		if err != nil {
			return fmt.Errorf("marshal payload: %w", err)
		}
		if err := json.Unmarshal(data, &payloadMap); err != nil {
			return fmt.Errorf("unmarshal to map: %w", err)
		}
	}

	// Add metadata fields
	metadata := map[string]interface{}{
		"_bravis_ingestion_id":        id,
		"_bravis_ingestion_loaded_at": time.Now().UTC().Format(time.RFC3339),
		"_bravis_provider":            env.Provider,
		"_bravis_entity":              env.Entity,
		"_bravis_source_key":          env.SourceKey,
		"_bravis_record_ts":           env.RecordTS,
	}

	// Merge metadata into payload
	for k, v := range metadata {
		payloadMap[k] = v
	}

	env.Payload = payloadMap
	return nil
}

// jsonSaver adapts an arbitrary decoded payload to BigQuery's ValueSaver.
//
// StructSaver cannot be used here: it reflects over the fields of a struct,
// and the SDK deliberately has no struct for the payload -- the whole point
// is that the caller owns the schema.
//
// insertID is left empty on purpose. Populating it would enable BigQuery's
// best-effort streaming dedup, which contradicts the documented contract
// that deduplication happens downstream, keyed on _bravis_ingestion_id.
type jsonSaver struct {
	row map[string]bigquery.Value
}

func (s jsonSaver) Save() (map[string]bigquery.Value, string, error) {
	return s.row, "", nil
}

// toRow converts a payload into the column/value map BigQuery expects.
func toRow(payload any) (map[string]bigquery.Value, int, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, 0, err
	}

	var obj map[string]any
	if err := json.Unmarshal(data, &obj); err != nil {
		return nil, 0, fmt.Errorf("payload must encode to a JSON object, got %s: %w", truncate(data, 80), err)
	}

	row := make(map[string]bigquery.Value, len(obj))
	for k, v := range obj {
		row[k] = v
	}
	return row, len(data), nil
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "..."
}

func (l *Loader) loadInline(ctx context.Context, table *bigquery.Table, envelopes []sdk.Envelope) (int64, error) {
	rows := make([]*jsonSaver, len(envelopes))

	var totalBytes int64
	for i, env := range envelopes {
		row, n, err := toRow(env.Payload)
		if err != nil {
			return 0, fmt.Errorf("marshal row %d: %w", i, err)
		}
		totalBytes += int64(n)
		rows[i] = &jsonSaver{row: row}
	}

	inserter := table.Inserter()
	if err := inserter.Put(ctx, rows); err != nil {
		return totalBytes, fmt.Errorf("insert rows: %w", err)
	}

	return totalBytes, nil
}

func (l *Loader) loadViaGCS(ctx context.Context, table *bigquery.Table, envelopes []sdk.Envelope) (int64, error) {
	// Generate staging file path
	today := time.Now().UTC().Format("2006-01-02")
	objName := fmt.Sprintf("%s%s/%d.ndjson", l.cfg.StagingPrefix, today, time.Now().UnixNano())

	bucket := l.gcs.Bucket(l.cfg.StagingBucket)
	obj := bucket.Object(objName)

	// Write envelopes to staging as NDJSON
	wc := obj.NewWriter(ctx)
	bytesWritten := int64(0)

	for _, env := range envelopes {
		data, err := json.Marshal(env.Payload)
		if err != nil {
			_ = wc.Close()
			_ = obj.Delete(ctx)
			return 0, fmt.Errorf("marshal row: %w", err)
		}

		data = append(data, '\n')

		n, err := wc.Write(data)
		if err != nil {
			_ = wc.Close()
			_ = obj.Delete(ctx)
			return 0, fmt.Errorf("write to gcs: %w", err)
		}
		bytesWritten += int64(n)
	}

	if err := wc.Close(); err != nil {
		_ = obj.Delete(ctx)
		return 0, fmt.Errorf("close gcs writer: %w", err)
	}

	// Load from GCS
	gcsRef := bigquery.NewGCSReference(fmt.Sprintf("gs://%s/%s", l.cfg.StagingBucket, objName))
	// We stage NDJSON. Without this BigQuery assumes CSV and every row of
	// every GCS-strategy load is parsed wrong.
	gcsRef.SourceFormat = bigquery.JSON

	loader := table.LoaderFrom(gcsRef)
	loader.WriteDisposition = bigquery.WriteAppend

	job, err := loader.Run(ctx)
	if err != nil {
		_ = obj.Delete(ctx)
		return 0, fmt.Errorf("start load job: %w", err)
	}

	status, err := job.Wait(ctx)
	if err != nil {
		_ = obj.Delete(ctx)
		return 0, fmt.Errorf("wait for load job: %w", err)
	}

	if status.Err() != nil {
		_ = obj.Delete(ctx)
		return 0, fmt.Errorf("load job failed: %w", status.Err())
	}

	// Delete staging file if successful
	if l.cfg.DeleteAfterLoad {
		_ = obj.Delete(ctx)
	}

	return bytesWritten, nil
}
