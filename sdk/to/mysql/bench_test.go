package mysql_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	frommy "github.com/AreteAcademy/brevis/sdk/from/mysql"
	tomy "github.com/AreteAcademy/brevis/sdk/to/mysql"

	"github.com/AreteAcademy/brevis/sdk"
)

// BenchmarkCargaMySQL mede a carga contra o servidor de verdade, para que a
// documentação não prometa desempenho que ninguém mediu.
//
//	BREVIS_IT_MYSQL_DSN=... go test -run XXX -bench CargaMySQL ./to/mysql/
func BenchmarkCargaMySQL(b *testing.B) {
	d := os.Getenv("BREVIS_IT_MYSQL_DSN")
	if d == "" {
		b.Skip("BREVIS_IT_MYSQL_DSN não definida")
	}

	db, err := sql.Open("mysql", frommy.ComParseTime(d))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = db.Close() })

	nome := fmt.Sprintf("bench_%d", time.Now().UnixNano())
	if _, err := db.Exec(fmt.Sprintf(`CREATE TABLE %s (
		ingestion_id VARCHAR(36) NOT NULL, ingestion_loaded_at DATETIME(6) NOT NULL,
		provider VARCHAR(64), source_key VARCHAR(64), valor DECIMAL(18,2))`, nome)); err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _, _ = db.Exec("DROP TABLE IF EXISTS " + nome) })

	const linhas = 10000
	lote := make([]sdk.Envelope, linhas)
	agora := time.Now().UTC().Format(time.RFC3339)
	for i := range lote {
		lote[i] = sdk.Envelope{Payload: map[string]any{
			"ingestion_id":        fmt.Sprintf("id-%06d", i),
			"ingestion_loaded_at": agora,
			"provider":            "bench",
			"source_key":          fmt.Sprintf("k%d", i),
			"valor":               "10.50",
		}}
	}

	destino := tomy.Table{DSN: d, Name: nome}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := destino.Write(context.Background(), lote, sdk.WriteOptions{}); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	b.ReportMetric(float64(linhas*b.N)/b.Elapsed().Seconds(), "linhas/s")
}
