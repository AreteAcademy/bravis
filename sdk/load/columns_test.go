package load

import (
	"strings"
	"testing"

	"cloud.google.com/go/bigquery"
	core "github.com/AreteAcademy/bravis/sdk/internal/core"
)

func linha(campos map[string]any) []core.Envelope {
	return []core.Envelope{{Payload: campos}}
}

// Critério 2: coluna declarada que nem o Transform nem o Metadata entregam.
func TestColumnsRecusaColunaQueNinguemEntregou(t *testing.T) {
	err := checkColumns(
		[]string{"ingestion_id", "provider", "entity", "payload"},
		linha(map[string]any{"ingestion_id": "x", "provider": "p", "payload": "{}"}),
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
	err := checkColumns(
		[]string{"provider", "payload"},
		linha(map[string]any{"provider": "p", "payload": "{}", "surpresa": 1}),
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
	err := checkColumns(
		[]string{"ingestion_id", "ingestion_loaded_at", "provider", "entity", "source_key", "payload"},
		linha(map[string]any{
			"ingestion_id": "u", "ingestion_loaded_at": "2026-01-01T00:00:00Z",
			"provider": "p", "entity": "e", "source_key": "k", "payload": "{}",
		}),
	)
	if err != nil {
		t.Fatalf("as seis colunas do DDL deveriam passar: %v", err)
	}
}

func TestColumnsVazioNaoConfereNada(t *testing.T) {
	if err := checkColumns(nil, linha(map[string]any{"qualquer": 1})); err != nil {
		t.Errorf("sem declaração não há o que conferir: %v", err)
	}
}

// Critério 4: a declaração contra a tabela real.
func TestColumnsRecusaColunaQueATabelaNaoTem(t *testing.T) {
	schema := bigquery.Schema{
		{Name: "ingestion_id", Type: bigquery.StringFieldType},
		{Name: "payload", Type: bigquery.JSONFieldType},
	}

	err := checkDeclaredAgainstTable(
		[]string{"ingestion_id", "payload", "provider"}, schema, "bronze.pedidos")
	if err == nil {
		t.Fatal("uma coluna declarada que a tabela não tem é uma carga que não funciona")
	}
	for _, want := range []string{"provider", "bronze.pedidos", "ingestion_id"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("o erro não menciona %q (precisa nomear os dois lados): %v", want, err)
		}
	}
}

// Assimétrico, como a reconcile: coluna da tabela que a declaração omite fica
// NULL, e uma landing legitimamente tem dessas.
func TestColumnsAceitaColunaDaTabelaNaoDeclarada(t *testing.T) {
	schema := bigquery.Schema{
		{Name: "ingestion_id", Type: bigquery.StringFieldType},
		{Name: "payload", Type: bigquery.JSONFieldType},
		{Name: "source_key", Type: bigquery.StringFieldType},
	}
	if err := checkDeclaredAgainstTable([]string{"ingestion_id", "payload"}, schema, "t"); err != nil {
		t.Errorf("coluna da tabela que a declaração não lista fica NULL, e isso é legítimo: %v", err)
	}
}
