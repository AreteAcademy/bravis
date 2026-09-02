package load

import (
	"context"
	"strings"
	"testing"

	"cloud.google.com/go/bigquery"
	"github.com/AreteAcademy/bravis/sdk"
)

// --- resolveConfig --------------------------------------------------------

func TestResolveConfigRequiresIdentity(t *testing.T) {
	cases := []struct {
		name string
		cfg  sdk.LoadConfig
		want string
	}{
		{"no project", sdk.LoadConfig{Dataset: "d", Table: "t"}, "projectID"},
		{"no dataset", sdk.LoadConfig{ProjectID: "p", Table: "t"}, "dataset"},
		{"no table", sdk.LoadConfig{ProjectID: "p", Dataset: "d"}, "table"},
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
	got, err := resolveConfig(&sdk.LoadConfig{ProjectID: "proj", Dataset: "d", Table: "t"})
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
	if got.MetadataNamespace != defaultMetadataNamespace {
		t.Errorf("MetadataNamespace = %q", got.MetadataNamespace)
	}
}

func TestResolveConfigFromOptionsAlone(t *testing.T) {
	got, err := resolveConfig(nil,
		sdk.WithProjectID("p"),
		sdk.WithDataset("d"),
		sdk.WithTable("t"),
		sdk.WithThresholdForGCS(10),
		sdk.WithMetadata(true),
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
	original := sdk.LoadConfig{ProjectID: "p", Dataset: "d", Table: "t"}
	snapshot := original

	if _, err := resolveConfig(&original, sdk.WithTable("other")); err != nil {
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

// --- toRow ----------------------------------------------------------------

func TestToRowConvertsObject(t *testing.T) {
	row, n, err := toRow(map[string]any{"amount": 100, "currency": "BRL"})
	if err != nil {
		t.Fatalf("toRow: %v", err)
	}
	if len(row) != 2 {
		t.Fatalf("Expected 2 columns, got %d: %v", len(row), row)
	}
	if row["currency"] != bigquery.Value("BRL") {
		t.Errorf("currency = %v", row["currency"])
	}
	if n == 0 {
		t.Error("Expected the encoded byte count to be reported")
	}
}

func TestToRowRejectsNonObject(t *testing.T) {
	// BigQuery rows are columns and values; a bare scalar or array has no
	// column names and must fail loudly rather than silently drop.
	for _, payload := range []any{42, "text", []int{1, 2}} {
		if _, _, err := toRow(payload); err == nil {
			t.Errorf("Expected %v (%T) to be rejected", payload, payload)
		}
	}
}

func TestToRowStructPayload(t *testing.T) {
	type tx struct {
		ID     string `json:"id"`
		Amount int    `json:"amount"`
	}
	row, _, err := toRow(tx{ID: "a", Amount: 7})
	if err != nil {
		t.Fatalf("toRow: %v", err)
	}
	if row["id"] != bigquery.Value("a") {
		t.Errorf("struct payload should use json tags: %v", row)
	}
}

func TestJSONSaverImplementsValueSaver(t *testing.T) {
	// The reason this type exists: StructSaver reflects over struct fields,
	// so it cannot carry a schema-less payload.
	var _ bigquery.ValueSaver = jsonSaver{}

	row, insertID, err := jsonSaver{row: map[string]bigquery.Value{"a": 1}}.Save()
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if row["a"] != bigquery.Value(1) {
		t.Errorf("row = %v", row)
	}
	if insertID != "" {
		t.Errorf("insertID must stay empty: dedup is documented as downstream, got %q", insertID)
	}
}

// --- metadata -------------------------------------------------------------

func metaLoader(ns string) *Loader {
	return &Loader{cfg: &sdk.LoadConfig{
		ProjectID: "p", Dataset: "d", Table: "t",
		AddMetadata: true, MetadataNamespace: ns, Format: "ndjson",
	}}
}

func TestAddMetadataInjectsFields(t *testing.T) {
	l := metaLoader(defaultMetadataNamespace)
	env := sdk.Envelope{
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
	for _, k := range []string{
		"_bravis_ingestion_id", "_bravis_ingestion_loaded_at", "_bravis_provider",
		"_bravis_entity", "_bravis_source_key", "_bravis_record_ts",
	} {
		if _, ok := got[k]; !ok {
			t.Errorf("missing metadata field %s", k)
		}
	}
	if got["_bravis_provider"] != "gov" || got["_bravis_source_key"] != "k1" {
		t.Errorf("metadata values wrong: %v", got)
	}
}

func TestAddMetadataIngestionIDIsDeterministic(t *testing.T) {
	// This id is the whole idempotency story: the same record seen twice must
	// produce the same id, and a different record must not collide.
	base := sdk.Envelope{
		Provider: "gov", Entity: "tx", SourceKey: "k1",
		RecordTS: "2026-01-01T00:00:00Z",
		Payload:  map[string]any{"amount": 10},
	}

	a, b := base, base
	l := metaLoader(defaultMetadataNamespace)
	if err := l.addMetadataToEnvelope(&a); err != nil {
		t.Fatal(err)
	}
	if err := l.addMetadataToEnvelope(&b); err != nil {
		t.Fatal(err)
	}

	idA := a.Payload.(map[string]any)["_bravis_ingestion_id"]
	idB := b.Payload.(map[string]any)["_bravis_ingestion_id"]
	if idA != idB {
		t.Errorf("same record produced different ids: %v vs %v", idA, idB)
	}

	other := base
	other.SourceKey = "k2"
	if err := l.addMetadataToEnvelope(&other); err != nil {
		t.Fatal(err)
	}
	if other.Payload.(map[string]any)["_bravis_ingestion_id"] == idA {
		t.Error("different source keys collided on the same ingestion id")
	}
}

func TestAddMetadataRequiresSourceKey(t *testing.T) {
	l := metaLoader(defaultMetadataNamespace)
	env := sdk.Envelope{Provider: "gov", Entity: "tx", Payload: map[string]any{}}
	if err := l.addMetadataToEnvelope(&env); err == nil {
		t.Fatal("Expected an error: without a source key there is no stable id")
	}
}

func TestAddMetadataConvertsStructPayload(t *testing.T) {
	l := metaLoader(defaultMetadataNamespace)
	type tx struct {
		Amount int `json:"amount"`
	}
	env := sdk.Envelope{
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
	if _, ok := got["_bravis_ingestion_id"]; !ok {
		t.Error("metadata not added to converted struct")
	}
}

// --- Load, empty batch ----------------------------------------------------

func TestLoadEmptyBatchTouchesNothing(t *testing.T) {
	// Must return before reaching BigQuery -- this Loader has nil clients, so
	// any call would panic.
	l := &Loader{cfg: &sdk.LoadConfig{ProjectID: "p", Dataset: "d", Table: "t", Format: "ndjson"}}

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
