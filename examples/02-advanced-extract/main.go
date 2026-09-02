// Command 02-advanced-extract shows the knobs that matter against a real API:
// headers, separate per-attempt and total timeouts, retry tuning, a guard that
// rejects a 200 carrying an error body, and a rate limiter.
package main

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"log/slog"
	"os"
	"time"

	"github.com/AreteAcademy/bravis/sdk"
	"github.com/AreteAcademy/bravis/sdk/extract"
)

// throttle is anything with Wait(ctx) error, so *rate.Limiter from
// golang.org/x/time/rate drops straight in. This one is hand-rolled to keep
// the example dependency-free.
type throttle struct{ every time.Duration }

func (t throttle) Wait(ctx context.Context) error {
	select {
	case <-time.After(t.every):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})))

	fonte := sdk.Fonte{
		URL:    "https://api.example.com/v1/transactions",
		Method: "GET",
		Header: map[string][]string{
			"Accept":        {"application/x-ndjson"},
			"Authorization": {"Bearer " + os.Getenv("API_TOKEN")},
		},

		// Per attempt vs. overall. A slow page fails its attempt and is
		// retried; the total budget still caps the whole walk.
		Timeout:      15 * time.Second,
		TotalTimeout: 2 * time.Minute,

		RetryConfig: &sdk.RetryConfig{
			MaxAttempts:    5,
			InitialBackoff: time.Second,
			MaxBackoff:     30 * time.Second,
			JitterFraction: 0.2,
		},

		// Plenty of APIs answer 200 with an error document. The guard runs
		// before decoding, so this fails loudly instead of loading garbage.
		Guard: func(status int, body []byte) error {
			if bytes.Contains(body, []byte(`"error"`)) {
				return fmt.Errorf("api returned an error document: %s", body)
			}
			return nil
		},

		RateLimiter: throttle{every: 200 * time.Millisecond},
	}

	lines, err := extract.NDJSON(context.Background(), fonte)
	if err != nil {
		log.Fatalf("extract: %v", err)
	}

	rows := 0
	for _, err := range lines {
		if err != nil {
			log.Fatalf("row %d: %v", rows, err)
		}
		rows++
	}

	fmt.Printf("%d rows\n", rows)
}
