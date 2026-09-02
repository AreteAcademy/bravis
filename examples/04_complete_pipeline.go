package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"time"

	"github.com/AreteAcademy/bravis/sdk"
	"github.com/AreteAcademy/bravis/sdk/extract"
	"github.com/AreteAcademy/bravis/sdk/load"
)

// Example 4: Complete pipeline - Extract from API, Transform, Load to BigQuery
//
// This is a realistic example showing:
// 1. Extract data from an external API (with retry and timeout)
// 2. Transform to Envelope format
// 3. Load to BigQuery with automatic strategy selection
//
// This pattern is suitable for:
// - Data pipelines and ETL jobs
// - Scheduled data ingestion
// - Multi-source consolidation
//
// Setup (run once):
//   export GCP_PROJECT=your-project
//   bq mk --dataset_id landing
//   bq mk --table landing.raw_data payload:JSON
//
// Run:
//   export GOOGLE_CLOUD_PROJECT=your-project
//   go run examples/04_complete_pipeline.go \
//     -url "https://api.example.com/transactions.csv" \
//     -table "raw_data" \
//     -metadata
func main() {
	url := flag.String("url", "", "Source URL (required)")
	projectID := flag.String("project", "", "GCP project ID (required)")
	dataset := flag.String("dataset", "landing", "BigQuery dataset")
	table := flag.String("table", "raw_data", "BigQuery table")
	dryRun := flag.Bool("dry-run", false, "Extract only, don't load to BigQuery")
	addMetadata := flag.Bool("metadata", false, "Add Bravis metadata to payload")
	flag.Parse()

	if *url == "" || *projectID == "" {
		fmt.Println("Usage: go run 04_complete_pipeline.go -url <url> -project <project> [-dataset <dataset>] [-table <table>] [-metadata]")
		fmt.Println("\nExample:")
		fmt.Println("  go run 04_complete_pipeline.go \\")
		fmt.Println("    -url 'https://api.example.gov/data.csv' \\")
		fmt.Println("    -project my-data-project \\")
		fmt.Println("    -table raw_api_data \\")
		fmt.Println("    -metadata")
		return
	}

	ctx := context.Background()
	logger := slog.New(slog.NewJSONHandler(nil))
	slog.SetDefault(logger)

	fmt.Println("=== Bravis Extract → Load Pipeline ===\n")

	// STEP 1: EXTRACT
	fmt.Printf("Step 1: Extracting from %s\n", *url)

	fonte := sdk.Fonte{
		URL: *url,
		Header: http.Header{
			"User-Agent": {"bravis-sdk/0.1.0"},
		},
		Timeout:      30 * time.Second,
		TotalTimeout: 5 * time.Minute,
		RetryConfig: &sdk.RetryConfig{
			MaxAttempts:    3,
			InitialBackoff: 1 * time.Second,
			MaxBackoff:     30 * time.Second,
			JitterFraction: 0.1,
		},
	}

	lines, err := extract.CSV(ctx, fonte)
	if err != nil {
		log.Fatalf("Extract failed: %v", err)
	}

	// STEP 2: TRANSFORM
	fmt.Println("Step 2: Transforming to Envelopes\n")

	envelopes := make([]sdk.Envelope, 0)
	recordTS := time.Now().UTC().Format(time.RFC3339)
	successCount := 0
	errorCount := 0

	for line, err := range lines {
		if err != nil {
			errorCount++
			slog.WarnContext(ctx, "row parse error", "error", err)
			continue
		}

		// Extract a key from the payload
		// In real usage, you'd parse the actual data structure
		var sourceKey string
		if payload, ok := line.Payload.(map[string]interface{}); ok {
			if id, exists := payload["id"]; exists {
				sourceKey = fmt.Sprintf("%v", id)
			}
		}

		if sourceKey == "" {
			errorCount++
			slog.WarnContext(ctx, "no id field in row")
			continue
		}

		// Create envelope
		envelope := sdk.Envelope{
			Provider:  "external_api",
			Entity:    "records",
			SourceKey: sourceKey,
			RecordTS:  recordTS,
			Payload:   line.Payload,
		}

		envelopes = append(envelopes, envelope)
		successCount++

		// Demo: limit to 100 rows for this example
		if len(envelopes) >= 100 {
			fmt.Printf("  (stopping at 100 rows for demo)\n")
			break
		}
	}

	fmt.Printf("✓ Extracted %d rows (%d errors)\n\n", successCount, errorCount)

	if *dryRun {
		fmt.Println("Dry-run mode: skipping load to BigQuery")
		return
	}

	// STEP 3: LOAD
	fmt.Println("Step 3: Loading to BigQuery")
	fmt.Printf("  Project:   %s\n", *projectID)
	fmt.Printf("  Dataset:   %s\n", *dataset)
	fmt.Printf("  Table:     %s\n", *table)
	fmt.Printf("  Metadata:  %v\n\n", *addMetadata)

	loader, err := load.New(ctx, &sdk.LoadConfig{
		ProjectID:   *projectID,
		Dataset:     *dataset,
		Table:       *table,
		Format:      "ndjson",
		AddMetadata: *addMetadata,
	})
	if err != nil {
		log.Fatalf("Create loader failed: %v", err)
	}

	result, err := loader.Load(ctx, envelopes...)
	if err != nil {
		log.Fatalf("Load failed: %v", err)
	}

	// RESULTS
	fmt.Println("=== Pipeline Complete ===\n")
	fmt.Printf("✓ Rows loaded:    %d\n", result.RowsLoaded)
	fmt.Printf("✓ Bytes staged:   %d\n", result.BytesStaged)
	fmt.Printf("✓ Duration:       %v\n", result.Duration)
	fmt.Printf("✓ Strategy:       %s\n", result.Strategy)
	fmt.Printf("✓ Format:         %s\n", result.Format)

	if len(result.ErrorRows) > 0 {
		fmt.Println("\n⚠ Load errors:")
		for _, errMsg := range result.ErrorRows {
			fmt.Printf("  • %s\n", errMsg)
		}
	}

	fmt.Printf("\n✓ Data successfully ingested to BigQuery!\n")
	fmt.Printf("  Access at: https://console.cloud.google.com/bigquery?project=%s\n", *projectID)
	fmt.Printf("  Table: %s.%s\n", *dataset, *table)

	if *addMetadata {
		fmt.Println("\n  Metadata fields added to payload:")
		fmt.Println("    - _bravis_ingestion_id (UUID v5)")
		fmt.Println("    - _bravis_ingestion_loaded_at (timestamp)")
		fmt.Println("    - _bravis_provider")
		fmt.Println("    - _bravis_entity")
		fmt.Println("    - _bravis_source_key")
		fmt.Println("    - _bravis_record_ts")
	}
}
