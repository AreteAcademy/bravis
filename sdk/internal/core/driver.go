package core

import (
	"context"
	"io"
	"iter"
)

// Reader produces records. One implementation per origin: HTTP today,
// Postgres, MySQL and files next.
//
// The driver is the value, not an enum: from.HTTP carries a URL and a Reading,
// from.Postgres will carry a DSN and a query. Neither has to make room for the
// other's fields, which is what keeps a source struct from collecting forty
// options of which any one driver reads six.
//
// It also decides what a consumer compiles. Go prunes dependencies by package
// imported, never by field used -- so a fetcher that never imports the
// BigQuery destination never builds it.
type Reader interface {
	// Read opens the source and yields its records, lazily. The sequence must
	// stay lazy: a driver that materialises the whole source before returning
	// puts a 5 GB export in memory.
	Read(ctx context.Context, opt ReadOptions) (iter.Seq2[Envelope, error], error)

	// Describe names the origin for logs and errors, with any secret
	// redacted. "http://api.example.com/v1/events", "postgres://host/db#pedidos".
	Describe() string
}

// ReadOptions is what every source honours, whatever it reads from.
type ReadOptions struct {
	// Preview prints the first N records once the read finishes. See the
	// Preview fields on Source.
	Preview       int
	PreviewBytes  int
	PreviewWriter io.Writer

	// Stats, when not nil, is filled in as the read proceeds.
	Stats *Stats

	// Run is what the engine knows about this execution. A source that reads
	// incrementally takes its window from here.
	Run RunContext
}

// Writer consumes records. One implementation per destination.
//
// It receives records with provenance already resolved -- provider, entity,
// It receives records exactly as the Transform chain composed them --
// ingestion_id included, when the fetcher asked for it -- so no driver reads
// the caller's record to work out what identifies it.
type Writer interface {
	// Write sends the batch and reports what actually happened.
	Write(ctx context.Context, records []Envelope, opt WriteOptions) (*LoadResult, error)

	// Describe names the destination for logs and errors: "bronze.pedidos".
	Describe() string
}

// WriteOptions is what every destination honours, whatever it writes to.
type WriteOptions struct {
	// Columns declares the destination's columns, in DDL order, including the
	// two the ingestion transformers write. Nil declares nothing. See
	// sdk.Target.Columns.
	Columns []string

	// Dedup selects deduplication. What it costs, and whether it is supported
	// at all, is the driver's to say -- a directory of files has no key to
	// match on, and saying so is better than ignoring the option.
	Dedup Dedup

	// Run is what the engine knows about this execution. A destination that
	// creates its table on the first run reads that from here.
	Run RunContext
}
