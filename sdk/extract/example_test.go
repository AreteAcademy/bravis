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
	lines, err := extract.CSV(context.Background(), sdk.Source{
		URL: "https://example.gov/data.csv",
	}, nil)
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
	lines, _ := extract.CSV(context.Background(), sdk.Source{
		URL:      "https://example.gov/headerless.csv",
		NoHeader: true,
	}, nil)
	for env := range lines {
		fmt.Println(env.Payload)
	}
}

// Retry, guard and the two timeouts are what make an unattended pipeline
// survive a flaky upstream.
func ExampleNDJSON_resilient() {
	source := sdk.Source{
		URL:          "https://api.example.com/events",
		Timeout:      15 * time.Second, // per attempt
		TotalTimeout: 5 * time.Minute,  // whole walk
		RetryConfig: &sdk.RetryConfig{
			MaxAttempts:    5,
			InitialBackoff: time.Second,
			MaxBackoff:     30 * time.Second,
			JitterFraction: 0.2,
		},
	}

	reading := func(r sdk.Response) ([]any, error) {
		if len(r.Bytes()) == 0 {
			return nil, sdk.Reject("empty body on %d", r.Status)
		}
		var docs []any
		return docs, r.JSON(&docs)
	}

	lines, _ := extract.NDJSON(context.Background(), source, reading)
	for range lines {
	}
}

// Follow RFC 8288 Link headers until the API stops offering rel="next".
func ExampleNDJSON_pagination() {
	lines, _ := extract.NDJSON(context.Background(), sdk.Source{
		URL:         "https://api.example.com/events",
		FollowLinks: true,
		MaxPages:    50,
	}, nil)
	for range lines {
	}
}

// A wrapped page: {"results": [...], "next_page": "abc"}. The cursor goes
// back as a query parameter of the same name; DataKey says where rows live.
func ExampleJSON_cursor() {
	lines, _ := extract.JSON(context.Background(), sdk.Source{
		URL:       "https://api.example.com/events",
		CursorKey: "next_page",
		DataKey:   "results",
	}, nil)
	for range lines {
	}
}

// XML turns each repeated element under the root into one Envelope.
func ExampleXML() {
	lines, _ := extract.XML(context.Background(), sdk.Source{
		URL: "https://example.gov/feed.xml",
	}, nil)
	for env := range lines {
		fmt.Println(env.Payload)
	}
}
