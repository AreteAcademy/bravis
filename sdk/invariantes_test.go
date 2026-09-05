package sdk_test

import (
	"context"
	"fmt"
	"iter"
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/sdk"
	"github.com/AreteAcademy/brevis/sdk/to/postgres"
)

// Os três invariantes da §14 do SDK_DECISOES, como testes.
//
// Eles estavam escritos como "decisão de produto, não técnica" -- e ficaram
// abertos meses. Um invariante que só existe em prosa é uma intenção; escrito
// como teste, ele é uma propriedade.

// I2 é exercitado onde ele DECIDE, em load.PlanoDeCriacao -- função pura,
// testável sem projeto do BigQuery. Um teste aqui que só afirmasse "deu erro"
// não distinguiria a recusa por falta de Schema de uma falta de credencial, e
// um teste que não distingue é quase um que não pode falhar.

// I2, a parte que dá para afirmar sem servidor: uma declaração sem tipo é
// recusada, e a recusa diz o que escrever.
func TestI2SchemaExigeTipo(t *testing.T) {
	alvo := sdk.Target{
		To:     postgres.Table{DSN: "postgres://x/y", Name: "t"},
		Schema: sdk.Schema{{Name: "a"}},
	}
	err := sdk.ValidateTarget(alvo)
	if err == nil {
		t.Fatal("coluna sem Type passou")
	}
	for _, quero := range []string{"a", "Type", "TypeString"} {
		if !strings.Contains(err.Error(), quero) {
			t.Errorf("o erro não diz %q: %v", quero, err)
		}
	}
}

// Columns e Schema juntos são duas fontes de verdade, e a que perde perde em
// silêncio.
func TestI2ColumnsESchemaJuntosERecusado(t *testing.T) {
	err := sdk.ValidateTarget(sdk.Target{
		To:      postgres.Table{DSN: "postgres://x/y", Name: "t"},
		Columns: []string{"a"},
		Schema:  sdk.Schema{{Name: "a", Type: sdk.TypeString}},
	})
	if err == nil {
		t.Fatal("declarar Columns e Schema passou")
	}
	if !strings.Contains(err.Error(), "drop Columns") {
		t.Errorf("o erro não diz qual manter: %v", err)
	}
}

// I3: a divergência aparece ANTES do extract.
//
// A mesma conferência já rodava no Load. O que mudou é o momento, e num vendor
// com cota isso é a diferença entre uma consulta de metadados e a janela
// inteira de quota gasta para descobrir que uma coluna não bate.
func TestI3ConfereAntesDoExtract(t *testing.T) {
	var extraiu bool
	fonte := fonteQueRegistra{&extraiu}

	p := sdk.Pipeline{
		Name:   "i3",
		Source: sdk.Source{From: fonte},
		Target: sdk.Target{
			To:      destinoQueRecusa{},
			Columns: []string{"coluna_que_nao_existe"},
		},
	}
	err := sdk.Execute(context.Background(), &p, nil)
	if err == nil {
		t.Fatal("o destino recusou e a execução seguiu")
	}
	if extraiu {
		t.Error("o extract rodou antes da conferência -- é exatamente a quota que o I3 economiza")
	}
	if !strings.Contains(err.Error(), "before the extract") {
		t.Errorf("o erro não diz que foi antes: %v", err)
	}
}

// I4: a partição é declarada.
func TestI4ParticaoDeclarada(t *testing.T) {
	err := sdk.ValidateTarget(sdk.Target{
		To:          postgres.Table{DSN: "postgres://x/y", Name: "t"},
		Schema:      sdk.Schema{{Name: "a", Type: sdk.TypeString}},
		PartitionBy: "nao_declarada",
	})
	if err == nil {
		t.Fatal("particionar por uma coluna que o Schema não declara passou")
	}
	if !strings.Contains(err.Error(), "nao_declarada") {
		t.Errorf("o erro não nomeia a coluna: %v", err)
	}
}

type fonteQueRegistra struct{ chamou *bool }

func (f fonteQueRegistra) Describe() string { return "fonte de teste" }
func (f fonteQueRegistra) Read(context.Context, sdk.ReadOptions) (iter.Seq2[sdk.Envelope, error], error) {
	*f.chamou = true
	return func(yield func(sdk.Envelope, error) bool) {}, nil
}

type destinoQueRecusa struct{}

func (destinoQueRecusa) Describe() string { return "destino de teste" }
func (destinoQueRecusa) Write(context.Context, []sdk.Envelope, sdk.WriteOptions) (*sdk.LoadResult, error) {
	return nil, fmt.Errorf("não deveria chegar aqui")
}
func (destinoQueRecusa) CheckDestination(_ context.Context, columns []string) error {
	return fmt.Errorf("the declaration lists %s, which the table does not have. "+
		"Caught before the extract, so no source quota was spent", strings.Join(columns, ", "))
}
