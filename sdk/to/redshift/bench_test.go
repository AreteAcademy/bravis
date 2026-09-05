package redshift

import (
	"fmt"
	"testing"

	"github.com/AreteAcademy/brevis/sdk/internal/core"
)

// BenchmarkEncodeNDJSON fica no repositório porque foi ele que mostrou o
// tamanho do ganho: 13 alocações para 10 mil linhas, contra ~50 mil quando
// cada registro passava por um map[string]any no json.Encoder.
func BenchmarkEncodeNDJSON(b *testing.B) {
	envelopes := make([]core.Envelope, 10000)
	for i := range envelopes {
		envelopes[i] = core.Envelope{Payload: map[string]any{
			"ingestion_id": fmt.Sprintf("id-%d", i), "provider": "acme",
			"valor": "10.50", "nome": "registro qualquer",
		}}
	}
	colunas := []string{"ingestion_id", "provider", "valor", "nome"}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := EncodeNDJSON(envelopes, colunas); err != nil {
			b.Fatal(err)
		}
	}
}
