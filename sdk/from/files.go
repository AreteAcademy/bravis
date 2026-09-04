package from

import (
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"iter"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/AreteAcademy/bravis/sdk/extract"
	core "github.com/AreteAcademy/bravis/sdk/internal/core"
)

// Files reads records out of files, on disk or in object storage.
//
//	from.Files{Path: "./entrada/*.csv", Format: sdk.FormatCSV}
//	from.Files{Path: "s3://bucket/dia=2026-09-04/*.ndjson.gz", Store: s3.New(cfg)}
//
// The scheme in Path says which backend: none or file:// is the local
// filesystem, s3:// and gs:// need the matching Store. The Store is passed in
// rather than chosen here so that reading a local CSV does not compile the AWS
// SDK -- see core.Store.
//
// Files are read in sorted order, always. Two runs over the same prefix
// produce the same sequence, which a positional Key depends on: without it the
// ingestion_id of a record would change between runs.
type Files struct {
	// Path is the directory, the glob, or the single object to read.
	// Required.
	Path string

	// Format of the file contents. Empty means FormatNDJSON, which is what a
	// file of records usually is.
	Format core.Format

	// NoHeader, for CSV: treat every row as data with field_N keys. The
	// default uses the first row as column names.
	NoHeader bool

	// Store is the object storage backend. Nil reads the local filesystem.
	// A path whose scheme the Store does not serve is an error naming both.
	Store core.Store
}

// Read satisfies core.Reader.
func (f Files) Read(ctx context.Context, opt core.ReadOptions) (iter.Seq2[core.Envelope, error], error) {
	loc, err := core.ParseLocation(f.Path)
	if err != nil {
		return nil, err
	}
	if err := f.checkStore(loc); err != nil {
		return nil, err
	}

	format := f.Format
	if format == "" {
		format = core.FormatNDJSON
	}

	// Listed eagerly, so a prefix that does not exist fails here rather than
	// as the first item of a sequence the caller has to drain to notice.
	keys, err := f.list(ctx, loc)
	if err != nil {
		return nil, err
	}

	start := time.Now()
	stats := opt.Stats
	if stats == nil {
		stats = &core.Stats{}
	}

	return func(yield func(core.Envelope, error) bool) {
		var (
			rows   int
			sample []any
		)

		defer func() {
			if opt.Preview > 0 {
				w := opt.PreviewWriter
				if w == nil {
					w = os.Stderr
				}
				_, _ = io.WriteString(w, core.RenderPreview(sample, opt.PreviewBytes, core.PreviewStats{
					Rows: rows, Pages: stats.Pages, Bytes: stats.Bytes,
					Duration: time.Since(start),
				}))
			}
		}()

		for _, key := range keys {
			n, err := f.drain(ctx, loc, key, format, stats, func(env core.Envelope) bool {
				rows++
				if opt.Preview > 0 && len(sample) < opt.Preview {
					sample = append(sample, env.Payload)
				}
				return yield(env, nil)
			})
			stats.Pages++
			if err != nil {
				if !isStopped(err) {
					yield(core.Envelope{}, fmt.Errorf("%s: %w", key, err))
				}
				return
			}
			_ = n
		}
	}, nil
}

// Describe satisfies core.Reader.
func (f Files) Describe() string { return f.Path }

// checkStore refuses a path the backend cannot serve, naming both -- an s3://
// path with no Store would otherwise look for a local directory called
// "bucket" and report a confusing "no such file".
func (f Files) checkStore(loc core.Location) error {
	switch {
	case loc.Scheme == "" && f.Store != nil:
		return fmt.Errorf("the Path %q is a local path, but a %s Store was given; "+
			"drop the Store, or point Path at %s://", f.Path, f.Store.Scheme(), f.Store.Scheme())
	case loc.Scheme != "" && f.Store == nil:
		return fmt.Errorf("the Path %q needs a %s Store; pass one, "+
			"for example Store: %s.New(...)", f.Path, loc.Scheme, loc.Scheme)
	case loc.Scheme != "" && f.Store.Scheme() != loc.Scheme:
		return fmt.Errorf("the Path %q is %s, but the Store serves %s",
			f.Path, loc.Scheme, f.Store.Scheme())
	}
	return nil
}

// list finds every file the path names, sorted.
func (f Files) list(ctx context.Context, loc core.Location) ([]string, error) {
	if loc.Scheme != "" {
		keys, err := f.Store.List(ctx, loc.Bucket, loc.Prefix)
		if err != nil {
			return nil, fmt.Errorf("listing %s: %w", loc, err)
		}
		return filter(keys, loc), nil
	}

	entries, err := os.ReadDir(dirOf(loc))
	if err != nil {
		return nil, fmt.Errorf("listing %s: %w", loc, err)
	}
	var keys []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		keys = append(keys, filepath.Join(dirOf(loc), e.Name()))
	}
	sort.Strings(keys)

	// The glob applies to the base name, the same way it does for a key.
	var out []string
	for _, k := range keys {
		if loc.Pattern == "" || loc.Matches(k) {
			out = append(out, k)
		}
	}
	return out, nil
}

func dirOf(loc core.Location) string {
	if loc.Prefix == "" {
		return "."
	}
	return loc.Prefix
}

func filter(keys []string, loc core.Location) []string {
	var out []string
	for _, k := range keys {
		if loc.Matches(k) {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// drain decodes one file, yielding every record it holds.
func (f Files) drain(ctx context.Context, loc core.Location, key string,
	format core.Format, stats *core.Stats, emit func(core.Envelope) bool) (int, error) {

	rc, err := f.open(ctx, loc, key)
	if err != nil {
		return 0, err
	}
	defer func() { _ = rc.Close() }()

	var r io.Reader = &countingReader{r: rc, stats: stats}

	// Compression by extension. A .gz that is not gzip fails here, naming the
	// file, rather than as a decode error about invalid JSON.
	if strings.HasSuffix(key, ".gz") {
		gz, err := gzip.NewReader(r)
		if err != nil {
			return 0, fmt.Errorf("not gzip: %w", err)
		}
		defer func() { _ = gz.Close() }()
		r = gz
	}

	decoder := extract.NewDecoder(r, core.Source{Format: format, NoHeader: f.NoHeader})
	if decoder == nil {
		return 0, fmt.Errorf("unsupported format %q", format)
	}

	n := 0
	for {
		env, err := decoder.Next(ctx)
		if err == io.EOF {
			return n, nil
		}
		if err != nil {
			return n, err
		}
		n++
		if !emit(env) {
			return n, errStopped
		}
	}
}

func (f Files) open(ctx context.Context, loc core.Location, key string) (io.ReadCloser, error) {
	if loc.Scheme != "" {
		return f.Store.Open(ctx, loc.Bucket, key)
	}
	return os.Open(key) //nolint:gosec // the path is the fetcher's own
}

// errStopped signals that the consumer broke out of the range loop.
var errStopped = fmt.Errorf("consumer stopped iterating")

func isStopped(err error) bool { return err == errStopped }

// countingReader tallies what was actually read, for Stats.Bytes.
type countingReader struct {
	r     io.Reader
	stats *core.Stats
}

func (c *countingReader) Read(b []byte) (int, error) {
	n, err := c.r.Read(b)
	c.stats.Bytes += int64(n)
	return n, err
}
