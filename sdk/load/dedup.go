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
	md := landingMetadata()
	md.ExpirationTime = time.Now().Add(6 * time.Hour)

	if err := temp.Create(ctx, md); err != nil {
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

	if rows, err := runLoadJob(ctx, temp.LoaderFrom(source)); err != nil {
		return 0, 0, rows, fmt.Errorf("loading the temporary table: %w", err)
	}

	// WHEN NOT MATCHED only. The landing layer is append-only by contract, so
	// a row already there is left exactly as it was -- a re-run must never
	// rewrite history, only skip it.
	sql := fmt.Sprintf(`
MERGE `+"`%s.%s.%s`"+` AS alvo
USING `+"`%s.%s.%s`"+` AS novo
ON alvo.ingestion_id = novo.ingestion_id
WHEN NOT MATCHED THEN
  INSERT (ingestion_id, ingestion_loaded_at, provider, entity, source_key, payload)
  VALUES (novo.ingestion_id, novo.ingestion_loaded_at, novo.provider, novo.entity, novo.source_key, novo.payload)`,
		table.ProjectID, table.DatasetID, table.TableID,
		temp.ProjectID, temp.DatasetID, temp.TableID)

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
