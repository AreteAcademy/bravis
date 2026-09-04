package from_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/storage"
	"github.com/aws/aws-sdk-go-v2/aws"
	awscfg "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/AreteAcademy/brevis/sdk/from"
	core "github.com/AreteAcademy/brevis/sdk/internal/core"
	"github.com/AreteAcademy/brevis/sdk/store/gcs"
	"github.com/AreteAcademy/brevis/sdk/store/s3"
	"github.com/AreteAcademy/brevis/sdk/to"
)

// Os testes de object storage rodam contra o MinIO do
// docker-compose.drivers.yml:
//
//	docker compose -f docker-compose.drivers.yml up -d minio
//	BREVIS_IT_S3_ENDPOINT=http://localhost:9000 go test ./from/... -run Integration
//
// Sem a variável eles pulam, e a suíte normal segue offline. São os únicos que
// provam que um objeto de verdade entra e sai -- os em memória provam os bytes
// que montamos, não o que o servidor aceita.
func s3Client(t *testing.T) (*awss3.Client, string) {
	t.Helper()

	if testing.Short() {
		t.Skip("integration test skipped under -short")
	}
	endpoint := os.Getenv("BREVIS_IT_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("BREVIS_IT_S3_ENDPOINT not set; skipping integration test")
	}

	cfg, err := awscfg.LoadDefaultConfig(context.Background(),
		awscfg.WithRegion("us-east-1"),
		awscfg.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			envOr("BREVIS_IT_S3_KEY", "brevis"),
			envOr("BREVIS_IT_S3_SECRET", "brevis-secret"), "")),
	)
	if err != nil {
		t.Fatalf("aws config: %v", err)
	}

	client := awss3.NewFromConfig(cfg, func(o *awss3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
		o.UsePathStyle = true // MinIO
	})

	bucket := fmt.Sprintf("it-files-%d", time.Now().UnixNano())
	if _, err := client.CreateBucket(context.Background(), &awss3.CreateBucketInput{
		Bucket: aws.String(bucket),
	}); err != nil {
		t.Fatalf("creating the bucket: %v", err)
	}
	t.Cleanup(func() { limpa(client, bucket) })

	return client, bucket
}

func envOr(k, padrao string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return padrao
}

func limpa(c *awss3.Client, bucket string) {
	ctx := context.Background()
	out, err := c.ListObjectsV2(ctx, &awss3.ListObjectsV2Input{Bucket: aws.String(bucket)})
	if err == nil {
		for _, o := range out.Contents {
			_, _ = c.DeleteObject(ctx, &awss3.DeleteObjectInput{Bucket: aws.String(bucket), Key: o.Key})
		}
	}
	_, _ = c.DeleteBucket(ctx, &awss3.DeleteBucketInput{Bucket: aws.String(bucket)})
}

// TestIntegrationS3RoundTrip é a prova da fase 1: um lote escrito em S3 e lido
// de volta, com os mesmos registros.
func TestIntegrationS3RoundTrip(t *testing.T) {
	client, bucket := s3Client(t)
	ctx := context.Background()
	store := s3.New(client)

	registros := []core.Envelope{
		{Payload: map[string]any{"sku": "W-1", "quantidade": 3}},
		{Payload: map[string]any{"sku": "W-2", "quantidade": 9}},
	}

	res, err := to.Files{Path: "s3://" + bucket + "/landing/", Store: store}.
		Write(ctx, registros, core.WriteOptions{})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if res.RowsLoaded != 2 {
		t.Errorf("RowsLoaded = %d", res.RowsLoaded)
	}

	seq, err := from.Files{Path: "s3://" + bucket + "/landing/*.ndjson", Store: store}.
		Read(ctx, core.ReadOptions{})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	var lidos []map[string]any
	for env, err := range seq {
		if err != nil {
			t.Fatalf("iterando: %v", err)
		}
		lidos = append(lidos, env.Payload.(map[string]any))
	}

	if len(lidos) != 2 {
		t.Fatalf("%d registros de volta, esperado 2", len(lidos))
	}
	if lidos[0]["sku"] != "W-1" || lidos[1]["sku"] != "W-2" {
		t.Errorf("os registros voltaram diferentes: %v", lidos)
	}
}

// A ordem é contrato, e num bucket ela depende da listagem do servidor.
func TestIntegrationS3LeEmOrdem(t *testing.T) {
	client, bucket := s3Client(t)
	ctx := context.Background()
	store := s3.New(client)

	// Escritos fora de ordem de propósito.
	for _, nome := range []string{"c", "a", "b"} {
		corpo := fmt.Sprintf(`{"n":%q}`+"\n", nome)
		if err := store.Create(ctx, bucket, "p/"+nome+".ndjson", bytes.NewReader([]byte(corpo))); err != nil {
			t.Fatal(err)
		}
	}

	for i := 0; i < 3; i++ {
		seq, err := from.Files{Path: "s3://" + bucket + "/p/*.ndjson", Store: store}.
			Read(ctx, core.ReadOptions{})
		if err != nil {
			t.Fatal(err)
		}
		var ordem []string
		for env, err := range seq {
			if err != nil {
				t.Fatal(err)
			}
			ordem = append(ordem, env.Payload.(map[string]any)["n"].(string))
		}
		if strings.Join(ordem, "") != "abc" {
			t.Fatalf("ordem = %v, esperado a,b,c em toda execução", ordem)
		}
	}
}

// O gzip atravessa a nuvem igual: escrito comprimido, lido pela extensão.
func TestIntegrationS3Comprimido(t *testing.T) {
	client, bucket := s3Client(t)
	ctx := context.Background()
	store := s3.New(client)

	if _, err := (to.Files{Path: "s3://" + bucket + "/z/", Compress: true, Store: store}).
		Write(ctx, []core.Envelope{{Payload: map[string]any{"id": 1}}}, core.WriteOptions{}); err != nil {
		t.Fatalf("Write: %v", err)
	}

	seq, err := from.Files{Path: "s3://" + bucket + "/z/*.gz", Store: store}.
		Read(ctx, core.ReadOptions{})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	n := 0
	for _, err := range seq {
		if err != nil {
			t.Fatalf("iterando: %v", err)
		}
		n++
	}
	if n != 1 {
		t.Errorf("%d registros, esperado 1", n)
	}
}

// A paginação do List existe porque um prefixo com mais de mil objetos seria
// lido pela metade -- e uma leitura parcial que reporta sucesso parece só um
// dia pequeno.
func TestIntegrationS3PaginaAListagem(t *testing.T) {
	client, bucket := s3Client(t)
	ctx := context.Background()
	store := s3.New(client)

	const n = 1005
	for i := 0; i < n; i++ {
		corpo := fmt.Sprintf(`{"i":%d}`+"\n", i)
		key := fmt.Sprintf("muitos/%05d.ndjson", i)
		if err := store.Create(ctx, bucket, key, bytes.NewReader([]byte(corpo))); err != nil {
			t.Fatalf("objeto %d: %v", i, err)
		}
	}

	keys, err := store.List(ctx, bucket, "muitos/")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(keys) != n {
		t.Errorf("List devolveu %d de %d objetos; a paginação não seguiu", len(keys), n)
	}
}

// TestIntegrationGCSRoundTrip roda contra o bucket real que a suíte do
// BigQuery já usa, porque não há emulador de GCS bom o bastante para provar
// o que este teste prova.
//
//	BREVIS_IT_BUCKET=meu-bucket go test ./from/... -run IntegrationGCS
func TestIntegrationGCSRoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped under -short")
	}
	bucket := os.Getenv("BREVIS_IT_BUCKET")
	if bucket == "" {
		t.Skip("BREVIS_IT_BUCKET not set; skipping integration test")
	}

	ctx := context.Background()
	client, err := storage.NewClient(ctx)
	if err != nil {
		t.Fatalf("gcs client: %v", err)
	}
	defer func() { _ = client.Close() }()

	prefixo := fmt.Sprintf("it-files-%d/", time.Now().UnixNano())
	store := gcs.New(client)
	t.Cleanup(func() {
		keys, _ := store.List(context.Background(), bucket, prefixo)
		for _, k := range keys {
			_ = client.Bucket(bucket).Object(k).Delete(context.Background())
		}
	})

	registros := []core.Envelope{
		{Payload: map[string]any{"sku": "W-1"}},
		{Payload: map[string]any{"sku": "W-2"}},
	}

	if _, err := (to.Files{Path: "gs://" + bucket + "/" + prefixo, Store: store}).
		Write(ctx, registros, core.WriteOptions{}); err != nil {
		t.Fatalf("Write: %v", err)
	}

	seq, err := from.Files{Path: "gs://" + bucket + "/" + prefixo + "*.ndjson", Store: store}.
		Read(ctx, core.ReadOptions{})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	n := 0
	for env, err := range seq {
		if err != nil {
			t.Fatalf("iterando: %v", err)
		}
		if env.Payload.(map[string]any)["sku"] == nil {
			t.Errorf("registro sem sku: %v", env.Payload)
		}
		n++
	}
	if n != 2 {
		t.Errorf("%d registros de volta, esperado 2", n)
	}
}
