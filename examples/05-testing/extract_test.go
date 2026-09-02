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

// Example 5: Testing SDK-based code
//
// This file demonstrates how to test code that uses the Bravis SDK.
// Key principle: use httptest.Server to mock HTTP endpoints.
// No need to hit real APIs during tests.
//
// Run tests:
//   go test -v examples/05_testing_example.go
//
// This example shows:
// - Mocking HTTP endpoints
// - Testing retry behavior
// - Testing error handling
// - Testing with different response formats
// - Using iter.Seq2 in tests

// FetchAndProcess is a real function that uses the SDK
// (This would normally be in your business logic)
func FetchAndProcess(ctx context.Context, url string, processor func(sdk.Envelope) error) (int, error) {
	lines, err := extract.CSV(ctx, extract.Fonte{
		URL: url,
	})
	if err != nil {
		return 0, err
	}

	count := 0
	for env, err := range lines {
		if err != nil {
			return count, err
		}

		if err := processor(env); err != nil {
			return count, err
		}

		count++
	}

	return count, nil
}

// =========== TESTS ===========

func TestFetchAndProcess_SuccessfulCSV(t *testing.T) {
	// Setup mock server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/csv")
		fmt.Fprintf(w, "id,name\n1,Alice\n2,Bob")
	}))
	defer server.Close()

	// Process function that tracks calls
	processedCount := 0
	processor := func(env sdk.Envelope) error {
		processedCount++
		return nil
	}

	// Execute
	ctx := context.Background()
	count, err := FetchAndProcess(ctx, server.URL, processor)

	// Assert
	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}
	if count != 3 { // header + 2 rows
		t.Errorf("Expected 3 rows, got %d", count)
	}
	if processedCount != 3 {
		t.Errorf("Expected processor called 3 times, got %d", processedCount)
	}
}

func TestFetchAndProcess_HTTPError(t *testing.T) {
	// Setup mock server that returns 500
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, "Server error")
	}))
	defer server.Close()

	processor := func(env sdk.Envelope) error { return nil }

	// Execute
	ctx := context.Background()
	_, err := FetchAndProcess(ctx, server.URL, processor)

	// Assert
	if err == nil {
		t.Error("Expected error on HTTP 500, got nil")
	}
}

func TestFetchAndProcess_ProcessorError(t *testing.T) {
	// Setup mock server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/csv")
		fmt.Fprintf(w, "id\n1\n2\n3")
	}))
	defer server.Close()

	// Processor that fails on second row
	processor := func(env sdk.Envelope) error {
		payload := env.Payload.(map[string]string)
		if payload["id"] == "2" {
			return fmt.Errorf("validation failed for id=2")
		}
		return nil
	}

	// Execute
	ctx := context.Background()
	count, err := FetchAndProcess(ctx, server.URL, processor)

	// Assert
	if err == nil {
		t.Error("Expected error from processor, got nil")
	}
	if count != 1 { // processed 1 row before error
		t.Errorf("Expected 1 row processed before error, got %d", count)
	}
}

func TestFetchAndProcess_EmptyResponse(t *testing.T) {
	// Setup mock server that returns empty response
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/csv")
		// Empty body
	}))
	defer server.Close()

	processor := func(env sdk.Envelope) error { return nil }

	// Execute
	ctx := context.Background()
	count, err := FetchAndProcess(ctx, server.URL, processor)

	// Assert
	if err != nil {
		t.Errorf("Expected no error for empty response, got %v", err)
	}
	if count != 0 {
		t.Errorf("Expected 0 rows for empty response, got %d", count)
	}
}

// Table-driven test for multiple formats
func TestFetchAndProcess_MultipleFormats(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		content string
		want    int
	}{
		{
			name:    "CSV with header",
			body:    "id,name\n1,Alice\n2,Bob",
			content: "text/csv",
			want:    3, // header + 2 rows
		},
		{
			name:    "CSV without header",
			body:    "1,Alice\n2,Bob",
			content: "text/csv",
			want:    2,
		},
		{
			name:    "Single line",
			body:    "42,Charlie",
			content: "text/csv",
			want:    1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", tt.content)
				fmt.Fprint(w, tt.body)
			}))
			defer server.Close()

			processor := func(env sdk.Envelope) error { return nil }
			ctx := context.Background()

			count, err := FetchAndProcess(ctx, server.URL, processor)

			if err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
			if count != tt.want {
				t.Errorf("Expected %d rows, got %d", tt.want, count)
			}
		})
	}
}

// Benchmark example
func BenchmarkFetchAndProcess(b *testing.B) {
	// Setup mock server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/csv")
		for i := 0; i < 1000; i++ {
			fmt.Fprintf(w, "%d,record_%d\n", i, i)
		}
	}))
	defer server.Close()

	processor := func(env sdk.Envelope) error { return nil }
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		FetchAndProcess(ctx, server.URL, processor)
	}
}

// === Running the tests ===
//
// From the examples directory:
//   go test -v 05_testing_example.go
//
// With benchmarks:
//   go test -bench=. 05_testing_example.go
//
// With coverage:
//   go test -cover 05_testing_example.go
