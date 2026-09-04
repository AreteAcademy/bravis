// Package s3 is the Amazon S3 backend for from.Files and to.Files.
//
// Importing it costs you the AWS SDK. A fetcher that reads local files, or one
// that uses GCS, never compiles it -- see core.Store for why the backend is
// passed in rather than chosen inside Files.
//
// It also serves anything that speaks the S3 API: MinIO, R2, Ceph. Point
// BaseEndpoint at them.
package s3

import (
	"context"
	"fmt"
	"io"
	"sort"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
)

// Store reads and writes objects in S3.
type Store struct{ client *awss3.Client }

// New wraps an S3 client.
//
//	cfg, _ := config.LoadDefaultConfig(ctx)
//	from.Files{Path: "s3://bucket/dia=1/*.ndjson", Store: s3.New(s3.Client(cfg))}
func New(client *awss3.Client) Store { return Store{client: client} }

// Scheme satisfies core.Store.
func (s Store) Scheme() string { return "s3" }

// List returns every object under prefix, sorted, following pagination.
//
// The paging matters: a prefix with more than a thousand objects would
// otherwise be read in part, and a partial read that reports success is the
// worst kind -- it looks like a small day.
func (s Store) List(ctx context.Context, bucket, prefix string) ([]string, error) {
	var keys []string

	p := awss3.NewListObjectsV2Paginator(s.client, &awss3.ListObjectsV2Input{
		Bucket: aws.String(bucket),
		Prefix: aws.String(prefix),
	})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, o := range page.Contents {
			key := aws.ToString(o.Key)
			// A "directory" placeholder has no content to decode.
			if key == "" || key[len(key)-1] == '/' {
				continue
			}
			keys = append(keys, key)
		}
	}

	sort.Strings(keys)
	return keys, nil
}

// Open reads one object.
func (s Store) Open(ctx context.Context, bucket, key string) (io.ReadCloser, error) {
	out, err := s.client.GetObject(ctx, &awss3.GetObjectInput{
		Bucket: aws.String(bucket), Key: aws.String(key),
	})
	if err != nil {
		return nil, fmt.Errorf("opening s3://%s/%s: %w", bucket, key, err)
	}
	return out.Body, nil
}

// Create writes one object. A single PUT is atomic in S3: the object appears
// whole or not at all.
func (s Store) Create(ctx context.Context, bucket, key string, r io.Reader) error {
	if _, err := s.client.PutObject(ctx, &awss3.PutObjectInput{
		Bucket: aws.String(bucket), Key: aws.String(key), Body: r,
	}); err != nil {
		return fmt.Errorf("writing s3://%s/%s: %w", bucket, key, err)
	}
	return nil
}
