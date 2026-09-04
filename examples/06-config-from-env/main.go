// Command 06-config-from-env builds the loader from environment variables,
// which is how this usually runs in Kubernetes.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"

	"github.com/AreteAcademy/brevis/sdk"
	"github.com/AreteAcademy/brevis/sdk/load"
)

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	v, err := strconv.Atoi(os.Getenv(key))
	if err != nil {
		return fallback
	}
	return v
}

func main() {
	project := os.Getenv("BREVIS_PROJECT")
	if project == "" {
		log.Fatal("BREVIS_PROJECT is required")
	}

	ctx := context.Background()

	loader, err := load.New(ctx, nil,
		sdk.WithProjectID(project),
		sdk.WithDataset(env("BREVIS_DATASET", "landing")),
		sdk.WithTable(env("BREVIS_TABLE", "raw_data")),
		sdk.WithStagingBucket(env("BREVIS_STAGING_BUCKET", project+"-brevis-staging")),
		sdk.WithThresholdForGCS(envInt("BREVIS_GCS_THRESHOLD", 5000)),
	)
	if err != nil {
		log.Fatalf("loader: %v", err)
	}

	result, err := loader.Load(ctx, sdk.Envelope{
		Provider:  env("BREVIS_PROVIDER", "example_api"),
		Entity:    env("BREVIS_ENTITY", "transactions"),
		SourceKey: "tx-1",
		RecordTS:  "2026-01-01T00:00:00Z",
		Payload:   map[string]any{"amount": 10},
	})
	if err != nil {
		log.Fatalf("load: %v", err)
	}

	fmt.Printf("%d rows via %s\n", result.RowsLoaded, result.Strategy)
}
