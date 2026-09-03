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

	// Source describes where and how to extract.
	Source = core.Source

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

	// Driver selects which implementation carries out an extract or a load.
	Driver = core.Driver

	// Stats counts what an extract actually did.
	Stats = core.Stats

	// Dedup names how a load avoids writing a record twice.
	Dedup = core.Dedup
)

// Drivers. One of each exists today; an empty Driver takes the default for
// its side, so nothing has to be set for the common case.
const (
	// DriverHTTP fetches over HTTP. The default for a Source.
	DriverHTTP = core.DriverHTTP

	// DriverBigQuery writes to BigQuery. The default for a Target.
	DriverBigQuery = core.DriverBigQuery
)

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

// Low-level load options, re-exported. Target covers the common cases.
var (
	WithProjectID              = core.WithProjectID
	WithDataset                = core.WithDataset
	WithTable                  = core.WithTable
	WithStagingBucket          = core.WithStagingBucket
	WithFormat                 = core.WithFormat
	WithThresholdForGCS        = core.WithThresholdForGCS
	WithExtraMetadata          = core.WithExtraMetadata
	WithClusterBy              = core.WithClusterBy
	WithDriver                 = core.WithDriver
	WithCreateTable            = core.WithCreateTable
	WithCreateSQL              = core.WithCreateSQL
	WithPartitionExpiration    = core.WithPartitionExpiration
	WithRequirePartitionFilter = core.WithRequirePartitionFilter
	WithDedup                  = core.WithDedup
)
