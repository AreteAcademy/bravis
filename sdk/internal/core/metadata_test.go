package core

import (
	"strings"
	"testing"
)

func linhaCore(campos map[string]any) []Envelope {
	return []Envelope{{Payload: campos}}
}

func TestColumnsRecusaColunaQueNinguemEntregou(t *testing.T) {
	err := CheckColumns(
		[]string{"ingestion_id", "provider", "entity", "payload"},
		linhaCore(map[string]any{"ingestion_id": "x", "provider": "p", "payload": "{}"}),
	)
	if err == nil {
		t.Fatal("uma coluna declarada e não entregue landa NULL sem ninguém saber")
	}
	if !strings.Contains(err.Error(), "entity") {
		t.Errorf("o erro não nomeia a coluna: %v", err)
	}
	// E diz o que a linha de fato tem, para o conserto sair de uma leitura.
	if !strings.Contains(err.Error(), "provider") {
		t.Errorf("o erro não lista o que a linha tem: %v", err)
	}
}

// Critério 3: campo na linha que a declaração não lista.
func TestColumnsRecusaCampoNaoDeclarado(t *testing.T) {
	err := CheckColumns(
		[]string{"provider", "payload"},
		linhaCore(map[string]any{"provider": "p", "payload": "{}", "surpresa": 1}),
	)
	if err == nil {
		t.Fatal("um campo não declarado seria escrito numa tabela que nunca o mencionou")
	}
	if !strings.Contains(err.Error(), "surpresa") {
		t.Errorf("o erro não nomeia o campo: %v", err)
	}
}

// As duas colunas do Metadata podem ser declaradas, e é o ponto da spec:
// dentro do Transform elas jamais poderiam, porque ainda não existem lá.
func TestColumnsAceitaAsColunasDoMetadata(t *testing.T) {
	err := CheckColumns(
		[]string{"ingestion_id", "ingestion_loaded_at", "provider", "entity", "source_key", "payload"},
		linhaCore(map[string]any{
			"ingestion_id": "u", "ingestion_loaded_at": "2026-01-01T00:00:00Z",
			"provider": "p", "entity": "e", "source_key": "k", "payload": "{}",
		}),
	)
	if err != nil {
		t.Fatalf("as seis colunas do DDL deveriam passar: %v", err)
	}
}

func TestColumnsVazioNaoConfereNada(t *testing.T) {
	if err := CheckColumns(nil, linhaCore(map[string]any{"qualquer": 1})); err != nil {
		t.Errorf("sem declaração não há o que conferir: %v", err)
	}
}

// Critério 4: a declaração contra a tabela real.
