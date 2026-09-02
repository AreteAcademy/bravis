package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strconv"
	"time"

	"github.com/AreteAcademy/bravis/sdk"
	"github.com/AreteAcademy/bravis/sdk/load"
)

// Example 6: Configuration from Environment Variables
//
// This example shows how to configure the SDK from environment variables.
// This is the recommended approach for production deployments.
//
// Environment variables:
//   BRAVIS_URL              - Data source URL (extract)
//   BRAVIS_PROJECT_ID       - GCP project ID (load)
//   BRAVIS_DATASET          - BigQuery dataset (default: landing)
//   BRAVIS_PROVIDER         - Data provider name
//   BRAVIS_ENTITY           - Entity type
//   BRAVIS_TIMEOUT          - Per-attempt timeout in seconds (default: 30)
//   BRAVIS_TOTAL_TIMEOUT    - Total timeout in seconds (default: 300)
//   BRAVIS_MAX_RETRIES      - Max retry attempts (default: 3)
//   BRAVIS_STAGING_BUCKET   - GCS staging bucket
//   BRAVIS_THRESHOLD_GCS    - Threshold for GCS strategy (default: 5000)
//   BRAVIS_DRY_RUN          - Only extract, don't load (true/false)
//
// Usage:
//   BRAVIS_PROJECT_ID=my-project go run examples/06_config_from_env.go
//
// Or with all options:
//   export BRAVIS_URL="https://api.example.com/data.csv"
//   export BRAVIS_PROJECT_ID="my-project"
//   export BRAVIS_DATASET="landing"
//   export BRAVIS_PROVIDER="example_api"
//   export BRAVIS_ENTITY="transactions"
//   export BRAVIS_TIMEOUT="60"
//   export BRAVIS_MAX_RETRIES="5"
//   go run examples/06_config_from_env.go

// Config holds all configuration from environment variables
type Config struct {
	// Extract configuration
	URL           string
	Timeout       time.Duration
	TotalTimeout  time.Duration
	MaxRetries    int

	// Load configuration
	ProjectID       string
	Dataset         string
	StagingBucket   string
	ThresholdForGCS int
	DryRun          bool

	// Data metadata
	Provider string
	Entity   string
}

// LoadConfigFromEnv loads configuration from environment variables with sensible defaults
func LoadConfigFromEnv() (*Config, error) {
	cfg := &Config{
		// Defaults
		URL:             os.Getenv("BRAVIS_URL"),
		ProjectID:       os.Getenv("BRAVIS_PROJECT_ID"),
		Dataset:         os.Getenv("BRAVIS_DATASET"),
		Timeout:         30 * time.Second,
		TotalTimeout:    5 * time.Minute,
		MaxRetries:      3,
		StagingBucket:   os.Getenv("BRAVIS_STAGING_BUCKET"),
		ThresholdForGCS: 5000,
		Provider:        os.Getenv("BRAVIS_PROVIDER"),
		Entity:          os.Getenv("BRAVIS_ENTITY"),
	}

	// Override defaults from environment
	if v := os.Getenv("BRAVIS_TIMEOUT"); v != "" {
		sec, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid BRAVIS_TIMEOUT: %v", err)
		}
		cfg.Timeout = time.Duration(sec) * time.Second
	}

	if v := os.Getenv("BRAVIS_TOTAL_TIMEOUT"); v != "" {
		sec, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid BRAVIS_TOTAL_TIMEOUT: %v", err)
		}
		cfg.TotalTimeout = time.Duration(sec) * time.Second
	}

	if v := os.Getenv("BRAVIS_MAX_RETRIES"); v != "" {
		retries, err := strconv.Atoi(v)
		if err != nil {
			return nil, fmt.Errorf("invalid BRAVIS_MAX_RETRIES: %v", err)
		}
		cfg.MaxRetries = retries
	}

	if v := os.Getenv("BRAVIS_THRESHOLD_GCS"); v != "" {
		threshold, err := strconv.Atoi(v)
		if err != nil {
			return nil, fmt.Errorf("invalid BRAVIS_THRESHOLD_GCS: %v", err)
		}
		cfg.ThresholdForGCS = threshold
	}

	if v := os.Getenv("BRAVIS_DRY_RUN"); v != "" {
		cfg.DryRun = v == "true" || v == "1" || v == "yes"
	}

	// Validate required fields
	if cfg.ProjectID == "" {
		return nil, fmt.Errorf("BRAVIS_PROJECT_ID is required")
	}

	if cfg.Dataset == "" {
		cfg.Dataset = "landing"
	}

	if cfg.Provider == "" {
		cfg.Provider = "external"
	}

	if cfg.Entity == "" {
		cfg.Entity = "records"
	}

	return cfg, nil
}

// PrintConfig prints the configuration for debugging
func (c *Config) Print() {
	fmt.Println("=== Configuration ===")
	fmt.Printf("URL:                %s\n", c.URL)
	fmt.Printf("Project ID:         %s\n", c.ProjectID)
	fmt.Printf("Dataset:            %s\n", c.Dataset)
	fmt.Printf("Provider:           %s\n", c.Provider)
	fmt.Printf("Entity:             %s\n", c.Entity)
	fmt.Printf("Timeout:            %v\n", c.Timeout)
	fmt.Printf("Total Timeout:      %v\n", c.TotalTimeout)
	fmt.Printf("Max Retries:        %d\n", c.MaxRetries)
	fmt.Printf("Staging Bucket:     %s\n", c.StagingBucket)
	fmt.Printf("Threshold for GCS:  %d\n", c.ThresholdForGCS)
	fmt.Printf("Dry Run:            %v\n", c.DryRun)
	fmt.Println()
}

// ToFonte converts config to sdk.Fonte
func (c *Config) ToFonte() sdk.Fonte {
	return sdk.Fonte{
		URL:           c.URL,
		Timeout:       c.Timeout,
		TotalTimeout:  c.TotalTimeout,
		RetryConfig: &sdk.RetryConfig{
			MaxAttempts:    c.MaxRetries,
			InitialBackoff: 1 * time.Second,
			MaxBackoff:     30 * time.Second,
			JitterFraction: 0.1,
		},
	}
}

// ToLoadConfig converts config to sdk.LoadConfig
func (c *Config) ToLoadConfig() *sdk.LoadConfig {
	return &sdk.LoadConfig{
		ProjectID:       c.ProjectID,
		Dataset:         c.Dataset,
		StagingBucket:   c.StagingBucket,
		ThresholdForGCS: c.ThresholdForGCS,
		Format:          "ndjson",
	}
}

func main() {
	flag.Parse()

	// Load configuration from environment
	cfg, err := LoadConfigFromEnv()
	if err != nil {
		log.Fatalf("Configuration error: %v", err)
	}

	cfg.Print()

	// TODO: Implement actual pipeline using:
	// - cfg.ToFonte() for extract
	// - cfg.ToLoadConfig() for load
	// - cfg.Provider and cfg.Entity for data identification

	fmt.Println("✓ Configuration loaded successfully")
	fmt.Println("\nNext steps:")
	fmt.Println("  1. Implement extract logic using cfg.ToFonte()")
	fmt.Println("  2. Transform data to Envelopes")
	fmt.Println("  3. Load using cfg.ToLoadConfig()")
}

// == Example usage from shell ==
//
// # Minimal configuration (uses defaults)
// export BRAVIS_PROJECT_ID=my-project
// go run examples/06_config_from_env.go
//
// # Full configuration for production
// export BRAVIS_URL="https://secure-api.example.com/data?token=xxx"
// export BRAVIS_PROJECT_ID="data-warehouse"
// export BRAVIS_DATASET="bronze_landing"
// export BRAVIS_PROVIDER="partner_api"
// export BRAVIS_ENTITY="order_transactions"
// export BRAVIS_TIMEOUT="120"
// export BRAVIS_TOTAL_TIMEOUT="600"
// export BRAVIS_MAX_RETRIES="5"
// export BRAVIS_STAGING_BUCKET="company-data-staging"
// export BRAVIS_THRESHOLD_GCS="10000"
// go run examples/06_config_from_env.go
//
// # Dry run (only extract, don't load)
// export BRAVIS_DRY_RUN=true
// go run examples/06_config_from_env.go