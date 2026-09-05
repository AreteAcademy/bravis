package postgres_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/sdk"
	topg "github.com/AreteAcademy/brevis/sdk/to/postgres"
)

// BenchmarkCargaPostgres mede a carga contra o servidor de verdade.
//
// Existe porque a §5 da fase 5 pede número: sem ele a documentação promete
// desempenho que ninguém mediu, que foi como o `DeleteAfterLoad` chegou ao
// texto com um default que ele não tinha.
//
//	BREVIS_IT_PG_DSN=... go test -run XXX -bench CargaPostgres ./to/postgres/
func BenchmarkCargaPostgres(b *testing.B) {
	d := ""
	if d = envOuPular(b); d == "" {
		return
	}
	conn := conectarBench(b, d)
	nome := tabelaBench(b, conn)

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

	destino := topg.Table{DSN: d, Name: nome}
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
