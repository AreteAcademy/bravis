package main

import (
	"context"
	"flag"
	"fmt"
	"log"

	"github.com/AreteAcademy/bravis/sdk/extract"
)

// Example 1: Basic CSV extraction
//
// This example shows the simplest way to extract data from an HTTP endpoint.
// The SDK handles retry, timeout, and format detection automatically.
//
// Run:
//   go run examples/01_basic_extract.go -url "https://example.com/data.csv"
func main() {
	url := flag.String("url", "", "URL to fetch (required)")
	flag.Parse()

	if *url == "" {
		fmt.Println("Usage: go run 01_basic_extract.go -url <url>")
		fmt.Println("\nExample:")
		fmt.Println("  go run 01_basic_extract.go -url 'https://example.gov/api/data.csv'")
		return
	}

	ctx := context.Background()

	// Extract CSV data
	lines, err := extract.CSV(ctx, extract.Fonte{
		URL: *url,
	})
	if err != nil {
		log.Fatalf("Failed to extract: %v", err)
	}

	// Process each row
	count := 0
	for env, err := range lines {
		if err != nil {
			log.Printf("Row error: %v", err)
			continue
		}

		count++
		fmt.Printf("Row %d: %+v\n", count, env.Payload)

		// Stop after first 10 for demo
		if count >= 10 {
			break
		}
	}

	fmt.Printf("\nProcessed %d rows\n", count)
}
