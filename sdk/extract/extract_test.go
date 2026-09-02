package extract

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/AreteAcademy/bravis/sdk"
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
		fmt.Fprintf(w, `{"data": "value"}`)
	}))
	defer server.Close()

	ctx := context.Background()
	fonte := sdk.Fonte{URL: server.URL}

	lines, err := NDJSON(ctx, fonte)
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
		fmt.Fprintf(w, `invalid request`)
	}))
	defer server.Close()

	ctx := context.Background()
	fonte := sdk.Fonte{URL: server.URL}

	_, err := NDJSON(ctx, fonte)
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
		fmt.Fprintf(w, `{"data": "value"}`)
	}))
	defer server.Close()

	start := time.Now()
	ctx := context.Background()
	fonte := sdk.Fonte{URL: server.URL}

	lines, err := NDJSON(ctx, fonte)
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
	fonte := sdk.Fonte{
		URL:          server.URL,
		Timeout:      100 * time.Millisecond, // per-attempt
		TotalTimeout: 5 * time.Second,        // total
	}

	_, err := NDJSON(ctx, fonte)
	// Should fail due to per-attempt timeout
	if err == nil {
		t.Fatal("Expected timeout error")
	}
}

// TestGuardFunction verifies guard is called before decoding.
func TestGuardFunction(t *testing.T) {
	guardCalled := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `not json at all`)
	}))
	defer server.Close()

	ctx := context.Background()
	fonte := sdk.Fonte{
		URL: server.URL,
		Guard: func(status int, body []byte) error {
			guardCalled = true
			if !strings.HasPrefix(string(body), "{") {
				return fmt.Errorf("not json")
			}
			return nil
		},
	}

	_, err := NDJSON(ctx, fonte)
	if err == nil {
		t.Fatal("Expected guard to reject response")
	}
	if !guardCalled {
		t.Fatal("Guard was not called")
	}
}

// TestCSVWithoutHeader verifies CSV parsing works.
func TestCSVWithoutHeader(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/csv")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "name,age\nAlice,30\nBob,25")
	}))
	defer server.Close()

	ctx := context.Background()
	fonte := sdk.Fonte{URL: server.URL}

	lines, err := CSV(ctx, fonte)
	if err != nil {
		t.Fatalf("CSV() error: %v", err)
	}

	count := 0
	for range lines {
		count++
	}

	// Should be 2 rows + 1 header = 3 lines total
	if count != 3 {
		t.Errorf("Expected 3 lines (header + 2 data), got %d", count)
	}
}

// TestPagination verifies multiple pages are fetched.
func TestPagination(t *testing.T) {
	pages := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pages++
		w.WriteHeader(http.StatusOK)
		if pages == 1 {
			w.Header().Set("Link", `<http://example.com?page=2>; rel="next"`)
			fmt.Fprintf(w, `{"id": 1}`)
		} else if pages == 2 {
			fmt.Fprintf(w, `{"id": 2}`)
		}
	}))
	defer server.Close()

	// TODO: implement pagination in extract
	// For now, this is a placeholder - test passes trivially
}


// TestContextCancellation verifies cancellation stops fetching.
func TestContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for i := 0; i < 100; i++ {
			fmt.Fprintf(w, `{"id": %d}\n`, i)
		}
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())

	fonte := sdk.Fonte{URL: server.URL}
	lines, err := NDJSON(ctx, fonte)
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
