package sdk_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/AreteAcademy/brevis/sdk"
	"github.com/AreteAcademy/brevis/sdk/from"
)

// TestSnapshotNaoDependeDaPosicaoNaCadeia é o item 3, e é a razão de ele NÃO
// ser um transformer.
//
// Como transformer, o retrato dependeria da posição: colocá-lo depois de um
// Compute produziria um registro "cru" carregando o campo que a cadeia acabou
// de escrever. Isso não dá erro -- dá um dado errado que ninguém percebe até
// alguém consultá-lo meses depois.
//
// Tirado onde o registro sai da fonte, não há ordem que possa contaminá-lo.
func TestSnapshotNaoDependeDaPosicaoNaCadeia(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"id":"1","temperatura":19.5}`)
	}))
	defer srv.Close()

	dados, err := sdk.Extract(context.Background(), sdk.Source{
		From: from.HTTP{URL: srv.URL}, Snapshot: "payload",
	})
	if err != nil {
		t.Fatal(err)
	}
	// A cadeia escreve DEPOIS do retrato, e escreve bastante.
	dados = sdk.Transform(dados,
		sdk.Compute("derivado", func(map[string]any) (any, error) { return "novo", nil }),
		sdk.Rename(map[string]string{"id": "source_key"}),
	)

	var linha map[string]any
	for env, err := range dados.Records {
		if err != nil {
			t.Fatal(err)
		}
		linha = env.Payload.(map[string]any)
	}

	retrato, ok := linha["payload"].(map[string]any)
	if !ok {
		t.Fatalf("sem retrato: %v", linha)
	}
	if _, contaminado := retrato["derivado"]; contaminado {
		t.Errorf("o retrato carrega um campo que a cadeia escreveu: %v", retrato)
	}
	if retrato["id"] != "1" {
		t.Errorf("o retrato perdeu o nome original do campo: %v", retrato)
	}
	if linha["source_key"] != "1" {
		t.Errorf("a cadeia não rodou sobre o registro: %v", linha)
	}
}

// TestSnapshotRecusaSobrescreverOQueAFonteMandou: gravar por cima perderia o
// que veio da fonte, em silêncio.
func TestSnapshotRecusaSobrescreverOQueAFonteMandou(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"payload":"da fonte"}`)
	}))
	defer srv.Close()

	dados, err := sdk.Extract(context.Background(), sdk.Source{
		From: from.HTTP{URL: srv.URL}, Snapshot: "payload",
	})
	if err != nil {
		t.Fatal(err)
	}
	var visto error
	for _, err := range dados.Records {
		if err != nil {
			visto = err
		}
	}
	if visto == nil {
		t.Fatal("gravou o retrato por cima de um campo da fonte")
	}
	if !strings.Contains(visto.Error(), "payload") {
		t.Errorf("o erro não nomeia o campo: %v", visto)
	}
}

// TestSkipWithoutDescartaEmVezDeDerrubar: uma linha sem o campo que compõe a chave
// não tem identidade estável e não pode entrar -- mas também não é motivo para
// derrubar a janela inteira.
func TestSkipWithoutDescartaEmVezDeDerrubar(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `[{"id":"1"},{"id":null},{"outro":2},{"id":"4"}]`)
	}))
	defer srv.Close()

	dados, err := sdk.Extract(context.Background(), sdk.Source{From: from.HTTP{URL: srv.URL}})
	if err != nil {
		t.Fatal(err)
	}
	dados = sdk.Transform(dados, sdk.SkipWithout("id"))

	var ids []string
	for env, err := range dados.Records {
		if err != nil {
			t.Fatalf("SkipWithout derrubou a janela: %v", err)
		}
		ids = append(ids, env.Payload.(map[string]any)["id"].(string))
	}
	if len(ids) != 2 || ids[0] != "1" || ids[1] != "4" {
		t.Errorf("sobraram %v; esperava as duas com id -- nulo e ausente são a mesma coisa aqui", ids)
	}
}

// TestNamespaceMudaOId é o item 1: o namespace é de quem usa, não da
// biblioteca.
func TestNamespaceMudaOId(t *testing.T) {
	registro := func() map[string]any {
		return map[string]any{
			"provider": "acme", "entity": "pedidos",
			"source_key": "1", "record_ts": "2026-09-05T12:00:00Z",
		}
	}

	padrao, err := sdk.IngestionID()(registro())
	if err != nil {
		t.Fatal(err)
	}
	meu := uuid.MustParse("11111111-2222-3333-4444-555555555555")
	outro, err := sdk.Namespace(meu).IngestionID()(registro())
	if err != nil {
		t.Fatal(err)
	}

	idPadrao := padrao.(map[string]any)[sdk.ColumnIngestionID].(string)
	idOutro := outro.(map[string]any)[sdk.ColumnIngestionID].(string)
	if idPadrao == idOutro {
		t.Error("namespaces diferentes produziram o mesmo id; então o namespace não é usado")
	}
}

// TestNamespacePadraoNaoMudou é a garantia que impede a feature de quebrar
// quem já gravou: o id de quem não escolhe namespace tem de ser byte a byte o
// de antes.
func TestNamespacePadraoNaoMudou(t *testing.T) {
	saida, err := sdk.IngestionID()(map[string]any{
		"provider": "open_meteo", "entity": "hourly",
		"source_key": "123", "record_ts": "2026-09-05T12:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	got := saida.(map[string]any)[sdk.ColumnIngestionID].(string)

	// Este valor é o mesmo desde a v0.1.x, conferido contra o uuid.uuid5 do
	// Python. Se ele mudar, toda linha já gravada perdeu a identidade.
	const congelado = "7e18f9f9-37c4-5033-abce-def940db4cba"
	if got != congelado {
		t.Errorf("o id padrão mudou: %s, era %s", got, congelado)
	}
}

// TestNamespaceEscolhidoEDeterministico: escolher um namespace não pode
// introduzir variação entre execuções.
func TestNamespaceEscolhidoEDeterministico(t *testing.T) {
	meu := sdk.Namespace(uuid.MustParse("11111111-2222-3333-4444-555555555555"))
	var anterior string
	for i := 0; i < 5; i++ {
		saida, err := meu.IngestionID()(map[string]any{
			"provider": "a", "entity": "b", "source_key": "c", "record_ts": "d",
		})
		if err != nil {
			t.Fatal(err)
		}
		got := saida.(map[string]any)[sdk.ColumnIngestionID].(string)
		if i > 0 && got != anterior {
			t.Fatalf("o id variou entre execuções: %s e %s", anterior, got)
		}
		anterior = got
	}
}
