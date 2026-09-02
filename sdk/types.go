package sdk

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

// LoadResult reports the outcome of a load operation.
type LoadResult struct {
	RowsLoaded  int64         // number of rows written to BigQuery
	BytesStaged int64         // number of bytes in the staging format
	Duration    time.Duration // total time from start to finish
	Strategy    string        // "inline" or "gcs"
	Format      string        // "ndjson", "csv", or "parquet"
	ErrorRows   []string      // error descriptions from BigQuery per row (truncated)
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
	ProjectID         string // GCP project ID; required
	Dataset           string // BigQuery dataset; required
	Table             string // BigQuery table; required
	StagingBucket     string // GCS bucket for staging; default: "{projectID}-bravis-staging"
	StagingPrefix     string // prefix for staged files; default: "extracts/"
	ThresholdForGCS   int    // row count above which to use GCS; default: 5000
	Format            string // "ndjson", "csv", or "parquet"; default: "ndjson"
	DeleteAfterLoad   bool   // delete staged file after successful load; default: true
	AddMetadata       bool   // add metadata fields to payload; default: false
	MetadataNamespace string // namespace UUID for ingestion_id; default: "e3a4f8c0-1b9d-4ea0-9c2e-77f6a6c4a4d7"
	SourceKeyField    string // which field in payload contains the source key; if empty, uses Envelope.SourceKey
}

// ExtractOption is a functional option for extract.Fonte.
type ExtractOption func(*Fonte)

// Limiter throttles outbound requests. It is satisfied by
// *golang.org/x/time/rate.Limiter, so callers can pass one directly without
// the SDK taking on the dependency:
//
//	fonte.RateLimiter = rate.NewLimiter(rate.Every(time.Second), 1)
//
// Any type with a matching Wait works too.
type Limiter interface {
	Wait(ctx context.Context) error
}

// Fonte describes the source for extraction.
type Fonte struct {
	URL          string    // required
	Method       string    // default: GET
	Body         io.Reader // for POST/PUT
	Header       map[string][]string
	Timeout      time.Duration // per attempt; default: 30s
	TotalTimeout time.Duration // total; default: 5 minutes
	RetryConfig  *RetryConfig  // nil uses defaults
	RateLimiter  Limiter       // throttles each attempt; nil disables
	Guard        func(status int, body []byte) error
	Format       string // "json", "ndjson", "csv", "xml"; auto-detected if omitted
	NoHeader     bool   // for CSV: treat every row as data with field_N keys.
	// Default (false) uses the first row as column names. Ignored for other formats.
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
