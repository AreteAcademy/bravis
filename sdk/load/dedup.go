package load

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"cloud.google.com/go/bigquery"
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

	// The temporary table carries an expiration so an interrupted run cannot
	// leave a table behind forever.
	temp := l.bq.Dataset(l.cfg.Dataset).Table(fmt.Sprintf("_bravis_merge_%d", time.Now().UnixNano()))
	// It takes its shape from the data, like the destination does, and
	// expires on its own so an interrupted run cannot leave it behind.
	if err := temp.Create(ctx, &bigquery.TableMetadata{
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
	source.AutoDetect = true

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
	// So: read both schemas, agree on a column list, and name it.
	destMeta, err := table.Metadata(ctx)
	if err != nil {
		return 0, 0, nil, fmt.Errorf("reading the destination schema: %w", err)
	}
	tempMeta, err := temp.Metadata(ctx)
	if err != nil {
		return 0, 0, nil, fmt.Errorf("reading the staged schema: %w", err)
	}

	cols, err := reconcile(destMeta.Schema, tempMeta.Schema)
	if err != nil {
		return 0, 0, nil, fmt.Errorf("the rows do not fit %s: %w", nameOf(table), err)
	}

	sql := mergeSQL(table, temp, cols, metadataID)

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
