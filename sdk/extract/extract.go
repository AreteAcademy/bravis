package extract

import (
	"context"
	"encoding/csv"
	"encoding/json"
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

	return func(yield func(sdk.Envelope, error) bool) {
		startTime := time.Now()
		ctx, cancel := context.WithTimeout(ctx, fonte.TotalTimeout)
		defer cancel()

		client := &http.Client{Timeout: fonte.Timeout}

		req, err := http.NewRequestWithContext(ctx, fonte.Method, fonte.URL, fonte.Body)
		if err != nil {
			yield(sdk.Envelope{}, fmt.Errorf("create request: %w", err))
			return
		}

		if fonte.Header != nil {
			req.Header = fonte.Header
		}

		var resp *http.Response
		for attempt := 0; attempt < fonte.RetryConfig.MaxAttempts; attempt++ {
			resp, err = client.Do(req)
			if err == nil {
				break
			}

			if shouldRetry(err) && attempt < fonte.RetryConfig.MaxAttempts-1 {
				backoff := calculateBackoff(attempt, fonte.RetryConfig)
				slog.DebugContext(ctx, "retry",
					"attempt", attempt+1,
					"backoff", backoff,
					"error", err)
				time.Sleep(backoff)
				continue
			}

			yield(sdk.Envelope{}, fmt.Errorf("fetch failed after %d attempts: %w", attempt+1, err))
			return
		}

		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			if shouldRetryStatus(resp.StatusCode) && resp.StatusCode != http.StatusTooManyRequests {
				// Implementar retry com Retry-After
			}
			body, _ := io.ReadAll(resp.Body)
			yield(sdk.Envelope{}, fmt.Errorf("http %d: %s", resp.StatusCode, string(body)))
			return
		}

		// Guard function
		if fonte.Guard != nil {
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				yield(sdk.Envelope{}, fmt.Errorf("read body for guard: %w", err))
				return
			}
			if err := fonte.Guard(resp.StatusCode, body); err != nil {
				yield(sdk.Envelope{}, fmt.Errorf("guard rejected response: %w", err))
				return
			}
			// Create new reader from read body
			resp.Body = io.NopCloser(strings.NewReader(string(body)))
		}

		// Decode based on format
		decoder := NewDecoder(resp.Body, fonte.Format)
		if decoder == nil {
			yield(sdk.Envelope{}, fmt.Errorf("unsupported format: %s", fonte.Format))
			return
		}

		for {
			env, err := decoder.Next(ctx)
			if err == io.EOF {
				break
			}
			if err != nil {
				if !yield(sdk.Envelope{}, err) {
					return
				}
				continue
			}

			if !yield(env, nil) {
				return
			}
		}

		duration := time.Since(startTime)
		slog.InfoContext(ctx, "extract complete",
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

func NewDecoder(r io.Reader, format string) Decoder {
	switch format {
	case "csv":
		return &csvDecoder{r: csv.NewReader(r)}
	case "ndjson":
		return &ndjsonDecoder{dec: json.NewDecoder(r)}
	case "json":
		return &jsonDecoder{dec: json.NewDecoder(r)}
	default:
		return nil
	}
}

type csvDecoder struct {
	r       *csv.Reader
	headers []string
	first   bool
}

func (d *csvDecoder) Next(ctx context.Context) (sdk.Envelope, error) {
	if !d.first {
		record, err := d.r.Read()
		if err != nil {
			return sdk.Envelope{}, err
		}
		d.headers = record
		d.first = true
	}

	record, err := d.r.Read()
	if err != nil {
		return sdk.Envelope{}, err
	}

	// Convert CSV record to JSON object
	obj := make(map[string]string)
	for i, header := range d.headers {
		if i < len(record) {
			obj[header] = record[i]
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
