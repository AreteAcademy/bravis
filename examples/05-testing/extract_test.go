// Package main shows how to test code built on the SDK.
//
// The whole point: httptest.Server stands in for the real API, so tests are
// fast, offline and deterministic. Nothing here touches the network.
package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/AreteAcademy/bravis/sdk"
	"github.com/AreteAcademy/bravis/sdk/extract"
)

// FetchAndProcess is the kind of function you would actually write: it uses
// the SDK and hands each row to your own logic.
func FetchAndProcess(ctx context.Context, url string, process func(sdk.Envelope) error) (int, error) {
	lines, err := extract.CSV(ctx, sdk.Fonte{URL: url})
	if err != nil {
		return 0, err
	}

	count := 0
	for env, err := range lines {
		if err != nil {
			return count, fmt.Errorf("row %d: %w", count, err)
		}
		if err := process(env); err != nil {
			return count, fmt.Errorf("process row %d: %w", count, err)
		}
		count++
	}
	return count, nil
}

func TestFetchAndProcess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/csv")
		_, _ = fmt.Fprint(w, "name,age\nAlice,30\nBob,25")
	}))
	defer server.Close()

	var seen []string
	n, err := FetchAndProcess(context.Background(), server.URL, func(env sdk.Envelope) error {
		row := env.Payload.(map[string]string)
		seen = append(seen, row["name"])
		return nil
	})
	if err != nil {
		t.Fatalf("FetchAndProcess: %v", err)
	}

	if n != 2 {
		t.Errorf("Expected 2 data rows, got %d", n)
	}
	if len(seen) != 2 || seen[0] != "Alice" || seen[1] != "Bob" {
		t.Errorf("Rows keyed by header wrong: %v", seen)
	}
}

// Retry is worth testing because it is invisible when it works: the caller
// sees one successful result, not the 503 underneath.
func TestRetriesTransientFailure(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/csv")
		_, _ = fmt.Fprint(w, "name\nAlice")
	}))
	defer server.Close()

	n, err := FetchAndProcess(context.Background(), server.URL, func(sdk.Envelope) error { return nil })
	if err != nil {
		t.Fatalf("FetchAndProcess: %v", err)
	}
	if attempts != 2 {
		t.Errorf("Expected a retry after the 503, got %d attempts", attempts)
	}
	if n != 1 {
		t.Errorf("Expected 1 row, got %d", n)
	}
}

// A 4xx is the caller's fault and must not be retried.
func TestDoesNotRetryClientError(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	if _, err := FetchAndProcess(context.Background(), server.URL, func(sdk.Envelope) error { return nil }); err == nil {
		t.Fatal("Expected an error on 404")
	}
	if attempts != 1 {
		t.Errorf("404 should not be retried, got %d attempts", attempts)
	}
}

// An error from your own processing must stop the walk and reach the caller.
func TestProcessorErrorPropagates(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/csv")
		_, _ = fmt.Fprint(w, "name\nAlice\nBob")
	}))
	defer server.Close()

	want := fmt.Errorf("boom")
	n, err := FetchAndProcess(context.Background(), server.URL, func(sdk.Envelope) error { return want })
	if err == nil {
		t.Fatal("Expected the processor error to surface")
	}
	if n != 0 {
		t.Errorf("Expected to stop on the first row, processed %d", n)
	}
}
