package sdk

import (
	"fmt"
	"time"

	core "github.com/AreteAcademy/brevis/sdk/internal/core"
)

// Target says where records land, and what every destination honours.
//
// The destination itself is To -- to.BigQuery, to.Postgres, to.Files. What
// lives here instead of in the driver is what is true of all of them: the
// columns declared, the metadata asked for, the deduplication wanted.
//
//	Target: sdk.Target{
//		To:      to.BigQuery{Dataset: "bronze", Table: "pedidos"},
//		Columns: []string{"ingestion_id", "ingestion_loaded_at", "payload"},
//		Columns: []string{"ingestion_id", "ingestion_loaded_at", "payload"},
//	}
type Target struct {
	// To is the destination. Required.
	To Writer

	// Columns declares the destination's columns, in the order of its DDL,
	// including the ones the SDK fills in:
	//
	//	Columns: []string{
	//		"ingestion_id",         // from sdk.IngestionID()
	//		"ingestion_loaded_at",  // from sdk.IngestionLoadedAt()
	//		"provider",
	//		"entity",
	//		"source_key",
	//		"payload",
	//	}
	//
	// One declaration, and it names every column -- including the two that
	// the ingestion transformers write, so nothing lands that the chain did
	// not compose.
	//
	// Checked against the row the Transform chain composed, so a declared
	// column the chain did not deliver is an error naming the column, and a
	// field the row carries that this list does not declare is an error naming
	// the field. Checked again against the real destination,
	// where a declared column it lacks is an error naming both sides.
	//
	// Nil declares nothing and checks nothing. There is no fallback: this
	// list is the only place the destination's columns are declared.
	Columns []string

	// Dedup selects deduplication. Zero value appends, which is free. What
	// DedupMerge costs, and whether a destination supports it at all, is the
	// driver's to say.
	Dedup core.Dedup
}

// validate checks what the facade owns. What the destination needs is the
// destination's to check, and it does so in Write.
func (d Target) validate() error {
	if d.To == nil {
		return fmt.Errorf("Target.To is required: pass a destination, such as " +
			"to.BigQuery{Dataset: \"bronze\", Table: \"pedidos\"}")
	}
	return nil
}

// options folds the Target into what every driver receives.
func (d Target) options(run RunContext) core.WriteOptions {
	return core.WriteOptions{
		Columns: d.Columns,
		Dedup:   d.Dedup,
		Run:     run,
	}
}

// Result describes what actually happened, end to end. Printing it is meant
// to be the whole of a fetcher's observability:
//
//	log.Info("pronto", res.Args()...)
type Result struct {
	// Extract
	Records      int64 // records that came out of the source, after expansion
	Pages        int   // pages fetched
	Attempts     int   // HTTP attempts spent, retries included
	ExtractBytes int64 // bytes read off the wire, before Transform
	ExtractTime  time.Duration

	// Load
	Rows         int64      // rows written
	Ignored      int64      // rows deduplication matched as already present
	Bytes        int64      // bytes in the staged format
	Strategy     string     // how the driver wrote: "inline", "gcs", "copy"
	Format       string     // the format actually written
	Dedup        core.Dedup // the deduplication that actually ran
	TableCreated bool       // whether this run created the destination
	Table        string     // the destination written to
	LoadTime     time.Duration

	// Diagnostics the destination reported per row, when it refused any.
	RowErrors []string

	Duration time.Duration
}

// Args renders the result as slog key-value pairs.
func (r *Result) Args() []any {
	return []any{
		"records", r.Records,
		"lines", r.Rows,
		"ignored", r.Ignored,
		"paginas", r.Pages,
		"attempts", r.Attempts,
		"extract_bytes", r.ExtractBytes,
		"bytes", r.Bytes,
		"table", r.Table,
		"estrategia", r.Strategy,
		"formato", r.Format,
		"dedup", r.Dedup,
		"tabela_criada", r.TableCreated,
		"extract", r.ExtractTime,
		"load", r.LoadTime,
		"duracao", r.Duration,
	}
}

func (r *Result) String() string {
	return fmt.Sprintf("%d records -> %d lines (%d ignored) em %s via %s, dedup %s, %s",
		r.Records, r.Rows, r.Ignored, r.Table, r.Strategy, r.Dedup, r.Duration)
}
