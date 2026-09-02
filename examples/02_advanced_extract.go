package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"time"

	"github.com/AreteAcademy/bravis/sdk"
	"github.com/AreteAcademy/bravis/sdk/extract"
)

// Example 2: Advanced extraction with retry, timeout, and guard function
//
// This example demonstrates:
// - Custom retry configuration
// - Per-attempt and total timeouts
// - Guard function to validate API responses
// - Custom headers (User-Agent, Authorization)
// - Structured logging
//
// Use this pattern when dealing with:
// - APIs that return 200 OK with error messages
// - APIs with strict rate limiting (429)
// - Large datasets requiring timeout tuning
func main() {
	ctx := context.Background()

	// Setup structured logging
	logger := slog.New(slog.NewJSONHandler(nil))
	slog.SetDefault(logger)

	// Fonte with advanced configuration
	fonte := sdk.Fonte{
		URL:    "https://api.example.gov/v1/data",
		Method: "GET",

		// Headers
		Header: http.Header{
			"User-Agent":    {"bravis-sdk/0.1.0"},
			"Authorization": {"Bearer YOUR_API_KEY"},
		},

		// Timeouts: per-attempt and total are separate
		Timeout:      10 * time.Second, // per attempt
		TotalTimeout: 2 * time.Minute,  // total time across all retries

		// Retry configuration
		RetryConfig: &sdk.RetryConfig{
			MaxAttempts:    5,           // retry up to 5 times
			InitialBackoff: 500 * time.Millisecond,
			MaxBackoff:     30 * time.Second,
			JitterFraction: 0.2, // 20% jitter
		},

		// Guard function: validate response before decoding
		// This catches cases where API returns 200 OK with an error message
		Guard: func(status int, body []byte) error {
			if status != http.StatusOK {
				return fmt.Errorf("unexpected status %d", status)
			}

			// Check that response is valid JSON/CSV, not error page
			if len(body) == 0 {
				return fmt.Errorf("empty response body")
			}

			// Example: reject responses that are error pages
			if bytes.Contains(body, []byte("error")) && bytes.Contains(body, []byte("message")) {
				return fmt.Errorf("API returned error response")
			}

			return nil
		},
	}

	// Extract NDJSON (newline-delimited JSON)
	fmt.Println("Extracting data with advanced configuration...")
	lines, err := extract.NDJSON(ctx, fonte)
	if err != nil {
		log.Fatalf("Failed to extract: %v", err)
	}

	// Process with error handling
	successCount := 0
	errorCount := 0

	for env, err := range lines {
		if err != nil {
			errorCount++
			slog.ErrorContext(ctx, "row error",
				"error", err,
				"total_errors", errorCount)
			continue
		}

		successCount++

		// Set envelope metadata
		env.Provider = "example_gov"
		env.Entity = "transactions"

		// Process envelope
		fmt.Printf("✓ Processed row %d: %+v\n", successCount, env.Payload)

		// Demo: stop after 5 successful rows
		if successCount >= 5 {
			break
		}
	}

	fmt.Printf("\n✓ Success: %d rows\n✗ Errors: %d rows\n", successCount, errorCount)
}
