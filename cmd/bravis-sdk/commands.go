package main

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"log"
	"time"

	"github.com/AreteAcademy/bravis/sdk"
	"github.com/AreteAcademy/bravis/sdk/extract"
	"github.com/AreteAcademy/bravis/sdk/load"
	"github.com/spf13/cobra"
)

// Extract command
var extractCmd = &cobra.Command{
	Use:   "extract <URL>",
	Short: "Extract data from HTTP endpoint",
	Long: `Extract data from an HTTP endpoint.

Supported formats: CSV, JSON, NDJSON, XML (auto-detected from Content-Type or URL)

Examples:
  bravis extract https://api.example.com/data.csv
  bravis extract https://api.example.com/data.json --format json
  bravis extract https://api.example.com/data --timeout 60s --retries 5`,
	Args: cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		url := args[0]
		format, _ := cmd.Flags().GetString("format")
		timeout, _ := cmd.Flags().GetDuration("timeout")
		totalTimeout, _ := cmd.Flags().GetDuration("total-timeout")
		maxRetries, _ := cmd.Flags().GetInt("retries")
		outputFormat, _ := cmd.Flags().GetString("output")

		ctx := context.Background()

		source := sdk.Source{
			URL:          url,
			Timeout:      timeout,
			TotalTimeout: totalTimeout,
			Format:       sdk.Format(format),
			RetryConfig: &sdk.RetryConfig{
				MaxAttempts: maxRetries,
			},
		}

		var lines iter.Seq2[sdk.Envelope, error]
		var err error

		switch format {
		case "csv":
			lines, err = extract.CSV(ctx, source, nil)
		case "json":
			lines, err = extract.JSON(ctx, source, nil)
		case "ndjson":
			lines, err = extract.NDJSON(ctx, source, nil)
		default:
			lines, err = extract.CSV(ctx, source, nil)
		}

		if err != nil {
			log.Fatalf("Extract failed: %v", err)
		}

		count := 0
		for env, err := range lines {
			if err != nil {
				if outputFormat == "json" {
					fmt.Printf(`{"error":"row error","message":"%v"}\n`, err)
				} else {
					fmt.Printf("❌ Error: %v\n", err)
				}
				continue
			}

			count++

			if outputFormat == "json" {
				data, _ := json.Marshal(env)
				fmt.Println(string(data))
			} else {
				fmt.Printf("✓ Row %d: %+v\n", count, env)
			}
		}

		fmt.Fprintf(cmd.OutOrStderr(), "\n✓ Extracted %d rows\n", count)
	},
}

// Load command
var loadCmd = &cobra.Command{
	Use:   "load",
	Short: "Load data to BigQuery",
	Long: `Load NDJSON data from stdin to BigQuery.

Reads NDJSON from stdin. Each line should be a valid JSON object.

Examples:
  cat data.ndjson | bravis load --project my-project --dataset landing --table raw_data
  bravis extract https://api.example.com/data.csv --output json | bravis load --project my-project --dataset landing --table raw_data`,
	Run: func(cmd *cobra.Command, args []string) {
		projectID, _ := cmd.Flags().GetString("project")
		dataset, _ := cmd.Flags().GetString("dataset")
		table, _ := cmd.Flags().GetString("table")
		addMetadata, _ := cmd.Flags().GetBool("metadata")

		if projectID == "" || dataset == "" || table == "" {
			log.Fatal("--project, --dataset, and --table are required")
		}

		ctx := context.Background()

		loader, err := load.New(ctx, &sdk.LoadConfig{
			ProjectID: projectID,
			Dataset:   dataset,
			Table:     table,
			Format:    "ndjson",
			Metadata:  addMetadata,
		})
		if err != nil {
			log.Fatalf("Create loader failed: %v", err)
		}

		// TODO: read from stdin and parse NDJSON
		envelopes := []sdk.Envelope{}

		result, err := loader.Load(ctx, envelopes...)
		if err != nil {
			log.Fatalf("Load failed: %v", err)
		}

		fmt.Printf("✓ Load completed\n")
		fmt.Printf("  Rows:     %d\n", result.RowsLoaded)
		fmt.Printf("  Duration: %v\n", result.Duration)
		fmt.Printf("  Strategy: %s\n", result.Strategy)
	},
}

// Run command (extract + load pipeline)
var runCmd = &cobra.Command{
	Use:   "run <URL>",
	Short: "Extract and load in one command",
	Long: `Extract from URL and load to BigQuery in one pipeline.

Examples:
  bravis run https://api.example.com/data.csv --project my-project --dataset landing --table raw_data
  bravis run https://api.example.com/data.json --project my-project --dataset landing --table raw_data --metadata`,
	Args: cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		url := args[0]
		projectID, _ := cmd.Flags().GetString("project")
		dataset, _ := cmd.Flags().GetString("dataset")
		table, _ := cmd.Flags().GetString("table")
		addMetadata, _ := cmd.Flags().GetBool("metadata")
		dryRun, _ := cmd.Flags().GetBool("dry-run")

		if projectID == "" {
			log.Fatal("--project is required")
		}

		if dataset == "" {
			dataset = "landing"
		}

		if table == "" {
			table = "raw_data"
		}

		ctx := context.Background()
		start := time.Now()

		// Extract
		fmt.Fprintf(cmd.OutOrStderr(), "📥 Extracting from %s...\n", url)

		source := sdk.Source{URL: url}
		lines, err := extract.CSV(ctx, source, nil)
		if err != nil {
			log.Fatalf("Extract failed: %v", err)
		}

		envelopes := []sdk.Envelope{}
		successCount := 0
		errorCount := 0

		for line, err := range lines {
			if err != nil {
				errorCount++
				fmt.Fprintf(cmd.OutOrStderr(), "⚠️  Row error: %v\n", err)
				continue
			}
			line.Provider = "cli"
			line.Entity = "record"
			line.RecordTS = time.Now().UTC().Format(time.RFC3339)
			envelopes = append(envelopes, line)
			successCount++
		}

		fmt.Fprintf(cmd.OutOrStderr(), "✓ Extracted: %d rows (%d errors)\n\n", successCount, errorCount)

		if dryRun {
			fmt.Fprintf(cmd.OutOrStderr(), "🔍 Dry-run mode: skipping load\n")
			return
		}

		// Load
		fmt.Fprintf(cmd.OutOrStderr(), "📤 Loading to BigQuery...\n")
		fmt.Fprintf(cmd.OutOrStderr(), "  Project:  %s\n", projectID)
		fmt.Fprintf(cmd.OutOrStderr(), "  Dataset:  %s\n", dataset)
		fmt.Fprintf(cmd.OutOrStderr(), "  Table:    %s\n", table)
		fmt.Fprintf(cmd.OutOrStderr(), "  Metadata: %v\n\n", addMetadata)

		loader, err := load.New(ctx, &sdk.LoadConfig{
			ProjectID: projectID,
			Dataset:   dataset,
			Table:     table,
			Metadata:  addMetadata,
		})
		if err != nil {
			log.Fatalf("Create loader failed: %v", err)
		}

		result, err := loader.Load(ctx, envelopes...)
		if err != nil {
			log.Fatalf("Load failed: %v", err)
		}

		duration := time.Since(start)
		fmt.Fprintf(cmd.OutOrStderr(), "✓ Pipeline completed in %v\n", duration)
		fmt.Fprintf(cmd.OutOrStderr(), "  Total rows:  %d\n", result.RowsLoaded)
		fmt.Fprintf(cmd.OutOrStderr(), "  Strategy:    %s\n", result.Strategy)
	},
}

// Version command
var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Show version",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("bravis version %s (commit: %s)\n", version, commit)
	},
}

func init() {
	// Extract flags
	extractCmd.Flags().StringP("format", "f", "", "Format: csv, json, ndjson, xml (auto-detect if empty)")
	extractCmd.Flags().DurationP("timeout", "t", 30*time.Second, "Timeout per attempt")
	extractCmd.Flags().Duration("total-timeout", 5*time.Minute, "Total timeout across all retries")
	extractCmd.Flags().IntP("retries", "r", 3, "Max retry attempts")
	extractCmd.Flags().StringP("output", "o", "table", "Output format: table or json")

	// Load flags
	loadCmd.Flags().StringP("project", "p", "", "GCP project ID (required)")
	loadCmd.Flags().StringP("dataset", "d", "landing", "BigQuery dataset")
	loadCmd.Flags().StringP("table", "t", "raw_data", "BigQuery table")
	loadCmd.Flags().BoolP("metadata", "m", false, "Add ingestion_id and ingestion_loaded_at to each row")

	// Run flags
	runCmd.Flags().StringP("project", "p", "", "GCP project ID (required)")
	runCmd.Flags().StringP("dataset", "d", "landing", "BigQuery dataset")
	runCmd.Flags().StringP("table", "t", "raw_data", "BigQuery table")
	runCmd.Flags().BoolP("metadata", "m", false, "Add ingestion_id and ingestion_loaded_at to each row")
	runCmd.Flags().Bool("dry-run", false, "Extract only, don't load")
}
