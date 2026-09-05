package sdk

import (
	"fmt"
	"testing"
)

// cadeiaTipica é a cadeia que um fetcher real escreve: limpar, renomear,
// carimbar proveniência e compor o ingestion_id.
func cadeiaTipica() []Transformer {
	return []Transformer{
		Accept("id", "nome", "valor", "ts"),
		Rename(map[string]string{"id": "source_key", "ts": "record_ts"}),
		Compute("provider", func(map[string]any) (any, error) { return "bench", nil }),
		Compute("entity", func(map[string]any) (any, error) { return "registros", nil }),
		IngestionID(),
		IngestionLoadedAt(),
	}
}

func registro(i int) map[string]any {
	return map[string]any{
		"id":    fmt.Sprint(i),
		"nome":  "registro qualquer",
		"valor": 10.5,
		"ts":    "2026-09-05T12:00:00Z",
	}
}

// BenchmarkCadeiaDeTransform mede só a cadeia, sem HTTP nem decodificação --
// o benchmark anterior misturava os três, e o profile precisou de um segundo
// olhar para separar o que era de quem.
func BenchmarkCadeiaDeTransform(b *testing.B) {
	fns := cadeiaTipica()
	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		payload, _, err := applyAll(fns, registro(i))
		if err != nil {
			b.Fatal(err)
		}
		if payload == nil {
			b.Fatal("payload vazio")
		}
	}
	b.StopTimer()
	b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "registros/s")
}

// BenchmarkUmTransformerSo isola o custo de UMA cópia de mapa, para que a
// conta "quantas cópias a cadeia faz" seja verificável e não inferida.
func BenchmarkUmTransformerSo(b *testing.B) {
	fns := []Transformer{Compute("provider", func(map[string]any) (any, error) { return "x", nil })}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := applyAll(fns, registro(i)); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkIngestionID isola a composição do id, que o profile apontou como o
// maior bloco sozinho.
func BenchmarkIngestionID(b *testing.B) {
	fns := []Transformer{IngestionID()}
	r := map[string]any{
		"provider": "acme", "entity": "pedidos",
		"source_key": "12345", "record_ts": "2026-09-05T12:00:00Z",
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		copia := make(map[string]any, len(r))
		for k, v := range r {
			copia[k] = v
		}
		if _, _, err := applyAll(fns, copia); err != nil {
			b.Fatal(err)
		}
	}
}
