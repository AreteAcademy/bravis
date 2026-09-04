// Package from holds the sources a pipeline reads.
//
// One type per origin, each carrying its own configuration and knowing how to
// read itself. Importing this package costs you the HTTP driver and nothing
// else -- Go prunes by package, so a fetcher that reads from Postgres never
// compiles the BigQuery client.
package from

import (
	"context"
	"io"
	"iter"
	"time"

	"github.com/AreteAcademy/bravis/sdk/extract"
	core "github.com/AreteAcademy/bravis/sdk/internal/core"
)

// HTTP reads from an HTTP API.
//
//	From: from.HTTP{
//		URL:     "https://api.open-meteo.com/v1/forecast?...",
//		Timeout: 15 * time.Second,
//		Records: func(r sdk.Response) ([]any, error) { ... },
//	}
//
// Everything a fetcher needs to say about an HTTP source is here: where, how
// to ask, how to survive a flaky upstream, how to page, and what a response
// means. A source that is not HTTP has none of these fields, which is the
// point of the type existing.
type HTTP struct {
	URL          string    // required
	Method       string    // default: GET
	Body         io.Reader // for POST/PUT
	Header       map[string][]string
	Timeout      time.Duration     // per attempt; default: 30s
	TotalTimeout time.Duration     // total; default: 5 minutes
	RetryConfig  *core.RetryConfig // nil uses defaults
	RateLimiter  core.Limiter      // throttles each attempt; nil disables

	// Format of the response. Empty means FormatJSON.
	Format core.Format

	// Records decides what each successful response means -- the records it
	// carries, or a refusal saying why. Nil decodes the body and treats each
	// document as one record. See core.Reading.
	//
	// It lives here, with the rest of what describes an HTTP source, because
	// a Postgres source has no Response to be handed one.
	Records core.Reading

	// NoHeader, for CSV: treat every row as data with field_N keys.
	NoHeader bool

	// Pagination. At most one strategy applies, checked in this order:
	// FollowLinks, CursorKey, OffsetKey. MaxPages caps the walk.
	FollowLinks bool
	CursorKey   string
	OffsetKey   string
	DataKey     string
	PageSize    int
	MaxPages    int
}

// Read satisfies core.Reader.
func (h HTTP) Read(ctx context.Context, opt core.ReadOptions) (iter.Seq2[core.Envelope, error], error) {
	source := h.source(opt)

	switch source.Format {
	case "", core.FormatJSON:
		source.Format = core.FormatJSON
		return extract.JSON(ctx, source, h.Records)
	case core.FormatNDJSON:
		return extract.NDJSON(ctx, source, h.Records)
	case core.FormatCSV:
		return extract.CSV(ctx, source, h.Records)
	case core.FormatXML:
		return extract.XML(ctx, source, h.Records)
	default:
		return nil, core.Reject("unknown format %q; use JSON, NDJSON, CSV or XML", source.Format)
	}
}

// Describe satisfies core.Reader, with the query string's secrets redacted.
func (h HTTP) Describe() string { return extract.Redact(h.URL) }

// source folds the driver's fields and the cross-cutting options into the one
// struct the extract package takes.
func (h HTTP) source(opt core.ReadOptions) core.Source {
	return core.Source{
		URL:           h.URL,
		Method:        h.Method,
		Body:          h.Body,
		Header:        h.Header,
		Timeout:       h.Timeout,
		TotalTimeout:  h.TotalTimeout,
		RetryConfig:   h.RetryConfig,
		RateLimiter:   h.RateLimiter,
		Format:        h.Format,
		NoHeader:      h.NoHeader,
		FollowLinks:   h.FollowLinks,
		CursorKey:     h.CursorKey,
		OffsetKey:     h.OffsetKey,
		DataKey:       h.DataKey,
		PageSize:      h.PageSize,
		MaxPages:      h.MaxPages,
		Stats:         opt.Stats,
		Preview:       opt.Preview,
		PreviewBytes:  opt.PreviewBytes,
		PreviewWriter: opt.PreviewWriter,
	}
}
