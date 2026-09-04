package sdk

import (
	core "github.com/AreteAcademy/bravis/sdk/internal/core"
)

// The shared types live in an internal package so that sdk/extract and
// sdk/load can use them without importing this one -- this package imports
// both to offer Extract and Load side by side, and Go would reject the cycle.
//
// These are aliases, not definitions: sdk.Envelope and extract's Envelope are
// the same type, so values pass between the packages freely.

type (
	// Envelope is one extracted record plus its provenance.
	Envelope = core.Envelope

	// Response is one successful HTTP response, handed to Pipeline.Records.
	Response = core.Response

	// Reading decides what a successful response means: the records it
	// carries, or a refusal saying why. See from.HTTP.Records.
	Reading = core.Reading

	// Reader is a source: from.HTTP, and the drivers that follow it.
	Reader = core.Reader

	// Writer is a destination: to.BigQuery, and the drivers that follow it.
	Writer = core.Writer

	// ReadOptions and WriteOptions are what every driver honours, whatever it
	// reads from or writes to. A fetcher does not build these -- Source and
	// Target do.
	ReadOptions  = core.ReadOptions
	WriteOptions = core.WriteOptions

	// Rejection is what Reject returns: the source answered, and what it
	// sent is not data.
	Rejection = core.Rejection

	// RetryConfig tunes the backoff between attempts.
	RetryConfig = core.RetryConfig

	// Limiter throttles outbound requests; *rate.Limiter satisfies it.
	Limiter = core.Limiter

	// LoadConfig is the low-level loader configuration. Prefer Target.
	LoadConfig = core.LoadConfig

	// LoadOption configures a LoadConfig.
	LoadOption = core.LoadOption

	// LoadResult is the low-level load outcome. Prefer Result.
	LoadResult = core.LoadResult

	// Format names the wire format of a response.
	Format = core.Format

	// Stats counts what an extract actually did.
	Stats = core.Stats

	// Dedup names how a load avoids writing a record twice.
	Dedup = core.Dedup
)

// Bool returns a pointer to b, for the tri-state options where nil means
// "not set" and has to be told apart from false.
//
//	Target{CreateTable: sdk.Bool(false)}   // never, not even on a first run
func Bool(b bool) *bool { return &b }

// Wire formats accepted by Source.Format.
const (
	FormatJSON   = core.FormatJSON
	FormatNDJSON = core.FormatNDJSON
	FormatCSV    = core.FormatCSV
	FormatXML    = core.FormatXML
)

// Deduplication modes accepted by Target.Dedup.
const (
	// DedupNone appends; the bronze layer deduplicates on ingestion_id.
	DedupNone = core.DedupNone

	// DedupMerge stages and MERGEs on ingestion_id, so a re-run is a no-op.
	// It costs one scan of the destination per load.
	DedupMerge = core.DedupMerge
)

// Reject refuses a response, or a record, saying why. See core.Reject.
//
//	return nil, sdk.Reject("open-meteo refused: %v", doc["reason"])
var Reject = core.Reject

// ErrRejected marks an error as "the source answered, and what it sent is not
// data". Test for it with errors.Is:
//
//	if errors.Is(err, sdk.ErrRejected) { ... }
var ErrRejected = core.ErrRejected

// Low-level load options, re-exported. Target covers the common cases.
var (
	WithProjectID              = core.WithProjectID
	WithDataset                = core.WithDataset
	WithTable                  = core.WithTable
	WithStagingBucket          = core.WithStagingBucket
	WithStagingPrefix          = core.WithStagingPrefix
	WithKeepStagedFile         = core.WithKeepStagedFile
	WithFormat                 = core.WithFormat
	WithThresholdForGCS        = core.WithThresholdForGCS
	WithColumns                = core.WithColumns
	WithClusterBy              = core.WithClusterBy
	WithCreateTable            = core.WithCreateTable
	WithCreateSQL              = core.WithCreateSQL
	WithPartitionExpiration    = core.WithPartitionExpiration
	WithRequirePartitionFilter = core.WithRequirePartitionFilter
	WithDedup                  = core.WithDedup
)
