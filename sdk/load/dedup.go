package load

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"cloud.google.com/go/bigquery"
	core "github.com/AreteAcademy/brevis/sdk/internal/core"
)

// loadWithMerge stages the batch in a temporary table and MERGEs it into
// the destination on ingestion_id, so re-running the same window is a no-op.
//
// This costs one scan of the destination per load, which is why it is opt-in.
// The alternative that costs nothing is the streaming insertID, but that only
// exists on the streaming API -- billed per row, with rows invisible to DML
// for up to 90 minutes. This SDK is batch, so MERGE is the honest way to
// deduplicate at load time.
//
// Reports how many rows were inserted and how many the MERGE matched as
// already present.
func (l *Loader) loadWithMerge(ctx context.Context, table *bigquery.Table, data []byte, total int64) (inserted, ignored int64, rowErrs []string, err error) {
	format, err := sourceFormat(l.cfg.Format)
	if err != nil {
		return 0, 0, nil, err
	}

	// The staging table takes the DESTINATION's schema, not one inferred from
	// the data.
	//
	// Autodetect turns a nested JSON object into a RECORD, so a destination
	// that declares that column as JSON -- the right type for a vendor payload
	// -- could not receive it: "type mismatch on payload (destination JSON,
	// incoming RECORD)". Reported by a consumer whose landing has a JSON
	// column, and reproducible in one line.
	//
	// Taking the destination's schema fixes more than the type. The staged
	// column ORDER stops being inferred too, which removes at the root the
	// class of bug that the named column list in the MERGE below exists to
	// compensate for: with both schemas equal by construction, there is no
	// order to get wrong.
	destMeta, err := table.Metadata(ctx)
	if err != nil {
		return 0, 0, nil, fmt.Errorf("reading the destination schema: %w", err)
	}

	temp := l.bq.Dataset(l.cfg.Dataset).Table(fmt.Sprintf("_brevis_merge_%d", time.Now().UnixNano()))
	// It expires on its own so an interrupted run cannot leave it behind.
	if err := temp.Create(ctx, &bigquery.TableMetadata{
		Schema:         destMeta.Schema,
		ExpirationTime: time.Now().Add(6 * time.Hour),
	}); err != nil {
		return 0, 0, nil, fmt.Errorf("creating temporary table: %w", err)
	}
	defer func() {
		if err := temp.Delete(context.WithoutCancel(ctx)); err != nil {
			// Not fatal: the expiration will collect it.
			_ = err
		}
	}()

	source := bigquery.NewReaderSource(bytes.NewReader(data))
	source.SourceFormat = format
	// No AutoDetect: the schema is the destination's, read above. A field the
	// rows carry and the destination does not have is refused by the load job
	// -- which is the right answer, and the same one Target.Columns gives
	// earlier and better when it is declared.

	stage := temp.LoaderFrom(source)
	stage.CreateDisposition = bigquery.CreateIfNeeded

	if rows, err := runLoadJob(ctx, stage); err != nil {
		return 0, 0, rows, fmt.Errorf("loading the temporary table: %w", err)
	}

	// The columns are named explicitly, and that is the whole point of this
	// block. INSERT ROW matches the two tables by POSITION, not by name: if
	// the staging table's autodetected column order differs from the
	// destination's, BigQuery either fails with a type error naming a column
	// that is perfectly fine, or -- when the types happen to line up -- writes
	// each value into the wrong column and says nothing at all.
	//
	// So: name the columns. The two schemas agree by construction now, so
	// reconcile is an assertion rather than a negotiation -- it still earns the
	// call it costs, because it would catch a staging table that came back
	// different from the one asked for, and it is the one place that derives
	// the column list.
	tempMeta, err := temp.Metadata(ctx)
	if err != nil {
		return 0, 0, nil, fmt.Errorf("reading the staged schema: %w", err)
	}

	cols, err := reconcile(destMeta.Schema, tempMeta.Schema)
	if err != nil {
		return 0, 0, nil, fmt.Errorf("the rows do not fit %s: %w", nameOf(table), err)
	}

	sql := mergeSQL(table, temp, cols, core.MetadataID)

	job, err := l.bq.Query(sql).Run(ctx)
	if err != nil {
		return 0, 0, nil, fmt.Errorf("starting merge: %w", err)
	}

	status, err := job.Wait(ctx)
	if err != nil {
		return 0, 0, nil, fmt.Errorf("waiting for merge: %w", err)
	}
	if err := status.Err(); err != nil {
		return 0, 0, rowErrors(status), fmt.Errorf("merge failed: %w", err)
	}

	inserted = total
	if qs, ok := status.Statistics.Details.(*bigquery.QueryStatistics); ok {
		inserted = qs.NumDMLAffectedRows
	}
	if inserted > total {
		inserted = total
	}

	return inserted, total - inserted, nil, nil
}
