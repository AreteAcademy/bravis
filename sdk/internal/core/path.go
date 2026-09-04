package core

import (
	"fmt"
	"path"
	"strings"
)

// Location is a parsed Files path: which backend, which bucket, and the
// prefix plus optional glob under it.
type Location struct {
	Scheme  string // "", "s3", "gs" -- empty is the local filesystem
	Bucket  string
	Prefix  string // the directory part, always ending in "/" when non-empty
	Pattern string // the glob, or "" when the path names a directory
}

// ParseLocation reads a Files path.
//
//	./entrada/*.csv              local, glob
//	/var/dados/                  local, directory
//	s3://bucket/dia=1/*.ndjson   S3, glob under a prefix
//	gs://bucket/landing/         GCS, directory
func ParseLocation(p string) (Location, error) {
	if p == "" {
		return Location{}, fmt.Errorf("a Path is required")
	}

	scheme, rest := "", p
	if i := strings.Index(p, "://"); i >= 0 {
		scheme, rest = p[:i], p[i+3:]
		switch scheme {
		case "s3", "gs":
		case "file":
			scheme = ""
		default:
			return Location{}, fmt.Errorf("unknown scheme %q in %q; use s3://, gs:// or a local path",
				scheme, p)
		}
	}

	loc := Location{Scheme: scheme}

	if scheme != "" {
		bucket, after, _ := strings.Cut(rest, "/")
		if bucket == "" {
			return Location{}, fmt.Errorf("%q names no bucket", p)
		}
		loc.Bucket, rest = bucket, after
	}

	// A trailing slash, or no glob character, means the whole directory.
	if strings.HasSuffix(rest, "/") || rest == "" {
		loc.Prefix = rest
		return loc, nil
	}

	dir, file := path.Split(rest)
	if strings.ContainsAny(file, "*?[") {
		loc.Prefix, loc.Pattern = dir, file
		return loc, nil
	}

	// No glob and no trailing slash: a single named object.
	loc.Prefix, loc.Pattern = dir, file
	return loc, nil
}

// Matches reports whether a key under the prefix is one this location names.
func (l Location) Matches(key string) bool {
	if l.Pattern == "" {
		return true
	}
	ok, err := path.Match(l.Pattern, path.Base(key))
	return err == nil && ok
}

// String renders the location back, for logs and errors.
func (l Location) String() string {
	var b strings.Builder
	if l.Scheme != "" {
		b.WriteString(l.Scheme)
		b.WriteString("://")
		b.WriteString(l.Bucket)
		b.WriteString("/")
	}
	b.WriteString(l.Prefix)
	b.WriteString(l.Pattern)
	return b.String()
}
