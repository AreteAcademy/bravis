package load

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"cloud.google.com/go/bigquery"
	"cloud.google.com/go/storage"
	"github.com/AreteAcademy/bravis/sdk"
)

// Loader writes Envelopes to BigQuery.
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
		cfg.Dataset = "landing"
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
// The table name is derived as: vendors_{provider}_{entity}s
func (l *Loader) Load(ctx context.Context, provider string, entity string, envelopes ...sdk.Envelope) (*sdk.LoadResult, error) {
	start := time.Now()

	if len(envelopes) == 0 {
		return &sdk.LoadResult{
			Duration: time.Since(start),
			Strategy: "inline",
			Format:   l.cfg.Format,
		}, nil
	}

	// Validate all envelopes
	for _, e := range envelopes {
		if e.SourceKey == "" {
			return nil, fmt.Errorf("SourceKey cannot be empty")
		}
	}

	// Ensure timestamps are set
	for i := range envelopes {
		if envelopes[i].RecordTS == "" {
			envelopes[i].RecordTS = time.Now().UTC().Format(time.RFC3339)
		}
		envelopes[i].Provider = provider
		envelopes[i].Entity = entity
	}

	tableName := fmt.Sprintf("vendors_%s_%ss", provider, entity)
	table := l.bq.Dataset(l.cfg.Dataset).Table(tableName)

	// Ensure table exists with correct schema
	if err := l.ensureTable(ctx, table); err != nil {
		return nil, fmt.Errorf("ensure table: %w", err)
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
		"table", tableName,
		"rows", result.RowsLoaded,
		"bytes", result.BytesStaged,
		"strategy", strategy,
		"duration", result.Duration)

	return result, nil
}

func (l *Loader) loadInline(ctx context.Context, table *bigquery.Table, envelopes []sdk.Envelope) (int64, error) {
	rows := make([]*bigquery.StructSaver, len(envelopes))

	for i, e := range envelopes {
		id, err := e.IngestionID()
		if err != nil {
			return 0, fmt.Errorf("ingestion id: %w", err)
		}

		now := time.Now().UTC()
		rows[i] = &bigquery.StructSaver{
			Struct: &bqRow{
				IngestionID:     id,
				IngestionLoadedAt: now,
				Provider:        e.Provider,
				Entity:          e.Entity,
				SourceKey:       e.SourceKey,
				Payload:         mustJSON(e.Payload),
			},
		}
	}

	inserter := table.Inserter()
	if err := inserter.Put(ctx, rows); err != nil {
		return 0, fmt.Errorf("insert rows: %w", err)
	}

	// Calculate bytes (rough estimate)
	var buf bytes.Buffer
	for _, env := range envelopes {
		b, _ := json.Marshal(env.Payload)
		buf.Write(b)
		buf.WriteString("\n")
	}

	return int64(buf.Len()), nil
}

func (l *Loader) loadViaGCS(ctx context.Context, table *bigquery.Table, envelopes []sdk.Envelope) (int64, error) {
	// Generate staging file path
	today := time.Now().UTC().Format("2006-01-02")
	objName := fmt.Sprintf("%s%s/%d.ndjson", l.cfg.StagingPrefix, today, time.Now().UnixNano())

	bucket := l.gcs.Bucket(l.cfg.StagingBucket)
	obj := bucket.Object(objName)

	// Write envelopes to staging
	wc := obj.NewWriter(ctx)
	bytesWritten := int64(0)

	for _, e := range envelopes {
		id, err := e.IngestionID()
		if err != nil {
			return 0, fmt.Errorf("ingestion id: %w", err)
		}

		now := time.Now().UTC()
		row := bqRow{
			IngestionID:     id,
			IngestionLoadedAt: now,
			Provider:        e.Provider,
			Entity:          e.Entity,
			SourceKey:       e.SourceKey,
			Payload:         mustJSON(e.Payload),
		}

		data, _ := json.Marshal(row)
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
	gcsRef.Format = bigquery.NDJSON

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

func (l *Loader) ensureTable(ctx context.Context, table *bigquery.Table) error {
	_, err := table.Metadata(ctx)
	if err == nil {
		return nil // table exists
	}

	schema := bigquery.Schema{
		{Name: "ingestion_id", Type: bigquery.StringFieldType, Required: true},
		{Name: "ingestion_loaded_at", Type: bigquery.TimestampFieldType, Required: true},
		{Name: "provider", Type: bigquery.StringFieldType, Required: true},
		{Name: "entity", Type: bigquery.StringFieldType, Required: true},
		{Name: "source_key", Type: bigquery.StringFieldType},
		{Name: "payload", Type: bigquery.JSONFieldType, Required: true},
	}

	// Create table with partitioning and clustering
	meta := &bigquery.TableMetadata{
		Schema: schema,
		TimePartitioning: &bigquery.TimePartitioning{
			Type:  bigquery.DayPartitioningType,
			Field: "ingestion_loaded_at",
		},
		Clustering: &bigquery.Clustering{
			Fields: []string{"provider", "entity"},
		},
	}

	if err := table.Create(ctx, meta); err != nil {
		return fmt.Errorf("create table: %w", err)
	}

	return nil
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

type bqRow struct {
	IngestionID      string          `bigquery:"ingestion_id"`
	IngestionLoadedAt time.Time        `bigquery:"ingestion_loaded_at"`
	Provider         string          `bigquery:"provider"`
	Entity           string          `bigquery:"entity"`
	SourceKey        string          `bigquery:"source_key"`
	Payload          json.RawMessage `bigquery:"payload"`
}