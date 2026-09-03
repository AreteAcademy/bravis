package load

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"cloud.google.com/go/bigquery"
	"cloud.google.com/go/storage"
	core "github.com/AreteAcademy/bravis/sdk/internal/core"
)

// Loader writes Envelopes to BigQuery as generic JSON.
// The SDK does NOT impose a table schema — you define it.
// Metadata can be optionally added to the payload itself.
type Loader struct {
	cfg *core.LoadConfig
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
//		core.WithProjectID("my-project"),
//		core.WithDataset("landing"),
//		core.WithTable("raw_data"),
//	)
func New(ctx context.Context, cfg *core.LoadConfig, opts ...core.LoadOption) (*Loader, error) {
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
func resolveConfig(cfg *core.LoadConfig, opts ...core.LoadOption) (*core.LoadConfig, error) {
	var c core.LoadConfig
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
	if _, err := sourceFormat(c.Format); err != nil {
		return nil, err
	}

	switch c.Driver {
	case "", core.DriverBigQuery:
		c.Driver = core.DriverBigQuery
	default:
		return nil, fmt.Errorf("load driver %q is not implemented; use %q",
			c.Driver, core.DriverBigQuery)
	}

	if c.Dedup == core.DedupMerge && !c.WriteEnvelopeColumns {
		return nil, fmt.Errorf("DedupMerge requires WriteEnvelopeColumns: the merge matches on " +
			"ingestion_id, which only exists as a column in the landing contract")
	}

	if c.AddMetadata && c.WriteEnvelopeColumns {
		return nil, fmt.Errorf("AddMetadata and WriteEnvelopeColumns are two different answers " +
			"to the same question: the first folds provenance flat into your payload, the " +
			"second wraps it in the six envelope columns. Pick one")
	}
	return &c, nil
}

const (
	defaultThresholdForGCS = 5000
)

// sourceFormat maps a configured format onto the BigQuery source format, and
// refuses the ones the SDK does not actually write.
//
// LoadConfig.Format used to accept "csv" and "parquet" while every code path
// wrote NDJSON regardless, and LoadResult echoed the configured value back --
// so WithFormat("parquet") reported a Parquet load that never happened. An API
// that rejects what it cannot do is trustworthy; one that accepts and ignores
// is not.
func sourceFormat(format string) (bigquery.DataFormat, error) {
	switch format {
	case "", "ndjson":
		return bigquery.JSON, nil
	case "csv", "parquet":
		return "", fmt.Errorf("format %q is not implemented in this version: the SDK writes ndjson. "+
			"Track it at https://github.com/AreteAcademy/bravis/issues", format)
	default:
		return "", fmt.Errorf("unknown format %q, want \"ndjson\"", format)
	}
}

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
func (l *Loader) Load(ctx context.Context, envelopes ...core.Envelope) (*core.LoadResult, error) {
	start := time.Now()

	// Every return carries a result, including the failures: the documented
	// way to read per-row diagnostics is result.ErrorRows after a non-nil
	// error, and returning nil there turns a failed load into a panic.
	dedup := l.cfg.Dedup
	if dedup == "" {
		dedup = core.DedupNone
	}

	result := &core.LoadResult{
		Format:   l.cfg.Format,
		Strategy: strategyFor(len(envelopes), l.cfg.ThresholdForGCS),
		Dedup:    dedup,
	}
	fail := func(err error) (*core.LoadResult, error) {
		result.Duration = time.Since(start)
		return result, err
	}

	if len(envelopes) == 0 {
		return fail(nil)
	}

	if l.cfg.AddMetadata {
		for i := range envelopes {
			if err := l.addMetadataToEnvelope(&envelopes[i]); err != nil {
				return fail(fmt.Errorf("add metadata: %w", err))
			}
		}
	}

	table := l.bq.Dataset(l.cfg.Dataset).Table(l.cfg.Table)

	created, err := l.ensureTable(ctx, table)
	if err != nil {
		return fail(err)
	}
	result.TableCreated = created

	data, err := l.encodeRows(envelopes)
	if err != nil {
		return fail(err)
	}

	var rowErrs []string

	if dedup == core.DedupMerge {
		inserted, ignored, errs, err := l.loadWithMerge(ctx, table, data, int64(len(envelopes)))
		result.BytesStaged = int64(len(data))
		result.ErrorRows = errs
		if err != nil {
			return fail(err)
		}
		result.RowsLoaded = inserted
		result.RowsIgnored = ignored
	} else {
		var bytesStaged int64
		if result.Strategy == "gcs" {
			bytesStaged, rowErrs, err = l.loadViaGCS(ctx, table, data)
		} else {
			bytesStaged, rowErrs, err = l.loadInline(ctx, table, data)
		}

		result.BytesStaged = bytesStaged
		result.ErrorRows = rowErrs
		if err != nil {
			return fail(err)
		}
		result.RowsLoaded = int64(len(envelopes))
	}
	result.Duration = time.Since(start)

	slog.InfoContext(ctx, "load complete",
		"table", fmt.Sprintf("%s.%s", l.cfg.Dataset, l.cfg.Table),
		"rows", result.RowsLoaded,
		"ignored", result.RowsIgnored,
		"bytes", result.BytesStaged,
		"strategy", result.Strategy,
		"dedup", result.Dedup,
		"created", result.TableCreated,
		"duration", result.Duration)

	return result, nil
}

// envelopeColumns is the six-column landing contract: one place produces the
// ingestion_id, so a row written by this SDK matches the row a Python fetcher
// writes for the same record. That single owner is the whole point -- rebuild
// these columns per consumer and the ids drift apart, which is exactly the
// duplication the contract exists to prevent.
//
//	ingestion_id        STRING    NOT NULL
//	ingestion_loaded_at TIMESTAMP NOT NULL
//	provider            STRING    NOT NULL
//	entity              STRING    NOT NULL
//	source_key          STRING
//	payload             JSON      NOT NULL
func (l *Loader) envelopeColumns(env core.Envelope) (map[string]any, error) {
	id, err := env.IngestionID()
	if err != nil {
		return nil, err
	}

	return map[string]any{
		"ingestion_id":        id,
		"ingestion_loaded_at": time.Now().UTC().Format(time.RFC3339),
		"provider":            env.Provider,
		"entity":              env.Entity,
		"source_key":          env.SourceKey,
		"payload":             env.Payload,
	}, nil
}

// metadataFields are the provenance keys AddMetadata merges into a payload.
//
// They carry no prefix: these are the same names the envelope contract uses
// as columns, so a flat row and a wrapped row describe a record identically
// and downstream SQL reads one spelling, not two.
//
// The prefix used to be "_bravis_", which made collisions impossible. Without
// it a source that has its own "provider" field would be silently overwritten
// -- so a collision is an error now, naming the field. Silently replacing a
// vendor's data with our provenance is the worse failure: it is invisible.
var metadataFields = []string{
	"ingestion_id",
	"ingestion_loaded_at",
	"provider",
	"entity",
	"source_key",
	"record_ts",
}

// addMetadataToEnvelope merges provenance into the envelope's payload.
func (l *Loader) addMetadataToEnvelope(env *core.Envelope) error {
	id, err := env.IngestionID()
	if err != nil {
		return err
	}

	var payload map[string]any
	switch p := env.Payload.(type) {
	case map[string]any:
		// Copy: the caller may still hold this map, and a load must not
		// mutate what it was handed.
		payload = make(map[string]any, len(p)+len(metadataFields))
		for k, v := range p {
			payload[k] = v
		}
	default:
		data, err := json.Marshal(env.Payload)
		if err != nil {
			return fmt.Errorf("marshal payload: %w", err)
		}
		if err := json.Unmarshal(data, &payload); err != nil {
			return fmt.Errorf("unmarshal to map: %w", err)
		}
	}

	var clashes []string
	for _, f := range metadataFields {
		if _, taken := payload[f]; taken {
			clashes = append(clashes, f)
		}
	}
	if len(clashes) > 0 {
		return fmt.Errorf("payload already has the field(s) %s, which AddMetadata would overwrite. "+
			"Rename them in your payload, or use WriteEnvelopeColumns, which nests the payload "+
			"under a payload column and cannot collide", strings.Join(clashes, ", "))
	}

	payload["ingestion_id"] = id
	payload["ingestion_loaded_at"] = time.Now().UTC().Format(time.RFC3339)
	payload["provider"] = env.Provider
	payload["entity"] = env.Entity
	payload["source_key"] = env.SourceKey
	payload["record_ts"] = env.RecordTS

	env.Payload = payload
	return nil
}

// encodeRows renders the batch as NDJSON, which is what both strategies load.
//
// Every line must be a JSON object: BigQuery maps its keys onto the columns of
// the destination table, so a bare scalar or an array has nothing to map and
// must fail here rather than halfway through a load job.
func (l *Loader) encodeRows(envelopes []core.Envelope) ([]byte, error) {
	var buf bytes.Buffer

	for i, env := range envelopes {
		row := env.Payload

		if l.cfg.WriteEnvelopeColumns {
			cols, err := l.envelopeColumns(env)
			if err != nil {
				return nil, fmt.Errorf("row %d: %w", i, err)
			}
			row = cols
		}

		data, err := json.Marshal(row)
		if err != nil {
			return nil, fmt.Errorf("marshal row %d: %w", i, err)
		}

		var probe map[string]json.RawMessage
		if err := json.Unmarshal(data, &probe); err != nil {
			return nil, fmt.Errorf("row %d must encode to a JSON object, got %s", i, truncate(data, 80))
		}

		buf.Write(data)
		buf.WriteByte('\n')
	}

	return buf.Bytes(), nil
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "..."
}

// maxReportedErrors caps how many per-row failures travel back in
// LoadResult. A load job can fail on every row; the first handful say what is
// wrong, and the rest only make the message unreadable.
const maxReportedErrors = 10

// runLoadJob submits a load job and waits for it. Both strategies end here;
// they differ only in where BigQuery reads the NDJSON from.
//
// It returns the per-row failures BigQuery reported alongside the error,
// because "load failed" on its own costs an investigation.
func runLoadJob(ctx context.Context, loader *bigquery.Loader) ([]string, error) {
	loader.WriteDisposition = bigquery.WriteAppend

	job, err := loader.Run(ctx)
	if err != nil {
		return nil, fmt.Errorf("start load job: %w", err)
	}

	status, err := job.Wait(ctx)
	if err != nil {
		return nil, fmt.Errorf("wait for load job: %w", err)
	}
	if err := status.Err(); err != nil {
		return rowErrors(status), fmt.Errorf("load job failed: %w", err)
	}

	return nil, nil
}

// rowErrors renders the per-row diagnostics BigQuery attaches to a failed job.
func rowErrors(status *bigquery.JobStatus) []string {
	if status == nil {
		return nil
	}

	out := make([]string, 0, len(status.Errors))
	for i, e := range status.Errors {
		if i == maxReportedErrors {
			out = append(out, fmt.Sprintf("... and %d more", len(status.Errors)-maxReportedErrors))
			break
		}
		if e == nil {
			continue
		}
		if e.Location != "" {
			out = append(out, fmt.Sprintf("%s: %s", e.Location, e.Message))
			continue
		}
		out = append(out, e.Message)
	}
	return out
}

// loadInline embeds the NDJSON in the load job itself.
//
// This used to go through table.Inserter(), which is the streaming insert API:
// billed per row, and rows sit in a streaming buffer where DML cannot see them
// for up to 90 minutes. Strategy said "inline" and the docs said "load job",
// but the consistency model was neither. It is a batch load job now, matching
// what the rest of the SDK promises; the Storage Write API stays out of v1 for
// the same reason.
func (l *Loader) loadInline(ctx context.Context, table *bigquery.Table, data []byte) (int64, []string, error) {
	format, err := sourceFormat(l.cfg.Format)
	if err != nil {
		return 0, nil, err
	}

	source := bigquery.NewReaderSource(bytes.NewReader(data))
	source.SourceFormat = format

	if rows, err := runLoadJob(ctx, table.LoaderFrom(source)); err != nil {
		return 0, rows, err
	}

	return int64(len(data)), nil, nil
}

func (l *Loader) loadViaGCS(ctx context.Context, table *bigquery.Table, data []byte) (int64, []string, error) {
	format, err := sourceFormat(l.cfg.Format)
	if err != nil {
		return 0, nil, err
	}

	today := time.Now().UTC().Format("2006-01-02")
	objName := fmt.Sprintf("%s%s/%d.ndjson", l.cfg.StagingPrefix, today, time.Now().UnixNano())

	obj := l.gcs.Bucket(l.cfg.StagingBucket).Object(objName)

	wc := obj.NewWriter(ctx)
	if _, err := wc.Write(data); err != nil {
		_ = wc.Close()
		_ = obj.Delete(ctx)
		return 0, nil, fmt.Errorf("write to gcs: %w", err)
	}
	if err := wc.Close(); err != nil {
		_ = obj.Delete(ctx)
		return 0, nil, fmt.Errorf("close gcs writer: %w", err)
	}

	gcsRef := bigquery.NewGCSReference(fmt.Sprintf("gs://%s/%s", l.cfg.StagingBucket, objName))
	// NewGCSReference leaves SourceFormat empty and BigQuery reads empty as
	// CSV. We stage NDJSON, so without this every row of every GCS-strategy
	// load was parsed wrong -- and the job still succeeded.
	gcsRef.SourceFormat = format

	rows, err := runLoadJob(ctx, table.LoaderFrom(gcsRef))
	if err != nil {
		_ = obj.Delete(ctx)
		return 0, rows, err
	}

	if l.cfg.DeleteAfterLoad {
		if err := obj.Delete(ctx); err != nil {
			slog.WarnContext(ctx, "staged object left behind", "object", objName, "error", err)
		}
	}

	return int64(len(data)), nil, nil
}
