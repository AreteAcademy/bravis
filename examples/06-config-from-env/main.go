// Command 06-config-from-env builds the loader from environment variables,
// which is how this usually runs in Kubernetes.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"

	"github.com/AreteAcademy/bravis/sdk"
	"github.com/AreteAcademy/bravis/sdk/load"
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

func envBool(key string, fallback bool) bool {
	v, err := strconv.ParseBool(os.Getenv(key))
	if err != nil {
		return fallback
	}
	return v
}

func main() {
	project := os.Getenv("BRAVIS_PROJECT")
	if project == "" {
		log.Fatal("BRAVIS_PROJECT is required")
	}

	ctx := context.Background()

	loader, err := load.New(ctx, nil,
		sdk.WithProjectID(project),
		sdk.WithDataset(env("BRAVIS_DATASET", "landing")),
		sdk.WithTable(env("BRAVIS_TABLE", "raw_data")),
		sdk.WithStagingBucket(env("BRAVIS_STAGING_BUCKET", project+"-bravis-staging")),
		sdk.WithThresholdForGCS(envInt("BRAVIS_GCS_THRESHOLD", 5000)),
		sdk.WithMetadata(envBool("BRAVIS_ADD_METADATA", false)),
	)
	if err != nil {
		log.Fatalf("loader: %v", err)
	}

	result, err := loader.Load(ctx, sdk.Envelope{
		Provider:  env("BRAVIS_PROVIDER", "example_api"),
		Entity:    env("BRAVIS_ENTITY", "transactions"),
		SourceKey: "tx-1",
		RecordTS:  "2026-01-01T00:00:00Z",
		Payload:   map[string]any{"amount": 10},
	})
	if err != nil {
		log.Fatalf("load: %v", err)
	}

	fmt.Printf("%d rows via %s\n", result.RowsLoaded, result.Strategy)
}
