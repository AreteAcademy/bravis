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
func New(ctx context.Context, cfg *sdk.LoadConfig) (*Loader, error) {
	if cfg.ProjectID == "" {
		return nil, fmt.Errorf("ProjectID is required")
	}

	if cfg.Dataset == "" {
		return nil, fmt.Errorf("Dataset is required")
	}

	if cfg.Table == "" {
		return nil, fmt.Errorf("Table is required")
	}

	if cfg.StagingBucket == "" {
		cfg.StagingBucket = fmt.Sprintf("%s-bravis-staging", cfg.ProjectID)
	}

	if cfg.StagingPrefix == "" {
		cfg.StagingPrefix = "extracts/"
	}

	if cfg.ThresholdForGCS == 0 {
		cfg.ThresholdForGCS = 5000
	}

	if cfg.Format == "" {
		cfg.Format = "ndjson"
	}

	if cfg.MetadataNamespace == "" {
		cfg.MetadataNamespace = "e3a4f8c0-1b9d-4ea0-9c2e-77f6a6c4a4d7"
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
	var strategy string

	if len(envelopes) > l.cfg.ThresholdForGCS {
		strategy = "gcs"
		var err error
		bytesStaged, err = l.loadViaGCS(ctx, table, envelopes)
		if err != nil {
			return nil, err
		}
	} else {
		strategy = "inline"
		var err error
		bytesStaged, err = l.loadInline(ctx, table, envelopes)
		if err != nil {
			return nil, err
		}
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

func (l *Loader) loadInline(ctx context.Context, table *bigquery.Table, envelopes []sdk.Envelope) (int64, error) {
	// Convert envelopes to JSON rows
	rows := make([]*bigquery.StructSaver, len(envelopes))

	var totalBytes int64
	for i, env := range envelopes {
		// Marshal payload to JSON
		data, err := json.Marshal(env.Payload)
		if err != nil {
			return 0, fmt.Errorf("marshal row %d: %w", i, err)
		}

		totalBytes += int64(len(data))

		rows[i] = &bigquery.StructSaver{
			Struct: json.RawMessage(data),
		}
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
			wc.Close()
			obj.Delete(ctx)
			return 0, fmt.Errorf("marshal row: %w", err)
		}

		data = append(data, '\n')

		n, err := wc.Write(data)
		if err != nil {
			wc.Close()
			obj.Delete(ctx)
			return 0, fmt.Errorf("write to gcs: %w", err)
		}
		bytesWritten += int64(n)
	}

	if err := wc.Close(); err != nil {
		obj.Delete(ctx)
		return 0, fmt.Errorf("close gcs writer: %w", err)
	}

	// Load from GCS
	gcsRef := bigquery.NewGCSReference(fmt.Sprintf("gs://%s/%s", l.cfg.StagingBucket, objName))

	loader := table.LoaderFrom(gcsRef)
	loader.WriteDisposition = bigquery.WriteAppend

	job, err := loader.Run(ctx)
	if err != nil {
		obj.Delete(ctx)
		return 0, fmt.Errorf("start load job: %w", err)
	}

	status, err := job.Wait(ctx)
	if err != nil {
		obj.Delete(ctx)
		return 0, fmt.Errorf("wait for load job: %w", err)
	}

	if status.Err() != nil {
		obj.Delete(ctx)
		return 0, fmt.Errorf("load job failed: %w", status.Err())
	}

	// Delete staging file if successful
	if l.cfg.DeleteAfterLoad {
		obj.Delete(ctx)
	}

	return bytesWritten, nil
}