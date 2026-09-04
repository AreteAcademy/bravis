package extract

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/AreteAcademy/brevis/sdk/internal/core"
)

// defaultWarnAfter is when an unconfigured WarnAfter starts warning. A week is
// long enough for somebody to notice on a Monday.
const defaultWarnAfter = 7 * 24 * time.Hour

// authenticate resolves the credential and writes it onto the header the
// requests will carry.
//
// It runs before the client is built, so that a secret applied as a cookie is
// seeded into the jar like any other -- and from there the jar is the single
// place a cookie lives, including one the refresh reissues.
func authenticate(ctx context.Context, source *core.Source) error {
	if source.Auth == nil {
		return nil
	}
	if err := source.Auth.Check(); err != nil {
		return err
	}

	secret, err := source.Auth.Get(ctx)
	if err != nil {
		return err
	}

	// The caller's header is theirs; they may reuse the map on another
	// pipeline, and it must not come back carrying a secret.
	h := http.Header(source.Header).Clone()
	if h == nil {
		h = http.Header{}
	}
	source.Auth.Apply(h, secret)
	source.Header = h

	return nil
}

// renew calls the refresh endpoint before the first page.
//
// It shares the walk's client, so a Set-Cookie in the response lands in the
// jar and applies to every page that follows -- which is the whole mechanism.
// Nothing is written anywhere: the reissued value lives for this run only.
func renew(ctx context.Context, client *http.Client, source core.Source, stats *core.Stats) error {
	r := source.Auth.Refresh

	method := r.Method
	if method == "" {
		method = "GET"
	}

	req, err := http.NewRequestWithContext(ctx, method, r.URL, nil)
	if err != nil {
		return fmt.Errorf("refresh %s: %w", redactURL(r.URL), err)
	}
	req.Header = http.Header(source.Header).Clone()
	// Same rule as the pages: the jar is where cookies come from.
	req.Header.Del("Cookie")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("refresh %s: %w", redactURL(r.URL), err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("refresh %s: read response: %w", redactURL(r.URL), err)
	}

	// A refresh that fails is not a warning to move past: every page after it
	// would go out with a credential the API just refused, and the run would
	// fail anyway -- later, and blaming the data endpoint.
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("refresh %s: http %d: %s", redactURL(r.URL), resp.StatusCode, string(body))
	}

	if r.ExpiresAt == nil {
		return nil
	}

	expires, err := r.ExpiresAt(body)
	if err != nil {
		return fmt.Errorf("refresh %s: %w", redactURL(r.URL), err)
	}
	if stats != nil {
		stats.CredentialExpiry = expires
	}

	warnAfter := r.WarnAfter
	if warnAfter == 0 {
		warnAfter = defaultWarnAfter
	}
	left := time.Until(expires)

	switch {
	case left <= 0:
		slog.WarnContext(ctx, "credential has expired",
			"expires", expires.Format(time.RFC3339),
			"url", redactURL(r.URL))
	case left < warnAfter:
		slog.WarnContext(ctx, "credential expires soon",
			"expires", expires.Format(time.RFC3339),
			"left", core.RoundDuration(left),
			"url", redactURL(r.URL))
	default:
		slog.DebugContext(ctx, "credential renewed",
			"expires", expires.Format(time.RFC3339),
			"left", core.RoundDuration(left))
	}

	return nil
}
