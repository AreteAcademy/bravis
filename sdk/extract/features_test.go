package extract

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AreteAcademy/bravis/sdk"
)

func collect(t *testing.T, lines func(func(sdk.Envelope, error) bool)) []sdk.Envelope {
	t.Helper()
	var out []sdk.Envelope
	for env, err := range lines {
		if err != nil {
			t.Fatalf("row %d: %v", len(out), err)
		}
		out = append(out, env)
	}
	return out
}

// --- XML ------------------------------------------------------------------

func TestXMLDecodesRepeatedChildren(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		_, _ = fmt.Fprint(w, `<items>
			<item id="a"><name>Alice</name><age>30</age></item>
			<item id="b"><name>Bob</name><age>25</age></item>
		</items>`)
	}))
	defer server.Close()

	lines, err := XML(context.Background(), sdk.Fonte{URL: server.URL})
	if err != nil {
		t.Fatalf("XML() error: %v", err)
	}

	rows := collect(t, lines)
	if len(rows) != 2 {
		t.Fatalf("Expected 2 items, got %d", len(rows))
	}

	first, ok := rows[0].Payload.(map[string]any)
	if !ok {
		t.Fatalf("Expected a map payload, got %T", rows[0].Payload)
	}
	if first["name"] != "Alice" || first["age"] != "30" {
		t.Errorf("Child elements wrong: %v", first)
	}
	if first["@id"] != "a" {
		t.Errorf("Attribute should be folded in as @id: %v", first)
	}
}

func TestXMLIsNoLongerUnsupported(t *testing.T) {
	// XML() used to return a sequence that immediately failed with
	// "unsupported format: xml" because NewDecoder had no xml case.
	if d := NewDecoder(nil, sdk.Fonte{Format: "xml"}); d == nil {
		t.Fatal("NewDecoder returned nil for xml")
	}
}

// --- Rate limiting --------------------------------------------------------

type countingLimiter struct {
	calls atomic.Int32
	delay time.Duration
}

func (l *countingLimiter) Wait(ctx context.Context) error {
	l.calls.Add(1)
	select {
	case <-time.After(l.delay):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestRateLimiterIsConsulted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"id": 1}`)
	}))
	defer server.Close()

	lim := &countingLimiter{delay: 150 * time.Millisecond}
	start := time.Now()

	lines, err := NDJSON(context.Background(), sdk.Fonte{URL: server.URL, RateLimiter: lim})
	if err != nil {
		t.Fatalf("NDJSON() error: %v", err)
	}
	collect(t, lines)

	if lim.calls.Load() != 1 {
		t.Errorf("Expected the limiter to gate the request once, got %d calls", lim.calls.Load())
	}
	if elapsed := time.Since(start); elapsed < 150*time.Millisecond {
		t.Errorf("Request did not wait on the limiter: %v", elapsed)
	}
}

func TestRateLimiterErrorAborts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"id": 1}`)
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // limiter will see a dead context

	_, err := NDJSON(ctx, sdk.Fonte{URL: server.URL, RateLimiter: &countingLimiter{}})
	if err == nil {
		t.Fatal("Expected the limiter's error to abort the fetch")
	}
}

// --- Pagination: Link header ---------------------------------------------

func TestPaginationFollowsLinkHeader(t *testing.T) {
	var mu = make(chan struct{}, 1)
	mu <- struct{}{}
	page := 0

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-mu
		page++
		p := page
		mu <- struct{}{}

		if p < 3 {
			w.Header().Set("Link", fmt.Sprintf(`<%s/?page=%d>; rel="next"`, srv.URL, p+1))
		}
		_, _ = fmt.Fprintf(w, `{"page": %d}`, p)
	}))
	defer srv.Close()

	lines, err := NDJSON(context.Background(), sdk.Fonte{URL: srv.URL, FollowLinks: true})
	if err != nil {
		t.Fatalf("NDJSON() error: %v", err)
	}

	rows := collect(t, lines)
	if len(rows) != 3 {
		t.Fatalf("Expected 3 rows across 3 pages, got %d", len(rows))
	}
}

func TestParseLinkNext(t *testing.T) {
	cases := []struct {
		header string
		want   string
	}{
		{`<https://api/x?page=2>; rel="next"`, "https://api/x?page=2"},
		{`<https://api/x?page=1>; rel="prev", <https://api/x?page=3>; rel="next"`, "https://api/x?page=3"},
		{`<https://api/x?page=9>; rel="last"`, ""},
		{"", ""},
		{"garbage", ""},
	}
	for _, c := range cases {
		if got := parseLinkNext(c.header); got != c.want {
			t.Errorf("parseLinkNext(%q) = %q, want %q", c.header, got, c.want)
		}
	}
}

// --- Pagination: cursor ---------------------------------------------------

func TestPaginationFollowsCursor(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("next_page") {
		case "":
			_, _ = fmt.Fprint(w, `{"results": [{"id": 1}, {"id": 2}], "next_page": "abc"}`)
		case "abc":
			_, _ = fmt.Fprint(w, `{"results": [{"id": 3}], "next_page": null}`)
		default:
			t.Errorf("unexpected cursor %q", r.URL.Query().Get("next_page"))
		}
	}))
	defer server.Close()

	lines, err := JSON(context.Background(), sdk.Fonte{
		URL:       server.URL,
		CursorKey: "next_page",
		DataKey:   "results",
	})
	if err != nil {
		t.Fatalf("JSON() error: %v", err)
	}

	rows := collect(t, lines)
	if len(rows) != 3 {
		t.Fatalf("Expected 3 rows across 2 pages, got %d", len(rows))
	}
}

func TestCursorStopsWhenAbsent(t *testing.T) {
	hits := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = fmt.Fprint(w, `{"results": [{"id": 1}]}`)
	}))
	defer server.Close()

	lines, err := JSON(context.Background(), sdk.Fonte{
		URL: server.URL, CursorKey: "next_page", DataKey: "results",
	})
	if err != nil {
		t.Fatalf("JSON() error: %v", err)
	}
	collect(t, lines)

	if hits != 1 {
		t.Errorf("Missing cursor should end pagination, but made %d requests", hits)
	}
}

// --- Pagination: offset ---------------------------------------------------

func TestPaginationAdvancesOffset(t *testing.T) {
	var seen []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		off := r.URL.Query().Get("offset")
		seen = append(seen, off)
		switch off {
		case "":
			_, _ = fmt.Fprint(w, "{\"id\": 1}\n{\"id\": 2}")
		case "2":
			_, _ = fmt.Fprint(w, "{\"id\": 3}\n{\"id\": 4}")
		default: // empty page ends it
		}
	}))
	defer server.Close()

	lines, err := NDJSON(context.Background(), sdk.Fonte{
		URL: server.URL, OffsetKey: "offset", PageSize: 2,
	})
	if err != nil {
		t.Fatalf("NDJSON() error: %v", err)
	}

	rows := collect(t, lines)
	if len(rows) != 4 {
		t.Fatalf("Expected 4 rows across 2 full pages, got %d", len(rows))
	}
	if len(seen) != 3 || seen[1] != "2" || seen[2] != "4" {
		t.Errorf("Offset should advance by PageSize: %v", seen)
	}
}

// --- Pagination: safety ---------------------------------------------------

func TestMaxPagesStopsRunawayPagination(t *testing.T) {
	hits := 0
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		// A server that always claims there is a next page.
		w.Header().Set("Link", fmt.Sprintf(`<%s/>; rel="next"`, srv.URL))
		_, _ = fmt.Fprint(w, `{"id": 1}`)
	}))
	defer srv.Close()

	lines, err := NDJSON(context.Background(), sdk.Fonte{
		URL: srv.URL, FollowLinks: true, MaxPages: 5,
	})
	if err != nil {
		t.Fatalf("NDJSON() error: %v", err)
	}

	rows := collect(t, lines)
	if len(rows) != 5 {
		t.Errorf("MaxPages should cap the walk at 5, got %d rows", len(rows))
	}
	if hits != 5 {
		t.Errorf("Expected 5 requests, got %d", hits)
	}
}

// --- LoadOption -----------------------------------------------------------

func TestLoadOptionsApply(t *testing.T) {
	var cfg sdk.LoadConfig
	for _, opt := range []sdk.LoadOption{
		sdk.WithProjectID("proj"),
		sdk.WithDataset("ds"),
		sdk.WithTable("tbl"),
		sdk.WithStagingBucket("bucket"),
		sdk.WithFormat("ndjson"),
		sdk.WithThresholdForGCS(42),
		sdk.WithMetadata(true),
		sdk.WithMetadataNamespace("ns"),
	} {
		opt(&cfg)
	}

	if cfg.ProjectID != "proj" || cfg.Dataset != "ds" || cfg.Table != "tbl" {
		t.Errorf("identity options did not apply: %+v", cfg)
	}
	if cfg.StagingBucket != "bucket" || cfg.Format != "ndjson" {
		t.Errorf("staging options did not apply: %+v", cfg)
	}
	if cfg.ThresholdForGCS != 42 || !cfg.AddMetadata || cfg.MetadataNamespace != "ns" {
		t.Errorf("behaviour options did not apply: %+v", cfg)
	}
}
