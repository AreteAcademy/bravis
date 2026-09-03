package core

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"
)

// Envelope is the contract between extract and load.
// It represents a single record extracted from a source.
type Envelope struct {
	Provider  string // identifies the data provider (e.g., "example_gov", "api_platform")
	Entity    string // identifies the entity type (e.g., "transactions", "users")
	SourceKey string // unique key from the source; empty is an error
	RecordTS  string // timestamp when the record was created at source; must be set before Load
	Payload   any    // the actual data; will become the JSON column
}

// IngestionID returns the deterministic UUID v5 for this envelope.
// This is the only place that computes the UUID; it's the idempotency key.
//
// The UUID is computed from:
//   - namespace: fixed UUID for ingestion operations
//   - key: "provider|entity|source_key|record_ts" (pipe-separated)
//
// Changing this function breaks idempotency with all prior loads.
func (e *Envelope) IngestionID() (string, error) {
	if e.SourceKey == "" {
		return "", fmt.Errorf("SourceKey cannot be empty")
	}

	const ingestNS = "e3a4f8c0-1b9d-4ea0-9c2e-77f6a6c4a4d7"
	ns, err := uuid.Parse(ingestNS)
	if err != nil {
		return "", err
	}

	key := fmt.Sprintf("%s|%s|%s|%s", e.Provider, e.Entity, e.SourceKey, e.RecordTS)
	id := uuid.NewSHA1(ns, []byte(key))
	return id.String(), nil
}

// Dedup names how a load avoids writing a record twice.
type Dedup string

const (
	// DedupNone appends. Two runs over the same window write the same rows
	// twice, and the bronze layer deduplicates on ingestion_id. This is the
	// default because it is the only mode that costs nothing.
	DedupNone Dedup = "none"

	// DedupMerge stages into a temporary table and MERGEs on ingestion_id, so
	// a re-run is a no-op. It costs one scan of the destination per load, so
	// it is opt-in: nobody should pay for a scan they did not ask for.
	DedupMerge Dedup = "merge"
)

// LoadResult reports the outcome of a load operation.
type LoadResult struct {
	RowsLoaded   int64         // number of rows written to BigQuery
	BytesStaged  int64         // number of bytes in the staging format
	Duration     time.Duration // total time from start to finish
	Strategy     string        // "inline" or "gcs"
	Format       string        // the format actually written
	Dedup        Dedup         // the deduplication that actually ran
	RowsIgnored  int64         // rows MERGE matched as already present; 0 unless DedupMerge
	TableCreated bool          // whether this load created the destination table
	ErrorRows    []string      // error descriptions from BigQuery per row (truncated)
}

// RetryConfig controls retry behavior for extract.
type RetryConfig struct {
	MaxAttempts    int           // default: 3
	InitialBackoff time.Duration // default: 1s
	MaxBackoff     time.Duration // default: 60s
	JitterFraction float64       // [0, 1]; default: 0.1
}

// LoadConfig controls load behavior.
type LoadConfig struct {
	ProjectID       string // GCP project ID; required
	Dataset         string // BigQuery dataset; required
	Table           string // BigQuery table; required
	StagingBucket   string // GCS bucket for staging; default: "{projectID}-bravis-staging"
	StagingPrefix   string // prefix for staged files; default: "extracts/"
	ThresholdForGCS int    // row count above which to use GCS; default: 5000
	Format          string // "ndjson", "csv", or "parquet"; default: "ndjson"
	DeleteAfterLoad bool   // delete staged file after successful load; default: true
	AddMetadata     bool   // fold _bravis_* fields into the payload; default: false

	// Driver selects the destination. Empty means DriverBigQuery, the only
	// one implemented today.
	Driver Driver

	// Dedup selects how repeated records are handled; see Dedup.
	// Zero value is DedupNone.
	Dedup Dedup

	// CreateTable lets the loader create the destination table when it does
	// not exist. Only possible alongside WriteEnvelopeColumns, since that is
	// the only case where the SDK knows the schema.
	//
	// It never alters an existing table: a table whose schema differs is an
	// error naming the difference. A loader that can ALTER or DROP is a
	// loader that can erase history.
	CreateTable bool

	// WriteEnvelopeColumns wraps each payload in the six-column landing
	// contract instead of writing it flat:
	//
	//	ingestion_id, ingestion_loaded_at, provider, entity, source_key, payload
	//
	// Off by default -- the SDK stays schema-agnostic, and callers who want
	// the contract ask for it. Turning it on is what keeps a single owner for
	// ingestion_id, so a row written here matches the row a Python fetcher
	// writes for the same record. Mutually exclusive with AddMetadata.
	WriteEnvelopeColumns bool
	MetadataNamespace    string // namespace UUID for ingestion_id; default: "e3a4f8c0-1b9d-4ea0-9c2e-77f6a6c4a4d7"
	SourceKeyField       string // which field in payload contains the source key; if empty, uses Envelope.SourceKey
}

// Driver selects which implementation carries out an extract or a load.
//
// Only one of each exists today; the type is here so that adding a second
// does not change any signature. An empty Driver takes the default for its
// side, so nothing has to be set for the common case.
type Driver string

const (
	// DriverHTTP fetches over HTTP. The default for a Source.
	DriverHTTP Driver = "http"

	// DriverBigQuery writes to BigQuery. The default for a Target.
	DriverBigQuery Driver = "bigquery"
)

// Format names the wire format of a response.
type Format string

const (
	FormatJSON   Format = "json"
	FormatNDJSON Format = "ndjson"
	FormatCSV    Format = "csv"
	FormatXML    Format = "xml"
)

// ExtractOption is a functional option for a Source.
type ExtractOption func(*Source)

// Limiter throttles outbound requests. It is satisfied by
// *golang.org/x/time/rate.Limiter, so callers can pass one directly without
// the SDK taking on the dependency:
//
//	source.RateLimiter = rate.NewLimiter(rate.Every(time.Second), 1)
//
// Any type with a matching Wait works too.
type Limiter interface {
	Wait(ctx context.Context) error
}

// Source describes where and how to extract.
type Source struct {
	// Driver selects the transport. Empty means DriverHTTP, the only one
	// implemented today.
	Driver Driver

	URL          string    // required
	Method       string    // default: GET
	Body         io.Reader // for POST/PUT
	Header       map[string][]string
	Timeout      time.Duration // per attempt; default: 30s
	TotalTimeout time.Duration // total; default: 5 minutes
	RetryConfig  *RetryConfig  // nil uses defaults
	RateLimiter  Limiter       // throttles each attempt; nil disables

	// Guard inspects a 200 before it is decoded, so a body that is not data
	// fails loudly instead of landing in the warehouse. See RejectIf.
	Guard func(status int, body []byte) error

	// Format of the response. Empty means FormatJSON.
	Format Format

	// Expand turns one decoded document into the records it holds, for the
	// common case of an API that wraps its readings. Nil means each decoded
	// document is one record. See ParallelArrays and ArrayAt.
	Expand func(payload any) ([]any, error)

	// NoHeader, for CSV: treat every row as data with field_N keys. The
	// default uses the first row as column names. Ignored for other formats.
	NoHeader bool

	// Pagination. At most one strategy applies, checked in this order:
	//
	//   FollowLinks  follow RFC 8288 Link headers with rel="next"
	//   CursorKey    name of the field in each JSON page holding the next
	//                cursor; it is sent back as a query parameter of the
	//                same name. Requires the page to be a JSON object.
	//   OffsetKey    query parameter advanced by PageSize each page,
	//                stopping at the first page that yields no rows
	//
	// DataKey names the field holding the rows when a paginated response
	// wraps them: {"results": [...], "next": "..."} needs DataKey
	// "results". Without it the whole page object becomes one Envelope.
	FollowLinks bool
	CursorKey   string
	OffsetKey   string
	DataKey     string
	PageSize    int
	MaxPages    int // safety stop on runaway pagination; 0 means 1000
}

// LoadOption is a functional option for load.
type LoadOption func(*LoadConfig)

// WithProjectID sets the GCP project for BigQuery operations.
func WithProjectID(id string) LoadOption {
	return func(cfg *LoadConfig) {
		cfg.ProjectID = id
	}
}

// WithDataset sets the target BigQuery dataset.
func WithDataset(name string) LoadOption {
	return func(cfg *LoadConfig) {
		cfg.Dataset = name
	}
}

// WithTable sets the target BigQuery table.
func WithTable(name string) LoadOption {
	return func(cfg *LoadConfig) {
		cfg.Table = name
	}
}

// WithStagingBucket sets the GCS bucket for staging.
func WithStagingBucket(bucket string) LoadOption {
	return func(cfg *LoadConfig) {
		cfg.StagingBucket = bucket
	}
}

// WithFormat sets the staging format: ndjson, csv, or parquet.
func WithFormat(format string) LoadOption {
	return func(cfg *LoadConfig) {
		cfg.Format = format
	}
}

// WithThresholdForGCS sets the row count above which to use GCS staging.
func WithThresholdForGCS(threshold int) LoadOption {
	return func(cfg *LoadConfig) {
		cfg.ThresholdForGCS = threshold
	}
}

// WithDriver selects the destination implementation.
func WithDriver(d Driver) LoadOption {
	return func(cfg *LoadConfig) {
		cfg.Driver = d
	}
}

// WithDedup selects the deduplication mode.
func WithDedup(d Dedup) LoadOption {
	return func(cfg *LoadConfig) {
		cfg.Dedup = d
	}
}

// WithCreateTable lets the loader create the landing table when absent.
func WithCreateTable(enabled bool) LoadOption {
	return func(cfg *LoadConfig) {
		cfg.CreateTable = enabled
	}
}

// WithEnvelopeColumns writes the six-column landing contract instead of a
// flat payload. See LoadConfig.WriteEnvelopeColumns.
func WithEnvelopeColumns(enabled bool) LoadOption {
	return func(cfg *LoadConfig) {
		cfg.WriteEnvelopeColumns = enabled
	}
}

// WithMetadata enables adding metadata fields to payloads.
func WithMetadata(enabled bool) LoadOption {
	return func(cfg *LoadConfig) {
		cfg.AddMetadata = enabled
	}
}

// WithMetadataNamespace sets the UUID namespace for ingestion IDs.
func WithMetadataNamespace(ns string) LoadOption {
	return func(cfg *LoadConfig) {
		cfg.MetadataNamespace = ns
	}
}
