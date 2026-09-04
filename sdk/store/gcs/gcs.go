// Package gcs is the Google Cloud Storage backend for from.Files and to.Files.
//
// Importing it costs you the Google storage client. A fetcher that reads local
// files, or one that uses S3, never compiles it -- see core.Store for why the
// backend is passed in rather than chosen inside Files.
package gcs

import (
	"context"
	"fmt"
	"io"
	"sort"

	"cloud.google.com/go/storage"
	"google.golang.org/api/iterator"
)

// Store reads and writes objects in GCS.
type Store struct{ client *storage.Client }

// New wraps a storage client. The caller owns it, and closes it.
//
//	client, _ := storage.NewClient(ctx)
//	defer client.Close()
//	from.Files{Path: "gs://bucket/dia=1/*.ndjson", Store: gcs.New(client)}
func New(client *storage.Client) Store { return Store{client: client} }

// Scheme satisfies core.Store.
func (s Store) Scheme() string { return "gs" }

// List returns every object under prefix, sorted.
func (s Store) List(ctx context.Context, bucket, prefix string) ([]string, error) {
	it := s.client.Bucket(bucket).Objects(ctx, &storage.Query{Prefix: prefix})

	var keys []string
	for {
		attrs, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, err
		}
		// A "directory" placeholder has no content to decode.
		if attrs.Size == 0 && attrs.Name[len(attrs.Name)-1] == '/' {
			continue
		}
		keys = append(keys, attrs.Name)
	}

	// GCS lists lexicographically already, but the contract says sorted and a
	// contract that depends on someone else's default is not a contract.
	sort.Strings(keys)
	return keys, nil
}

// Open reads one object.
func (s Store) Open(ctx context.Context, bucket, key string) (io.ReadCloser, error) {
	r, err := s.client.Bucket(bucket).Object(key).NewReader(ctx)
	if err != nil {
		return nil, fmt.Errorf("opening gs://%s/%s: %w", bucket, key, err)
	}
	return r, nil
}

// Create writes one object. A single write is atomic in GCS: the object
// appears whole or not at all.
func (s Store) Create(ctx context.Context, bucket, key string, r io.Reader) error {
	w := s.client.Bucket(bucket).Object(key).NewWriter(ctx)
	if _, err := io.Copy(w, r); err != nil {
		_ = w.Close()
		return fmt.Errorf("writing gs://%s/%s: %w", bucket, key, err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("closing gs://%s/%s: %w", bucket, key, err)
	}
	return nil
}
