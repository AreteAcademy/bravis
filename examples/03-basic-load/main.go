// Command 03-basic-load writes envelopes to BigQuery.
//
// This is the low-level API: you hand load.New a batch of envelopes you built
// yourself, provenance included. The two-call API above it (sdk.Extract and
// sdk.Load) is what a fetcher uses -- see 08-fetcher-minimo.
//
// The SDK has no opinion about your columns: it writes each record as you
// built it, and adds the two metadata fields only because WithMetadata asks.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"

	"github.com/AreteAcademy/bravis/sdk"
	"github.com/AreteAcademy/bravis/sdk/load"
)

func main() {
	project := flag.String("project", "", "GCP project (required)")
	dataset := flag.String("dataset", "landing", "BigQuery dataset")
	table := flag.String("table", "raw_data", "BigQuery table")
	flag.Parse()

	if *project == "" {
		log.Fatal("-project is required")
	}

	ctx := context.Background()

	// Functional options, or a LoadConfig literal -- both work, and the
	// config you pass is never mutated.
	loader, err := load.New(ctx, nil,
		sdk.WithProjectID(*project),
		sdk.WithDataset(*dataset),
		sdk.WithTable(*table),
		sdk.WithCreateTable(true), // so this example runs against an empty dataset
	)
	if err != nil {
		log.Fatalf("loader: %v", err)
	}

	envelopes := []sdk.Envelope{
		{
			Provider:  "example_api",
			Entity:    "transactions",
			SourceKey: "tx-123",
			RecordTS:  "2026-01-01T10:00:00Z",
			Payload:   map[string]any{"amount": 100, "currency": "BRL"},
		},
		{
			Provider:  "example_api",
			Entity:    "transactions",
			SourceKey: "tx-124",
			RecordTS:  "2026-01-01T10:05:00Z",
			Payload:   map[string]any{"amount": 250, "currency": "BRL"},
		},
	}

	result, err := loader.Load(ctx, envelopes...)
	if err != nil {
		log.Fatalf("load: %v", err)
	}

	fmt.Printf("%d rows in %v via %s\n", result.RowsLoaded, result.Duration, result.Strategy)
}
