// Command 01-basic-extract is the smallest useful thing the SDK does:
// pull a CSV off an HTTP endpoint and walk its rows.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"

	"github.com/AreteAcademy/bravis/sdk"
	"github.com/AreteAcademy/bravis/sdk/extract"
)

func main() {
	url := flag.String("url", "https://raw.githubusercontent.com/AreteAcademy/bravis/master/examples/testdata/people.csv", "CSV endpoint")
	flag.Parse()

	// The first row names the columns; pass NoHeader to key rows positionally.
	lines, err := extract.CSV(context.Background(), sdk.Source{URL: *url})
	if err != nil {
		log.Fatalf("extract: %v", err)
	}

	rows := 0
	for env, err := range lines {
		if err != nil {
			log.Fatalf("row %d: %v", rows, err)
		}
		rows++
		fmt.Printf("%d: %v\n", rows, env.Payload)
	}

	fmt.Printf("\n%d rows\n", rows)
}
