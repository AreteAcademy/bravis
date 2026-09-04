package core

import "testing"

func TestParseLocation(t *testing.T) {
	casos := []struct {
		in                              string
		scheme, bucket, prefix, pattern string
	}{
		{"./entrada/*.csv", "", "", "./entrada/", "*.csv"},
		{"/var/dados/", "", "", "/var/dados/", ""},
		{"dados.ndjson", "", "", "", "dados.ndjson"},
		{"s3://b/dia=1/*.ndjson.gz", "s3", "b", "dia=1/", "*.ndjson.gz"},
		{"gs://b/landing/", "gs", "b", "landing/", ""},
		{"gs://b/landing/parte-0001.ndjson", "gs", "b", "landing/", "parte-0001.ndjson"},
		{"file:///tmp/x/*.json", "", "", "/tmp/x/", "*.json"},
	}
	for _, c := range casos {
		got, err := ParseLocation(c.in)
		if err != nil {
			t.Errorf("%q: %v", c.in, err)
			continue
		}
		if got.Scheme != c.scheme || got.Bucket != c.bucket ||
			got.Prefix != c.prefix || got.Pattern != c.pattern {
			t.Errorf("%q =\n  %+v\nwant scheme=%q bucket=%q prefix=%q pattern=%q",
				c.in, got, c.scheme, c.bucket, c.prefix, c.pattern)
		}
	}
}

func TestParseLocationRecusaOQueNaoSabeLer(t *testing.T) {
	for _, in := range []string{"", "azure://b/x", "s3:///x"} {
		if _, err := ParseLocation(in); err == nil {
			t.Errorf("%q deveria ser recusado", in)
		}
	}
}

func TestLocationMatches(t *testing.T) {
	loc, _ := ParseLocation("s3://b/dia=1/*.ndjson")
	if !loc.Matches("dia=1/parte-0001.ndjson") {
		t.Error("o glob deveria casar")
	}
	if loc.Matches("dia=1/parte-0001.csv") {
		t.Error("o glob não deveria casar outra extensão")
	}

	dir, _ := ParseLocation("gs://b/landing/")
	if !dir.Matches("landing/qualquer-coisa") {
		t.Error("um diretório casa tudo sob ele")
	}
}
