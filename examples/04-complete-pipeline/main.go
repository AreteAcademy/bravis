// Command 04-complete-pipeline walks a paginated API and loads every page
// into BigQuery, batching so memory stays flat regardless of total size.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"os"
	"time"

	"github.com/AreteAcademy/bravis/sdk"
	"github.com/AreteAcademy/bravis/sdk/extract"
	"github.com/AreteAcademy/bravis/sdk/load"
)

const batchSize = 1000

func main() {
	project := flag.String("project", "", "GCP project (required)")
	dataset := flag.String("dataset", "landing", "BigQuery dataset")
	table := flag.String("table", "raw_data", "BigQuery table")
	source := flag.String("url", "https://api.example.com/v1/transactions", "source endpoint")
	flag.Parse()

	if *project == "" {
		log.Fatal("-project is required")
	}

	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	ctx := context.Background()

	loader, err := load.New(ctx, nil,
		sdk.WithProjectID(*project),
		sdk.WithDataset(*dataset),
		sdk.WithTable(*table),
		sdk.WithMetadata(true),
	)
	if err != nil {
		log.Fatalf("loader: %v", err)
	}

	// Follows Link: rel="next" until the API stops offering one. Use
	// CursorKey or OffsetKey instead when the API paginates in the body.
	lines, err := extract.NDJSON(ctx, sdk.Source{
		URL:          *source,
		FollowLinks:  true,
		TotalTimeout: 30 * time.Minute,
	})
	if err != nil {
		log.Fatalf("extract: %v", err)
	}

	batch := make([]sdk.Envelope, 0, batchSize)
	total := int64(0)

	flush := func() {
		if len(batch) == 0 {
			return
		}
		result, err := loader.Load(ctx, batch...)
		if err != nil {
			log.Fatalf("load: %v", err)
		}
		total += result.RowsLoaded
		slog.Info("batch loaded", "rows", result.RowsLoaded, "strategy", result.Strategy)
		batch = batch[:0]
	}

	for env, err := range lines {
		if err != nil {
			log.Fatalf("extract: %v", err)
		}

		// Extract does not know your identity fields; you fill them in.
		env.Provider = "example_api"
		env.Entity = "transactions"
		if m, ok := env.Payload.(map[string]any); ok {
			env.SourceKey = fmt.Sprint(m["id"])
			env.RecordTS = fmt.Sprint(m["created_at"])
		}

		batch = append(batch, env)
		if len(batch) >= batchSize {
			flush()
		}
	}
	flush()

	fmt.Printf("%d rows loaded\n", total)
}
