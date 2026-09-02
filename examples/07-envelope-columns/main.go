// Command 07-envelope-columns writes the six-column landing contract.
//
// Use this when rows have to match a bronze layer that deduplicates on
// ingestion_id — typically because a Python fetcher writes the same entity and
// both sides must produce identical ids for the same record.
//
// Create the table first:
//
//	CREATE TABLE landing.vendors_acme_transactions (
//	  ingestion_id        STRING    NOT NULL,
//	  ingestion_loaded_at TIMESTAMP NOT NULL,
//	  provider            STRING    NOT NULL,
//	  entity              STRING    NOT NULL,
//	  source_key          STRING,
//	  payload             JSON      NOT NULL
//	)
//	PARTITION BY DATE(ingestion_loaded_at)
//	CLUSTER BY provider, entity;
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
	table := flag.String("table", "vendors_acme_transactions", "BigQuery table")
	flag.Parse()

	if *project == "" {
		log.Fatal("-project is required")
	}

	ctx := context.Background()

	loader, err := load.New(ctx, nil,
		sdk.WithProjectID(*project),
		sdk.WithDataset(*dataset),
		sdk.WithTable(*table),
		// Wraps each payload in the six columns instead of writing it flat.
		// Mutually exclusive with WithMetadata.
		sdk.WithEnvelopeColumns(true),
	)
	if err != nil {
		log.Fatalf("loader: %v", err)
	}

	envelopes := []sdk.Envelope{
		{
			Provider:  "acme",
			Entity:    "transactions",
			SourceKey: "tx-123", // empty is an error: no stable ingestion_id without it
			RecordTS:  "2026-01-01T10:00:00Z",
			Payload:   map[string]any{"amount": 100, "currency": "BRL"},
		},
	}

	// The id written to the table is exactly this, computed by the same
	// function — nothing recomputes it downstream.
	id, err := envelopes[0].IngestionID()
	if err != nil {
		log.Fatalf("ingestion id: %v", err)
	}
	fmt.Printf("ingestion_id: %s\n", id)

	result, err := loader.Load(ctx, envelopes...)
	if err != nil {
		log.Printf("load failed: %v", err)
		for _, e := range result.ErrorRows {
			log.Printf("  %s", e)
		}
		return
	}

	fmt.Printf("%d rows in %v via %s\n", result.RowsLoaded, result.Duration, result.Strategy)
}
