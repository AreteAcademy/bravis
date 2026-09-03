package load

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"cloud.google.com/go/bigquery"
	core "github.com/AreteAcademy/bravis/sdk/internal/core"
)

// --- resolveConfig --------------------------------------------------------

func TestResolveConfigRequiresIdentity(t *testing.T) {
	cases := []struct {
		name string
		cfg  core.LoadConfig
		want string
	}{
		{"no project", core.LoadConfig{Dataset: "d", Table: "t"}, "projectID"},
		{"no dataset", core.LoadConfig{ProjectID: "p", Table: "t"}, "dataset"},
		{"no table", core.LoadConfig{ProjectID: "p", Dataset: "d"}, "table"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := resolveConfig(&c.cfg)
			if err == nil {
				t.Fatalf("Expected an error naming %s", c.want)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("Error should name the missing field %q, got %v", c.want, err)
			}
		})
	}
}

func TestResolveConfigDefaults(t *testing.T) {
	got, err := resolveConfig(&core.LoadConfig{ProjectID: "proj", Dataset: "d", Table: "t"})
	if err != nil {
		t.Fatalf("resolveConfig: %v", err)
	}

	if got.StagingBucket != "proj-bravis-staging" {
		t.Errorf("StagingBucket should derive from the project, got %q", got.StagingBucket)
	}
	if got.StagingPrefix != "extracts/" {
		t.Errorf("StagingPrefix = %q", got.StagingPrefix)
	}
	if got.ThresholdForGCS != defaultThresholdForGCS {
		t.Errorf("ThresholdForGCS = %d", got.ThresholdForGCS)
	}
	if got.Format != "ndjson" {
		t.Errorf("Format = %q", got.Format)
	}
}

func TestResolveConfigFromOptionsAlone(t *testing.T) {
	got, err := resolveConfig(nil,
		core.WithProjectID("p"),
		core.WithDataset("d"),
		core.WithTable("t"),
		core.WithThresholdForGCS(10),
		core.WithMetadata(true),
	)
	if err != nil {
		t.Fatalf("resolveConfig: %v", err)
	}
	if got.ProjectID != "p" || got.Dataset != "d" || got.Table != "t" {
		t.Errorf("options did not build the config: %+v", got)
	}
	if got.ThresholdForGCS != 10 || !got.AddMetadata {
		t.Errorf("behaviour options did not apply: %+v", got)
	}
}

func TestResolveConfigDoesNotMutateCaller(t *testing.T) {
	// A caller reusing one LoadConfig for several Loaders must not see it
	// change underneath them.
	original := core.LoadConfig{ProjectID: "p", Dataset: "d", Table: "t"}
	snapshot := original

	if _, err := resolveConfig(&original, core.WithTable("other")); err != nil {
		t.Fatalf("resolveConfig: %v", err)
	}

	if original != snapshot {
		t.Errorf("caller's config was mutated: %+v became %+v", snapshot, original)
	}
}

// --- strategy -------------------------------------------------------------

func TestStrategyFor(t *testing.T) {
	cases := []struct {
		rows, threshold int
		want            string
	}{
		{0, 5000, "inline"},
		{1, 5000, "inline"},
		{5000, 5000, "inline"}, // at the threshold, still inline
		{5001, 5000, "gcs"},    // above it, stage
		{1, 0, "gcs"},          // a zero threshold sends everything to GCS
	}
	for _, c := range cases {
		if got := strategyFor(c.rows, c.threshold); got != c.want {
			t.Errorf("strategyFor(%d, %d) = %q, want %q", c.rows, c.threshold, got, c.want)
		}
	}
}

// --- format validation (SDK_LOAD.md 3) ----------------------------------

func TestSourceFormatAcceptsOnlyWhatWeWrite(t *testing.T) {
	for _, ok := range []string{"", "ndjson"} {
		if got, err := sourceFormat(ok); err != nil || got != bigquery.JSON {
			t.Errorf("sourceFormat(%q) = %v, %v", ok, got, err)
		}
	}

	// These were accepted while every path wrote NDJSON, so LoadResult
	// reported a Parquet load that never happened.
	for _, bad := range []string{"csv", "parquet"} {
		_, err := sourceFormat(bad)
		if err == nil {
			t.Errorf("sourceFormat(%q) should be refused until implemented", bad)
		} else if !strings.Contains(err.Error(), "not implemented") {
			t.Errorf("error for %q should say it is not implemented, got %v", bad, err)
		}
	}

	if _, err := sourceFormat("avro"); err == nil {
		t.Error("unknown formats should be refused")
	}
}

func TestResolveConfigRejectsUnwrittenFormat(t *testing.T) {
	_, err := resolveConfig(&core.LoadConfig{
		ProjectID: "p", Dataset: "d", Table: "t", Format: "parquet",
	})
	if err == nil {
		t.Fatal("New must refuse a format it does not write")
	}
}

func TestResolveConfigRejectsBothMetadataModes(t *testing.T) {
	_, err := resolveConfig(nil,
		core.WithProjectID("p"), core.WithDataset("d"), core.WithTable("t"),
		core.WithMetadata(true), core.WithEnvelopeColumns(true),
	)
	if err == nil {
		t.Fatal("AddMetadata and WriteEnvelopeColumns contradict each other and must not both apply")
	}
}

// --- encodeRows ----------------------------------------------------------

func decodeNDJSON(t *testing.T, data []byte) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		if line == "" {
			continue
		}
		var row map[string]any
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatalf("line %q is not a JSON object: %v", line, err)
		}
		out = append(out, row)
	}
	return out
}

func TestEncodeRowsWritesOneObjectPerLine(t *testing.T) {
	l := &Loader{cfg: &core.LoadConfig{Format: "ndjson"}}

	data, err := l.encodeRows([]core.Envelope{
		{Payload: map[string]any{"amount": 1}},
		{Payload: map[string]any{"amount": 2}},
	})
	if err != nil {
		t.Fatalf("encodeRows: %v", err)
	}

	rows := decodeNDJSON(t, data)
	if len(rows) != 2 {
		t.Fatalf("Expected 2 lines, got %d", len(rows))
	}
	if rows[0]["amount"] != float64(1) || rows[1]["amount"] != float64(2) {
		t.Errorf("rows = %v", rows)
	}
}

func TestEncodeRowsRejectsNonObject(t *testing.T) {
	l := &Loader{cfg: &core.LoadConfig{Format: "ndjson"}}

	// BigQuery maps an NDJSON object's keys onto columns. A scalar or array
	// has nothing to map, and must fail here rather than inside a load job.
	for _, payload := range []any{42, "text", []int{1, 2}} {
		if _, err := l.encodeRows([]core.Envelope{{Payload: payload}}); err == nil {
			t.Errorf("Expected %v (%T) to be rejected", payload, payload)
		}
	}
}

func TestEncodeRowsStructPayloadUsesJSONTags(t *testing.T) {
	l := &Loader{cfg: &core.LoadConfig{Format: "ndjson"}}
	type tx struct {
		ID     string `json:"id"`
		Amount int    `json:"amount"`
	}

	data, err := l.encodeRows([]core.Envelope{{Payload: tx{ID: "a", Amount: 7}}})
	if err != nil {
		t.Fatalf("encodeRows: %v", err)
	}

	rows := decodeNDJSON(t, data)
	if rows[0]["id"] != "a" || rows[0]["amount"] != float64(7) {
		t.Errorf("struct payload should honour json tags: %v", rows[0])
	}
}

// --- envelope columns (SDK_LOAD.md 5) -----------------------------------

func TestEncodeRowsEnvelopeMode(t *testing.T) {
	l := &Loader{cfg: &core.LoadConfig{
		Format: "ndjson", WriteEnvelopeColumns: true,
	}}

	data, err := l.encodeRows([]core.Envelope{{
		Provider: "gov", Entity: "tx", SourceKey: "k1",
		RecordTS: "2026-01-01T00:00:00Z",
		Payload:  map[string]any{"amount": 10},
	}})
	if err != nil {
		t.Fatalf("encodeRows: %v", err)
	}

	row := decodeNDJSON(t, data)[0]

	for _, col := range []string{
		"ingestion_id", "ingestion_loaded_at", "provider", "entity", "source_key", "payload",
	} {
		if _, ok := row[col]; !ok {
			t.Errorf("missing envelope column %s", col)
		}
	}
	if len(row) != 6 {
		t.Errorf("Expected exactly the 6 contract columns, got %d: %v", len(row), row)
	}
	if row["provider"] != "gov" || row["entity"] != "tx" || row["source_key"] != "k1" {
		t.Errorf("identity columns wrong: %v", row)
	}

	// The payload must stay nested, not be flattened into the row.
	payload, ok := row["payload"].(map[string]any)
	if !ok {
		t.Fatalf("payload should be a nested object, got %T", row["payload"])
	}
	if payload["amount"] != float64(10) {
		t.Errorf("payload = %v", payload)
	}
}

func TestEnvelopeIngestionIDMatchesEnvelope(t *testing.T) {
	// The contract is that one place produces this id. If envelope mode
	// computed it differently from Envelope.IngestionID, a row written here
	// would not match the equivalent row written anywhere else.
	env := core.Envelope{
		Provider: "gov", Entity: "tx", SourceKey: "k1",
		RecordTS: "2026-01-01T00:00:00Z",
		Payload:  map[string]any{"amount": 10},
	}

	want, err := env.IngestionID()
	if err != nil {
		t.Fatal(err)
	}

	l := &Loader{cfg: &core.LoadConfig{Format: "ndjson", WriteEnvelopeColumns: true}}
	data, err := l.encodeRows([]core.Envelope{env})
	if err != nil {
		t.Fatalf("encodeRows: %v", err)
	}

	if got := decodeNDJSON(t, data)[0]["ingestion_id"]; got != want {
		t.Errorf("envelope mode ingestion_id = %v, Envelope.IngestionID() = %v", got, want)
	}
}

func TestEnvelopeModeRequiresSourceKey(t *testing.T) {
	l := &Loader{cfg: &core.LoadConfig{Format: "ndjson", WriteEnvelopeColumns: true}}
	_, err := l.encodeRows([]core.Envelope{{Provider: "gov", Entity: "tx", Payload: map[string]any{}}})
	if err == nil {
		t.Fatal("Expected an error: without a source key there is no stable ingestion_id")
	}
}

// --- metadata -------------------------------------------------------------

func metaLoader() *Loader {
	return &Loader{cfg: &core.LoadConfig{
		ProjectID: "p", Dataset: "d", Table: "t",
		AddMetadata: true, Format: "ndjson",
	}}
}

func TestAddMetadataInjectsFields(t *testing.T) {
	l := metaLoader()
	env := core.Envelope{
		Provider:  "gov",
		Entity:    "tx",
		SourceKey: "k1",
		RecordTS:  "2026-01-01T00:00:00Z",
		Payload:   map[string]any{"amount": 10},
	}

	if err := l.addMetadataToEnvelope(&env); err != nil {
		t.Fatalf("addMetadataToEnvelope: %v", err)
	}

	got := env.Payload.(map[string]any)
	if got["amount"] != 10 {
		t.Errorf("original payload fields must survive: %v", got)
	}
	for _, k := range metadataFields {
		if _, ok := got[k]; !ok {
			t.Errorf("missing metadata field %s", k)
		}
	}
	if got["provider"] != "gov" || got["source_key"] != "k1" {
		t.Errorf("metadata values wrong: %v", got)
	}
}

func TestAddMetadataIngestionIDIsDeterministic(t *testing.T) {
	// This id is the whole idempotency story: the same record seen twice must
	// produce the same id, and a different record must not collide.
	base := core.Envelope{
		Provider: "gov", Entity: "tx", SourceKey: "k1",
		RecordTS: "2026-01-01T00:00:00Z",
		Payload:  map[string]any{"amount": 10},
	}

	a, b := base, base
	l := metaLoader()
	if err := l.addMetadataToEnvelope(&a); err != nil {
		t.Fatal(err)
	}
	if err := l.addMetadataToEnvelope(&b); err != nil {
		t.Fatal(err)
	}

	idA := a.Payload.(map[string]any)["ingestion_id"]
	idB := b.Payload.(map[string]any)["ingestion_id"]
	if idA != idB {
		t.Errorf("same record produced different ids: %v vs %v", idA, idB)
	}

	other := base
	other.SourceKey = "k2"
	if err := l.addMetadataToEnvelope(&other); err != nil {
		t.Fatal(err)
	}
	if other.Payload.(map[string]any)["ingestion_id"] == idA {
		t.Error("different source keys collided on the same ingestion id")
	}
}

func TestAddMetadataRequiresSourceKey(t *testing.T) {
	l := metaLoader()
	env := core.Envelope{Provider: "gov", Entity: "tx", Payload: map[string]any{}}
	if err := l.addMetadataToEnvelope(&env); err == nil {
		t.Fatal("Expected an error: without a source key there is no stable id")
	}
}

func TestAddMetadataConvertsStructPayload(t *testing.T) {
	l := metaLoader()
	type tx struct {
		Amount int `json:"amount"`
	}
	env := core.Envelope{
		Provider: "gov", Entity: "tx", SourceKey: "k1",
		RecordTS: "2026-01-01T00:00:00Z",
		Payload:  tx{Amount: 10},
	}

	if err := l.addMetadataToEnvelope(&env); err != nil {
		t.Fatalf("addMetadataToEnvelope: %v", err)
	}

	got, ok := env.Payload.(map[string]any)
	if !ok {
		t.Fatalf("payload should become a map, got %T", env.Payload)
	}
	if got["amount"] != float64(10) {
		t.Errorf("struct field lost in conversion: %v", got)
	}
	if _, ok := got["ingestion_id"]; !ok {
		t.Error("metadata not added to converted struct")
	}
}

// --- Load, empty batch ----------------------------------------------------

func TestLoadEmptyBatchTouchesNothing(t *testing.T) {
	// Must return before reaching BigQuery -- this Loader has nil clients, so
	// any call would panic.
	l := &Loader{cfg: &core.LoadConfig{ProjectID: "p", Dataset: "d", Table: "t", Format: "ndjson"}}

	result, err := l.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if result.RowsLoaded != 0 {
		t.Errorf("RowsLoaded = %d", result.RowsLoaded)
	}
	if result.Strategy != "inline" || result.Format != "ndjson" {
		t.Errorf("result = %+v", result)
	}
}

// --- truncate -------------------------------------------------------------

func TestTruncate(t *testing.T) {
	if got := truncate([]byte("short"), 80); got != "short" {
		t.Errorf("truncate should leave short input alone, got %q", got)
	}
	long := strings.Repeat("x", 100)
	got := truncate([]byte(long), 10)
	if got != strings.Repeat("x", 10)+"..." {
		t.Errorf("truncate = %q", got)
	}
}

// --- LoadResult on failure ------------------------------------------------

func TestLoadReturnsResultOnFailure(t *testing.T) {
	// The documented way to read per-row diagnostics is result.ErrorRows
	// after a non-nil error. Load used to return nil on every error path, so
	// following the documentation panicked.
	l := &Loader{cfg: &core.LoadConfig{
		ProjectID: "p", Dataset: "d", Table: "t", Format: "ndjson",
		AddMetadata: true}}

	// A missing SourceKey fails in metadata, before any client is touched.
	result, err := l.Load(context.Background(), core.Envelope{
		Provider: "gov", Entity: "tx", Payload: map[string]any{},
	})

	if err == nil {
		t.Fatal("Expected an error without a source key")
	}
	if result == nil {
		t.Fatal("Load must return a result alongside the error, not nil")
	}
	if result.Strategy == "" || result.Format != "ndjson" {
		t.Errorf("result should carry what is known: %+v", result)
	}
	if result.RowsLoaded != 0 {
		t.Errorf("nothing was written, RowsLoaded = %d", result.RowsLoaded)
	}
}

func TestRowErrorsTruncates(t *testing.T) {
	var errs []*bigquery.Error
	for i := 0; i < maxReportedErrors+5; i++ {
		errs = append(errs, &bigquery.Error{Message: fmt.Sprintf("bad row %d", i)})
	}

	got := rowErrors(&bigquery.JobStatus{Errors: errs})
	if len(got) != maxReportedErrors+1 {
		t.Fatalf("Expected %d entries plus a summary, got %d", maxReportedErrors, len(got))
	}
	if !strings.Contains(got[len(got)-1], "and 5 more") {
		t.Errorf("last entry should summarise the remainder, got %q", got[len(got)-1])
	}
}

func TestRowErrorsIncludesLocation(t *testing.T) {
	got := rowErrors(&bigquery.JobStatus{Errors: []*bigquery.Error{
		{Location: "row 3", Message: "no such field: amount"},
		{Message: "generic failure"},
	}})

	if len(got) != 2 {
		t.Fatalf("got %v", got)
	}
	if got[0] != "row 3: no such field: amount" {
		t.Errorf("location should be kept: %q", got[0])
	}
	if got[1] != "generic failure" {
		t.Errorf("a location-less error should pass through: %q", got[1])
	}
}

func TestRowErrorsNilStatus(t *testing.T) {
	if got := rowErrors(nil); got != nil {
		t.Errorf("nil status should yield nothing, got %v", got)
	}
}

func TestAddMetadataRefusesToOverwritePayloadFields(t *testing.T) {
	// The "_bravis_" prefix used to make this impossible. Without it a source
	// that already has "provider" would have its value silently replaced by
	// ours -- an invisible failure, and the worse one.
	l := metaLoader()
	env := core.Envelope{
		Provider: "gov", Entity: "tx", SourceKey: "k1", RecordTS: "2026-01-01T00:00:00Z",
		Payload: map[string]any{"provider": "the vendor's own value", "amount": 10},
	}

	err := l.addMetadataToEnvelope(&env)
	if err == nil {
		t.Fatal("a colliding payload field must be an error, not a silent overwrite")
	}
	if !strings.Contains(err.Error(), "provider") {
		t.Errorf("the error must name the colliding field: %v", err)
	}
	if !strings.Contains(err.Error(), "WriteEnvelopeColumns") {
		t.Errorf("the error should point at the mode that cannot collide: %v", err)
	}
}

func TestAddMetadataDoesNotMutateCallerPayload(t *testing.T) {
	l := metaLoader()
	original := map[string]any{"amount": 10}
	env := core.Envelope{
		Provider: "gov", Entity: "tx", SourceKey: "k1", RecordTS: "2026-01-01T00:00:00Z",
		Payload: original,
	}

	if err := l.addMetadataToEnvelope(&env); err != nil {
		t.Fatal(err)
	}

	if len(original) != 1 {
		t.Errorf("the caller's map was mutated: %v", original)
	}
}

func TestFlatAndEnvelopeUseTheSameNames(t *testing.T) {
	// One spelling downstream: a flat row and a wrapped row must describe a
	// record with the same field names, or SQL has to know which mode wrote it.
	l := metaLoader()
	env := core.Envelope{
		Provider: "gov", Entity: "tx", SourceKey: "k1", RecordTS: "2026-01-01T00:00:00Z",
		Payload: map[string]any{"amount": 10},
	}

	cols, err := l.envelopeColumns(env)
	if err != nil {
		t.Fatal(err)
	}

	flat := env
	if err := l.addMetadataToEnvelope(&flat); err != nil {
		t.Fatal(err)
	}
	flatMap := flat.Payload.(map[string]any)

	for _, f := range []string{"ingestion_id", "ingestion_loaded_at", "provider", "entity", "source_key"} {
		if _, ok := cols[f]; !ok {
			t.Errorf("envelope mode is missing %s", f)
		}
		if _, ok := flatMap[f]; !ok {
			t.Errorf("flat mode is missing %s", f)
		}
	}
	if cols["ingestion_id"] != flatMap["ingestion_id"] {
		t.Error("the two modes computed different ingestion_ids for the same record")
	}
}
