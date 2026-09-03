package load

import (
	"context"
	"fmt"
	"os"
	"strings"
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

// TestIntegrationEnvelopeColumns proves the six-column contract lands in a
// table shaped the way SDK.md documents it.
func TestIntegrationEnvelopeColumns(t *testing.T) {
	env := requireIntegration(t)
	ctx := context.Background()

	client, table := createTable(ctx, t, env, bigquery.Schema{
		{Name: "ingestion_id", Type: bigquery.StringFieldType, Required: true},
		{Name: "ingestion_loaded_at", Type: bigquery.TimestampFieldType, Required: true},
		{Name: "provider", Type: bigquery.StringFieldType, Required: true},
		{Name: "entity", Type: bigquery.StringFieldType, Required: true},
		{Name: "source_key", Type: bigquery.StringFieldType},
		{Name: "payload", Type: bigquery.JSONFieldType, Required: true},
	})

	loader, err := New(ctx, nil,
		core.WithProjectID(env.project),
		core.WithDataset(env.dataset),
		core.WithTable(table),
		core.WithEnvelopeColumns(true),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	batch := envelopes(3)
	if _, err := loader.Load(ctx, batch...); err != nil {
		t.Fatalf("Load: %v", err)
	}

	if got := countRows(ctx, t, client, env, table); got != int64(len(batch)) {
		t.Errorf("Loaded %d rows, table has %d", len(batch), got)
	}

	// The ingestion_id written must be the one Envelope.IngestionID produces,
	// or rows from this SDK will not match rows from any other producer.
	want, err := batch[0].IngestionID()
	if err != nil {
		t.Fatal(err)
	}
	q := client.Query(fmt.Sprintf(
		"SELECT COUNT(*) AS n FROM `%s.%s.%s` WHERE ingestion_id = @id",
		env.project, env.dataset, table))
	q.Parameters = []bigquery.QueryParameter{{Name: "id", Value: want}}

	it, err := q.Read(ctx)
	if err != nil {
		t.Fatalf("lookup query: %v", err)
	}
	var row struct{ N int64 }
	if err := it.Next(&row); err != nil {
		t.Fatalf("read lookup: %v", err)
	}
	if row.N != 1 {
		t.Errorf("expected exactly one row with ingestion_id %s, found %d", want, row.N)
	}
}

// TestIntegrationCriaTabelaDeLanding proves the SDK creates the six-column
// table, partitioned and clustered, rather than asking the caller to.
func TestIntegrationCriaTabelaDeLanding(t *testing.T) {
	env := requireIntegration(t)
	ctx := context.Background()

	client, err := bigquery.NewClient(ctx, env.project)
	if err != nil {
		t.Fatalf("bigquery client: %v", err)
	}
	defer func() { _ = client.Close() }()

	nome := fmt.Sprintf("it_criada_%d", time.Now().UnixNano())
	table := client.Dataset(env.dataset).Table(nome)
	t.Cleanup(func() { _ = table.Delete(context.Background()) })

	loader, err := New(ctx, nil,
		core.WithProjectID(env.project),
		core.WithDataset(env.dataset),
		core.WithTable(nome),
		core.WithEnvelopeColumns(true),
		core.WithCriarTabela(true),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	res, err := loader.Load(ctx, envelopes(3)...)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !res.TableCreated {
		t.Error("o resultado tem de dizer que criou a tabela")
	}

	md, err := table.Metadata(ctx)
	if err != nil {
		t.Fatalf("metadata: %v", err)
	}

	// Partitioning is not decoration: an unpartitioned landing table costs a
	// full scan on every MERGE the bronze layer runs.
	if md.TimePartitioning == nil || md.TimePartitioning.Field != "ingestion_loaded_at" {
		t.Errorf("tabela criada sem partição por ingestion_loaded_at: %+v", md.TimePartitioning)
	}
	if md.Clustering == nil || len(md.Clustering.Fields) != 2 {
		t.Errorf("tabela criada sem clustering por provider/entity: %+v", md.Clustering)
	}
	if len(md.Schema) != 6 {
		t.Errorf("esperado 6 colunas, veio %d", len(md.Schema))
	}
}

// TestIntegrationRecusaTabelaDivergente proves the SDK refuses to write into
// a table that does not match the contract, instead of altering it. A loader
// that can ALTER is a loader that can erase history.
func TestIntegrationRecusaTabelaDivergente(t *testing.T) {
	env := requireIntegration(t)
	ctx := context.Background()

	client, table := createTable(ctx, t, env, bigquery.Schema{
		{Name: "ingestion_id", Type: bigquery.StringFieldType},
		{Name: "outra_coisa", Type: bigquery.StringFieldType},
	})
	_ = client

	loader, err := New(ctx, nil,
		core.WithProjectID(env.project),
		core.WithDataset(env.dataset),
		core.WithTable(table),
		core.WithEnvelopeColumns(true),
		core.WithCriarTabela(true),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, err = loader.Load(ctx, envelopes(1)...)
	if err == nil {
		t.Fatal("carregar numa tabela que não bate com o contrato tem de falhar")
	}
	// O erro precisa dizer qual coluna está errada, senão custa investigação.
	if !strings.Contains(err.Error(), "provider") {
		t.Errorf("o erro deveria nomear as colunas faltando: %v", err)
	}
}

// TestIntegrationMergeNaoDobra is the criterion from SDK_V2 6.9: load the
// same batch twice and the count must not double.
func TestIntegrationMergeNaoDobra(t *testing.T) {
	env := requireIntegration(t)
	ctx := context.Background()

	client, err := bigquery.NewClient(ctx, env.project)
	if err != nil {
		t.Fatalf("bigquery client: %v", err)
	}
	defer func() { _ = client.Close() }()

	nome := fmt.Sprintf("it_merge_%d", time.Now().UnixNano())
	table := client.Dataset(env.dataset).Table(nome)
	t.Cleanup(func() { _ = table.Delete(context.Background()) })

	loader, err := New(ctx, nil,
		core.WithProjectID(env.project),
		core.WithDataset(env.dataset),
		core.WithTable(nome),
		core.WithEnvelopeColumns(true),
		core.WithCriarTabela(true),
		core.WithDedup(core.DedupMerge),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	lote := envelopes(24)

	primeira, err := loader.Load(ctx, lote...)
	if err != nil {
		t.Fatalf("primeira carga: %v", err)
	}
	if primeira.RowsLoaded != 24 {
		t.Errorf("primeira carga escreveu %d linhas, esperado 24", primeira.RowsLoaded)
	}

	segunda, err := loader.Load(ctx, lote...)
	if err != nil {
		t.Fatalf("segunda carga: %v", err)
	}
	if segunda.RowsLoaded != 0 {
		t.Errorf("a segunda carga escreveu %d linhas; o merge deveria ignorar todas", segunda.RowsLoaded)
	}
	if segunda.RowsIgnored != 24 {
		t.Errorf("RowsIgnored = %d, esperado 24", segunda.RowsIgnored)
	}
	if segunda.Dedup != core.DedupMerge {
		t.Errorf("o resultado tem de dizer qual dedup rodou: %q", segunda.Dedup)
	}

	// A prova que importa: sem dedup seriam 48.
	if got := countRows(ctx, t, client, env, nome); got != 24 {
		t.Errorf("após duas cargas do mesmo lote a tabela tem %d linhas, esperado 24", got)
	}
}
