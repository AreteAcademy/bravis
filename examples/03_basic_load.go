package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"time"

	"github.com/AreteAcademy/bravis/sdk"
	"github.com/AreteAcademy/bravis/sdk/load"
)

// Example 3: Basic BigQuery loading
//
// This example shows how to write data to BigQuery using the Bravis SDK.
// The SDK writes raw JSON payloads — you define the schema.
//
// Prerequisites:
// - Google Cloud credentials (gcloud auth application-default login)
// - BigQuery project with permissions
// - Table already created (see setup below)
//
// Setup (run once):
//   bq mk --dataset_id landing
//   bq mk --table landing.transactions \
//     payload:JSON
//
// Run:
//   export GOOGLE_CLOUD_PROJECT=your-project
//   go run examples/03_basic_load.go
func main() {
	projectID := flag.String("project", "", "GCP project ID (required)")
	dataset := flag.String("dataset", "landing", "BigQuery dataset")
	table := flag.String("table", "transactions", "BigQuery table")
	addMetadata := flag.Bool("metadata", false, "Add Bravis metadata to payload")
	flag.Parse()

	if *projectID == "" {
		fmt.Println("Usage: go run 03_basic_load.go -project <project-id> [-dataset <dataset>] [-table <table>] [-metadata]")
		fmt.Println("\nExample:")
		fmt.Println("  go run 03_basic_load.go -project my-data-project")
		fmt.Println("\nWith metadata fields:")
		fmt.Println("  go run 03_basic_load.go -project my-data-project -metadata")
		return
	}

	ctx := context.Background()

	// Create loader with configuration
	loader, err := load.New(ctx, &sdk.LoadConfig{
		ProjectID:   *projectID,
		Dataset:     *dataset,
		Table:       *table,
		Format:      "ndjson",
		AddMetadata: *addMetadata,
	})
	if err != nil {
		log.Fatalf("Failed to create loader: %v", err)
	}

	// Prepare sample data
	envelopes := []sdk.Envelope{
		{
			Provider:  "example_api",
			Entity:    "transactions",
			SourceKey: "tx-001",
			RecordTS:  time.Now().UTC().Format(time.RFC3339),
			Payload: map[string]interface{}{
				"amount":      100.50,
				"currency":    "USD",
				"description": "Payment for order #12345",
				"timestamp":   "2026-01-01T10:00:00Z",
			},
		},
		{
			Provider:  "example_api",
			Entity:    "transactions",
			SourceKey: "tx-002",
			RecordTS:  time.Now().UTC().Format(time.RFC3339),
			Payload: map[string]interface{}{
				"amount":      250.75,
				"currency":    "USD",
				"description": "Payment for order #12346",
				"timestamp":   "2026-01-01T10:05:00Z",
			},
		},
		{
			Provider:  "example_api",
			Entity:    "transactions",
			SourceKey: "tx-003",
			RecordTS:  time.Now().UTC().Format(time.RFC3339),
			Payload: map[string]interface{}{
				"amount":      75.25,
				"currency":    "USD",
				"description": "Refund for order #12345",
				"timestamp":   "2026-01-01T10:10:00Z",
			},
		},
	}

	// Load to BigQuery
	fmt.Printf("Loading %d envelopes to BigQuery...\n", len(envelopes))
	result, err := loader.Load(ctx, envelopes...)
	if err != nil {
		log.Fatalf("Failed to load: %v", err)
	}

	// Display results
	fmt.Println("\n✓ Load completed!")
	fmt.Printf("  Rows loaded:   %d\n", result.RowsLoaded)
	fmt.Printf("  Bytes staged:  %d\n", result.BytesStaged)
	fmt.Printf("  Duration:      %v\n", result.Duration)
	fmt.Printf("  Strategy:      %s\n", result.Strategy)
	fmt.Printf("  Format:        %s\n", result.Format)

	if len(result.ErrorRows) > 0 {
		fmt.Println("\n⚠ Row-level errors:")
		for i, errMsg := range result.ErrorRows {
			fmt.Printf("  [%d] %s\n", i, errMsg)
		}
	}
}
