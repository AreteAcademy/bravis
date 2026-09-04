package load

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"cloud.google.com/go/bigquery"
	core "github.com/AreteAcademy/brevis/sdk/internal/core"
	"google.golang.org/api/googleapi"
)

// prepareTable makes sure the destination is ready to receive the batch, and
// reports whether the table already existed.
//
// The SDK does not know your schema -- the payload is yours -- so it cannot
// write a CREATE for you. Two ways to get one anyway:
//
//   - CreateSQL: your DDL, run once when the table is absent
//   - CreateTable alone: the load job creates it, inferring the schema from
//     the data (BigQuery's own autodetect)
//
// It never alters a table that already exists. A loader that can ALTER or
// DROP is a loader that can erase history.
func (l *Loader) prepareTable(ctx context.Context, table *bigquery.Table, data []byte, prov provenance) (bool, error) {
	_, err := table.Metadata(ctx)
	if err == nil {
		return true, nil
	}
	if !isNotFound(err) {
		return false, fmt.Errorf("looking up %s: %w", nameOf(table), err)
	}

	if !l.cfg.CreateTable {
		return false, fmt.Errorf("table %s does not exist. Set CreateTable to let the SDK "+
			"create it, or create it yourself", nameOf(table))
	}

	if l.cfg.CreateSQL != "" {
		return false, l.createFromSQL(ctx, table)
	}

	// The SDK types a column only when the caller declared it. Columns is
	// where the destination's shape is written, so a declaration that names
	// ingestion_id is the fetcher asking for the SDK's column -- not a default
	// deciding the table's shape behind its back.
	//
	// Nothing declared, nothing to type: the load job infers every column.
	if !typesAnything(l.cfg.Columns) {
		return false, nil
	}

	// Two of the declared columns are the SDK's, and they have a shape:
	//
	//	ingestion_id         STRING    NOT NULL
	//	ingestion_loaded_at  TIMESTAMP NOT NULL
	//
	// Autodetect cannot produce that -- it infers both as NULLABLE, and
	// BigQuery refuses to tighten a NULLABLE column afterwards. So the table
	// is created here instead, from a schema BigQuery itself inferred over
	// the caller's columns, with the SDK's two overridden.
	//
	// The SDK still infers no type of its own. Guessing that a float64 out of
	// encoding/json means FLOAT64 would put the inference back through a side
	// door, on the columns least suited to it.
	return false, l.createTyped(ctx, table, data, prov)
}

// createFromSQL runs the caller's DDL and confirms it produced the table the
// load is about to write to.
//
// Running someone's statement and trusting it would move the failure to the
// load, where the error is about a missing column rather than about the DDL
// that forgot it.
func (l *Loader) createFromSQL(ctx context.Context, table *bigquery.Table) error {
	job, err := l.bq.Query(l.cfg.CreateSQL).Run(ctx)
	if err != nil {
		return fmt.Errorf("running CreateSQL: %w", err)
	}

	status, err := job.Wait(ctx)
	if err != nil {
		return fmt.Errorf("waiting for CreateSQL: %w", err)
	}
	if err := status.Err(); err != nil {
		return fmt.Errorf("CreateSQL failed: %w", err)
	}

	if _, err := table.Metadata(ctx); err != nil {
		if isNotFound(err) {
			return fmt.Errorf("CreateSQL ran without error but %s still does not exist; "+
				"does the statement name that table?", nameOf(table))
		}
		return fmt.Errorf("checking what CreateSQL produced: %w", err)
	}

	return nil
}

// applyLayout configures a load job that may create the table.
//
// Partitioning is the one layout decision the SDK can make on its own, and
// only with Metadata: ingestion_loaded_at is the only column it knows
// exists. An unpartitioned landing table costs a full scan on every MERGE the
// bronze layer runs -- measured on a consumer, one entity spent 58.96 GiB of
// MERGE against 0.0 GiB of SELECT.
//
// Clustering has to be named: the SDK does not know your payload.
func (l *Loader) applyLayout(loader *bigquery.Loader, file *bigquery.FileConfig) {
	if !l.cfg.CreateTable {
		loader.CreateDisposition = bigquery.CreateNever
		return
	}

	// The load job creates the table only when nobody else did. CreateSQL and
	// the typed-metadata path both create it first, and pointing autodetect
	// at a table that already has a schema is how a REQUIRED column gets
	// relaxed back to NULLABLE -- BigQuery refuses outright, which is the
	// good outcome, but it refuses the whole load.
	if l.cfg.CreateSQL != "" || typesAnything(l.cfg.Columns) {
		loader.CreateDisposition = bigquery.CreateNever
		return
	}

	loader.CreateDisposition = bigquery.CreateIfNeeded
	file.AutoDetect = true

	if len(l.cfg.ClusterBy) > 0 {
		loader.Clustering = &bigquery.Clustering{Fields: l.cfg.ClusterBy}
	}
}

// describeTable attaches a description and labels once the table exists.
//
// Best effort: a table that loaded fine must not be reported as a failure
// because a label did not stick. It answers "what writes here?" six months
// later, which is worth attempting and not worth failing over.
func (l *Loader) describeTable(ctx context.Context, table *bigquery.Table, prov provenance) {
	md, err := table.Metadata(ctx)
	if err != nil {
		return
	}
	if md.Description != "" || len(md.Labels) > 0 {
		return // someone already said something; leave it
	}

	update := bigquery.TableMetadataToUpdate{Description: tableDescription(l.cfg, prov)}
	for k, v := range tableLabels(prov) {
		update.SetLabel(k, v)
	}

	if _, err := table.Update(ctx, update, md.ETag); err != nil {
		return
	}
}

// provenance labels the created table, for cost attribution and for answering
// "what writes here?" six months later.
//
// It comes from the batch, not from configuration. There is no second place
// for a fetcher to say it, and a second place would be a second chance for the
// two to disagree.
type provenance struct{ Provider, Entity string }

func provenanceOf(records []core.Envelope) provenance {
	if len(records) == 0 {
		return provenance{}
	}

	// From the row's own provider and entity columns when it has them: that
	// is where a fetcher composes them, and reading them anywhere else would
	// be a second place for the two to disagree.
	if row, err := core.AsObject(records[0].Payload); err == nil {
		if p, e := text(row["provider"]), text(row["entity"]); p != "" || e != "" {
			return provenance{Provider: p, Entity: e}
		}
	}

	// The low-level API hands envelopes with provenance on them instead.
	return provenance{Provider: records[0].Provider, Entity: records[0].Entity}
}

func text(v any) string {
	s, _ := v.(string)
	return s
}

func tableDescription(cfg *core.LoadConfig, prov provenance) string {
	who := "the Brevis SDK"
	if prov.Provider != "" && prov.Entity != "" {
		who = fmt.Sprintf("%s/%s via the Brevis SDK", prov.Provider, prov.Entity)
	}
	if declares(cfg.Columns, core.MetadataID) {
		return fmt.Sprintf("Written by %s since %s. Rows carry ingestion_id; deduplicate "+
			"on it downstream. The SDK never alters this table.",
			who, time.Now().UTC().Format("2006-01-02"))
	}
	return fmt.Sprintf("Written by %s since %s. The SDK never alters this table.",
		who, time.Now().UTC().Format("2006-01-02"))
}

// tableLabels attach the source to the table for cost attribution in billing.
//
// BigQuery takes lowercase letters, digits, dashes and underscores, up to 63
// characters, starting with a letter. A value that does not fit is dropped
// rather than failing: a naming rule is not worth losing the load over.
func tableLabels(prov provenance) map[string]string {
	labels := map[string]string{}
	for key, raw := range map[string]string{"provider": prov.Provider, "entity": prov.Entity} {
		if v := sanitiseLabel(raw); v != "" {
			labels[key] = v
		}
	}
	return labels
}

var labelAllowed = regexp.MustCompile(`[^a-z0-9_-]+`)

func sanitiseLabel(v string) string {
	v = labelAllowed.ReplaceAllString(strings.ToLower(v), "_")
	v = strings.Trim(v, "_-")
	if len(v) > 63 {
		v = v[:63]
	}
	if v == "" || v[0] < 'a' || v[0] > 'z' {
		return ""
	}
	return v
}

func nameOf(t *bigquery.Table) string {
	return fmt.Sprintf("%s.%s", t.DatasetID, t.TableID)
}

// isNotFound distinguishes "the table is not there" from "we could not ask" --
// creating a table because of a permissions blip would be wrong.
func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	var apiErr *googleapi.Error
	if errors.As(err, &apiErr) {
		return apiErr.Code == 404
	}
	return false
}

// The declared shape of the two columns the SDK writes. Everything else in
// the table is the caller's, and its types come from BigQuery.
var metadataSchema = map[string]*bigquery.FieldSchema{
	core.MetadataID:       {Name: core.MetadataID, Type: bigquery.StringFieldType, Required: true},
	core.MetadataLoadedAt: {Name: core.MetadataLoadedAt, Type: bigquery.TimestampFieldType, Required: true},
}

// typesAnything reports whether the declaration names a column the SDK knows
// the shape of.
//
// This is the whole trigger for the typed-creation path, and it is the
// caller's own list -- which is what keeps it from being a default deciding
// the table's shape without appearing in the fetcher. Declare the column, get
// the guarantee; declare nothing, and autodetect infers everything nullable.
func typesAnything(columns []string) bool {
	for _, c := range columns {
		if _, mine := metadataSchema[c]; mine {
			return true
		}
	}
	return false
}

// createTyped creates the destination with ingestion_id and
// ingestion_loaded_at declared NOT NULL, and the caller's columns typed by
// BigQuery.
//
// It costs one extra load job, on the run that creates the table and never
// again. The alternative was to guess the caller's types in Go, which is the
// one thing this SDK will not do.
func (l *Loader) createTyped(ctx context.Context, table *bigquery.Table, data []byte, prov provenance) error {
	inferred, err := l.inferSchema(ctx, data)
	if err != nil {
		return err
	}

	meta := typedTable(l.cfg, inferred, prov)

	if err := table.Create(ctx, meta); err != nil {
		// Two loads racing to create the same table is normal, and the loser
		// wants the table, not the error.
		if isConflict(err) {
			return nil
		}
		return fmt.Errorf("creating %s: %w", nameOf(table), err)
	}

	return nil
}

// typedTable is the destination's declaration: the caller's columns as
// BigQuery typed them, with the SDK's two overridden to their declared shape,
// plus the layout.
//
// Pure, so a test can read the schema this produces without a BigQuery
// client -- which is where the NOT NULL either survives or quietly does not.
func typedTable(cfg *core.LoadConfig, inferred bigquery.Schema, prov provenance) *bigquery.TableMetadata {
	schema := make(bigquery.Schema, 0, len(inferred))
	for _, f := range inferred {
		if own, mine := metadataSchema[f.Name]; mine {
			schema = append(schema, own)
			continue
		}
		schema = append(schema, f)
	}

	meta := &bigquery.TableMetadata{
		Schema:      schema,
		Description: tableDescription(cfg, prov),
		Labels:      tableLabels(prov),
		TimePartitioning: &bigquery.TimePartitioning{
			Type:                   bigquery.DayPartitioningType,
			Field:                  core.MetadataLoadedAt,
			Expiration:             cfg.PartitionExpiration,
			RequirePartitionFilter: cfg.RequirePartitionFilter,
		},
	}
	if len(cfg.ClusterBy) > 0 {
		meta.Clustering = &bigquery.Clustering{Fields: cfg.ClusterBy}
	}
	return meta
}

// inferSchema asks BigQuery what the caller's columns are, by loading the
// batch into a throwaway table with autodetect on.
func (l *Loader) inferSchema(ctx context.Context, data []byte) (bigquery.Schema, error) {
	format, err := sourceFormat(l.cfg.Format)
	if err != nil {
		return nil, err
	}

	tmp := l.bq.Dataset(l.cfg.Dataset).Table(fmt.Sprintf("_brevis_schema_%d", time.Now().UnixNano()))
	// The expiration matters: an interrupted run must not leave a table
	// behind forever.
	if err := tmp.Create(ctx, &bigquery.TableMetadata{
		ExpirationTime: time.Now().Add(6 * time.Hour),
	}); err != nil {
		return nil, fmt.Errorf("creating the schema probe table: %w", err)
	}
	defer func() {
		if err := tmp.Delete(context.WithoutCancel(ctx)); err != nil {
			// Not fatal: the expiration collects it.
			_ = err
		}
	}()

	source := bigquery.NewReaderSource(bytes.NewReader(data))
	source.SourceFormat = format
	source.AutoDetect = true

	if rows, err := runLoadJob(ctx, tmp.LoaderFrom(source)); err != nil {
		return nil, fmt.Errorf("inferring the schema: %w%s", err, firstRow(rows))
	}

	md, err := tmp.Metadata(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading the inferred schema: %w", err)
	}
	return md.Schema, nil
}

func firstRow(rows []string) string {
	if len(rows) == 0 {
		return ""
	}
	return ": " + rows[0]
}

func isConflict(err error) bool {
	var apiErr *googleapi.Error
	return errors.As(err, &apiErr) && apiErr.Code == http.StatusConflict
}
