package core

import (
	"context"
	"io"
)

// Store is object storage: S3, GCS, or anything with the same three verbs.
//
// It is a value, like the drivers are, and for the same reason. If from.Files
// carried every backend, reading a CSV off the local disk would compile the
// AWS SDK and the Google one -- and the property the driver packages exist to
// buy would be gone. Instead the backend is passed in, so a fetcher compiles
// the one it names:
//
//	from.Files{Path: "./entrada/*.csv"}                          // nothing extra
//	from.Files{Path: "s3://bucket/dia=2026-09-04/*.ndjson",
//	           Store: s3.New(cfg)}                               // only the AWS SDK
//
// Nil means the local filesystem, which needs no backend at all.
type Store interface {
	// Scheme is the URL scheme this store serves: "s3", "gs". Files checks it
	// against the path, so a gs:// path with an S3 store is an error naming
	// both rather than a confusing 404.
	Scheme() string

	// List returns every key under prefix, sorted.
	//
	// Sorted is part of the contract, not a convenience: two runs over the
	// same prefix have to produce the same sequence, or a positional Key
	// changes the ingestion_id between runs and the preview shows a different
	// sample every time.
	List(ctx context.Context, bucket, prefix string) ([]string, error)

	// Open reads one object. The caller closes it.
	Open(ctx context.Context, bucket, key string) (io.ReadCloser, error)

	// Create writes one object, whole. Object storage makes a single PUT
	// atomic, which is what keeps a half-written file from being read.
	Create(ctx context.Context, bucket, key string, r io.Reader) error
}
