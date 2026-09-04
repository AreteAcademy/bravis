package extract

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	core "github.com/AreteAcademy/bravis/sdk/internal/core"
)

// TestRetryOn429 verifies retry on rate limit.
func TestRetryOn429(t *testing.T) {
	attempt := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt++
		if attempt < 2 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"data": "value"}`)
	}))
	defer server.Close()

	ctx := context.Background()
	source := core.Source{URL: server.URL}

	lines, err := NDJSON(ctx, source)
	if err != nil {
		t.Fatalf("NDJSON() error: %v", err)
	}

	count := 0
	for range lines {
		count++
	}

	if attempt != 2 {
		t.Errorf("Expected 2 attempts, got %d", attempt)
	}
	if count != 1 {
		t.Errorf("Expected 1 line, got %d", count)
	}
}

// TestNoRetryOn400 verifies no retry on client errors (except 429).
func TestNoRetryOn400(t *testing.T) {
	attempt := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt++
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprintf(w, `invalid request`)
	}))
	defer server.Close()

	ctx := context.Background()
	source := core.Source{URL: server.URL}

	_, err := NDJSON(ctx, source)
	if err == nil {
		t.Fatal("Expected error on 400")
	}

	if attempt != 1 {
		t.Errorf("Expected 1 attempt (no retry), got %d", attempt)
	}
}

// TestRetryAfterHeader verifies Retry-After respect.
func TestRetryAfterHeader(t *testing.T) {
	attempt := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt++
		if attempt < 2 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"data": "value"}`)
	}))
	defer server.Close()

	start := time.Now()
	ctx := context.Background()
	source := core.Source{URL: server.URL}

	lines, err := NDJSON(ctx, source)
	if err != nil {
		t.Fatalf("NDJSON() error: %v", err)
	}

	for range lines {
	}

	// Should have waited at least ~1 second due to Retry-After
	elapsed := time.Since(start)
	if elapsed < 500*time.Millisecond {
		t.Errorf("Expected delay for Retry-After, got %v", elapsed)
	}
}

// TestTimeoutPerAttempt verifies per-attempt timeout is distinct from total.
func TestTimeoutPerAttempt(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second) // Longer than per-attempt timeout
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	ctx := context.Background()
	source := core.Source{
		URL:          server.URL,
		Timeout:      100 * time.Millisecond, // per-attempt
		TotalTimeout: 5 * time.Second,        // total
	}

	_, err := NDJSON(ctx, source)
	// Should fail due to per-attempt timeout
	if err == nil {
		t.Fatal("Expected timeout error")
	}
}

// TestRecordsRunsBeforeDecoding verifies Records sees the raw body.
func TestRecordsRunsBeforeDecoding(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `not json at all`)
	}))
	defer server.Close()

	ctx := context.Background()
	source := core.Source{
		URL: server.URL,
		Records: func(r core.Response) ([]any, error) {
			called = true
			if !strings.HasPrefix(string(r.Bytes()), "{") {
				return nil, core.Reject("not json")
			}
			return nil, nil
		},
	}

	_, err := NDJSON(ctx, source)
	if err == nil {
		t.Fatal("Expected Records to reject the response")
	}
	if !called {
		t.Fatal("Records was not called")
	}
}

func csvServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/csv")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, "name,age\nAlice,30\nBob,25")
	}))
}

// TestCSVWithHeader verifies the default: the first row names the columns
// and only data rows become Envelopes.
func TestCSVWithHeader(t *testing.T) {
	server := csvServer(t)
	defer server.Close()

	lines, err := CSV(context.Background(), core.Source{URL: server.URL})
	if err != nil {
		t.Fatalf("CSV() error: %v", err)
	}

	var rows []map[string]string
	for env, err := range lines {
		if err != nil {
			t.Fatalf("row error: %v", err)
		}
		rows = append(rows, env.Payload.(map[string]string))
	}

	if len(rows) != 2 {
		t.Fatalf("Expected 2 data rows (header consumed), got %d", len(rows))
	}
	if rows[0]["name"] != "Alice" || rows[0]["age"] != "30" {
		t.Errorf("Row 0 keyed by header wrong: %v", rows[0])
	}
	if rows[1]["name"] != "Bob" || rows[1]["age"] != "25" {
		t.Errorf("Row 1 keyed by header wrong: %v", rows[1])
	}
}

// TestCSVWithoutHeader verifies NoHeader: every row is data, keyed positionally.
func TestCSVWithoutHeader(t *testing.T) {
	server := csvServer(t)
	defer server.Close()

	lines, err := CSV(context.Background(), core.Source{URL: server.URL, NoHeader: true})
	if err != nil {
		t.Fatalf("CSV() error: %v", err)
	}

	var rows []map[string]string
	for env, err := range lines {
		if err != nil {
			t.Fatalf("row error: %v", err)
		}
		rows = append(rows, env.Payload.(map[string]string))
	}

	if len(rows) != 3 {
		t.Fatalf("Expected 3 rows (no row treated as header), got %d", len(rows))
	}
	if rows[0]["field_0"] != "name" || rows[0]["field_1"] != "age" {
		t.Errorf("Row 0 should be the raw first line: %v", rows[0])
	}
	if rows[1]["field_0"] != "Alice" {
		t.Errorf("Row 1 keyed positionally wrong: %v", rows[1])
	}
}

// TestContextCancellation verifies cancellation stops fetching.
func TestContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for i := 0; i < 100; i++ {
			_, _ = fmt.Fprintf(w, `{"id": %d}\n`, i)
		}
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	source := core.Source{URL: server.URL}
	lines, err := NDJSON(ctx, source)
	if err != nil {
		t.Fatalf("NDJSON() error: %v", err)
	}

	count := 0
	for env, err := range lines {
		_ = env
		if err != nil {
			t.Logf("Got error: %v", err)
			break
		}
		count++
		if count >= 5 {
			cancel()
		}
	}

	if count >= 100 {
		t.Errorf("Expected cancellation to stop early, got %d", count)
	}
}

// TestMalformedStreamTerminates is a regression test: a decoder error that
// repeats (a JSON syntax error never clears) used to be yielded in an
// unbounded loop, spinning forever and flooding the consumer.
func TestMalformedStreamTerminates(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"id": 1}`+"\n"+`this is not json`+"\n"+`{"id": 2}`)
	}))
	defer server.Close()

	lines, err := NDJSON(context.Background(), core.Source{URL: server.URL})
	if err != nil {
		t.Fatalf("NDJSON() error: %v", err)
	}

	done := make(chan struct{})
	var rows, errs int
	go func() {
		defer close(done)
		for _, err := range lines {
			if err != nil {
				errs++
			} else {
				rows++
			}
			if rows+errs > 100 {
				return // loop is not terminating
			}
		}
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("iteration did not terminate on a malformed stream")
	}

	if rows != 1 {
		t.Errorf("Expected the 1 valid row before the garbage, got %d", rows)
	}
	if errs != 1 {
		t.Errorf("Expected exactly 1 error then stop, got %d", errs)
	}
}

// TestBodyStreamsFully is a regression test: the per-attempt context was
// cancelled as soon as client.Do returned, which killed the response body
// mid-read for any payload the transport had not already buffered.
func TestBodyStreamsFully(t *testing.T) {
	const rows = 5000
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.WriteHeader(http.StatusOK)
		for i := 0; i < rows; i++ {
			_, _ = fmt.Fprintf(w, "{\"id\": %d, \"pad\": \"%s\"}\n", i, strings.Repeat("x", 200))
		}
	}))
	defer server.Close()

	lines, err := NDJSON(context.Background(), core.Source{URL: server.URL})
	if err != nil {
		t.Fatalf("NDJSON() error: %v", err)
	}

	count := 0
	for _, err := range lines {
		if err != nil {
			t.Fatalf("row %d failed, body was truncated: %v", count, err)
		}
		count++
	}

	if count != rows {
		t.Errorf("Expected %d rows, got %d (response body truncated)", rows, count)
	}
}
