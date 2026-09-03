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

	// Fonte describes where and how to extract.
	Fonte = core.Fonte

	// RetryConfig tunes the backoff between attempts.
	RetryConfig = core.RetryConfig

	// Limiter throttles outbound requests; *rate.Limiter satisfies it.
	Limiter = core.Limiter

	// LoadConfig is the low-level loader configuration. Prefer Destino.
	LoadConfig = core.LoadConfig

	// LoadOption configures a LoadConfig.
	LoadOption = core.LoadOption

	// LoadResult is the low-level load outcome. Prefer Resultado.
	LoadResult = core.LoadResult

	// Formato names the wire format of a response.
	Formato = core.Formato

	// Dedup names how a load avoids writing a record twice.
	Dedup = core.Dedup
)

// Wire formats accepted by Fonte.Formato.
const (
	FormatoJSON   = core.FormatoJSON
	FormatoNDJSON = core.FormatoNDJSON
	FormatoCSV    = core.FormatoCSV
	FormatoXML    = core.FormatoXML
)

// Deduplication modes accepted by Destino.Dedup.
const (
	// DedupNenhum appends; the bronze layer deduplicates on ingestion_id.
	DedupNenhum = core.DedupNenhum

	// DedupMerge stages and MERGEs on ingestion_id, so a re-run is a no-op.
	// It costs one scan of the destination per load.
	DedupMerge = core.DedupMerge
)

// Low-level load options, re-exported. Destino covers the common cases.
var (
	WithProjectID         = core.WithProjectID
	WithDataset           = core.WithDataset
	WithTable             = core.WithTable
	WithStagingBucket     = core.WithStagingBucket
	WithFormat            = core.WithFormat
	WithThresholdForGCS   = core.WithThresholdForGCS
	WithMetadata          = core.WithMetadata
	WithEnvelopeColumns   = core.WithEnvelopeColumns
	WithMetadataNamespace = core.WithMetadataNamespace
)
