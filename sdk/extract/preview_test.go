package extract

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	core "github.com/AreteAcademy/brevis/sdk/internal/core"
)

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
