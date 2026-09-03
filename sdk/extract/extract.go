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
	"net/url"
	"strconv"
	"strings"
	"time"

	core "github.com/AreteAcademy/bravis/sdk/internal/core"
)

// CSV fetches and decodes CSV data from the given source.
// It returns an iterator of Envelopes, one per CSV row.
func CSV(ctx context.Context, fonte core.Fonte) (iter.Seq2[core.Envelope, error], error) {
	if fonte.Format == "" {
		fonte.Format = "csv"
	}
	return fetch(ctx, fonte)
}

// NDJSON fetches and decodes newline-delimited JSON.
func NDJSON(ctx context.Context, fonte core.Fonte) (iter.Seq2[core.Envelope, error], error) {
	if fonte.Format == "" {
		fonte.Format = "ndjson"
	}
	return fetch(ctx, fonte)
}

// JSON fetches and decodes a JSON array or object stream.
func JSON(ctx context.Context, fonte core.Fonte) (iter.Seq2[core.Envelope, error], error) {
	if fonte.Format == "" {
		fonte.Format = "json"
	}
	return fetch(ctx, fonte)
}

// XML fetches and decodes XML data.
func XML(ctx context.Context, fonte core.Fonte) (iter.Seq2[core.Envelope, error], error) {
	if fonte.Format == "" {
		fonte.Format = "xml"
	}
	return fetch(ctx, fonte)
}

func fetch(ctx context.Context, fonte core.Fonte) (iter.Seq2[core.Envelope, error], error) {
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
		fonte.RetryConfig = &core.RetryConfig{
			MaxAttempts:    3,
			InitialBackoff: 1 * time.Second,
			MaxBackoff:     60 * time.Second,
			JitterFraction: 0.1,
		}
	}

	if fonte.RetryConfig.MaxAttempts < 1 {
		fonte.RetryConfig.MaxAttempts = 1
	}

	if fonte.MaxPages <= 0 {
		fonte.MaxPages = defaultMaxPages
	}

	ctxTotal, cancelTotal := context.WithTimeout(ctx, fonte.TotalTimeout)

	// The first page is fetched eagerly so that an unreachable host, a 404 or
	// a guard rejection surfaces as an error from CSV/JSON/NDJSON/XML rather
	// than as the first item of a sequence the caller has to drain to notice.
	first, err := fetchPage(ctxTotal, fonte, fonte.URL)
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
		// A cursor that stops advancing is an infinite loop, and government
		// APIs do return the same token forever at the end of a collection.
		// MaxPages would eventually stop it, but only after MaxPages wasted
		// requests; catching the repeat ends it on the next one.
		vistos := map[string]bool{}

		for {
			pages++
			emitted, next, err := drainPage(ctxTotal, fonte, page, yield)
			rows += emitted
			page.close()

			if err != nil {
				if !errors.Is(err, errStopped) {
					yield(core.Envelope{}, err)
				}
				return
			}

			if next == "" || pages >= fonte.MaxPages {
				break
			}

			if vistos[next] {
				slog.WarnContext(ctxTotal, "paginação parou: a fonte repetiu a página",
					"pagina", pages, "url", redactURL(next))
				break
			}
			vistos[next] = true

			page, err = fetchPage(ctxTotal, fonte, next)
			if err != nil {
				yield(core.Envelope{}, fmt.Errorf("page %d: %w", pages+1, err))
				return
			}
		}

		slog.InfoContext(ctxTotal, "extract complete",
			"format", fonte.Format,
			"url", redactURL(fonte.URL),
			"pages", pages,
			"rows", rows,
			"duration", time.Since(startTime))
	}, nil
}

const defaultMaxPages = 1000

// errStopped signals that the consumer broke out of the range loop. It is
// never handed to the caller -- there is nobody left to hand it to.
var errStopped = errors.New("consumer stopped iterating")

// page is one fetched HTTP response, plus whatever had to be buffered to
// work out where the next page lives.
type page struct {
	body     io.ReadCloser
	linkNext string // rel="next" from the Link header
	cursor   string // value at fonte.CursorKey, when cursor paging
	offset   int    // offset this page was fetched at, for offset paging
	release  func()
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
func drainPage(ctx context.Context, fonte core.Fonte, p *page, yield func(core.Envelope, error) bool) (int, string, error) {
	decoder := NewDecoder(p.body, fonte)
	if decoder == nil {
		return 0, "", fmt.Errorf("unsupported format: %s", fonte.Format)
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

	next, err := nextPageURL(fonte, p, emitted)
	return emitted, next, err
}

// nextPageURL resolves where the following page lives, or "" when the current
// page was the last one.
func nextPageURL(fonte core.Fonte, p *page, emitted int) (string, error) {
	switch {
	case fonte.FollowLinks:
		return p.linkNext, nil

	case fonte.CursorKey != "":
		if p.cursor == "" {
			return "", nil
		}
		return withQuery(fonte.URL, fonte.CursorKey, p.cursor)

	case fonte.OffsetKey != "":
		// An empty page is the only reliable end-of-data signal for offset
		// paging; a short page can just be a partially filled one.
		if emitted == 0 {
			return "", nil
		}
		size := fonte.PageSize
		if size <= 0 {
			size = emitted
		}
		return withQuery(fonte.URL, fonte.OffsetKey, strconv.Itoa(p.offset+size))
	}

	return "", nil
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
func fetchPage(ctxTotal context.Context, fonte core.Fonte, pageURL string) (*page, error) {
	var resp *http.Response
	// release cancels the context of the attempt that produced resp. The body
	// is still streaming under that context, so it must stay alive until the
	// page is drained -- cancelling it early truncates the response.
	release := func() {}

	for attempt := 0; attempt < fonte.RetryConfig.MaxAttempts; attempt++ {
		if fonte.RateLimiter != nil {
			if err := fonte.RateLimiter.Wait(ctxTotal); err != nil {
				return nil, fmt.Errorf("rate limiter: %w", err)
			}
		}

		ctxAttempt, cancelAttempt := context.WithTimeout(ctxTotal, fonte.Timeout)

		client := &http.Client{Timeout: fonte.Timeout}

		req, err := http.NewRequestWithContext(ctxAttempt, fonte.Method, pageURL, fonte.Body)
		if err != nil {
			cancelAttempt()
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
			return nil, fmt.Errorf("fetch failed after %d attempts: %w", attempt+1, err)
		}

		if resp.StatusCode == http.StatusOK {
			release = cancelAttempt
			break
		}

		if shouldRetryStatus(resp.StatusCode) && attempt < fonte.RetryConfig.MaxAttempts-1 {
			backoff := retryAfter(resp, attempt, fonte.RetryConfig)
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

	p := &page{body: resp.Body, release: release}

	if fonte.FollowLinks {
		p.linkNext = parseLinkNext(resp.Header.Get("Link"))
	}
	if fonte.OffsetKey != "" {
		p.offset = currentOffset(resp.Request, fonte.OffsetKey)
	}

	// Anything that must see the whole body reads it here and hands the
	// decoder an equivalent reader.
	if fonte.Guard != nil || fonte.CursorKey != "" || fonte.DataKey != "" {
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			p.close()
			return nil, fmt.Errorf("read body: %w", err)
		}
		_ = resp.Body.Close()

		if fonte.Guard != nil {
			if err := fonte.Guard(resp.StatusCode, body); err != nil {
				p.body = nil
				p.close()
				return nil, fmt.Errorf("guard rejected response: %w", err)
			}
		}

		if fonte.CursorKey != "" || fonte.DataKey != "" {
			cursor, rows, err := unwrapPage(body, fonte.CursorKey, fonte.DataKey)
			if err != nil {
				p.body = nil
				p.close()
				return nil, err
			}
			p.cursor = cursor
			body = rows
		}

		p.body = io.NopCloser(bytes.NewReader(body))
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
	Next(ctx context.Context) (core.Envelope, error)
}

// NewDecoder builds a Decoder for fonte.Format reading from r.
// It returns nil if the format is not supported.
func NewDecoder(r io.Reader, fonte core.Fonte) Decoder {
	switch fonte.Format {
	case "csv":
		return &csvDecoder{r: csv.NewReader(r), noHeader: fonte.NoHeader}
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
