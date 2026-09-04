package core

import "testing"

// A fórmula é congelada: uma linha escrita em Go tem de casar com a que um
// fetcher Python escreve para o mesmo registro. Conferida contra o uuid.uuid5
// do Python, não contra outra implementação nossa -- duas implementações
// nossas podem mudar juntas e o teste passar.
func TestComputeIngestionIDContraOPython(t *testing.T) {
	casos := []struct {
		provider, entity, sourceKey, recordTS, esperado string
	}{
		// uuid.uuid5(UUID("e3a4f8c0-1b9d-4ea0-9c2e-77f6a6c4a4d7"), "p|e|k|2026-01-01T00:00:00Z")
		{"p", "e", "k", "2026-01-01T00:00:00Z", "178d0b49-dece-5738-b8eb-f5cae222a1ea"},
	}

	for _, c := range casos {
		got, err := ComputeIngestionID(c.provider, c.entity, c.sourceKey, c.recordTS)
		if err != nil {
			t.Fatalf("ComputeIngestionID: %v", err)
		}
		if got != c.esperado {
			t.Errorf("ComputeIngestionID(%q,%q,%q,%q) = %s, congelado em %s",
				c.provider, c.entity, c.sourceKey, c.recordTS, got, c.esperado)
		}
	}
}

// O Envelope e o transformer têm de sair no mesmo lugar, porque a mesma linha
// pode chegar pelos dois caminhos.
func TestEnvelopeUsaAMesmaFormula(t *testing.T) {
	env := Envelope{Provider: "p", Entity: "e", SourceKey: "k", RecordTS: "t"}
	pelaEnvelope, err := env.IngestionID()
	if err != nil {
		t.Fatal(err)
	}
	direto, _ := ComputeIngestionID("p", "e", "k", "t")
	if pelaEnvelope != direto {
		t.Errorf("os dois caminhos divergiram: %s != %s", pelaEnvelope, direto)
	}
}
