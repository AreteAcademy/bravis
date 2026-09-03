package sdk

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// perto compares floats. Writing 14.1*9/5+32 as a literal would be evaluated
// as an untyped constant at arbitrary precision and not equal the float64 the
// code actually produces.
func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func celsiusToF(c float64) float64 { return c*9/5 + 32 }

// meteoServer answers with the real Open-Meteo shape: two parallel arrays
// under "hourly", request metadata at the top level.
func meteoServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{
			"latitude": -23.514938, "longitude": -46.610504,
			"generationtime_ms": 0.0208616256713867,
			"utc_offset_seconds": 0, "timezone": "GMT",
			"timezone_abbreviation": "GMT", "elevation": 737,
			"hourly_units": {"time": "iso8601", "temperature_2m": "°C"},
			"hourly": {
				"time": ["2026-09-03T00:00", "2026-09-03T01:00", "2026-09-03T02:00"],
				"temperature_2m": [14.1, 13.7, 13.4]
			}
		}`)
	}))
}

func meteoRecords(t *testing.T, srv *httptest.Server) *Data {
	t.Helper()
	data, err := Extract(context.Background(), Source{
		URL:    srv.URL,
		Expand: ParallelArrays("hourly", "time", "temperature_2m"),
	})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	return data
}

func drain(t *testing.T, data *Data) []map[string]any {
	t.Helper()
	var out []map[string]any
	for env, err := range data.Records {
		if err != nil {
			t.Fatalf("record %d: %v", len(out), err)
		}
		out = append(out, env.Payload.(map[string]any))
	}
	return out
}

// --- the seam --------------------------------------------------------------

func TestTransformRunsTheCallersFunction(t *testing.T) {
	srv := meteoServer(t)
	defer srv.Close()

	// The whole point: an arbitrary function of the caller's, applied to each
	// record before it is loaded.
	data := Transform(meteoRecords(t, srv), func(p any) (any, error) {
		r := p.(map[string]any)
		c := r["temperature_2m"].(float64)
		r["temperature_f"] = c*9/5 + 32
		return r, nil
	})

	rows := drain(t, data)
	if len(rows) != 3 {
		t.Fatalf("expected 3 readings, got %d", len(rows))
	}
	got, ok := rows[0]["temperature_f"].(float64)
	if !ok || !near(got, celsiusToF(14.1)) {
		t.Errorf("the caller's transform did not run: %v", rows[0])
	}
}

func TestTransformChainsInOrder(t *testing.T) {
	srv := meteoServer(t)
	defer srv.Close()

	data := Transform(meteoRecords(t, srv),
		Rename(map[string]string{"temperature_2m": "temp_c"}),
		// This one only works if the rename already happened.
		Compute("temp_f", func(r map[string]any) (any, error) {
			c, ok := r["temp_c"].(float64)
			if !ok {
				return nil, fmt.Errorf("temp_c is missing, so the chain ran out of order")
			}
			return c*9/5 + 32, nil
		}),
		Only("time", "temp_c", "temp_f"),
	)

	rows := drain(t, data)
	r := rows[0]
	if len(r) != 3 {
		t.Fatalf("expected exactly the 3 projected fields, got %v", r)
	}
	f, ok := r["temp_f"].(float64)
	if r["temp_c"] != 14.1 || !ok || !near(f, celsiusToF(14.1)) {
		t.Errorf("row = %v", r)
	}
}

func TestTransformSkipRecordFilters(t *testing.T) {
	srv := meteoServer(t)
	defer srv.Close()

	data := Transform(meteoRecords(t, srv), func(p any) (any, error) {
		if p.(map[string]any)["temperature_2m"].(float64) > 13.5 {
			return nil, SkipRecord
		}
		return p, nil
	})

	rows := drain(t, data)
	if len(rows) != 1 {
		t.Fatalf("expected 1 reading to survive the filter, got %d", len(rows))
	}
	if rows[0]["temperature_2m"] != 13.4 {
		t.Errorf("the wrong reading survived: %v", rows[0])
	}
}

func TestTransformErrorIsAFormatError(t *testing.T) {
	srv := meteoServer(t)
	defer srv.Close()

	data := Transform(meteoRecords(t, srv), func(any) (any, error) {
		return nil, fmt.Errorf("boom")
	})

	var seen error
	for _, err := range data.Records {
		if err != nil {
			seen = err
			break
		}
	}

	// A failing transform is the caller's mapping, not the source: the action
	// is to fix the function, not to wait and retry.
	var format *FormatError
	if !errors.As(seen, &format) {
		t.Fatalf("expected *FormatError, got %T: %v", seen, seen)
	}
	if !errors.Is(seen, ErrFormat) {
		t.Error("errors.Is(err, ErrFormat) must work")
	}
}

func TestTransformIsLazy(t *testing.T) {
	srv := meteoServer(t)
	defer srv.Close()

	calls := 0
	data := Transform(meteoRecords(t, srv), func(p any) (any, error) {
		calls++
		return p, nil
	})

	// Nothing runs until the stream is pulled -- a paginated source must not
	// have to fit in memory first.
	if calls != 0 {
		t.Fatalf("Transform ran %d times before iteration started", calls)
	}

	for range data.Records {
		break
	}
	if calls != 1 {
		t.Errorf("expected exactly 1 record transformed after one pull, got %d", calls)
	}
}

func TestTransformWithNoFunctionsIsANoop(t *testing.T) {
	srv := meteoServer(t)
	defer srv.Close()

	data := meteoRecords(t, srv)
	if Transform(data) != data {
		t.Error("Transform with no functions should hand back the same Data")
	}
	if Transform(nil, Only("x")) != nil {
		t.Error("Transform(nil) should stay nil")
	}
}

// --- helpers ---------------------------------------------------------------

func TestWithoutDropsRequestMetadata(t *testing.T) {
	srv := meteoServer(t)
	defer srv.Close()

	// generationtime_ms changes on every call, so a row carrying it is a
	// different row every run for the same reading.
	rows := drain(t, Transform(meteoRecords(t, srv),
		Without("generationtime_ms", "timezone_abbreviation", "utc_offset_seconds")))

	for _, dropped := range []string{"generationtime_ms", "timezone_abbreviation", "utc_offset_seconds"} {
		if _, present := rows[0][dropped]; present {
			t.Errorf("%s survived Without: %v", dropped, rows[0])
		}
	}
	// Everything else stays.
	if rows[0]["latitude"] != -23.514938 || rows[0]["elevation"] != float64(737) {
		t.Errorf("Without removed more than it was asked to: %v", rows[0])
	}
}

func TestRenameRefusesToOverwrite(t *testing.T) {
	_, err := Rename(map[string]string{"a": "b"})(map[string]any{"a": 1, "b": 2})
	if err == nil {
		t.Fatal("renaming onto an existing field must be an error")
	}
	// Which value survived would otherwise depend on map iteration order.
	if !strings.Contains(err.Error(), "a -> b") {
		t.Errorf("the error must name the clash: %v", err)
	}
}

func TestRenameLeavesUnknownFieldsAlone(t *testing.T) {
	got, err := Rename(map[string]string{"missing": "x"})(map[string]any{"a": 1})
	if err != nil {
		t.Fatal(err)
	}
	r := got.(map[string]any)
	if len(r) != 1 || r["a"] != 1 {
		t.Errorf("row = %v", r)
	}
}

func TestComputeRefusesToOverwrite(t *testing.T) {
	_, err := Compute("a", func(map[string]any) (any, error) { return 2, nil })(map[string]any{"a": 1})
	if err == nil {
		t.Fatal("computing onto an existing field must be an error")
	}
	if !strings.Contains(err.Error(), "Without") {
		t.Errorf("the error should say how to do it deliberately: %v", err)
	}
}

func TestComputeReportsTheFieldOnFailure(t *testing.T) {
	_, err := Compute("temp_f", func(map[string]any) (any, error) {
		return nil, fmt.Errorf("no temperature")
	})(map[string]any{"a": 1})
	if err == nil || !strings.Contains(err.Error(), "temp_f") {
		t.Errorf("the error must name the field being computed: %v", err)
	}
}

func TestTransformersLeaveNonObjectsAlone(t *testing.T) {
	// A CSV row is a map[string]string, and a scalar payload is possible too.
	// Projection helpers pass those through rather than failing.
	for _, fn := range []Transformer{Only("a"), Without("a"), Rename(map[string]string{"a": "b"})} {
		got, err := fn("just a string")
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if got != "just a string" {
			t.Errorf("payload was altered: %v", got)
		}
	}
}

// --- end to end ------------------------------------------------------------

func TestTransformFeedsTheKeyAndTimestamp(t *testing.T) {
	srv := meteoServer(t)
	defer srv.Close()

	// Target.Key reads the payload after every Transformer has run, so a
	// rename here has to be reflected there.
	data := Transform(meteoRecords(t, srv),
		Rename(map[string]string{"time": "observed_at"}),
		Only("observed_at", "temperature_2m", "latitude", "longitude"),
	)

	envelopes, err := collect(data, Target{
		Provider: "open_meteo", Entity: "hourly",
		Key:           Key("latitude", "longitude", "observed_at"),
		When:          Field("observed_at"),
		ExtraMetadata: true,
	})
	if err != nil {
		t.Fatalf("collect: %v", err)
	}

	if len(envelopes) != 3 {
		t.Fatalf("expected 3 readings, got %d", len(envelopes))
	}
	if envelopes[0].SourceKey != "-23.514938|-46.610504|2026-09-03T00:00" {
		t.Errorf("SourceKey = %q", envelopes[0].SourceKey)
	}
	if envelopes[0].RecordTS != "2026-09-03T00:00" {
		t.Errorf("RecordTS = %q", envelopes[0].RecordTS)
	}
}

func TestTransformKeyOnARenamedFieldFailsLoudly(t *testing.T) {
	srv := meteoServer(t)
	defer srv.Close()

	// Renaming a field that Target.Key still names by its old name must be an
	// error, not a silent short key -- that would change every ingestion_id.
	data := Transform(meteoRecords(t, srv), Rename(map[string]string{"time": "observed_at"}))

	_, err := collect(data, Target{
		Provider: "open_meteo", Entity: "hourly", Key: Key("time"), ExtraMetadata: true,
	})
	if err == nil {
		t.Fatal("a key naming a field that no longer exists must fail")
	}
	if !strings.Contains(err.Error(), "observed_at") {
		t.Errorf("the error should list what the record actually has: %v", err)
	}
}
