package load

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"cloud.google.com/go/bigquery"
	"google.golang.org/api/googleapi"
)

// landingSchema is the six-column landing contract, the only schema the SDK
// knows well enough to create.
//
// Partitioning and clustering ship by default and are not optional: an
// unpartitioned landing table costs a full scan on every MERGE the bronze
// layer runs. Measured on a consumer, one entity spent 58.96 GiB of MERGE
// against 0.0 GiB of SELECT.
func landingSchema() bigquery.Schema {
	return bigquery.Schema{
		{Name: "ingestion_id", Type: bigquery.StringFieldType, Required: true},
		{Name: "ingestion_loaded_at", Type: bigquery.TimestampFieldType, Required: true},
		{Name: "provider", Type: bigquery.StringFieldType, Required: true},
		{Name: "entity", Type: bigquery.StringFieldType, Required: true},
		{Name: "source_key", Type: bigquery.StringFieldType},
		{Name: "payload", Type: bigquery.JSONFieldType, Required: true},
	}
}

func landingMetadata() *bigquery.TableMetadata {
	return &bigquery.TableMetadata{
		Schema: landingSchema(),
		TimePartitioning: &bigquery.TimePartitioning{
			Type:  bigquery.DayPartitioningType,
			Field: "ingestion_loaded_at",
		},
		Clustering: &bigquery.Clustering{
			Fields: []string{"provider", "entity"},
		},
	}
}

// ensureTable makes sure the destination exists and matches the contract.
//
// It creates the table when absent and, when present, compares and refuses on
// any difference. It never alters: a loader that can ALTER or DROP on its own
// is a loader that can erase history, and no convenience is worth that.
//
// Reports whether it created the table.
func (l *Loader) ensureTable(ctx context.Context, table *bigquery.Table) (bool, error) {
	md, err := table.Metadata(ctx)
	if err == nil {
		return false, checkSchema(table, md)
	}

	if !isNotFound(err) {
		return false, fmt.Errorf("looking up %s: %w", nameOf(table), err)
	}

	if !l.cfg.CreateTable {
		return false, fmt.Errorf("table %s does not exist. Create it, or set CreateTable to let the SDK "+
			"create it with the six-column contract", nameOf(table))
	}
	if !l.cfg.WriteEnvelopeColumns {
		return false, fmt.Errorf("CreateTable requires WriteEnvelopeColumns: the SDK only knows the " +
			"landing contract schema, not yours")
	}

	if err := table.Create(ctx, landingMetadata()); err != nil {
		// Another process may have created it between our check and this call.
		if md, second := table.Metadata(ctx); second == nil {
			return false, checkSchema(table, md)
		}
		return false, fmt.Errorf("creating %s: %w", nameOf(table), err)
	}

	return true, nil
}

// checkSchema refuses a table that does not match the contract, naming the
// difference. A vague "schema mismatch" costs an investigation; the caller
// needs to know which column is wrong.
func checkSchema(table *bigquery.Table, md *bigquery.TableMetadata) error {
	expected := landingSchema()

	types := map[string]bigquery.FieldType{}
	for _, f := range md.Schema {
		types[f.Name] = f.Type
	}

	var missing, differing []string
	for _, f := range expected {
		tipo, ok := types[f.Name]
		if !ok {
			missing = append(missing, f.Name)
			continue
		}
		if tipo != f.Type {
			differing = append(differing, fmt.Sprintf("%s is %s, expected %s", f.Name, tipo, f.Type))
		}
	}

	if len(missing) == 0 && len(differing) == 0 {
		return nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "table %s exists but does not match the landing contract", nameOf(table))
	if len(missing) > 0 {
		sort.Strings(missing)
		fmt.Fprintf(&b, "; missing columns: %s", strings.Join(missing, ", "))
	}
	if len(differing) > 0 {
		sort.Strings(differing)
		fmt.Fprintf(&b, "; types differing: %s", strings.Join(differing, "; "))
	}
	b.WriteString(". The SDK never alters an existing table -- fix it, or point somewhere else")

	return fmt.Errorf("%s", b.String())
}

func nameOf(t *bigquery.Table) string {
	return fmt.Sprintf("%s.%s", t.DatasetID, t.TableID)
}

// isNotFound distinguishes "the table is not there" from "we could not
// ask" -- creating a table because of a permissions blip would be wrong, so
// this checks for a 404 specifically rather than any failure.
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
