package extract

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"log/slog"
	"math"
	"math/rand"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/AreteAcademy/bravis/sdk"
)

// CSV fetches and decodes CSV data from the given source.
// It returns an iterator of Envelopes, one per CSV row.
func CSV(ctx context.Context, fonte sdk.Fonte) (iter.Seq2[sdk.Envelope, error], error) {
	if fonte.Format == "" {
		fonte.Format = "csv"
	}
	return fetch(ctx, fonte)
}

// NDJSON fetches and decodes newline-delimited JSON.
func NDJSON(ctx context.Context, fonte sdk.Fonte) (iter.Seq2[sdk.Envelope, error], error) {
	if fonte.Format == "" {
		fonte.Format = "ndjson"
	}
	return fetch(ctx, fonte)
}

// JSON fetches and decodes a JSON array or object stream.
func JSON(ctx context.Context, fonte sdk.Fonte) (iter.Seq2[sdk.Envelope, error], error) {
	if fonte.Format == "" {
		fonte.Format = "json"
	}
	return fetch(ctx, fonte)
}

// XML fetches and decodes XML data.
func XML(ctx context.Context, fonte sdk.Fonte) (iter.Seq2[sdk.Envelope, error], error) {
	if fonte.Format == "" {
		fonte.Format = "xml"
	}
	return fetch(ctx, fonte)
}

func fetch(ctx context.Context, fonte sdk.Fonte) (iter.Seq2[sdk.Envelope, error], error) {
	if fonte.URL == "" {
		return nil, fmt.Errorf("URL is required")
	}

	if fonte.Method == "" {
		fonte.Method = "GET"
	}

	if fonte.Timeout == 0 {
		fonte.Timeout = 30 * time.Second
	}

	if fonte.TotalTimeout == 0 {
		fonte.TotalTimeout = 5 * time.Minute
	}

	if fonte.RetryConfig == nil {
		fonte.RetryConfig = &sdk.RetryConfig{
			MaxAttempts:    3,
			InitialBackoff: 1 * time.Second,
			MaxBackoff:     60 * time.Second,
			JitterFraction: 0.1,
		}
	}

	if fonte.RetryConfig.MaxAttempts < 1 {
		fonte.RetryConfig.MaxAttempts = 1
	}

	// Make initial request to check for errors before returning sequence
	ctxTotal, cancelTotal := context.WithTimeout(ctx, fonte.TotalTimeout)

	var resp *http.Response
	// release cancels the context of the attempt that produced resp. The body
	// is still streaming under that context, so it must stay alive until the
	// caller is done iterating -- cancelling it here truncates the response.
	release := func() {}

	for attempt := 0; attempt < fonte.RetryConfig.MaxAttempts; attempt++ {
		// Create context with per-attempt timeout
		ctxAttempt, cancelAttempt := context.WithTimeout(ctxTotal, fonte.Timeout)

		client := &http.Client{Timeout: fonte.Timeout}

		req, err := http.NewRequestWithContext(ctxAttempt, fonte.Method, fonte.URL, fonte.Body)
		if err != nil {
			cancelAttempt()
			cancelTotal()
			return nil, fmt.Errorf("create request: %w", err)
		}

		if fonte.Header != nil {
			req.Header = fonte.Header
		}

		resp, err = client.Do(req)

		if err != nil {
			cancelAttempt()
			if shouldRetry(err) && attempt < fonte.RetryConfig.MaxAttempts-1 {
				backoff := calculateBackoff(attempt, fonte.RetryConfig)
				slog.DebugContext(ctxTotal, "retry",
					"attempt", attempt+1,
					"backoff", backoff,
					"error", err)
				time.Sleep(backoff)
				continue
			}
			cancelTotal()
			return nil, fmt.Errorf("fetch failed after %d attempts: %w", attempt+1, err)
		}

		if resp.StatusCode == http.StatusOK {
			release = cancelAttempt
			break
		}

		// Retry on 429 or 5xx
		if shouldRetryStatus(resp.StatusCode) && attempt < fonte.RetryConfig.MaxAttempts-1 {
			var backoff time.Duration
			if retryAfter := resp.Header.Get("Retry-After"); retryAfter != "" {
				if seconds, err := time.ParseDuration(retryAfter + "s"); err == nil {
					backoff = seconds
				} else {
					backoff = calculateBackoff(attempt, fonte.RetryConfig)
				}
			} else {
				backoff = calculateBackoff(attempt, fonte.RetryConfig)
			}

			slog.DebugContext(ctxTotal, "retry",
				"attempt", attempt+1,
				"status", resp.StatusCode,
				"backoff", backoff)
			_ = resp.Body.Close()
			cancelAttempt()
			time.Sleep(backoff)
			continue
		}

		// Non-retryable status code (400, 404, etc.)
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		cancelAttempt()
		cancelTotal()
		return nil, fmt.Errorf("http %d: %s", resp.StatusCode, string(body))
	}

	// Guard function (before decoding)
	if fonte.Guard != nil {
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			_ = resp.Body.Close()
			release()
			cancelTotal()
			return nil, fmt.Errorf("read body for guard: %w", err)
		}
		if err := fonte.Guard(resp.StatusCode, body); err != nil {
			_ = resp.Body.Close()
			release()
			cancelTotal()
			return nil, fmt.Errorf("guard rejected response: %w", err)
		}
		// Create new reader from read body
		resp.Body = io.NopCloser(strings.NewReader(string(body)))
	}

	return func(yield func(sdk.Envelope, error) bool) {
		startTime := time.Now()
		defer func() { _ = resp.Body.Close() }()
		defer release()
		defer cancelTotal()

		// Decode based on format
		decoder := NewDecoder(resp.Body, fonte)
		if decoder == nil {
			yield(sdk.Envelope{}, fmt.Errorf("unsupported format: %s", fonte.Format))
			return
		}

		for {
			select {
			case <-ctxTotal.Done():
				yield(sdk.Envelope{}, fmt.Errorf("context cancelled"))
				return
			default:
			}

			env, err := decoder.Next(ctxTotal)
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				// The underlying stream cannot recover: a syntax error or a
				// read failure repeats forever. Report it once and stop.
				yield(sdk.Envelope{}, err)
				return
			}

			if !yield(env, nil) {
				return
			}
		}

		duration := time.Since(startTime)
		slog.InfoContext(ctxTotal, "extract complete",
			"format", fonte.Format,
			"url", redactURL(fonte.URL),
			"duration", duration)
	}, nil
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

func calculateBackoff(attempt int, cfg *sdk.RetryConfig) time.Duration {
	backoff := time.Duration(math.Pow(2, float64(attempt))) * cfg.InitialBackoff
	if backoff > cfg.MaxBackoff {
		backoff = cfg.MaxBackoff
	}

	jitter := time.Duration(rand.Int63n(int64(float64(backoff) * cfg.JitterFraction)))
	return backoff + jitter
}

func redactURL(urlStr string) string {
	u, err := url.Parse(urlStr)
	if err != nil {
		return "[invalid url]"
	}

	// Redact API keys in query params
	q := u.Query()
	keys := []string{"key", "api_key", "token", "auth", "password"}
	for _, k := range keys {
		if v := q.Get(k); v != "" {
			q.Set(k, "***")
		}
	}
	u.RawQuery = q.Encode()

	return u.String()
}

// Decoder abstraction
type Decoder interface {
	Next(ctx context.Context) (sdk.Envelope, error)
}

// NewDecoder builds a Decoder for fonte.Format reading from r.
// It returns nil if the format is not supported.
func NewDecoder(r io.Reader, fonte sdk.Fonte) Decoder {
	switch fonte.Format {
	case "csv":
		return &csvDecoder{r: csv.NewReader(r), noHeader: fonte.NoHeader}
	case "ndjson":
		return &ndjsonDecoder{dec: json.NewDecoder(r)}
	case "json":
		return &jsonDecoder{dec: json.NewDecoder(r)}
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

func (d *csvDecoder) Next(ctx context.Context) (sdk.Envelope, error) {
	if !d.noHeader && d.headers == nil {
		header, err := d.r.Read()
		if err != nil {
			return sdk.Envelope{}, err
		}
		d.headers = header
	}

	record, err := d.r.Read()
	if err != nil {
		return sdk.Envelope{}, err
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

	return sdk.Envelope{
		Payload: obj,
	}, nil
}

type ndjsonDecoder struct {
	dec *json.Decoder
}

func (d *ndjsonDecoder) Next(ctx context.Context) (sdk.Envelope, error) {
	var obj any
	if err := d.dec.Decode(&obj); err != nil {
		return sdk.Envelope{}, err
	}
	return sdk.Envelope{
		Payload: obj,
	}, nil
}

type jsonDecoder struct {
	dec    *json.Decoder
	inited bool
	arrays []any
	index  int
}

func (d *jsonDecoder) Next(ctx context.Context) (sdk.Envelope, error) {
	if !d.inited {
		var obj any
		if err := d.dec.Decode(&obj); err != nil {
			return sdk.Envelope{}, err
		}

		if arr, ok := obj.([]any); ok {
			d.arrays = arr
		} else {
			d.arrays = []any{obj}
		}
		d.inited = true
	}

	if d.index >= len(d.arrays) {
		return sdk.Envelope{}, io.EOF
	}

	env := sdk.Envelope{
		Payload: d.arrays[d.index],
	}
	d.index++
	return env, nil
}
