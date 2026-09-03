package load

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"cloud.google.com/go/bigquery"
	core "github.com/AreteAcademy/bravis/sdk/internal/core"
)

// These are the only tests that prove a row actually lands. The in-memory
// tests assert the bytes we build; they cannot catch a wrong SourceFormat, a
// saver BigQuery refuses, or a payload the destination schema rejects -- all
// three of which shipped undetected.
//
// Run them against a real project:
//
//	export BRAVIS_IT_PROJECT=my-project
//	export BRAVIS_IT_DATASET=bravis_it        # must already exist
//	export BRAVIS_IT_BUCKET=my-staging-bucket # for the GCS strategy
//	go test ./load/... -run Integration
//
// They skip under -short and skip when BRAVIS_IT_PROJECT is unset, so the
// normal suite and CI stay offline.

type itEnv struct {
	project string
	dataset string
	bucket  string
}

func requireIntegration(t *testing.T) itEnv {
	t.Helper()

	if testing.Short() {
		t.Skip("integration test skipped under -short")
	}

	project := os.Getenv("BRAVIS_IT_PROJECT")
	if project == "" {
		t.Skip("BRAVIS_IT_PROJECT not set; skipping integration test")
	}

	dataset := os.Getenv("BRAVIS_IT_DATASET")
	if dataset == "" {
		dataset = "bravis_it"
	}

	return itEnv{project: project, dataset: dataset, bucket: os.Getenv("BRAVIS_IT_BUCKET")}
}

// createTable makes a throwaway table with the given schema and removes it
// when the test ends, so runs do not collide or leak.
func createTable(ctx context.Context, t *testing.T, env itEnv, schema bigquery.Schema) (*bigquery.Client, string) {
	t.Helper()

	client, err := bigquery.NewClient(ctx, env.project)
	if err != nil {
		t.Fatalf("bigquery client: %v", err)
	}

	name := fmt.Sprintf("it_%d", time.Now().UnixNano())
	table := client.Dataset(env.dataset).Table(name)

	if err := table.Create(ctx, &bigquery.TableMetadata{Schema: schema}); err != nil {
		t.Fatalf("create table %s: %v", name, err)
	}

	t.Cleanup(func() {
		if err := table.Delete(context.Background()); err != nil {
			t.Logf("could not drop %s: %v", name, err)
		}
		_ = client.Close()
	})

	return client, name
}

func countRows(ctx context.Context, t *testing.T, client *bigquery.Client, env itEnv, table string) int64 {
	t.Helper()

	q := client.Query(fmt.Sprintf("SELECT COUNT(*) AS n FROM `%s.%s.%s`", env.project, env.dataset, table))
	it, err := q.Read(ctx)
	if err != nil {
		t.Fatalf("count query: %v", err)
	}

	// A COUNT query always yields exactly one row.
	var row struct{ N int64 }
	if err := it.Next(&row); err != nil {
		t.Fatalf("read count: %v", err)
	}
	return row.N
}

func envelopes(n int) []core.Envelope {
	out := make([]core.Envelope, n)
	for i := range out {
		out[i] = core.Envelope{
			Provider:  "integration",
			Entity:    "rows",
			SourceKey: fmt.Sprintf("k-%d", i),
			RecordTS:  "2026-01-01T00:00:00Z",
			Payload:   map[string]any{"amount": i, "label": fmt.Sprintf("row-%d", i)},
		}
	}
	return out
}

// TestIntegrationInlineStrategy loads a small batch, which stays inline.
func TestIntegrationInlineStrategy(t *testing.T) {
	env := requireIntegration(t)
	ctx := context.Background()

	client, table := createTable(ctx, t, env, bigquery.Schema{
		{Name: "amount", Type: bigquery.IntegerFieldType},
		{Name: "label", Type: bigquery.StringFieldType},
	})

	loader, err := New(ctx, nil,
		core.WithProjectID(env.project),
		core.WithDataset(env.dataset),
		core.WithTable(table),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	const rows = 3
	result, err := loader.Load(ctx, envelopes(rows)...)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if result.Strategy != "inline" {
		t.Errorf("Strategy = %q, want inline", result.Strategy)
	}
	if result.Format != "ndjson" {
		t.Errorf("Format = %q, want the format actually written", result.Format)
	}

	if got := countRows(ctx, t, client, env, table); got != rows {
		t.Errorf("Loaded %d rows, table has %d", rows, got)
	}
}

// TestIntegrationGCSStrategy forces staging by dropping the threshold, and is
// the only check that SourceFormat is right: without it BigQuery reads our
// NDJSON as CSV and the row count comes back wrong.
func TestIntegrationGCSStrategy(t *testing.T) {
	env := requireIntegration(t)
	if env.bucket == "" {
		t.Skip("BRAVIS_IT_BUCKET not set; skipping GCS strategy")
	}
	ctx := context.Background()

	client, table := createTable(ctx, t, env, bigquery.Schema{
		{Name: "amount", Type: bigquery.IntegerFieldType},
		{Name: "label", Type: bigquery.StringFieldType},
	})

	loader, err := New(ctx, nil,
		core.WithProjectID(env.project),
		core.WithDataset(env.dataset),
		core.WithTable(table),
		core.WithStagingBucket(env.bucket),
		core.WithThresholdForGCS(1), // anything above 1 row stages
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	const rows = 3
	result, err := loader.Load(ctx, envelopes(rows)...)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if result.Strategy != "gcs" {
		t.Errorf("Strategy = %q, want gcs", result.Strategy)
	}

	if got := countRows(ctx, t, client, env, table); got != rows {
		t.Errorf("Loaded %d rows, table has %d", rows, got)
	}
}

// TestIntegrationMergeNaoDobra is the criterion from SDK_V2 6.9: load the
// same batch twice and the count must not double.
func TestIntegrationMergeDoesNotDouble(t *testing.T) {
	env := requireIntegration(t)
	ctx := context.Background()

	client, err := bigquery.NewClient(ctx, env.project)
	if err != nil {
		t.Fatalf("bigquery client: %v", err)
	}
	defer func() { _ = client.Close() }()

	name := fmt.Sprintf("it_merge_%d", time.Now().UnixNano())
	table := client.Dataset(env.dataset).Table(name)
	t.Cleanup(func() { _ = table.Delete(context.Background()) })

	loader, err := New(ctx, nil,
		core.WithProjectID(env.project),
		core.WithDataset(env.dataset),
		core.WithTable(name),
		core.WithExtraMetadata(true),
		core.WithCreateTable(true),
		core.WithDedup(core.DedupMerge),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	batch := envelopes(24)

	first, err := loader.Load(ctx, batch...)
	if err != nil {
		t.Fatalf("first load: %v", err)
	}
	if first.RowsLoaded != 24 {
		t.Errorf("the first load wrote %d rows, expected 24", first.RowsLoaded)
	}

	second, err := loader.Load(ctx, batch...)
	if err != nil {
		t.Fatalf("second load: %v", err)
	}
	if second.RowsLoaded != 0 {
		t.Errorf("the second load wrote %d rows; the merge should have ignored them all", second.RowsLoaded)
	}
	if second.RowsIgnored != 24 {
		t.Errorf("RowsIgnored = %d, expected 24", second.RowsIgnored)
	}
	if second.Dedup != core.DedupMerge {
		t.Errorf("the result must say which dedup ran: %q", second.Dedup)
	}

	// The proof that matters: without dedup this would be 48.
	if got := countRows(ctx, t, client, env, name); got != 24 {
		t.Errorf("after loading the same batch twice the table has %d rows, expected 24", got)
	}
}

// TestIntegrationCreatesTableFromData proves the load job creates the table
// on a first run, inferring the schema from the payload -- the only thing
// that can, since the SDK does not know your columns.
func TestIntegrationCreatesTableFromData(t *testing.T) {
	env := requireIntegration(t)
	ctx := context.Background()

	client, err := bigquery.NewClient(ctx, env.project)
	if err != nil {
		t.Fatalf("bigquery client: %v", err)
	}
	defer func() { _ = client.Close() }()

	name := fmt.Sprintf("it_created_%d", time.Now().UnixNano())
	table := client.Dataset(env.dataset).Table(name)
	t.Cleanup(func() { _ = table.Delete(context.Background()) })

	loader, err := New(ctx, nil,
		core.WithProjectID(env.project),
		core.WithDataset(env.dataset),
		core.WithTable(name),
		core.WithCreateTable(true),
		core.WithExtraMetadata(true),
		// Um campo que os próprios registros têm: o SDK não impõe coluna
		// nenhuma desde a v0.9.0, então "provider" não existe mais aqui.
		core.WithClusterBy("label"),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	res, err := loader.Load(ctx, envelopes(3)...)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !res.TableCreated {
		t.Error("the result must say it created the table")
	}

	md, err := table.Metadata(ctx)
	if err != nil {
		t.Fatalf("metadata: %v", err)
	}

	// Partitioning is not decoration: an unpartitioned landing table costs a
	// full scan on every MERGE the bronze layer runs.
	if md.TimePartitioning == nil || md.TimePartitioning.Field != "ingestion_loaded_at" {
		t.Errorf("created without partitioning on ingestion_loaded_at: %+v", md.TimePartitioning)
	}
	if md.Clustering == nil || md.Clustering.Fields[0] != "label" {
		t.Errorf("created without the clustering asked for: %+v", md.Clustering)
	}

	// The payload's own fields became columns, inferred from the data.
	names := map[string]bool{}
	for _, f := range md.Schema {
		names[f.Name] = true
	}
	for _, want := range []string{"amount", "label", "ingestion_id", "ingestion_loaded_at"} {
		if !names[want] {
			t.Errorf("column %s missing from the inferred schema: %v", want, names)
		}
	}
	// And nothing was imposed.
	for _, imposed := range []string{"payload", "entity", "source_key"} {
		if names[imposed] {
			t.Errorf("the SDK imposed column %q: %v", imposed, names)
		}
	}

	if got := countRows(ctx, t, client, env, name); got != 3 {
		t.Errorf("loaded 3 rows, table has %d", got)
	}
}

// TestIntegrationRefusesMissingTableUnasked proves the SDK does not run DDL
// on its own.
func TestIntegrationRefusesMissingTableUnasked(t *testing.T) {
	env := requireIntegration(t)
	ctx := context.Background()

	loader, err := New(ctx, nil,
		core.WithProjectID(env.project),
		core.WithDataset(env.dataset),
		core.WithTable(fmt.Sprintf("it_absent_%d", time.Now().UnixNano())),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if _, err := loader.Load(ctx, envelopes(1)...); err == nil {
		t.Fatal("loading into a missing table without CreateTable must fail")
	}
}

// TestIntegrationMergeIntoADifferentColumnOrder is the regression for the
// positional MERGE.
//
// The destination is created with its columns in a deliberately different
// order from the one autodetect produces for the staged payload. With
// INSERT ROW, BigQuery matches the two by position: the INT64 amount lands
// on ingestion_id and the load dies with a type error naming a column that
// is perfectly correct. Naming the columns is what makes this pass.
//
// The old tests never caught it because they let the SDK create the
// destination from the same batch, so both orders were the same by accident.
func TestIntegrationMergeIntoADifferentColumnOrder(t *testing.T) {
	env := requireIntegration(t)
	ctx := context.Background()

	// Reverse of what autodetect yields (json.Marshal sorts a map's keys:
	// amount, ingestion_id, ingestion_loaded_at, label).
	client, name := createTable(ctx, t, env, bigquery.Schema{
		{Name: "label", Type: bigquery.StringFieldType},
		{Name: "ingestion_loaded_at", Type: bigquery.TimestampFieldType},
		{Name: "ingestion_id", Type: bigquery.StringFieldType},
		{Name: "amount", Type: bigquery.IntegerFieldType},
	})

	loader, err := New(ctx, nil,
		core.WithProjectID(env.project),
		core.WithDataset(env.dataset),
		core.WithTable(name),
		core.WithExtraMetadata(true),
		core.WithDedup(core.DedupMerge),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	batch := envelopes(6)
	if _, err := loader.Load(ctx, batch...); err != nil {
		t.Fatalf("merging into a table whose column order differs: %v", err)
	}

	// Landing without an error is only half of it. Positional matching can
	// also succeed and put every value in the wrong column, so read the rows
	// back and check each one is where it belongs.
	q := client.Query(fmt.Sprintf(
		"SELECT amount, label FROM `%s.%s.%s` ORDER BY amount", env.project, env.dataset, name))
	it, err := q.Read(ctx)
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}
	for i := 0; i < 6; i++ {
		var row struct {
			Amount int64
			Label  string
		}
		if err := it.Next(&row); err != nil {
			t.Fatalf("row %d: %v", i, err)
		}
		if row.Amount != int64(i) {
			t.Errorf("row %d: amount = %d", i, row.Amount)
		}
		if want := fmt.Sprintf("row-%d", i); row.Label != want {
			t.Errorf("row %d: label = %q, want %q", i, row.Label, want)
		}
	}

	// And it still deduplicates through the named column list.
	second, err := loader.Load(ctx, batch...)
	if err != nil {
		t.Fatalf("second load: %v", err)
	}
	if second.RowsLoaded != 0 || second.RowsIgnored != 6 {
		t.Errorf("second load wrote %d and ignored %d, expected 0 and 6",
			second.RowsLoaded, second.RowsIgnored)
	}
}

// TestIntegrationFirstMergeLoadStillPartitions guards the seam left by the
// 0.11.0 fix.
//
// On a first load DedupMerge cedes to the plain path, because there is
// nothing to deduplicate against yet. That is the path that has to apply the
// layout -- and if it ever stops doing so, no row count would notice: the
// data lands, the table is simply unpartitioned forever, and every query
// against it scans everything.
func TestIntegrationFirstMergeLoadStillPartitions(t *testing.T) {
	env := requireIntegration(t)
	ctx := context.Background()

	client, err := bigquery.NewClient(ctx, env.project)
	if err != nil {
		t.Fatalf("bigquery client: %v", err)
	}
	defer func() { _ = client.Close() }()

	name := fmt.Sprintf("it_layout_%d", time.Now().UnixNano())
	table := client.Dataset(env.dataset).Table(name)
	t.Cleanup(func() { _ = table.Delete(context.Background()) })

	loader, err := New(ctx, nil,
		core.WithProjectID(env.project),
		core.WithDataset(env.dataset),
		core.WithTable(name),
		core.WithExtraMetadata(true),
		core.WithCreateTable(true),
		core.WithDedup(core.DedupMerge),
		core.WithClusterBy("label"),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if _, err := loader.Load(ctx, envelopes(4)...); err != nil {
		t.Fatalf("first load: %v", err)
	}

	meta, err := table.Metadata(ctx)
	if err != nil {
		t.Fatalf("reading metadata: %v", err)
	}
	if meta.TimePartitioning == nil {
		t.Fatal("the table was created without partitioning; every query over it scans everything")
	}
	if meta.TimePartitioning.Field != "ingestion_loaded_at" {
		t.Errorf("partitioned on %q, expected ingestion_loaded_at", meta.TimePartitioning.Field)
	}
	if meta.Clustering == nil || len(meta.Clustering.Fields) != 1 || meta.Clustering.Fields[0] != "label" {
		t.Errorf("ClusterBy did not reach the created table: %+v", meta.Clustering)
	}
}

// TestIntegrationWritesOnlyTheCallersFields is the contract, checked against
// the thing that actually decides it.
//
// With ExtraMetadata off the SDK adds nothing: the columns in the destination
// are the caller's fields and no others. No provider, no entity, no
// source_key, no payload wrapper, no ingestion_id -- the row shape is the
// caller's decision, made in Transform, and the SDK writes it.
func TestIntegrationWritesOnlyTheCallersFields(t *testing.T) {
	env := requireIntegration(t)
	ctx := context.Background()

	client, err := bigquery.NewClient(ctx, env.project)
	if err != nil {
		t.Fatalf("bigquery client: %v", err)
	}
	defer func() { _ = client.Close() }()

	name := fmt.Sprintf("it_own_shape_%d", time.Now().UnixNano())
	table := client.Dataset(env.dataset).Table(name)
	t.Cleanup(func() { _ = table.Delete(context.Background()) })

	loader, err := New(ctx, nil,
		core.WithProjectID(env.project),
		core.WithDataset(env.dataset),
		core.WithTable(name),
		core.WithCreateTable(true),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Provenance is deliberately filled in: it must not reach the table.
	batch := []core.Envelope{{
		Provider:  "acme",
		Entity:    "widgets",
		SourceKey: "k-1",
		RecordTS:  "2026-01-01T00:00:00Z",
		Payload:   map[string]any{"sku": "W-1", "quantidade": 3},
	}}

	if _, err := loader.Load(ctx, batch...); err != nil {
		t.Fatalf("load: %v", err)
	}

	meta, err := table.Metadata(ctx)
	if err != nil {
		t.Fatalf("reading metadata: %v", err)
	}

	got := map[string]bool{}
	for _, f := range meta.Schema {
		got[f.Name] = true
	}
	for _, want := range []string{"sku", "quantidade"} {
		if !got[want] {
			t.Errorf("the caller's field %q is not in the table", want)
		}
	}
	for _, forbidden := range []string{"provider", "entity", "source_key", "payload", "ingestion_id", "ingestion_loaded_at"} {
		if got[forbidden] {
			t.Errorf("the SDK wrote %q without being asked", forbidden)
		}
	}
	if len(meta.Schema) != 2 {
		t.Errorf("the table has %d columns, expected exactly the caller's 2", len(meta.Schema))
	}
}

// And the other half: with the flag on, exactly two fields are added.
func TestIntegrationExtraMetadataAddsExactlyTwoFields(t *testing.T) {
	env := requireIntegration(t)
	ctx := context.Background()

	client, err := bigquery.NewClient(ctx, env.project)
	if err != nil {
		t.Fatalf("bigquery client: %v", err)
	}
	defer func() { _ = client.Close() }()

	name := fmt.Sprintf("it_meta_%d", time.Now().UnixNano())
	table := client.Dataset(env.dataset).Table(name)
	t.Cleanup(func() { _ = table.Delete(context.Background()) })

	loader, err := New(ctx, nil,
		core.WithProjectID(env.project),
		core.WithDataset(env.dataset),
		core.WithTable(name),
		core.WithCreateTable(true),
		core.WithExtraMetadata(true),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	batch := []core.Envelope{{
		Provider: "acme", Entity: "widgets", SourceKey: "k-1",
		RecordTS: "2026-01-01T00:00:00Z",
		Payload:  map[string]any{"sku": "W-1", "quantidade": 3},
	}}
	if _, err := loader.Load(ctx, batch...); err != nil {
		t.Fatalf("load: %v", err)
	}

	meta, err := table.Metadata(ctx)
	if err != nil {
		t.Fatalf("reading metadata: %v", err)
	}

	got := map[string]bool{}
	for _, f := range meta.Schema {
		got[f.Name] = true
	}
	for _, want := range []string{"sku", "quantidade", "ingestion_id", "ingestion_loaded_at"} {
		if !got[want] {
			t.Errorf("%q is missing from the table", want)
		}
	}
	for _, forbidden := range []string{"provider", "entity", "source_key", "payload"} {
		if got[forbidden] {
			t.Errorf("ExtraMetadata wrote %q; it adds two fields, not six", forbidden)
		}
	}
	if len(meta.Schema) != 4 {
		t.Errorf("the table has %d columns, expected the caller's 2 plus exactly 2", len(meta.Schema))
	}
}
