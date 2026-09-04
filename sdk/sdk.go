// Package sdk is the front door: two calls to write a fetcher.
//
//	data, err := sdk.Extract(ctx, sdk.Source{
//		URL:      "https://api.open-meteo.com/v1/forecast?...",
//		Records: func(r sdk.Response) ([]any, error) {
//			doc, err := r.Object()
//			if err != nil {
//				return nil, err
//			}
//			return sdk.ParallelArrays("hourly", "time", "temperature_2m")(doc)
//		},
//	})
//
//	res, err := sdk.Load(ctx, data, sdk.Target{
//		Provider: "open_meteo",
//		Entity:   "hourly_temperature",
//		Key:    sdk.Key("latitude", "longitude", "time"),
//		When:   sdk.Field("time"),
//	})
//
// Everything between those two calls that is not specific to the vendor lives
// in here: config, retry, pagination, expansion, provenance, table creation,
// deduplication and the result you log.
//
// The lower-level packages stay available and unchanged. Reach for
// sdk/extract and sdk/load directly when you need a shape these two calls do
// not cover -- the hard case has to stay possible.
package sdk

import (
	"context"
	"fmt"
	"iter"
	"log/slog"
	"time"

	core "github.com/AreteAcademy/brevis/sdk/internal/core"
)

// Data is a stream of records with the statistics of the fetch that produced
// them. It is an iterator, not a slice: a paginated source must not have to
// fit in memory before the first record can be used.
type Data struct {
	Records iter.Seq2[Envelope, error]

	source Source
	start  time.Time
	stats  *core.Stats
}

// Stats reports what the fetch actually did: pages walked and HTTP attempts
// spent, retries included. Attempts above Pages means the source was flaky.
//
// The counters are written as the stream is pulled, so read this after the
// iteration ends. Before that it reports the walk so far, which is only the
// first page.
//
// Load copies these into Result, so a pipeline that loads does not need this.
// It is here for the cases that do not: a dry run, a validation pass, or an
// extract feeding something other than Load.
func (d *Data) Stats() core.Stats {
	if d == nil || d.stats == nil {
		return core.Stats{}
	}
	return *d.stats
}

// Extract fetches and decodes. When Source.Records is set the fetcher decides
// what each response holds; otherwise each decoded document is one record.
//
// The returned records carry only Payload. Provider, Entity, SourceKey and
// RecordTS are provenance, and provenance is decided at Load, where Target
// says how to derive it.
func Extract(ctx context.Context, source Source) (*Data, error) {
	if err := source.validate(); err != nil {
		return nil, err
	}

	start := time.Now()

	// The counters are the driver's to fill, and Result copies them, so
	// Pages and Attempts describe what happened rather than being zero.
	stats := source.Stats
	if stats == nil {
		stats = &core.Stats{}
	}
	opt := source.options(runContextFromEnv())
	opt.Stats = stats

	lines, err := source.From.Read(ctx, opt)
	if err != nil {
		return nil, classifyExtract(source.From.Describe(), err)
	}

	return &Data{Records: lines, source: source, start: start, stats: stats}, nil
}

// Load stamps provenance on every record and writes them to BigQuery.
//
// It resolves configuration with the documented precedence, logs where each
// value came from, creates the landing table when absent, and reports what it
// actually did.
func Load(ctx context.Context, data *Data, target Target) (*Result, error) {
	return loadWith(ctx, data, target, runContextFromEnv())
}

// loadWith is Load with the engine context already read, so a test can supply
// one without touching the process environment.
func loadWith(ctx context.Context, data *Data, target Target, run RunContext) (*Result, error) {
	start := time.Now()

	if data == nil {
		return nil, fmt.Errorf("Load got nil data: call Extract first")
	}
	if err := target.validate(); err != nil {
		return nil, err
	}

	envelopes, err := collect(data, target)
	if err != nil {
		return nil, err
	}

	// Read after collect drained the stream: that is when the counters are
	// final.
	res := &Result{
		Records:     int64(len(envelopes)),
		ExtractTime: time.Since(data.start),
		Table:       target.To.Describe(),
	}
	if data.stats != nil {
		res.Pages = data.stats.Pages
		res.Attempts = data.stats.Attempts
		res.ExtractBytes = data.stats.Bytes
	}

	if len(envelopes) == 0 {
		res.Duration = time.Since(start)
		return res, nil
	}

	loadStart := time.Now()
	lr, err := target.To.Write(ctx, envelopes, target.options(run))
	res.LoadTime = time.Since(loadStart)
	res.Duration = time.Since(start)

	if lr != nil {
		apply(res, lr)
	}
	if err != nil {
		return res, &TargetError{Table: res.Table, Rows: res.RowErrors, Cause: err}
	}

	return res, nil
}

// collect drains the stream, stamping provenance from Target onto each
// record. Load needs the batch in hand to choose a strategy and to size the
// staged file, so this is where streaming ends.
// collect drains the stream into envelopes.
//
// It does nothing else. Everything the row carries was composed in Transform,
// including ingestion_id and ingestion_loaded_at when the fetcher asked for
// them -- so there is no step here that reads a field out of your record, and
// no way for one to fail.
func collect(data *Data, _ Target) ([]Envelope, error) {
	var envelopes []Envelope
	for env, err := range data.Records {
		if err != nil {
			return nil, err
		}
		envelopes = append(envelopes, env)
	}
	return envelopes, nil
}

func apply(res *Result, lr *core.LoadResult) {
	res.Rows = lr.RowsLoaded
	res.Ignored = lr.RowsIgnored
	res.Bytes = lr.BytesStaged
	res.Strategy = lr.Strategy
	res.Format = lr.Format
	res.Dedup = lr.Dedup
	res.TableCreated = lr.TableCreated
	res.RowErrors = lr.ErrorRows
}

// classifyExtract turns a transport or decode failure into the typed error
// that says which action it calls for.
//
// The driver no longer tells us how many attempts it spent -- Stats does, and
// it is read where it is final. What stays here is the classification, which
// is what decides whether the answer is "retry later" or "fix the mapping".
func classifyExtract(source string, err error) error {
	if status, ok := statusOf(err); ok {
		return &SourceError{URL: source, Status: status, Cause: err}
	}
	if isTransport(err) {
		return &SourceError{URL: source, Cause: err}
	}
	return &FormatError{URL: source, Line: -1, Cause: err}
}

var _ = slog.LevelInfo
