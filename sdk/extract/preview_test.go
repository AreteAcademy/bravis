package extract

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	core "github.com/AreteAcademy/bravis/sdk/internal/core"
)

func rows() []any {
	return []any{
		map[string]any{"time": "2026-01-01T00:00", "temperature_2m": 12.4, "is_day": true},
		map[string]any{"time": "2026-01-01T01:00", "temperature_2m": 12.15, "is_day": true},
		map[string]any{"time": "2026-01-01T02:00", "temperature_2m": -3.8, "is_day": false},
	}
}

func TestPreviewLaysRecordsOutAsATable(t *testing.T) {
	out := renderPreview(rows(), 4096, previewStats{Rows: 3, Pages: 1, Bytes: 512, Duration: 90 * time.Millisecond})

	// The header names the columns, sorted, and every row gets an index.
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if !strings.Contains(lines[0], "is_day") || !strings.Contains(lines[0], "temperature_2m") {
		t.Errorf("header does not name the columns: %q", lines[0])
	}
	for i, want := range []string{"0", "1", "2"} {
		if !strings.HasPrefix(lines[i+1], want) {
			t.Errorf("row %d is not indexed: %q", i, lines[i+1])
		}
	}
	if !strings.Contains(out, "2026-01-01T02:00") {
		t.Error("the third record never made it into the table")
	}
}

// Columns come from a Go map, which has no order to preserve. Sorting is what
// makes two runs of the same extract comparable.
func TestPreviewColumnsAreStableAcrossRuns(t *testing.T) {
	first := renderPreview(rows(), 4096, previewStats{Rows: 3, Pages: 1})
	for i := 0; i < 20; i++ {
		if got := renderPreview(rows(), 4096, previewStats{Rows: 3, Pages: 1}); got != first {
			t.Fatalf("the preview reshuffled itself between runs:\n%s\nvs\n%s", first, got)
		}
	}
}

// The budget is the whole point of the feature: a preview must not become the
// thing that floods the log.
func TestPreviewHonoursTheByteBudget(t *testing.T) {
	out := renderPreview(rows(), 120, previewStats{Rows: 3, Pages: 1, Bytes: 512})

	if len(out) > 220 { // the block plus its footer, which is never dropped
		t.Errorf("a 120-byte budget produced %d bytes:\n%s", len(out), out)
	}
	if !strings.Contains(out, "of 3 rows") {
		t.Errorf("the footer must say the sample was cut short:\n%s", out)
	}
	if strings.Contains(out, "2026-01-01T02:00") {
		t.Errorf("a row past the budget was printed anyway:\n%s", out)
	}
}

// Cutting to nothing would be worse than printing too much: the caller asked
// to see the shape, and one row still shows it.
func TestPreviewAlwaysShowsAtLeastOneRow(t *testing.T) {
	out := renderPreview(rows(), 1, previewStats{Rows: 3, Pages: 1})
	if !strings.Contains(out, "2026-01-01T00:00") {
		t.Errorf("an impossible budget printed no data at all:\n%s", out)
	}
	if !strings.Contains(out, "1 of 3 rows") {
		t.Errorf("the footer must own up to showing one row:\n%s", out)
	}
}

func TestPreviewReportsTheTotalsNotTheSample(t *testing.T) {
	out := renderPreview(rows(), 4096, previewStats{
		Rows: 168, Pages: 4, Bytes: 34821, Duration: 412 * time.Millisecond,
	})

	for _, want := range []string{"3 of 168 rows", "34.8 kB", "4 pages in 412ms", "103ms/page"} {
		if !strings.Contains(out, want) {
			t.Errorf("the footer is missing %q:\n%s", want, out)
		}
	}

	// A sub-millisecond average must not round to "0s", which reads as
	// "not measured". Asserted separately because 412ms/4 lands on a whole
	// millisecond either way, so it cannot tell the two roundings apart.
	fast := renderPreview(rows(), 4096, previewStats{Rows: 6, Pages: 3, Duration: 262 * time.Microsecond})
	if strings.Contains(fast, "0s/page") {
		t.Errorf("a fast extract reported 0s per page:\n%s", fast)
	}
	if !strings.Contains(fast, "87µs/page") {
		t.Errorf("the per-page average is wrong:\n%s", fast)
	}
}

// One page has no average worth printing -- it is the duration again.
func TestPreviewOmitsThePerPageAverageForASinglePage(t *testing.T) {
	out := renderPreview(rows(), 4096, previewStats{Rows: 3, Pages: 1, Duration: 90 * time.Millisecond})
	if strings.Contains(out, "/page") {
		t.Errorf("a single page does not need an average:\n%s", out)
	}
}

func TestPreviewSaysSoWhenThereAreNoRows(t *testing.T) {
	out := renderPreview(nil, 4096, previewStats{Rows: 0, Pages: 1, Bytes: 18})
	if !strings.Contains(out, "no rows") {
		t.Errorf("an empty extract must say it was empty:\n%s", out)
	}
}

// A record that is not an object still has a shape worth seeing.
func TestPreviewRendersScalarRecords(t *testing.T) {
	out := renderPreview([]any{"alfa", "beta"}, 4096, previewStats{Rows: 2, Pages: 1})
	if !strings.Contains(out, "value") || !strings.Contains(out, "alfa") {
		t.Errorf("scalar records did not render:\n%s", out)
	}
}

// A blob in one field must not push every other column off the screen.
func TestPreviewTruncatesAWideCell(t *testing.T) {
	long := strings.Repeat("x", 500)
	out := renderPreview([]any{map[string]any{"blob": long, "id": 1.0}}, 4096, previewStats{Rows: 1, Pages: 1})

	if strings.Contains(out, long) {
		t.Error("a 500-character value was printed in full")
	}
	if !strings.Contains(out, "…") {
		t.Errorf("a truncated cell must show it was truncated:\n%s", out)
	}
	if !strings.Contains(out, "id") {
		t.Errorf("the wide cell pushed another column out:\n%s", out)
	}
}

func TestPreviewElidesColumnsPastTheLineWidthAndSaysHowMany(t *testing.T) {
	wide := map[string]any{}
	for i := 0; i < 40; i++ {
		wide[fmt.Sprintf("column_number_%02d", i)] = i
	}
	out := renderPreview([]any{wide}, 8192, previewStats{Rows: 1, Pages: 1})

	for _, line := range strings.Split(out, "\n") {
		if len([]rune(line)) > maxLineWidth+maxCellWidth {
			t.Errorf("a line ran to %d characters:\n%s", len([]rune(line)), line)
		}
	}
	if !strings.Contains(out, "not shown") {
		t.Errorf("columns were dropped without saying so:\n%s", out)
	}
}

// A newline inside a value would break the table into rows that are not rows.
func TestPreviewEscapesNewlinesInsideAValue(t *testing.T) {
	out := renderPreview([]any{map[string]any{"note": "linha um\nlinha dois"}}, 4096, previewStats{Rows: 1, Pages: 1})

	body := strings.SplitN(out, "\n\n", 2)[0]
	if n := len(strings.Split(body, "\n")); n != 2 {
		t.Errorf("a value's newline added table rows: got %d lines\n%s", n, out)
	}
	if !strings.Contains(out, `\n`) {
		t.Errorf("the newline was not escaped:\n%s", out)
	}
}

func TestHumanBytes(t *testing.T) {
	for _, tc := range []struct {
		in   int64
		want string
	}{
		{0, "0 B"}, {999, "999 B"}, {1000, "1.0 kB"}, {34821, "34.8 kB"},
		{1500000, "1.5 MB"}, {2500000000, "2.5 GB"},
	} {
		if got := humanBytes(tc.in); got != tc.want {
			t.Errorf("humanBytes(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// --- the preview wired into a real extract ---

func TestExtractPrintsThePreviewItWasAskedFor(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `[{"id":1,"nome":"alfa"},{"id":2,"nome":"beta"},{"id":3,"nome":"gama"}]`)
	}))
	defer srv.Close()

	var out bytes.Buffer
	stats := &core.Stats{}
	records, err := JSON(context.Background(), core.Source{
		URL:           srv.URL,
		Preview:       2,
		PreviewWriter: &out,
		Stats:         stats,
	}, nil)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}

	got := 0
	for range records {
		got++
	}
	if got != 3 {
		t.Fatalf("the consumer received %d records, expected 3", got)
	}

	block := out.String()
	if block == "" {
		t.Fatal("Preview was set and nothing was printed")
	}
	if !strings.Contains(block, "nome") || !strings.Contains(block, "alfa") {
		t.Errorf("the preview does not show the data:\n%s", block)
	}
	// Two sampled out of three seen -- the footer reports the total, not the
	// sample, or it would understate what the extract pulled.
	if !strings.Contains(block, "2 of 3 rows") {
		t.Errorf("the footer misreports the sample:\n%s", block)
	}
	if strings.Contains(block, "gama") {
		t.Errorf("Preview: 2 printed a third record:\n%s", block)
	}
}

// The bytes have to be the ones that came off the wire.
func TestExtractCountsTheBytesItRead(t *testing.T) {
	body := `[{"id":1},{"id":2}]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()

	stats := &core.Stats{}
	records, err := JSON(context.Background(), core.Source{URL: srv.URL, Stats: stats}, nil)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	for range records {
	}

	if stats.Bytes != int64(len(body)) {
		t.Errorf("Stats.Bytes = %d, expected the %d bytes the server sent", stats.Bytes, len(body))
	}
}

// The Records path buffers the body with io.ReadAll, which is a second way
// to read it -- and a counter that only wraps one of the two would report zero.
func TestExtractCountsBytesOnTheBufferedPath(t *testing.T) {
	body := `[{"id":1},{"id":2}]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()

	stats := &core.Stats{}
	records, err := JSON(context.Background(), core.Source{
		URL:   srv.URL,
		Stats: stats,
	}, func(r core.Response) ([]any, error) {
		var docs []any
		if err := r.JSON(&docs); err != nil {
			return nil, err
		}
		return docs, nil
	})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	for range records {
	}

	if stats.Bytes != int64(len(body)) {
		t.Errorf("Stats.Bytes = %d on the buffered path, expected %d", stats.Bytes, len(body))
	}
}

// Breaking out early is exactly when someone wants to see what did arrive.
func TestPreviewStillPrintsWhenTheConsumerStopsEarly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `[{"id":1},{"id":2},{"id":3}]`)
	}))
	defer srv.Close()

	var out bytes.Buffer
	records, err := JSON(context.Background(), core.Source{
		URL: srv.URL, Preview: 5, PreviewWriter: &out,
	}, nil)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}

	for range records {
		break
	}

	if !strings.Contains(out.String(), "id") {
		t.Errorf("nothing was printed after an early break:\n%q", out.String())
	}
}

// Off by default: nobody gets a table they did not ask for.
func TestNoPreviewByDefault(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `[{"id":1}]`)
	}))
	defer srv.Close()

	var out bytes.Buffer
	records, err := JSON(context.Background(), core.Source{URL: srv.URL, PreviewWriter: &out}, nil)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	for range records {
	}

	if out.Len() != 0 {
		t.Errorf("a preview was printed without being asked for:\n%s", out.String())
	}
}

// "0s" reads as "not measured" rather than "quick", which is how a fast
// extract used to be reported.
func TestRoundDurationStaysReadableAtEveryScale(t *testing.T) {
	for _, tc := range []struct {
		in   time.Duration
		want string
	}{
		{20458 * time.Nanosecond, "20µs"},
		{412 * time.Millisecond, "412ms"},
		{2*time.Second + 3*time.Millisecond, "2s"},
		{900 * time.Nanosecond, "900ns"},
	} {
		if got := roundDuration(tc.in).String(); got != tc.want {
			t.Errorf("roundDuration(%v) = %s, want %s", tc.in, got, tc.want)
		}
	}
}
