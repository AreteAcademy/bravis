// Package sdk is the front door: two calls to write a fetcher.
//
//	data, err := sdk.Extract(ctx, sdk.Source{
//		URL:      "https://api.open-meteo.com/v1/forecast?...",
//		Guard:   sdk.RejectIf("error"),
//		Expand: sdk.ParallelArrays("hourly", "time", "temperature_2m"),
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

	"github.com/AreteAcademy/bravis/sdk/extract"
	core "github.com/AreteAcademy/bravis/sdk/internal/core"
	"github.com/AreteAcademy/bravis/sdk/load"
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

// Extract fetches, decodes and, when Source.Expand is set, expands the
// response into one record per reading.
//
// The returned records carry only Payload. Provider, Entity, SourceKey and
// RecordTS are provenance, and provenance is decided at Load, where Target
// says how to derive it.
func Extract(ctx context.Context, source Source) (*Data, error) {
	switch source.Driver {
	case "", DriverHTTP:
		source.Driver = DriverHTTP
	default:
		return nil, fmt.Errorf("extract driver %q is not implemented; use %q", source.Driver, DriverHTTP)
	}

	if source.Format == "" {
		source.Format = FormatJSON
	}

	start := time.Now()

	// Filled in as the walk proceeds, read once the stream is drained. Load
	// copies it into Result, so Pages and Attempts describe what happened
	// rather than being zeroes nobody doubts.
	stats := &core.Stats{}
	source.Stats = stats

	var (
		lines iter.Seq2[Envelope, error]
		err   error
	)
	switch source.Format {
	case FormatJSON:
		lines, err = extract.JSON(ctx, source)
	case FormatNDJSON:
		lines, err = extract.NDJSON(ctx, source)
	case FormatCSV:
		lines, err = extract.CSV(ctx, source)
	case FormatXML:
		lines, err = extract.XML(ctx, source)
	default:
		return nil, fmt.Errorf("unknown format %q; use JSON, NDJSON, CSV or XML", source.Format)
	}
	if err != nil {
		return nil, classifyExtract(source, err)
	}

	if source.Expand != nil {
		lines = expandStream(source, lines)
	}

	return &Data{Records: lines, source: source, start: start, stats: stats}, nil
}

// expandStream applies the expansor to each decoded document, emitting one
// record per reading. It stays lazy: page N is not held waiting for page N+1.
func expandStream(source Source, lines iter.Seq2[Envelope, error]) iter.Seq2[Envelope, error] {
	return func(yield func(Envelope, error) bool) {
		doc := 0
		for env, err := range lines {
			if err != nil {
				if !yield(Envelope{}, classifyExtract(source, err)) {
					return
				}
				continue
			}

			records, err := source.Expand(env.Payload)
			if err != nil {
				yield(Envelope{}, &FormatError{
					URL:    redact(source.URL),
					Format: string(source.Format),
					Line:   doc,
					Cause:  err,
				})
				return
			}
			doc++

			for _, r := range records {
				if !yield(Envelope{Payload: r}, nil) {
					return
				}
			}
		}
	}
}

// Load stamps provenance on every record and writes them to BigQuery.
//
// It resolves configuration with the documented precedence, logs where each
// value came from, creates the landing table when absent, and reports what it
// actually did.
func Load(ctx context.Context, data *Data, target Target) (*Result, error) {
	start := time.Now()

	if data == nil {
		return nil, fmt.Errorf("Load got nil data: call Extract first")
	}

	cfg, origins, err := target.resolve()
	if err != nil {
		return nil, err
	}
	logResolution(ctx, origins)

	envelopes, err := collect(data, target)
	if err != nil {
		return nil, err
	}

	// Read after collect drained the stream: that is when the counters are
	// final.
	res := &Result{
		Records:     int64(len(envelopes)),
		ExtractTime: time.Since(data.start),
		Table:       fmt.Sprintf("%s.%s", cfg.Dataset, cfg.Table),
	}
	if data.stats != nil {
		res.Pages = data.stats.Pages
		res.Attempts = data.stats.Attempts
	}

	if len(envelopes) == 0 {
		res.Duration = time.Since(start)
		return res, nil
	}

	loadStart := time.Now()

	loader, err := load.New(ctx, cfg)
	if err != nil {
		return res, &TargetError{Table: res.Table, Cause: err}
	}

	lr, err := loader.Load(ctx, envelopes...)
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
func collect(data *Data, target Target) ([]Envelope, error) {
	when := target.When
	if when == nil {
		when = Now()
	}

	var envelopes []Envelope
	i := 0
	for env, err := range data.Records {
		if err != nil {
			return nil, err
		}

		key, err := target.Key(env.Payload)
		if err != nil {
			return nil, &FormatError{
				URL: redact(data.source.URL), Format: string(data.source.Format),
				Line: i, Cause: fmt.Errorf("building source_key: %w", err),
			}
		}

		ts, err := when(env.Payload)
		if err != nil {
			return nil, &FormatError{
				URL: redact(data.source.URL), Format: string(data.source.Format),
				Line: i, Cause: fmt.Errorf("reading record_ts: %w", err),
			}
		}

		env.Provider = target.Provider
		env.Entity = target.Entity
		env.SourceKey = key
		env.RecordTS = ts

		envelopes = append(envelopes, env)
		i++
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
func classifyExtract(source Source, err error) error {
	url := redact(source.URL)

	attempts := 1
	if source.RetryConfig != nil {
		attempts = source.RetryConfig.MaxAttempts
	}

	if status, ok := statusOf(err); ok {
		return &SourceError{URL: url, Status: status, Attempts: attempts, Cause: err}
	}
	if isTransport(err) {
		return &SourceError{URL: url, Attempts: attempts, Cause: err}
	}
	return &FormatError{URL: url, Format: string(source.Format), Line: -1, Cause: err}
}

var _ = slog.LevelInfo
