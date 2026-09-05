// Package from holds the sources a pipeline reads.
//
// One type per origin, each carrying its own configuration and knowing how to
// read itself. Importing this package costs you the HTTP driver and nothing
// else -- Go prunes by package, so a fetcher that reads from Postgres never
// compiles the BigQuery client.
package from

import (
	"context"
	"io"
	"iter"
	"time"

	"github.com/AreteAcademy/brevis/sdk/extract"
	core "github.com/AreteAcademy/brevis/sdk/internal/core"
)

// HTTP reads from an HTTP API.
//
//	From: from.HTTP{
//		URL:     "https://api.open-meteo.com/v1/forecast?...",
//		Timeout: 15 * time.Second,
//		Records: func(r sdk.Response) ([]any, error) { ... },
//	}
//
// Everything a fetcher needs to say about an HTTP source is here: where, how
// to ask, how to survive a flaky upstream, how to page, and what a response
// means. A source that is not HTTP has none of these fields, which is the
// point of the type existing.
type HTTP struct {
	URL          string    // required
	Method       string    // default: GET
	Body         io.Reader // for POST/PUT
	Header       map[string][]string
	Timeout      time.Duration     // per attempt; default: 30s
	TotalTimeout time.Duration     // total; default: 5 minutes
	RetryConfig  *core.RetryConfig // nil uses defaults
	RateLimiter  core.Limiter      // throttles each attempt; nil disables

	// Format of the response. Empty means FormatJSON.
	Format core.Format

	// Records decides what each successful response means -- the records it
	// carries, or a refusal saying why. Nil decodes the body and treats each
	// document as one record. See core.Reading.
	//
	// It lives here, with the rest of what describes an HTTP source, because
	// a Postgres source has no Response to be handed one.
	Records core.Reading

	// Auth is how this source authenticates, and what keeps the credential
	// alive. A static key needs none of it -- put it in Header and be done.
	//
	// What it buys is the two things consumers were writing by hand: caching
	// a login so the API is not asked for a token once per run, and renewing
	// a session that would otherwise expire in silence.
	//
	//	Auth: &from.Credential{
	//	    Value: from.FromEnv("APP_SESSION_COOKIE"),
	//	    Apply: from.AsCookie,
	//	    Refresh: &from.Refresh{
	//	        URL:       "https://api.example.com/auth/session",
	//	        ExpiresAt: from.JSONField("expires"),
	//	        WarnAfter: 7 * 24 * time.Hour,
	//	    },
	//	}
	Auth *Credential

	// NoHeader, for CSV: treat every row as data with field_N keys.
	NoHeader bool

	// PreserveNumbers entrega os números JSON como json.Number, com o literal
	// intacto. Ligue quando a identidade depender da forma do número -- ver
	// sdk.IngestionIDPython.
	PreserveNumbers bool

	// Pagination. Exactly one strategy may be set; two is an error, because
	// the loser would be a field that was set and does nothing.
	//
	//	FollowLinks: true              // Link: <...>; rel="next"
	//	CursorKey:   "next_cursor"     // the page carries the next cursor
	//	PageKey:     "page"            // ?page=1, then 2, then 3
	//	OffsetKey:   "offset"          // ?offset=0, then PageSize, then 2x
	//
	// MaxPages caps the walk.
	FollowLinks bool
	CursorKey   string
	OffsetKey   string
	DataKey     string
	MaxPages    int

	// MoreKey e o caminho, separado por pontos, para um booleano na resposta
	// que diz se ha proxima pagina -- "pageMeta.hasNextPage". Falso encerra.
	//
	// Nao e uma estrategia e sim um CRITERIO DE PARADA: combina com qualquer
	// uma das quatro. Sem ele a parada e a pagina vazia, o que custa uma
	// requisicao a mais por origem -- e num fan-out de centenas de origens
	// isso e centenas de requisicoes por execucao.
	//
	// A parada por pagina vazia continua valendo como rede de seguranca: uma
	// API que mente no campo nao pode virar laco infinito.
	MoreKey string

	// PageKey is the query parameter holding the page NUMBER. It advances by
	// one page at a time and ignores PageSize.
	//
	// Before this existed the way to paginate by page number was OffsetKey
	// with PageSize 1, which worked by accident: PageSize is the offset
	// increment, so 1 made the "offset" count pages. Use PageKey.
	PageKey string

	// FirstPage numbers the first page, for PageKey. Zero means one, so a
	// zero-indexed API says so in the URL instead -- "…?page=0" -- and a
	// number already in the URL always wins over this field.
	//
	// One of the two always goes on the first request, so the server never
	// picks a default of its own that the SDK would then guess wrong from:
	// guessing wrong skips a whole page of rows in silence.
	FirstPage int

	// PageSize is how many rows OffsetKey advances by each page. Zero uses
	// the number of rows the last page returned.
	PageSize int
}

// Read satisfies core.Reader.
func (h HTTP) Read(ctx context.Context, opt core.ReadOptions) (iter.Seq2[core.Envelope, error], error) {
	source := h.source(opt)

	switch source.Format {
	case "", core.FormatJSON:
		source.Format = core.FormatJSON
		return extract.JSON(ctx, source, h.Records)
	case core.FormatNDJSON:
		return extract.NDJSON(ctx, source, h.Records)
	case core.FormatCSV:
		return extract.CSV(ctx, source, h.Records)
	case core.FormatXML:
		return extract.XML(ctx, source, h.Records)
	default:
		return nil, core.Reject("unknown format %q; use JSON, NDJSON, CSV or XML", source.Format)
	}
}

// Describe satisfies core.Reader, with the query string's secrets redacted.
func (h HTTP) Describe() string { return extract.Redact(h.URL) }

// source folds the driver's fields and the cross-cutting options into the one
// struct the extract package takes.
func (h HTTP) source(opt core.ReadOptions) core.Source {
	return core.Source{
		URL:             h.URL,
		Method:          h.Method,
		Body:            h.Body,
		Header:          h.Header,
		Timeout:         h.Timeout,
		TotalTimeout:    h.TotalTimeout,
		RetryConfig:     h.RetryConfig,
		RateLimiter:     h.RateLimiter,
		Format:          h.Format,
		Auth:            h.Auth,
		NoHeader:        h.NoHeader,
		PreserveNumbers: h.PreserveNumbers,
		FollowLinks:     h.FollowLinks,
		CursorKey:       h.CursorKey,
		PageKey:         h.PageKey,
		FirstPage:       h.FirstPage,
		OffsetKey:       h.OffsetKey,
		DataKey:         h.DataKey,
		PageSize:        h.PageSize,
		MaxPages:        h.MaxPages,
		Stats:           opt.Stats,
		Preview:         opt.Preview,
		PreviewBytes:    opt.PreviewBytes,
		PreviewWriter:   opt.PreviewWriter,
	}
}
