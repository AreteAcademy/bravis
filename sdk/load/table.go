package load

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"cloud.google.com/go/bigquery"
	core "github.com/AreteAcademy/bravis/sdk/internal/core"
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
func (l *Loader) prepareTable(ctx context.Context, table *bigquery.Table) (bool, error) {
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

	if l.cfg.CreateSQL == "" {
		// The load job creates it, inferring the schema from the data.
		return false, nil
	}

	return false, l.createFromSQL(ctx, table)
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
// only with ExtraMetadata: ingestion_loaded_at is the only column it knows
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

	loader.CreateDisposition = bigquery.CreateIfNeeded

	// Only when the SDK is inventing the table. With CreateSQL the caller
	// already said what the columns are, and inferring over that would be
	// second-guessing them.
	if l.cfg.CreateSQL == "" {
		file.AutoDetect = true
	}

	if l.cfg.ExtraMetadata {
		loader.TimePartitioning = &bigquery.TimePartitioning{
			Type:                   bigquery.DayPartitioningType,
			Field:                  metadataLoadedAt,
			Expiration:             l.cfg.PartitionExpiration,
			RequirePartitionFilter: l.cfg.RequirePartitionFilter,
		}
	}

	if len(l.cfg.ClusterBy) > 0 {
		loader.Clustering = &bigquery.Clustering{Fields: l.cfg.ClusterBy}
	}
}

// describeTable attaches a description and labels once the table exists.
//
// Best effort: a table that loaded fine must not be reported as a failure
// because a label did not stick. It answers "what writes here?" six months
// later, which is worth attempting and not worth failing over.
func (l *Loader) describeTable(ctx context.Context, table *bigquery.Table) {
	md, err := table.Metadata(ctx)
	if err != nil {
		return
	}
	if md.Description != "" || len(md.Labels) > 0 {
		return // someone already said something; leave it
	}

	update := bigquery.TableMetadataToUpdate{Description: tableDescription(l.cfg)}
	for k, v := range tableLabels(l.cfg) {
		update.SetLabel(k, v)
	}

	if _, err := table.Update(ctx, update, md.ETag); err != nil {
		return
	}
}

func tableDescription(cfg *core.LoadConfig) string {
	who := "the Bravis SDK"
	if cfg.Provider != "" && cfg.Entity != "" {
		who = fmt.Sprintf("%s/%s via the Bravis SDK", cfg.Provider, cfg.Entity)
	}
	if cfg.ExtraMetadata {
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
func tableLabels(cfg *core.LoadConfig) map[string]string {
	labels := map[string]string{}
	for key, raw := range map[string]string{"provider": cfg.Provider, "entity": cfg.Entity} {
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
