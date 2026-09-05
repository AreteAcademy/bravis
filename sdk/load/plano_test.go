package load

import (
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/sdk/internal/core"
)

// TestI2PlanoDeCriacaoNuncaInfere é o invariante I2 como propriedade
// verificável, sem projeto do BigQuery nenhum.
//
// Ele era prosa numa seção chamada "onde a discussão continua", e ficou aberto
// meses. O que fecha um invariante não é decidi-lo: é conseguir exercitá-lo.
func TestI2PlanoDeCriacaoNuncaInfere(t *testing.T) {
	casos := []struct {
		nome  string
		cfg   *core.LoadConfig
		quero ComoCriar
		erro  bool
	}{
		{
			"com Schema, monta o DDL da declaração",
			&core.LoadConfig{Schema: core.Schema{{Name: "a", Type: core.TypeString}}},
			CriarPorSchema, false,
		},
		{
			"com CreateSQL, roda o DDL de quem escreveu",
			&core.LoadConfig{CreateSQL: "CREATE TABLE t (a STRING)"},
			CriarPorSQL, false,
		},
		{
			"CreateSQL vence o Schema: um DDL escrito à mão diz mais",
			&core.LoadConfig{
				CreateSQL: "CREATE TABLE t (a NUMERIC(18,2))",
				Schema:    core.Schema{{Name: "a", Type: core.TypeNumeric}},
			},
			CriarPorSQL, false,
		},
		{
			"sem nenhum dos dois, RECUSA -- e é isto que o I2 exige",
			&core.LoadConfig{Columns: []string{"a", "b"}},
			0, true,
		},
	}

	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			got, err := PlanoDeCriacao(c.cfg, "d.t")
			if c.erro {
				if err == nil {
					t.Fatal("aceitou criar tabela sem dizer os tipos -- o autodetect voltou")
				}
				for _, quero := range []string{"Target.Schema", "CreateSQL", "does not infer"} {
					if !strings.Contains(err.Error(), quero) {
						t.Errorf("o erro não diz %q: %v", quero, err)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("PlanoDeCriacao: %v", err)
			}
			if got != c.quero {
				t.Errorf("= %v, esperado %v", got, c.quero)
			}
		})
	}
}

// TestI2SchemaViraOTipoDoBigQuery: a tabela de tradução, uma linha por tipo.
func TestI2SchemaViraOTipoDoBigQuery(t *testing.T) {
	s := core.Schema{
		{Name: "texto", Type: core.TypeString, Required: true},
		{Name: "inteiro", Type: core.TypeInt64},
		{Name: "flutuante", Type: core.TypeFloat64},
		{Name: "decimal", Type: core.TypeNumeric},
		{Name: "booleano", Type: core.TypeBool},
		{Name: "instante", Type: core.TypeTimestamp},
		{Name: "data", Type: core.TypeDate},
		{Name: "documento", Type: core.TypeJSON},
		{Name: "bytes", Type: core.TypeBytes},
	}

	got, err := bigquerySchema(s)
	if err != nil {
		t.Fatal(err)
	}
	esperado := map[string]string{
		"texto": "STRING", "inteiro": "INTEGER", "flutuante": "FLOAT",
		"decimal": "NUMERIC", "booleano": "BOOLEAN", "instante": "TIMESTAMP",
		"data": "DATE", "documento": "JSON", "bytes": "BYTES",
	}
	if len(got) != len(esperado) {
		t.Fatalf("%d colunas, esperado %d", len(got), len(esperado))
	}
	for _, f := range got {
		if string(f.Type) != esperado[f.Name] {
			t.Errorf("%s = %s, esperado %s", f.Name, f.Type, esperado[f.Name])
		}
	}
	if !got[0].Required {
		t.Error("Required não virou NOT NULL")
	}
}

// TestI2ColunasDoSDKVencemADeclaracao: ingestion_id e ingestion_loaded_at são
// do SDK, e um NULLABLE ali deixaria a dedup casar com nulo.
func TestI2ColunasDoSDKVencemADeclaracao(t *testing.T) {
	got, err := bigquerySchema(core.Schema{
		{Name: core.MetadataID, Type: core.TypeInt64},        // errado de propósito
		{Name: core.MetadataLoadedAt, Type: core.TypeString}, // errado de propósito
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(got[0].Type) != "STRING" || !got[0].Required {
		t.Errorf("%s = %s required=%v; a forma do SDK devia vencer",
			core.MetadataID, got[0].Type, got[0].Required)
	}
	if string(got[1].Type) != "TIMESTAMP" || !got[1].Required {
		t.Errorf("%s = %s required=%v", core.MetadataLoadedAt, got[1].Type, got[1].Required)
	}
}

// TestI4ParticaoSaiDaDeclaracao.
func TestI4ParticaoSaiDaDeclaracao(t *testing.T) {
	if got := particaoDe(&core.LoadConfig{PartitionBy: "minha_coluna"}); got != "minha_coluna" {
		t.Errorf("= %q, a declaração devia vencer", got)
	}
	if got := particaoDe(&core.LoadConfig{}); got != core.MetadataLoadedAt {
		t.Errorf("= %q, sem declaração fica o padrão documentado", got)
	}
}
