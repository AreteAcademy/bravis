package to

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	core "github.com/AreteAcademy/brevis/sdk/internal/core"
)

// Files writes records to files, on disk or in object storage.
//
//	to.Files{Path: "./saida/"}
//	to.Files{Path: "gs://bucket/landing/pedidos/", PartitionBy: "ingestion_loaded_at",
//	         Compress: true, Store: gcs.New(client)}
//
// The scheme in Path says which backend, and the Store is passed in rather
// than chosen here -- see core.Store.
//
// A batch becomes one object. Writing is atomic: on disk through a temporary
// file and a rename, in object storage through a single PUT. Nobody ever
// reads half a file.
type Files struct {
	// Path is the directory to write into. Required, and it is a directory:
	// the file name is this driver's to choose, because a batch is one object
	// and a second load must not overwrite the first.
	Path string

	// Format written. Empty means FormatNDJSON.
	//
	// Only NDJSON and CSV. Parquet would bring Arrow, and a fetcher that only
	// writes files should not pay for it.
	Format core.Format

	// PartitionBy names a column whose date becomes a path segment:
	// landing/dt=2026-09-04/parte-....ndjson. Empty writes flat.
	PartitionBy string

	// Compress writes .gz.
	Compress bool

	// Store is the object storage backend. Nil writes the local filesystem.
	Store core.Store
}

// Write satisfies core.Writer.
func (f Files) Write(ctx context.Context, records []core.Envelope, opt core.WriteOptions) (*core.LoadResult, error) {
	start := time.Now()

	if err := f.refuseUnsupported(opt); err != nil {
		return nil, err
	}

	loc, err := core.ParseLocation(f.Path)
	if err != nil {
		return nil, err
	}
	if err := checkStore(f.Path, loc, f.Store); err != nil {
		return nil, err
	}

	format := f.Format
	if format == "" {
		format = core.FormatNDJSON
	}

	if err := core.CheckColumns(opt.Columns, records); err != nil {
		return nil, err
	}

	data, err := encode(records, format)
	if err != nil {
		return nil, err
	}
	if f.Compress {
		if data, err = compress(data); err != nil {
			return nil, err
		}
	}

	key := f.name(loc, records, format)
	if err := f.put(ctx, loc, key, data); err != nil {
		return nil, err
	}

	return &core.LoadResult{
		RowsLoaded:  int64(len(records)),
		BytesStaged: int64(len(data)),
		Duration:    time.Since(start),
		Strategy:    "file",
		Format:      string(format),
		Dedup:       core.DedupNone,
	}, nil
}

// Describe satisfies core.Writer.
func (f Files) Describe() string { return f.Path }

// refuseUnsupported says no to what a directory cannot do, naming the option
// and the driver. A flag that is quietly ignored is worse than an error.
func (f Files) refuseUnsupported(opt core.WriteOptions) error {
	if opt.Dedup == core.DedupMerge {
		return fmt.Errorf("to.Files cannot deduplicate: a directory has no key to match on. " +
			"Drop Dedup, or write to a destination that has one")
	}
	switch f.Format {
	case "", core.FormatNDJSON, core.FormatCSV:
	default:
		return fmt.Errorf("to.Files writes NDJSON or CSV; %q is not supported", f.Format)
	}
	return nil
}

// name is the object this batch becomes.
//
// The timestamp keeps a second load from overwriting the first: a directory
// has no notion of "the same rows again", so every batch is its own file and
// what to do with duplicates is decided downstream.
func (f Files) name(loc core.Location, records []core.Envelope, format core.Format) string {
	ext := "." + string(format)
	if f.Compress {
		ext += ".gz"
	}

	parts := []string{strings.TrimSuffix(loc.Prefix, "/")}
	if seg := f.partition(records); seg != "" {
		parts = append(parts, seg)
	}
	parts = append(parts, fmt.Sprintf("parte-%d%s", time.Now().UTC().UnixNano(), ext))

	var kept []string
	for _, p := range parts {
		if p != "" {
			kept = append(kept, p)
		}
	}
	if loc.Scheme == "" {
		return filepath.Join(kept...)
	}
	return strings.Join(kept, "/")
}

// partition reads the date out of PartitionBy on the first record.
func (f Files) partition(records []core.Envelope) string {
	if f.PartitionBy == "" || len(records) == 0 {
		return ""
	}
	row, ok := records[0].Payload.(map[string]any)
	if !ok {
		return ""
	}
	v, ok := row[f.PartitionBy].(string)
	if !ok || len(v) < 10 {
		return ""
	}
	return f.PartitionBy + "=" + v[:10]
}

func (f Files) put(ctx context.Context, loc core.Location, key string, data []byte) error {
	if loc.Scheme != "" {
		// A single PUT is atomic, so nobody reads a partial object.
		if err := f.Store.Create(ctx, loc.Bucket, key, bytes.NewReader(data)); err != nil {
			return fmt.Errorf("writing %s://%s/%s: %w", loc.Scheme, loc.Bucket, key, err)
		}
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(key), 0o750); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(key), err)
	}

	// Temporary file then rename: on the same filesystem a rename is atomic,
	// so a reader watching the directory never sees a half-written file.
	tmp, err := os.CreateTemp(filepath.Dir(key), ".brevis-*")
	if err != nil {
		return fmt.Errorf("creating a temporary file: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing %s: %w", key, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", key, err)
	}
	if err := os.Rename(tmp.Name(), key); err != nil {
		return fmt.Errorf("renaming into %s: %w", key, err)
	}
	return nil
}

// encode turns the batch into the bytes that land.
func encode(records []core.Envelope, format core.Format) ([]byte, error) {
	switch format {
	case core.FormatCSV:
		return encodeCSV(records)
	default:
		return encodeNDJSON(records)
	}
}

func encodeNDJSON(records []core.Envelope) ([]byte, error) {
	var buf bytes.Buffer
	for i, env := range records {
		data, err := json.Marshal(env.Payload)
		if err != nil {
			return nil, fmt.Errorf("record %d: %w", i, err)
		}
		buf.Write(data)
		buf.WriteByte('\n')
	}
	return buf.Bytes(), nil
}

// encodeCSV writes the union of every record's fields, sorted, so a record
// that omits one still lines up under the right header.
func encodeCSV(records []core.Envelope) ([]byte, error) {
	seen := map[string]bool{}
	rows := make([]map[string]any, len(records))
	for i, env := range records {
		row, err := core.AsObject(env.Payload)
		if err != nil {
			return nil, fmt.Errorf("record %d: %w", i, err)
		}
		rows[i] = row
		for k := range row {
			seen[k] = true
		}
	}

	cols := make([]string, 0, len(seen))
	for k := range seen {
		cols = append(cols, k)
	}
	sort.Strings(cols)

	var buf bytes.Buffer
	w := csvWriter{&buf}
	w.row(cols)
	for _, row := range rows {
		cells := make([]string, len(cols))
		for j, c := range cols {
			cells[j] = cell(row[c])
		}
		w.row(cells)
	}
	return buf.Bytes(), nil
}

func compress(data []byte) ([]byte, error) {
	var out bytes.Buffer
	w := gzip.NewWriter(&out)
	if _, err := w.Write(data); err != nil {
		return nil, fmt.Errorf("compressing: %w", err)
	}
	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("compressing: %w", err)
	}
	return out.Bytes(), nil
}

// checkStore refuses a path the backend cannot serve, naming both.
func checkStore(path string, loc core.Location, store core.Store) error {
	switch {
	case loc.Scheme == "" && store != nil:
		return fmt.Errorf("the Path %q is a local path, but a %s Store was given; "+
			"drop the Store, or point Path at %s://", path, store.Scheme(), store.Scheme())
	case loc.Scheme != "" && store == nil:
		return fmt.Errorf("the Path %q needs a %s Store; pass one, "+
			"for example Store: %s.New(...)", path, loc.Scheme, loc.Scheme)
	case loc.Scheme != "" && store.Scheme() != loc.Scheme:
		return fmt.Errorf("the Path %q is %s, but the Store serves %s",
			path, loc.Scheme, store.Scheme())
	}
	return nil
}

type csvWriter struct{ w io.Writer }

func (c csvWriter) row(cells []string) {
	for i, cell := range cells {
		if i > 0 {
			_, _ = io.WriteString(c.w, ",")
		}
		if strings.ContainsAny(cell, `",`+"\n") {
			_, _ = io.WriteString(c.w, `"`+strings.ReplaceAll(cell, `"`, `""`)+`"`)
			continue
		}
		_, _ = io.WriteString(c.w, cell)
	}
	_, _ = io.WriteString(c.w, "\n")
}

func cell(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	default:
		data, err := json.Marshal(t)
		if err != nil {
			return fmt.Sprint(t)
		}
		return strings.Trim(string(data), `"`)
	}
}
