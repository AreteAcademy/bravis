package extract_test

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/AreteAcademy/bravis/sdk"
	"github.com/AreteAcademy/bravis/sdk/extract"
)

// These compile as part of `go test`, so the snippets on pkg.go.dev cannot
// drift away from the real API the way the README once did.

func ExampleCSV() {
	lines, err := extract.CSV(context.Background(), sdk.Fonte{
		URL: "https://example.gov/data.csv",
	})
	if err != nil {
		log.Fatal(err)
	}

	for env, err := range lines {
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println(env.Payload)
	}
}

// The first CSV row names the columns by default. Pass NoHeader when the file
// has none, and every line is keyed field_0, field_1, ...
func ExampleCSV_noHeader() {
	lines, _ := extract.CSV(context.Background(), sdk.Fonte{
		URL:      "https://example.gov/headerless.csv",
		NoHeader: true,
	})
	for env := range lines {
		fmt.Println(env.Payload)
	}
}

// Retry, guard and the two timeouts are what make an unattended pipeline
// survive a flaky upstream.
func ExampleNDJSON_resilient() {
	fonte := sdk.Fonte{
		URL:          "https://api.example.com/events",
		Timeout:      15 * time.Second, // per attempt
		TotalTimeout: 5 * time.Minute,  // whole walk
		RetryConfig: &sdk.RetryConfig{
			MaxAttempts:    5,
			InitialBackoff: time.Second,
			MaxBackoff:     30 * time.Second,
			JitterFraction: 0.2,
		},
		Guard: func(status int, body []byte) error {
			if len(body) == 0 {
				return fmt.Errorf("empty body on %d", status)
			}
			return nil
		},
	}

	lines, _ := extract.NDJSON(context.Background(), fonte)
	for range lines {
	}
}

// Follow RFC 8288 Link headers until the API stops offering rel="next".
func ExampleNDJSON_pagination() {
	lines, _ := extract.NDJSON(context.Background(), sdk.Fonte{
		URL:         "https://api.example.com/events",
		FollowLinks: true,
		MaxPages:    50,
	})
	for range lines {
	}
}

// A wrapped page: {"results": [...], "next_page": "abc"}. The cursor goes
// back as a query parameter of the same name; DataKey says where rows live.
func ExampleJSON_cursor() {
	lines, _ := extract.JSON(context.Background(), sdk.Fonte{
		URL:       "https://api.example.com/events",
		CursorKey: "next_page",
		DataKey:   "results",
	})
	for range lines {
	}
}

// XML turns each repeated element under the root into one Envelope.
func ExampleXML() {
	lines, _ := extract.XML(context.Background(), sdk.Fonte{
		URL: "https://example.gov/feed.xml",
	})
	for env := range lines {
		fmt.Println(env.Payload)
	}
}
