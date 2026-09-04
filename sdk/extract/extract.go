package extract

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"iter"
	"log/slog"
	"math"
	"math/rand"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode"

	core "github.com/AreteAcademy/brevis/sdk/internal/core"
)

// CSV fetches and decodes CSV data from the given source.
// It returns an iterator of Envelopes, one per CSV row.
func CSV(ctx context.Context, source core.Source, records core.Reading) (iter.Seq2[core.Envelope, error], error) {
	if source.Format == "" {
		source.Format = "csv"
	}
	return fetch(ctx, source, records)
}

// NDJSON fetches and decodes newline-delimited JSON.
func NDJSON(ctx context.Context, source core.Source, records core.Reading) (iter.Seq2[core.Envelope, error], error) {
	if source.Format == "" {
		source.Format = "ndjson"
	}
	return fetch(ctx, source, records)
}

// JSON fetches and decodes a JSON array or object stream.
func JSON(ctx context.Context, source core.Source, records core.Reading) (iter.Seq2[core.Envelope, error], error) {
	if source.Format == "" {
		source.Format = "json"
	}
	return fetch(ctx, source, records)
}

// XML fetches and decodes XML data.
func XML(ctx context.Context, source core.Source, records core.Reading) (iter.Seq2[core.Envelope, error], error) {
	if source.Format == "" {
		source.Format = "xml"
	}
	return fetch(ctx, source, records)
}

func fetch(ctx context.Context, source core.Source, records core.Reading) (iter.Seq2[core.Envelope, error], error) {
	if source.URL == "" {
		return nil, fmt.Errorf("URL is required")
	}

	// Both answer "which part of the response holds the records", and with
	// Records set the decoder never sees the body DataKey unwrapped -- so
	// DataKey would sit there doing nothing, which is worse than an error.
	if records != nil && source.DataKey != "" {
		return nil, fmt.Errorf("the Reading and Source.DataKey both say where the records are, "+
			"and the Reading wins -- DataKey would be ignored. Read the field inside it "+
			"instead: sdk.ArrayAt(%q)(doc)", source.DataKey)
	}

	if err := checkPagination(source); err != nil {
		return nil, err
	}

	if source.Method == "" {
		source.Method = "GET"
	}

	if source.Timeout == 0 {
		source.Timeout = 30 * time.Second
	}

	if source.TotalTimeout == 0 {
		source.TotalTimeout = 5 * time.Minute
	}

	if source.RetryConfig == nil {
		source.RetryConfig = &core.RetryConfig{
			MaxAttempts:    3,
			InitialBackoff: 1 * time.Second,
			MaxBackoff:     60 * time.Second,
			JitterFraction: 0.1,
		}
	}

	if source.RetryConfig.MaxAttempts < 1 {
		source.RetryConfig.MaxAttempts = 1
	}

	if source.MaxPages <= 0 {
		source.MaxPages = defaultMaxPages
	}

	// Before the client, so a secret applied as a cookie is seeded into the
	// jar with the rest.
	if err := authenticate(ctx, &source); err != nil {
		return nil, err
	}

	client, err := newClient(source)
	if err != nil {
		return nil, err
	}

	ctxTotal, cancelTotal := context.WithTimeout(ctx, source.TotalTimeout)

	if source.Auth != nil && source.Auth.Refresh != nil {
		if err := renew(ctxTotal, client, source, source.Stats); err != nil {
			cancelTotal()
			return nil, err
		}
	}

	// Counted from the first byte of the first page, which is fetched before
	// the closure exists -- so the counter has to outlive both.
	var bytesRead int64

	// The first page is fetched eagerly so that an unreachable host, a 404 or
	// a guard rejection surfaces as an error from CSV/JSON/NDJSON/XML rather
	// than as the first item of a sequence the caller has to drain to notice.
	firstURL := source.URL
	if source.PageKey != "" {
		numbered, err := firstPageURL(source)
		if err != nil {
			cancelTotal()
			return nil, err
		}
		firstURL = numbered
	}

	first, err := fetchPage(ctxTotal, client, source, records, firstURL, &bytesRead)
	if err != nil {
		cancelTotal()
		return nil, err
	}

	return func(yield func(core.Envelope, error) bool) {
		startTime := time.Now()
		defer cancelTotal()

		page := first
		rows := 0
		pages := 0

		// The sample is taken as records go past, so a preview costs N
		// records of memory and never touches what the consumer receives.
		var sample []any
		emit := yield
		if source.Preview > 0 {
			emit = func(env core.Envelope, err error) bool {
				if err == nil && len(sample) < source.Preview {
					sample = append(sample, env.Payload)
				}
				return yield(env, err)
			}
		}

		// Deferred so the report still comes out when the source dies
		// halfway or the consumer breaks out of the loop -- which is exactly
		// when someone wants to see what did arrive.
		defer func() {
			read := atomic.LoadInt64(&bytesRead)
			if source.Stats != nil {
				source.Stats.Bytes = read
			}
			elapsed := time.Since(startTime)

			args := []any{
				"format", source.Format,
				"url", redactURL(source.URL),
				"pages", pages,
				"rows", rows,
				"bytes", read,
				"duration", elapsed,
			}
			if pages > 0 {
				args = append(args, "per_page", core.RoundDuration(elapsed/time.Duration(pages)))
			}
			// On the summary line too, not only on the warning: the summary
			// is what people keep, and the credential this renews is the one
			// a human has to re-paste before it lapses.
			if source.Stats != nil && !source.Stats.CredentialExpiry.IsZero() {
				args = append(args, "credential_expires",
					source.Stats.CredentialExpiry.Format(time.RFC3339))
			}
			slog.InfoContext(ctxTotal, "extract complete", args...)

			if source.Preview > 0 {
				w := source.PreviewWriter
				if w == nil {
					w = os.Stderr
				}
				_, _ = io.WriteString(w, core.RenderPreview(sample, source.PreviewBytes, core.PreviewStats{
					Rows: rows, Pages: pages, Bytes: read, Duration: elapsed,
				}))
			}
		}()
		// A cursor that stops advancing is an infinite loop, and government
		// APIs do return the same token forever at the end of a collection.
		// MaxPages would eventually stop it, but only after MaxPages wasted
		// requests; catching the repeat ends it on the next one.
		seen := map[string]bool{}

		for {
			pages++
			if source.Stats != nil {
				source.Stats.Pages = pages
			}
			emitted, next, err := drainPage(ctxTotal, source, page, emit)
			rows += emitted
			page.close()

			if err != nil {
				if !errors.Is(err, errStopped) {
					yield(core.Envelope{}, err)
				}
				return
			}

			if next == "" || pages >= source.MaxPages {
				break
			}

			if seen[next] {
				slog.WarnContext(ctxTotal, "pagination stopped: the source repeated a page",
					"page", pages, "url", redactURL(next))
				break
			}
			seen[next] = true

			page, err = fetchPage(ctxTotal, client, source, records, next, &bytesRead)
			if err != nil {
				yield(core.Envelope{}, fmt.Errorf("page %d: %w", pages+1, err))
				return
			}
		}
	}, nil
}

const defaultMaxPages = 1000

// errStopped signals that the consumer broke out of the range loop. It is
// never handed to the caller -- there is nobody left to hand it to.
var errStopped = errors.New("consumer stopped iterating")

// countingBody tallies what actually came off the wire.
//
// It wraps the response body rather than measuring the decoded records,
// because the number that explains a slow extract is the one the source
// sent -- a page can be mostly envelope and still take a minute to arrive.
type countingBody struct {
	io.ReadCloser
	n *int64
}

func (c countingBody) Read(b []byte) (int, error) {
	n, err := c.ReadCloser.Read(b)
	atomic.AddInt64(c.n, int64(n))
	return n, err
}

// page is one fetched HTTP response, plus whatever had to be buffered to
// work out where the next page lives.
type page struct {
	body     io.ReadCloser
	linkNext string // rel="next" from the Link header
	cursor   string // value at source.CursorKey, when cursor paging
	offset   int    // row offset this page was fetched at, for offset paging
	number   int    // page number this page was fetched at, for page paging
	release  func()

	// Set when the Reading answered for this page. The records then come
	// from the fetcher rather than the decoder, and hasRecords distinguishes
	// "the fetcher said none" from "the fetcher was not asked".
	records    []any
	hasRecords bool
}

func (p *page) close() {
	if p.body != nil {
		_ = p.body.Close()
	}
	if p.release != nil {
		p.release()
	}
}

// drainPage decodes one page, yielding every row. It reports how many rows it
// emitted and the URL of the next page, if any.
func drainPage(ctx context.Context, source core.Source, p *page, yield func(core.Envelope, error) bool) (int, string, error) {
	// The fetcher already said what this response holds, so there is nothing
	// to decode. Zero records is a legitimate answer -- an empty window --
	// and it must not be mistaken for a decode that found nothing.
	if p.hasRecords {
		for _, r := range p.records {
			if !yield(core.Envelope{Payload: r}, nil) {
				return 0, "", errStopped
			}
		}
		next, err := nextPageURL(source, p, len(p.records))
		return len(p.records), next, err
	}

	decoder := NewDecoder(p.body, source)
	if decoder == nil {
		return 0, "", fmt.Errorf("unsupported format: %s", source.Format)
	}

	emitted := 0
	for {
		select {
		case <-ctx.Done():
			return emitted, "", fmt.Errorf("context cancelled")
		default:
		}

		env, err := decoder.Next(ctx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			// The underlying stream cannot recover: a syntax error or a read
			// failure repeats forever. Report it once and stop.
			return emitted, "", err
		}

		emitted++
		if !yield(env, nil) {
			return emitted, "", errStopped
		}
	}

	next, err := nextPageURL(source, p, emitted)
	return emitted, next, err
}

// nextPageURL resolves where the following page lives, or "" when the current
// page was the last one.
func nextPageURL(source core.Source, p *page, emitted int) (string, error) {
	switch {
	case source.FollowLinks:
		return p.linkNext, nil

	case source.CursorKey != "":
		if p.cursor == "" {
			return "", nil
		}
		return withQuery(source.URL, source.CursorKey, p.cursor)

	case source.PageKey != "":
		// An empty page is the only reliable end-of-data signal here; a short
		// page can just be a partially filled one.
		if emitted == 0 {
			return "", nil
		}
		return withQuery(source.URL, source.PageKey, strconv.Itoa(p.number+1))

	case source.OffsetKey != "":
		// Same reasoning as above.
		if emitted == 0 {
			return "", nil
		}
		size := source.PageSize
		if size <= 0 {
			size = emitted
		}
		return withQuery(source.URL, source.OffsetKey, strconv.Itoa(p.offset+size))
	}

	return "", nil
}

// checkPagination refuses two strategies at once. The alternative -- a
// documented precedence order -- leaves the loser as a field that was set and
// does nothing, which is the failure mode this SDK keeps finding in itself.
func checkPagination(source core.Source) error {
	set := []string{}
	if source.FollowLinks {
		set = append(set, "FollowLinks")
	}
	if source.CursorKey != "" {
		set = append(set, "CursorKey")
	}
	if source.PageKey != "" {
		set = append(set, "PageKey")
	}
	if source.OffsetKey != "" {
		set = append(set, "OffsetKey")
	}
	if len(set) > 1 {
		return fmt.Errorf("pagination: %s are all set, and only one can apply -- "+
			"the others would be read and ignored. Keep the one this API uses",
			strings.Join(set, ", "))
	}

	if source.PageSize != 0 && source.OffsetKey == "" {
		return fmt.Errorf("pagination: PageSize is the number of rows OffsetKey advances " +
			"by, and OffsetKey is not set. For page numbers use PageKey, which advances " +
			"by one page and ignores PageSize")
	}
	if source.FirstPage != 0 && source.PageKey == "" {
		return fmt.Errorf("pagination: FirstPage numbers the pages of PageKey, and " +
			"PageKey is not set")
	}
	return nil
}

// newClient builds the one client the whole walk shares.
//
// It carries a cookie jar, which is why it is built once instead of per
// request: a session cookie the API refreshes mid-walk has to survive to the
// next page. Before this, every consumer that needed cookies wrote the same
// merge by hand -- and the same bug, because a NextAuth session cookie is a
// JWT whose base64 padding is "=", so splitting name=value on every "=" cuts
// the token and the API answers 401 rather than a parse error.
//
// The jar has no public suffix list. A walk talks to one host, and pulling
// x/net in as a direct dependency to police cookie domains would risk the
// go.mod floor this SDK promises (1.23) on the next tidy.
func newClient(source core.Source) (*http.Client, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("cookie jar: %w", err)
	}

	// A Cookie header written by the caller seeds the jar, so that from here
	// on there is exactly one place a cookie lives and a refreshed one
	// replaces the old by name.
	if raw := http.Header(source.Header).Get("Cookie"); raw != "" {
		u, err := url.Parse(source.URL)
		if err != nil {
			return nil, fmt.Errorf("parse url: %w", err)
		}
		cookies, err := http.ParseCookie(raw)
		if err != nil {
			return nil, fmt.Errorf("Header[\"Cookie\"] is not a valid cookie header: %w", err)
		}
		jar.SetCookies(u, cookies)
	}

	return &http.Client{Timeout: source.Timeout, Jar: jar}, nil
}

// firstPageURL puts the page number on the very first request. Letting the
// server pick its own default would leave us guessing whether it was 0 or 1,
// and guessing wrong skips a whole page of rows in silence.
func firstPageURL(source core.Source) (string, error) {
	u, err := url.Parse(source.URL)
	if err != nil {
		return "", fmt.Errorf("parse url: %w", err)
	}
	if u.Query().Has(source.PageKey) {
		return source.URL, nil // the caller numbered it; that number wins
	}

	first := source.FirstPage
	if first == 0 {
		first = 1
	}
	return withQuery(source.URL, source.PageKey, strconv.Itoa(first))
}

func withQuery(rawURL, key, value string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("build next page url: %w", err)
	}
	q := u.Query()
	q.Set(key, value)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// fetchPage performs one request, with retry, and prepares the response for
// decoding. Everything that needs the whole body -- the guard, cursor paging
// -- buffers it here so the streaming path below stays streaming.
func fetchPage(ctxTotal context.Context, client *http.Client, source core.Source, records core.Reading, pageURL string, bytesRead *int64) (*page, error) {
	var resp *http.Response
	// release cancels the context of the attempt that produced resp. The body
	// is still streaming under that context, so it must stay alive until the
	// page is drained -- cancelling it early truncates the response.
	release := func() {}

	for attempt := 0; attempt < source.RetryConfig.MaxAttempts; attempt++ {
		if source.Stats != nil {
			source.Stats.Attempts++
		}

		if source.RateLimiter != nil {
			if err := source.RateLimiter.Wait(ctxTotal); err != nil {
				return nil, fmt.Errorf("rate limiter: %w", err)
			}
		}

		ctxAttempt, cancelAttempt := context.WithTimeout(ctxTotal, source.Timeout)

		req, err := http.NewRequestWithContext(ctxAttempt, source.Method, pageURL, source.Body)
		if err != nil {
			cancelAttempt()
			return nil, fmt.Errorf("create request: %w", err)
		}

		if source.Header != nil {
			req.Header = http.Header(source.Header).Clone()
		}
		// The jar is the single place cookies live, so a Cookie header the
		// caller wrote is seeded into it and dropped here. Keeping both would
		// send two values for the same name once the server refreshed it, and
		// which one the server honours is anyone's guess.
		req.Header.Del("Cookie")

		resp, err = client.Do(req)

		if err != nil {
			cancelAttempt()
			if shouldRetry(err) && attempt < source.RetryConfig.MaxAttempts-1 {
				backoff := calculateBackoff(attempt, source.RetryConfig)
				slog.DebugContext(ctxTotal, "retry",
					"attempt", attempt+1,
					"backoff", backoff,
					"error", err)
				time.Sleep(backoff)
				continue
			}
			return nil, fmt.Errorf("fetch failed after %d attempts: %w", attempt+1, err)
		}

		// Every 2xx, not just 200. A vendor answering 204 on an empty
		// window, or 206 on a partial page, is answering -- and what that
		// means is the fetcher's call, made in Records. Failing the run on
		// "http 204" turns a quiet Tuesday into a red pipeline.
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			release = cancelAttempt
			break
		}

		if shouldRetryStatus(resp.StatusCode) && attempt < source.RetryConfig.MaxAttempts-1 {
			backoff := retryAfter(resp, attempt, source.RetryConfig)
			slog.DebugContext(ctxTotal, "retry",
				"attempt", attempt+1,
				"status", resp.StatusCode,
				"backoff", backoff)
			_ = resp.Body.Close()
			cancelAttempt()
			time.Sleep(backoff)
			continue
		}

		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		cancelAttempt()
		return nil, fmt.Errorf("http %d: %s", resp.StatusCode, string(body))
	}

	body := countingBody{ReadCloser: resp.Body, n: bytesRead}
	p := &page{body: body, release: release}

	if source.FollowLinks {
		p.linkNext = parseLinkNext(resp.Header.Get("Link"))
	}
	if source.OffsetKey != "" {
		p.offset = currentOffset(resp.Request, source.OffsetKey)
	}
	if source.PageKey != "" {
		p.number = currentOffset(resp.Request, source.PageKey)
	}

	// Anything that must see the whole body reads it here and hands the
	// decoder an equivalent reader.
	if records != nil || source.CursorKey != "" || source.DataKey != "" {
		buffered, err := io.ReadAll(body)
		if err != nil {
			p.close()
			return nil, fmt.Errorf("read body: %w", err)
		}
		_ = resp.Body.Close()

		if records != nil {
			found, err := records(core.NewResponse(
				resp.StatusCode, resp.Header, redactURL(pageURL), buffered))
			if err != nil {
				p.body = nil
				p.close()
				return nil, err
			}
			p.records = found
			p.hasRecords = true
		}

		if source.CursorKey != "" || source.DataKey != "" {
			cursor, rows, err := unwrapPage(buffered, source.CursorKey, source.DataKey)
			if err != nil {
				p.body = nil
				p.close()
				return nil, err
			}
			p.cursor = cursor
			buffered = rows
		}

		p.body = io.NopCloser(bytes.NewReader(buffered))
	}

	return p, nil
}

// unwrapPage pulls the next cursor and, when DataKey is set, the row payload
// out of a wrapper object such as {"results": [...], "next": "abc"}.
func unwrapPage(body []byte, cursorKey, dataKey string) (cursor string, rows []byte, err error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		return "", nil, fmt.Errorf("cursor and data keys need a JSON object page: %w", err)
	}

	if cursorKey != "" {
		if raw, ok := obj[cursorKey]; ok {
			// A cursor is usually a string but numeric page ids are common.
			if err := json.Unmarshal(raw, &cursor); err != nil {
				cursor = strings.Trim(string(raw), `"`)
				if cursor == "null" {
					cursor = ""
				}
			}
		}
	}

	rows = body
	if dataKey != "" {
		raw, ok := obj[dataKey]
		if !ok {
			return "", nil, fmt.Errorf("DataKey %q not found in page", dataKey)
		}
		rows = raw
	}

	return cursor, rows, nil
}

// parseLinkNext returns the URL of the rel="next" link in an RFC 8288 header.
func parseLinkNext(header string) string {
	for _, link := range strings.Split(header, ",") {
		parts := strings.Split(link, ";")
		if len(parts) < 2 {
			continue
		}
		target := strings.TrimSpace(parts[0])
		if !strings.HasPrefix(target, "<") || !strings.HasSuffix(target, ">") {
			continue
		}
		for _, param := range parts[1:] {
			p := strings.TrimSpace(param)
			if p == `rel="next"` || p == "rel=next" || p == `rel='next'` {
				return target[1 : len(target)-1]
			}
		}
	}
	return ""
}

// currentOffset reads the offset the request that produced this page used, so
// the next one can advance from it rather than from zero.
func currentOffset(req *http.Request, key string) int {
	if req == nil || req.URL == nil {
		return 0
	}
	n, err := strconv.Atoi(req.URL.Query().Get(key))
	if err != nil {
		return 0
	}
	return n
}

func retryAfter(resp *http.Response, attempt int, cfg *core.RetryConfig) time.Duration {
	if v := resp.Header.Get("Retry-After"); v != "" {
		if secs, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && secs >= 0 {
			return time.Duration(secs) * time.Second
		}
		if t, err := http.ParseTime(v); err == nil {
			if d := time.Until(t); d > 0 {
				return d
			}
		}
	}
	return calculateBackoff(attempt, cfg)
}

func shouldRetry(err error) bool {
	// Check for network errors, timeouts, temporary errors
	if err == nil {
		return false
	}
	str := err.Error()
	return strings.Contains(str, "connection") ||
		strings.Contains(str, "timeout") ||
		strings.Contains(str, "temporary")
}

func shouldRetryStatus(status int) bool {
	return status == http.StatusTooManyRequests || status >= http.StatusInternalServerError
}

func calculateBackoff(attempt int, cfg *core.RetryConfig) time.Duration {
	backoff := time.Duration(math.Pow(2, float64(attempt))) * cfg.InitialBackoff
	if backoff > cfg.MaxBackoff {
		backoff = cfg.MaxBackoff
	}

	jitter := time.Duration(rand.Int63n(int64(float64(backoff) * cfg.JitterFraction)))
	return backoff + jitter
}

// Redact strips secrets out of a URL, for logs and errors. Exported so a
// driver can describe its source without leaking a token.
func Redact(urlStr string) string { return redactURL(urlStr) }

// secretMarkers are the substrings that make a query parameter a secret.
//
// Matched against the name with case folded and separators stripped, so
// api_key, API-KEY and apikey are all caught. A vendor invents its own names,
// so an exact list would only ever cover the vendors we happened to think of.
//
// It over-redacts: a parameter called "monkey" contains "key" and comes out
// redacted. That is the direction to be wrong in -- a log line hiding
// something harmless costs nothing, and the other mistake puts a live
// credential in a log aggregator that many people can read.
// redacted is what a secret becomes. Letters only, because url.Values.Encode
// percent-escapes anything else -- and "%2A%2A%2A" in a log is exactly the
// kind of noise that makes people stop reading logs.
const redacted = "REDACTED"

var secretMarkers = []string{
	"key", "token", "secret", "password", "passwd", "pwd",
	"auth", "credential", "signature", "sig", "session", "cookie",
}

func redactURL(urlStr string) string {
	u, err := url.Parse(urlStr)
	if err != nil {
		return "[invalid url]"
	}

	// The password in https://user:pass@host never reaches a log. url.String
	// prints it in full, which is how it used to.
	if u.User != nil {
		if _, hasPassword := u.User.Password(); hasPassword {
			u.User = url.UserPassword(u.User.Username(), redacted)
		}
	}

	q := u.Query()
	for name := range q {
		if isSecret(name) {
			q.Set(name, redacted)
		}
	}
	u.RawQuery = q.Encode()

	return u.String()
}

// isSecret folds case and drops separators before looking for a marker.
func isSecret(name string) bool {
	folded := strings.Map(func(r rune) rune {
		switch r {
		case '_', '-', '.', ' ':
			return -1
		}
		return unicode.ToLower(r)
	}, name)

	for _, marker := range secretMarkers {
		if strings.Contains(folded, marker) {
			return true
		}
	}
	return false
}

// Decoder abstraction
type Decoder interface {
	Next(ctx context.Context) (core.Envelope, error)
}

// NewDecoder builds a Decoder for source.Format reading from r.
// It returns nil if the format is not supported.
func NewDecoder(r io.Reader, source core.Source) Decoder {
	switch source.Format {
	case "csv":
		return &csvDecoder{r: csv.NewReader(r), noHeader: source.NoHeader}
	case "ndjson":
		return &ndjsonDecoder{dec: json.NewDecoder(r)}
	case "json":
		return &jsonDecoder{dec: json.NewDecoder(r)}
	case "xml":
		return &xmlDecoder{dec: xml.NewDecoder(r)}
	default:
		return nil
	}
}

// csvDecoder turns CSV rows into Envelopes.
//
// By default the first row is consumed as the column names and every
// following row is keyed by them, so a file with a header and N data rows
// yields N Envelopes. With noHeader set, no row is treated as special and
// every row is keyed positionally as field_0, field_1, ... yielding one
// Envelope per line.
type csvDecoder struct {
	r        *csv.Reader
	headers  []string
	noHeader bool
}

func (d *csvDecoder) Next(ctx context.Context) (core.Envelope, error) {
	if !d.noHeader && d.headers == nil {
		header, err := d.r.Read()
		if err != nil {
			return core.Envelope{}, err
		}
		d.headers = header
	}

	record, err := d.r.Read()
	if err != nil {
		return core.Envelope{}, err
	}

	obj := make(map[string]string, len(record))
	for i, value := range record {
		if d.noHeader {
			obj[fmt.Sprintf("field_%d", i)] = value
			continue
		}
		if i < len(d.headers) {
			obj[d.headers[i]] = value
		}
	}

	return core.Envelope{
		Payload: obj,
	}, nil
}

type ndjsonDecoder struct {
	dec *json.Decoder
}

func (d *ndjsonDecoder) Next(ctx context.Context) (core.Envelope, error) {
	var obj any
	if err := d.dec.Decode(&obj); err != nil {
		return core.Envelope{}, err
	}
	return core.Envelope{
		Payload: obj,
	}, nil
}

type jsonDecoder struct {
	dec    *json.Decoder
	inited bool
	arrays []any
	index  int
}

func (d *jsonDecoder) Next(ctx context.Context) (core.Envelope, error) {
	if !d.inited {
		var obj any
		if err := d.dec.Decode(&obj); err != nil {
			return core.Envelope{}, err
		}

		if arr, ok := obj.([]any); ok {
			d.arrays = arr
		} else {
			d.arrays = []any{obj}
		}
		d.inited = true
	}

	if d.index >= len(d.arrays) {
		return core.Envelope{}, io.EOF
	}

	env := core.Envelope{
		Payload: d.arrays[d.index],
	}
	d.index++
	return env, nil
}

// xmlDecoder streams the direct children of the root element, one Envelope
// each. For
//
//	<items><item><id>1</id></item><item><id>2</id></item></items>
//
// that is two Envelopes, {id: 1} and {id: 2}.
//
// XML has no notion of a list, so there is nothing to infer beyond "the
// repeated thing under the root is the record". Attributes are folded in with
// an "@" prefix, and an element holding only text becomes that text.
type xmlDecoder struct {
	dec     *xml.Decoder
	entered bool
}

func (d *xmlDecoder) Next(ctx context.Context) (core.Envelope, error) {
	for {
		tok, err := d.dec.Token()
		if err != nil {
			return core.Envelope{}, err
		}

		start, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}

		// Skip past the root element; its children are the records.
		if !d.entered {
			d.entered = true
			continue
		}

		var node xmlNode
		if err := d.dec.DecodeElement(&node, &start); err != nil {
			return core.Envelope{}, err
		}

		return core.Envelope{Payload: node.value()}, nil
	}
}

// xmlNode is the generic shape encoding/xml can unmarshal any element into.
type xmlNode struct {
	Attrs   []xml.Attr `xml:",any,attr"`
	Content string     `xml:",chardata"`
	Nodes   []xmlNode  `xml:",any"`
	XMLName xml.Name
}

// value renders a node as a plain Go value: a string for a leaf, a map
// otherwise. Repeated sibling names collapse into a slice.
func (n xmlNode) value() any {
	if len(n.Nodes) == 0 && len(n.Attrs) == 0 {
		return strings.TrimSpace(n.Content)
	}

	out := make(map[string]any, len(n.Nodes)+len(n.Attrs))

	for _, attr := range n.Attrs {
		out["@"+attr.Name.Local] = attr.Value
	}

	for _, child := range n.Nodes {
		name := child.Name()
		v := child.value()

		existing, seen := out[name]
		if !seen {
			out[name] = v
			continue
		}
		if list, ok := existing.([]any); ok {
			out[name] = append(list, v)
			continue
		}
		out[name] = []any{existing, v}
	}

	if text := strings.TrimSpace(n.Content); text != "" && len(n.Nodes) == 0 {
		out["#text"] = text
	}

	return out
}

func (n xmlNode) Name() string {
	return n.XMLName.Local
}
