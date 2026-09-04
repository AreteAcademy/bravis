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
	return ComputeIngestionID(e.Provider, e.Entity, e.SourceKey, e.RecordTS)
}

// ComputeIngestionID is the frozen formula, and the only implementation.
//
// FROZEN, and deliberately not configurable. This namespace, the field order
// and the "|" separator together define ingestion_id. A row written here has
// to match the row a Python fetcher writes for the same record, and it does --
// checked against uuid.uuid5. Make any of the three a setting and the
// guarantee is gone, silently, for whoever changes it.
//
// It lives here, exported, because sdk.IngestionID -- the transformer that
// writes the column -- needs it, and there must be exactly one place that
// computes this. A fmt.Sprintf in a fetcher would look identical and produce a
// different id on the first float formatted differently.
func ComputeIngestionID(provider, entity, sourceKey, recordTS string) (string, error) {
	const ingestNS = "e3a4f8c0-1b9d-4ea0-9c2e-77f6a6c4a4d7"
	ns, err := uuid.Parse(ingestNS)
	if err != nil {
		return "", err
	}

	key := fmt.Sprintf("%s|%s|%s|%s", provider, entity, sourceKey, recordTS)
	return uuid.NewSHA1(ns, []byte(key)).String(), nil
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
	StagingBucket   string // GCS bucket for staging; default: "{projectID}-brevis-staging"
	StagingPrefix   string // prefix for staged files; default: "extracts/"
	ThresholdForGCS int    // row count above which to use GCS; default: 5000
	Format          string // "ndjson", "csv", or "parquet"; default: "ndjson"
	// KeepStagedFile leaves the staged object in the bucket after a
	// successful load. The default is to delete it: a bucket filling up with
	// files nobody looks at is a bill nobody reviews.
	//
	// This used to be DeleteAfterLoad, documented as defaulting to true --
	// which a bool cannot do. Anyone using load.New directly got the zero
	// value, false, and never cleaned up. The integration test found three
	// objects left behind, one per run.
	KeepStagedFile bool

	// Dedup selects how repeated records are handled; see Dedup.
	// Zero value is DedupNone.
	Dedup Dedup

	// CreateTable lets the loader create the destination table when it does
	// not exist. Off by default: nothing runs DDL against your warehouse
	// without being asked.
	//
	// It never alters an existing table: a table whose schema differs is an
	// error naming the difference. A loader that can ALTER or DROP is a
	// loader that can erase history.
	CreateTable bool

	// CreateSQL is the DDL to run instead of the built-in landing schema.
	// Only consulted when CreateTable is set and the table is absent.
	//
	// The SDK still checks afterwards that what your statement produced can
	// take the rows it writes -- a DDL that creates the wrong shape fails
	// here rather than on the first load.
	CreateSQL string

	// PartitionExpiration drops partitions older than this. Zero keeps them
	// forever, which is the default: deleting data is never something a
	// library should start doing on its own.
	PartitionExpiration time.Duration

	// RequirePartitionFilter makes BigQuery reject any query that does not
	// filter on the partition column, which stops an accidental full scan of
	// a landing table.
	//
	// Incompatible with DedupMerge, and New refuses the pair. The merge
	// matches on ingestion_id across every partition, and it cannot be scoped:
	// ingestion_loaded_at is the load time, so a re-run of the same record
	// lands in a different partition than the original. A partition filter
	// would make the merge miss the match and write the duplicate it exists
	// to prevent.
	RequirePartitionFilter bool

	// Columns declares the destination's columns, in DDL order, including
	// the two sdk.IngestionID and sdk.IngestionLoadedAt write. Nil declares
	// nothing.
	//
	// See sdk.Target.Columns for what it checks and when.
	Columns []string

	// ClusterBy names the columns the created table is clustered on. The SDK
	// cannot guess: it does not know your payload. Ignored when the table
	// already exists.
	ClusterBy []string

	// Provider and Entity label the created table, for cost attribution and
	// for answering "what writes here?" six months later. Taken from the
	// batch; not part of addressing.
	Provider string
	Entity   string
}

// Stats counts what an extract actually did.
//
// Pass a pointer in Source.Stats and it is filled in as the walk proceeds.
// Read it after the iteration ends: it is written by the goroutine doing the
// pulling, and it is only final once the stream is drained.
//
// It exists because Result.Pages and Result.Attempts have to describe what
// happened. A number in a result that is always zero is worse than no number,
// because nobody doubts it.
type Stats struct {
	// Pages fetched, including the first. Always at least 1 on success.
	Pages int

	// Attempts is every HTTP request made, retries included, across all
	// pages. Attempts above Pages means the source was flaky.
	Attempts int

	// Bytes read off the wire, across every page. This is the size of what
	// the source sent, not of what survived Transform -- the two differ, and
	// the one that explains a slow extract is this one.
	Bytes int64
}

// Format names the wire format of a response.
type Format string

const (
	FormatJSON   Format = "json"
	FormatNDJSON Format = "ndjson"
	FormatCSV    Format = "csv"
	FormatXML    Format = "xml"
)

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
	URL          string    // required
	Method       string    // default: GET
	Body         io.Reader // for POST/PUT
	Header       map[string][]string
	Timeout      time.Duration // per attempt; default: 30s
	TotalTimeout time.Duration // total; default: 5 minutes
	RetryConfig  *RetryConfig  // nil uses defaults
	RateLimiter  Limiter       // throttles each attempt; nil disables

	// Format of the response. Empty means FormatJSON.
	Format Format

	// Stats, when not nil, is filled in as the extract runs. See Stats.
	Stats *Stats

	// Preview prints the first N records once the extract finishes, the way
	// a dataframe's head() shows the top of a frame. Zero, the default,
	// prints nothing.
	//
	// It answers "what did I actually just pull?" without a debugger and
	// without draining the stream into a variable to look at it. The sample
	// is taken as the records stream past, so it costs N records of memory
	// and nothing else -- and it never changes what the consumer receives.
	Preview int

	// PreviewBytes caps the printed block. Zero uses 4096.
	//
	// Rows are dropped from the bottom until the block fits, and the footer
	// says how many were held back: a preview that quietly showed less than
	// it sampled would be lying about the sample.
	PreviewBytes int

	// PreviewWriter is where the table goes. Nil means os.Stderr.
	//
	// It is not routed through slog because slog's TextHandler escapes
	// newlines, so a table logged as an attribute arrives as one unreadable
	// line of \n. The counters do go through slog, where a structured number
	// belongs.
	PreviewWriter io.Writer

	// NoHeader, for CSV: treat every row as data with field_N keys. The
	// default uses the first row as column names. Ignored for other formats.
	NoHeader bool

	// Pagination. Exactly one strategy may be set; setting two is an error
	// rather than a silent precedence rule, because the loser would be a
	// field that does nothing.
	//
	//   FollowLinks  follow RFC 8288 Link headers with rel="next"
	//   CursorKey    name of the field in each JSON page holding the next
	//                cursor; it is sent back as a query parameter of the
	//                same name. Requires the page to be a JSON object.
	//   PageKey      query parameter holding the page NUMBER, incremented
	//                by one each page from FirstPage
	//   OffsetKey    query parameter holding the ROW OFFSET, advanced by
	//                PageSize rows each page
	//
	// PageKey and OffsetKey both stop at the first page that yields no rows;
	// a short page is not an end signal, because it can be a partially
	// filled one.
	//
	// DataKey names the field holding the rows when a paginated response
	// wraps them: {"results": [...], "next": "..."} needs DataKey
	// "results". Without it the whole page object becomes one Envelope.
	FollowLinks bool
	CursorKey   string
	PageKey     string
	OffsetKey   string
	DataKey     string

	// FirstPage is the number the first page carries, for PageKey. Zero
	// means one; a zero-indexed API puts "?page=0" in the URL, and a number
	// already in the URL wins over this field. Either way the first request
	// carries a number, so the server never picks a default of its own that
	// we would then guess wrong from.
	FirstPage int

	// PageSize is the number of rows OffsetKey advances by each page. Zero
	// uses the row count of the page just read. It has no effect on PageKey,
	// where pages advance by one regardless of how many rows they carry.
	PageSize int

	MaxPages int // safety stop on runaway pagination; 0 means 1000
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

// WithCreateSQL supplies the DDL to run instead of the built-in schema.
func WithCreateSQL(ddl string) LoadOption {
	return func(cfg *LoadConfig) {
		cfg.CreateSQL = ddl
	}
}

// WithPartitionExpiration drops partitions older than d. Zero keeps them.
func WithPartitionExpiration(d time.Duration) LoadOption {
	return func(cfg *LoadConfig) {
		cfg.PartitionExpiration = d
	}
}

// WithRequirePartitionFilter rejects queries that do not filter on the
// partition column. Incompatible with DedupMerge; see the field.
func WithRequirePartitionFilter(enabled bool) LoadOption {
	return func(cfg *LoadConfig) {
		cfg.RequirePartitionFilter = enabled
	}
}

// WithStagingPrefix sets where staged objects go inside the bucket.
func WithStagingPrefix(prefix string) LoadOption {
	return func(cfg *LoadConfig) {
		cfg.StagingPrefix = prefix
	}
}

// WithKeepStagedFile leaves the staged object in the bucket after a load.
func WithKeepStagedFile(keep bool) LoadOption {
	return func(cfg *LoadConfig) {
		cfg.KeepStagedFile = keep
	}
}

// WithColumns declares the destination's columns. See LoadConfig.Columns.
func WithColumns(columns []string) LoadOption {
	return func(cfg *LoadConfig) {
		cfg.Columns = columns
	}
}

// WithClusterBy names the columns a created table is clustered on.
func WithClusterBy(fields ...string) LoadOption {
	return func(cfg *LoadConfig) {
		cfg.ClusterBy = fields
	}
}
