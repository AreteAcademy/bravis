package load

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/bigquery"
	core "github.com/AreteAcademy/bravis/sdk/internal/core"
	"github.com/google/uuid"
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
	if got.ThresholdForGCS != 10 || !got.Metadata {
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

	// reflect.DeepEqual, not !=: LoadConfig has a slice field now.
	if !reflect.DeepEqual(original, snapshot) {
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

// --- metadata -------------------------------------------------------------

func metaLoader() *Loader {
	return &Loader{cfg: &core.LoadConfig{
		ProjectID: "p", Dataset: "d", Table: "t",
		Metadata: true, Format: "ndjson",
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
	// Only two. provider, entity and source_key are provenance the SDK uses
	// to build the id, not columns it imposes on your rows.
	if len(got) != 3 {
		t.Errorf("expected the payload plus exactly 2 metadata fields, got %v", got)
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
		Metadata: true}}

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
	// The two fields carry no prefix, so a payload that already owns one of
	// those names would have its value silently replaced by ours -- an
	// invisible failure, and the worse one.
	l := metaLoader()
	env := core.Envelope{
		Provider: "gov", Entity: "tx", SourceKey: "k1", RecordTS: "2026-01-01T00:00:00Z",
		Payload: map[string]any{"ingestion_id": "the vendor's own value", "amount": 10},
	}

	err := l.addMetadataToEnvelope(&env)
	if err == nil {
		t.Fatal("a colliding payload field must be an error, not a silent overwrite")
	}
	if !strings.Contains(err.Error(), "ingestion_id") {
		t.Errorf("the error must name the colliding field: %v", err)
	}
	if !strings.Contains(err.Error(), "Transform") {
		t.Errorf("the error should say where to fix it: %v", err)
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

// --- table creation --------------------------------------------------------

func TestSanitiseLabel(t *testing.T) {
	// BigQuery takes lowercase letters, digits, dashes and underscores, up to
	// 63 characters, starting with a letter. Anything else is dropped rather
	// than failing the create.
	cases := map[string]string{
		"open_meteo":            "open_meteo",
		"Open Meteo":            "open_meteo",
		"acme-corp":             "acme-corp",
		"123":                   "", // must start with a letter
		"":                      "",
		"...":                   "",
		strings.Repeat("a", 80): strings.Repeat("a", 63),
	}
	for in, want := range cases {
		if got := sanitiseLabel(in); got != want {
			t.Errorf("sanitiseLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRequirePartitionFilterRefusesMerge(t *testing.T) {
	// The merge matches on ingestion_id across every partition and cannot be
	// scoped: ingestion_loaded_at is the load time, so a re-run of the same
	// record lands in a different partition than the original. A partition
	// filter would make the merge miss and write the duplicate.
	_, err := resolveConfig(nil,
		core.WithProjectID("p"), core.WithDataset("d"), core.WithTable("t"),
		core.WithMetadata(true),
		core.WithRequirePartitionFilter(true),
		core.WithDedup(core.DedupMerge),
	)
	if err == nil {
		t.Fatal("the pair must be refused")
	}
	if !strings.Contains(err.Error(), "partition") || !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("the error must explain why: %v", err)
	}
}

func TestCreateTableAloneIsEnough(t *testing.T) {
	// The load job creates the table, inferring the schema from the data, so
	// the SDK no longer needs to know the shape to create one.
	cfg, err := resolveConfig(nil,
		core.WithProjectID("p"), core.WithDataset("d"), core.WithTable("t"),
		core.WithCreateTable(true),
	)
	if err != nil {
		t.Fatalf("CreateTable alone should be valid: %v", err)
	}
	if !cfg.CreateTable || cfg.CreateSQL != "" {
		t.Errorf("cfg = %+v", cfg)
	}
}

func TestPartitionOptionsNeedMetadata(t *testing.T) {
	// The table is partitioned on ingestion_loaded_at, and that column only
	// exists when Metadata adds it. Asking for one without the other is
	// a contradiction, caught before anything touches the warehouse.
	for _, opt := range []core.LoadOption{
		core.WithPartitionExpiration(24 * time.Hour),
		core.WithRequirePartitionFilter(true),
	} {
		_, err := resolveConfig(nil,
			core.WithProjectID("p"), core.WithDataset("d"), core.WithTable("t"), opt,
		)
		if err == nil {
			t.Error("a partition option without Metadata must be refused")
		} else if !strings.Contains(err.Error(), "Metadata") {
			t.Errorf("the error must name what is missing: %v", err)
		}
	}
}

func TestDedupMergeNeedsMetadata(t *testing.T) {
	// The merge matches rows on ingestion_id; without Metadata there is
	// no such column to match on.
	_, err := resolveConfig(nil,
		core.WithProjectID("p"), core.WithDataset("d"), core.WithTable("t"),
		core.WithDedup(core.DedupMerge),
	)
	if err == nil {
		t.Fatal("DedupMerge without Metadata must be refused")
	}
	if !strings.Contains(err.Error(), "ingestion_id") {
		t.Errorf("the error must say what is missing: %v", err)
	}
}

// --- what the SDK writes ---------------------------------------------------

func TestDefaultWritesThePayloadUntouched(t *testing.T) {
	// The whole point: what a row looks like is the caller's decision, made
	// in Transform. With Metadata off the SDK adds nothing at all.
	l := &Loader{cfg: &core.LoadConfig{Format: "ndjson"}}

	data, err := l.encodeRows([]core.Envelope{{
		Provider: "open_meteo", Entity: "hourly", SourceKey: "k1",
		RecordTS: "2026-01-01T00:00:00Z",
		Payload:  map[string]any{"temperature_c": 20, "observed_at": "2026-01-01T00:00"},
	}})
	if err != nil {
		t.Fatal(err)
	}

	row := decodeNDJSON(t, data)[0]
	if len(row) != 2 {
		t.Fatalf("the SDK added something: %v", row)
	}
	// Provenance stays provenance: it builds the id, it is not a column.
	for _, imposed := range []string{"provider", "entity", "source_key", "payload", "ingestion_id"} {
		if _, present := row[imposed]; present {
			t.Errorf("the SDK imposed %q on the row: %v", imposed, row)
		}
	}
}

func TestMetadataAddsExactlyTwoFields(t *testing.T) {
	l := metaLoader()
	env := core.Envelope{
		Provider: "open_meteo", Entity: "hourly", SourceKey: "k1",
		RecordTS: "2026-01-01T00:00:00Z",
		Payload:  map[string]any{"temperature_c": 20},
	}

	if err := l.addMetadataToEnvelope(&env); err != nil {
		t.Fatal(err)
	}

	got := env.Payload.(map[string]any)
	if len(got) != 3 {
		t.Fatalf("expected the payload plus 2 fields, got %v", got)
	}
	if got["ingestion_id"] == nil || got["ingestion_loaded_at"] == nil {
		t.Errorf("row = %v", got)
	}

	// The id is the one Envelope.IngestionID produces: one owner, so a row
	// written here matches the row any other producer writes for the record.
	want, err := env.IngestionID()
	if err != nil {
		t.Fatal(err)
	}
	if got["ingestion_id"] != want {
		t.Errorf("ingestion_id = %v, Envelope.IngestionID() = %v", got["ingestion_id"], want)
	}
}

func TestClusterByIsCarriedNotGuessed(t *testing.T) {
	// The SDK does not know the payload, so it cannot pick cluster columns.
	cfg, err := resolveConfig(nil,
		core.WithProjectID("p"), core.WithDataset("d"), core.WithTable("t"),
		core.WithCreateTable(true), core.WithClusterBy("provider", "observed_at"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(cfg.ClusterBy, ",") != "provider,observed_at" {
		t.Errorf("ClusterBy = %v", cfg.ClusterBy)
	}

	none, err := resolveConfig(nil,
		core.WithProjectID("p"), core.WithDataset("d"), core.WithTable("t"))
	if err != nil {
		t.Fatal(err)
	}
	if len(none.ClusterBy) != 0 {
		t.Errorf("nothing should be clustered unless named: %v", none.ClusterBy)
	}
}

func TestTableDescriptionNamesTheSource(t *testing.T) {
	cfg := &core.LoadConfig{}
	prov := provenance{Provider: "open_meteo", Entity: "hourly_temperature"}

	if d := tableDescription(cfg, prov); !strings.Contains(d, "open_meteo/hourly_temperature") {
		t.Errorf("description should say what writes here: %q", d)
	}

	cfg.Metadata = true
	if d := tableDescription(cfg, prov); !strings.Contains(d, "ingestion_id") {
		t.Errorf("with metadata on it should say how to deduplicate: %q", d)
	}
}

// A proveniência vem do lote, não da configuração. É o que o godoc do
// LoadConfig sempre prometeu e o load nunca fez -- as tabelas criadas saíam
// sem os labels de atribuição de custo, e nenhuma contagem de linha muda com
// isso.
func TestProvenanceComesFromTheBatch(t *testing.T) {
	got := provenanceOf([]core.Envelope{
		{Provider: "open_meteo", Entity: "hourly", Payload: map[string]any{"a": 1}},
	})
	if got.Provider != "open_meteo" || got.Entity != "hourly" {
		t.Errorf("provenanceOf = %+v", got)
	}

	labels := tableLabels(got)
	if labels["provider"] != "open_meteo" || labels["entity"] != "hourly" {
		t.Errorf("os labels não saíram do lote: %v", labels)
	}

	if vazio := provenanceOf(nil); vazio.Provider != "" {
		t.Errorf("um lote vazio não tem proveniência: %+v", vazio)
	}
}

// --- the load job carries the layout ---------------------------------------

// These lock applyLayout to the job. It was written and then not called from
// either path, which would have made CreateTable a flag that does nothing --
// the defect this project keeps finding.

func layoutFor(cfg *core.LoadConfig) (*bigquery.Loader, *bigquery.FileConfig) {
	l := &Loader{cfg: cfg}
	source := bigquery.NewReaderSource(strings.NewReader(""))
	loader := &bigquery.Loader{}
	l.applyLayout(loader, &source.FileConfig)
	return loader, &source.FileConfig
}

func TestLayoutRefusesToCreateWhenNotAsked(t *testing.T) {
	loader, file := layoutFor(&core.LoadConfig{Format: "ndjson"})

	if loader.CreateDisposition != bigquery.CreateNever {
		t.Errorf("without CreateTable the job must not create: %v", loader.CreateDisposition)
	}
	if file.AutoDetect {
		t.Error("nothing should be inferred when no table is being created")
	}
}

func TestLayoutCreatesWithAutodetect(t *testing.T) {
	loader, file := layoutFor(&core.LoadConfig{Format: "ndjson", CreateTable: true})

	if loader.CreateDisposition != bigquery.CreateIfNeeded {
		t.Errorf("CreateDisposition = %v", loader.CreateDisposition)
	}
	if !file.AutoDetect {
		t.Error("the schema has to come from the data: nothing else knows it")
	}
	// No metadata, so no timestamp column to partition on.
	if loader.TimePartitioning != nil {
		t.Errorf("nothing to partition on without Metadata: %+v", loader.TimePartitioning)
	}
}

func TestLayoutDoesNotInferOverCreateSQL(t *testing.T) {
	// The caller already said what the columns are; inferring over that would
	// be second-guessing them.
	_, file := layoutFor(&core.LoadConfig{
		Format: "ndjson", CreateTable: true, CreateSQL: "CREATE TABLE d.t (a INT64)",
	})
	if file.AutoDetect {
		t.Error("CreateSQL means the schema is the caller's, not inferred")
	}
}

// The layout now lives on the created table, not on the load job: with
// metadata the SDK creates the destination itself, so the job must not carry
// a schema of its own.
func TestLayoutOnTheJobIsOffWhenTheSDKCreatesTheTable(t *testing.T) {
	loader, file := layoutFor(&core.LoadConfig{
		Format: "ndjson", CreateTable: true, Metadata: true,
	})

	if file.AutoDetect {
		t.Error("autodetect against a table the SDK already created relaxes its NOT NULL columns")
	}
	if loader.CreateDisposition != bigquery.CreateNever {
		t.Errorf("CreateDisposition = %q; the table was created before the job",
			loader.CreateDisposition)
	}
}

func TestTypedTableDeclaresTheMetadataColumnsNotNull(t *testing.T) {
	inferred := bigquery.Schema{
		{Name: "quantidade", Type: bigquery.IntegerFieldType},
		{Name: "ingestion_loaded_at", Type: bigquery.TimestampFieldType},
		{Name: "sku", Type: bigquery.StringFieldType},
		{Name: "ingestion_id", Type: bigquery.StringFieldType},
	}

	meta := typedTable(&core.LoadConfig{
		Format: "ndjson", CreateTable: true, Metadata: true,
		PartitionExpiration:    30 * 24 * time.Hour,
		RequirePartitionFilter: true,
		ClusterBy:              []string{"sku"},
	}, inferred, provenance{})

	byName := map[string]*bigquery.FieldSchema{}
	for _, f := range meta.Schema {
		byName[f.Name] = f
	}

	// The two the SDK owns, exactly as declared.
	if f := byName["ingestion_id"]; f == nil || f.Type != bigquery.StringFieldType || !f.Required {
		t.Errorf("ingestion_id is not STRING NOT NULL: %+v", f)
	}
	if f := byName["ingestion_loaded_at"]; f == nil || f.Type != bigquery.TimestampFieldType || !f.Required {
		t.Errorf("ingestion_loaded_at is not TIMESTAMP NOT NULL: %+v", f)
	}

	// The caller's, untouched -- the SDK infers no type of its own.
	for _, name := range []string{"quantidade", "sku"} {
		if f := byName[name]; f == nil || f.Required {
			t.Errorf("the SDK changed the caller's column %q: %+v", name, f)
		}
	}
	if len(meta.Schema) != 4 {
		t.Errorf("the schema has %d columns, expected the 4 that came in", len(meta.Schema))
	}

	if meta.TimePartitioning == nil || meta.TimePartitioning.Field != "ingestion_loaded_at" {
		t.Fatalf("partitioning did not reach the table: %+v", meta.TimePartitioning)
	}
	if meta.TimePartitioning.Expiration != 30*24*time.Hour {
		t.Errorf("expiration = %v", meta.TimePartitioning.Expiration)
	}
	if !meta.TimePartitioning.RequirePartitionFilter {
		t.Error("RequirePartitionFilter did not reach the table")
	}
	if meta.Clustering == nil || meta.Clustering.Fields[0] != "sku" {
		t.Errorf("clustering did not reach the table: %+v", meta.Clustering)
	}
}

func TestClusterByMustBeInTheRows(t *testing.T) {
	// The table is created from these rows, so a clustering column has to be
	// one of them. BigQuery says so too, but only after the job is submitted
	// and without saying what the rows do have.
	err := checkClusterFields([]string{"provider", "label"}, []core.Envelope{{
		Payload: map[string]any{"amount": 1, "label": "x"},
	}})
	if err == nil {
		t.Fatal("clustering on an absent column must be refused")
	}
	if !strings.Contains(err.Error(), "provider") {
		t.Errorf("the error must name the missing field: %v", err)
	}
	if !strings.Contains(err.Error(), "amount") {
		t.Errorf("the error must list what the rows do have: %v", err)
	}
	if strings.Contains(err.Error(), "label,") || strings.Contains(err.Error(), ", label") {
		// label exists, so it must not be reported as missing
		if strings.Contains(err.Error(), "ClusterBy names provider, label") {
			t.Errorf("only the absent field should be reported: %v", err)
		}
	}

	if err := checkClusterFields([]string{"label"}, []core.Envelope{{
		Payload: map[string]any{"label": "x"},
	}}); err != nil {
		t.Errorf("a present column must pass: %v", err)
	}
	if err := checkClusterFields(nil, nil); err != nil {
		t.Errorf("nothing to check must not be an error: %v", err)
	}
}

func TestLoadDoesNotMutateTheCallersBatch(t *testing.T) {
	// A variadic call shares the backing array, so stamping metadata into
	// `envelopes` writes into the slice the caller still holds. Loading the
	// same batch twice then failed on the second try -- which is what a retry
	// does, and what DedupMerge exists to handle.
	//
	// The narrower test above proves the payload map is copied; this one
	// proves the slice element is too. Found by the integration test.
	l := metaLoader()
	batch := []core.Envelope{{
		Provider: "gov", Entity: "tx", SourceKey: "k1", RecordTS: "2026-01-01T00:00:00Z",
		Payload: map[string]any{"amount": 10},
	}}

	stamped := make([]core.Envelope, len(batch))
	copy(stamped, batch)
	for i := range stamped {
		if err := l.addMetadataToEnvelope(&stamped[i]); err != nil {
			t.Fatal(err)
		}
	}

	original, ok := batch[0].Payload.(map[string]any)
	if !ok {
		t.Fatalf("payload = %T", batch[0].Payload)
	}
	if len(original) != 1 {
		t.Errorf("the caller's envelope was altered: %v", original)
	}
	if _, present := original["ingestion_id"]; present {
		t.Error("a second load of the same batch would now fail on a collision")
	}
}

func TestStagedFileIsDeletedByDefault(t *testing.T) {
	// DeleteAfterLoad was a bool documented as defaulting to true, which a
	// bool cannot do: load.New got the zero value and never cleaned up. The
	// integration test found three objects left in the bucket, one per run.
	cfg, err := resolveConfig(nil,
		core.WithProjectID("p"), core.WithDataset("d"), core.WithTable("t"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.KeepStagedFile {
		t.Error("the zero value must clean up: a bucket filling with files nobody " +
			"looks at is a bill nobody reviews")
	}
}

// A merge on a random id matches nothing, so it would write exactly the
// duplicates it exists to prevent. Refused rather than surprising.
func TestAutoIDComDedupMergeERecusado(t *testing.T) {
	_, err := resolveConfig(&core.LoadConfig{
		ProjectID: "p", Dataset: "d", Table: "t", Format: "ndjson",
		Metadata: true, AutoID: true, Dedup: core.DedupMerge,
	})
	if err == nil {
		t.Fatal("DedupMerge com AutoID nunca casa e duplica tudo; tem de ser recusado")
	}
	for _, want := range []string{"DedupMerge", "AutoID"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("o erro não nomeia %q: %v", want, err)
		}
	}
}

func TestAutoIDGeraIDsDiferentesParaOMesmoRegistro(t *testing.T) {
	l := &Loader{cfg: &core.LoadConfig{Metadata: true, AutoID: true}}

	env := core.Envelope{Payload: map[string]any{"a": 1}}
	primeiro, err := l.ingestionID(&env)
	if err != nil {
		t.Fatalf("ingestionID: %v", err)
	}
	segundo, err := l.ingestionID(&env)
	if err != nil {
		t.Fatalf("ingestionID: %v", err)
	}
	if primeiro == segundo {
		t.Error("AutoID gera um id novo por linha; dois iguais significam que não é aleatório")
	}
	if _, err := uuid.Parse(primeiro); err != nil {
		t.Errorf("o id não é um UUID: %q", primeiro)
	}
}

// E sem AutoID continua determinístico, que é o que torna um re-run seguro.
func TestSemAutoIDOIDContinuaDeterministico(t *testing.T) {
	l := &Loader{cfg: &core.LoadConfig{Metadata: true}}

	env := core.Envelope{
		Provider: "p", Entity: "e", SourceKey: "k", RecordTS: "2026-01-01T00:00:00Z",
	}
	primeiro, err := l.ingestionID(&env)
	if err != nil {
		t.Fatalf("ingestionID: %v", err)
	}
	segundo, _ := l.ingestionID(&env)
	if primeiro != segundo {
		t.Errorf("o id determinístico mudou entre chamadas: %s != %s", primeiro, segundo)
	}
}
