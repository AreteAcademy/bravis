package sdk_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/sdk"
	"github.com/AreteAcademy/brevis/sdk/from"
	"github.com/AreteAcademy/brevis/sdk/pycompat"
)

// Estes ficam no pacote sdk porque exercitam o SEAM -- KeyWith e
// IngestionIDWith --, e não a renderização em si. O pycompat é só a
// implementação que passa por ele.

// TestIngestionIDComPycompatCasaComOPython é a prova que interessa a quem
// porta: o id que o Go compõe é o mesmo que o Python compunha, nos três casos
// que divergem (nil, bool e float integral).
func TestIngestionIDComPycompatCasaComOPython(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("sem python3")
	}

	// source_key entra como json.Number, que e o que PreserveNumbers entrega
	// -- um float64 cru o Texto recusa, porque ali o literal ja se perdeu.
	registro := func() map[string]any {
		return map[string]any{
			"provider": "acme", "entity": nil,
			"source_key": json.Number("19.0"), "record_ts": true,
		}
	}

	saida, err := sdk.IngestionIDWith(pycompat.Texto)(registro())
	if err != nil {
		t.Fatal(err)
	}
	got := saida.(map[string]any)[sdk.ColumnIngestionID].(string)

	script := `
import uuid
ns = uuid.UUID('e3a4f8c0-1b9d-4ea0-9c2e-77f6a6c4a4d7')
chave = '|'.join(str(v) for v in ['acme', None, 19.0, True])
print(uuid.uuid5(ns, chave))`
	b, err := exec.Command("python3", "-c", script).Output()
	if err != nil {
		t.Fatalf("python3: %v", err)
	}
	quero := strings.TrimSpace(string(b))
	if got != quero {
		t.Errorf("IngestionIDWith(pycompat.Texto) = %s, e o Python dá %s", got, quero)
	}

	// E o padrão NÃO casa. É essa a divergência que motivou tudo, e mantê-la é
	// decisão escrita: trocar o padrão reescreveria o id de toda linha que o
	// Go já gravou.
	saidaPadrao, err := sdk.IngestionID()(registro())
	if err != nil {
		t.Fatal(err)
	}
	if padrao := saidaPadrao.(map[string]any)[sdk.ColumnIngestionID].(string); padrao == quero {
		t.Error("o padrão passou a casar com o Python; se foi intencional, a decisão " +
			"documentada em asText mudou e este teste precisa ser reescrito")
	}
}

// TestKeyWithCasaComOPython: o mesmo, para a chave.
func TestKeyWithCasaComOPython(t *testing.T) {
	registro := map[string]any{"a": nil, "b": json.Number("19.0"), "c": true}

	got, err := sdk.KeyWith(pycompat.Texto, "a", "b", "c")(registro)
	if err != nil {
		t.Fatal(err)
	}
	if got != "None|19.0|True" {
		t.Errorf("KeyWith = %q, o Python daria \"None|19.0|True\"", got)
	}

	padrao, err := sdk.Key("a", "b", "c")(registro)
	if err != nil {
		t.Fatal(err)
	}
	if padrao != "|19.0|true" {
		t.Errorf("Key = %q -- se mudou, o padrão mudou", padrao)
	}
}

// TestKeyWithRecusaFloat64NomeandoOCampo: a recusa do item 11 chega ao
// consumidor pela porta que ele usa, e nomeia o campo -- sem o nome, quem lê o
// erro não sabe qual dos seis é.
func TestKeyWithRecusaFloat64NomeandoOCampo(t *testing.T) {
	_, err := sdk.KeyWith(pycompat.Texto, "a", "b")(map[string]any{
		"a": "ok", "b": float64(19),
	})
	if err == nil {
		t.Fatal("um float64 passou; o literal já se perdeu e ele adivinhou")
	}
	if !strings.Contains(err.Error(), `"b"`) {
		t.Errorf("o erro não nomeia o campo: %v", err)
	}
	if !strings.Contains(err.Error(), "PreserveNumbers") {
		t.Errorf("o erro não oferece a saída: %v", err)
	}
}

// TestKeyWithRecusaNomeandoOCampo: sem o nome, quem lê o erro não sabe qual
// dos seis campos é.
func TestKeyWithRecusaNomeandoOCampo(t *testing.T) {
	_, err := sdk.KeyWith(pycompat.Texto, "a", "b")(map[string]any{"a": "ok", "b": 1e-5})
	if err == nil {
		t.Fatal("a faixa exponencial passou")
	}
	if !strings.Contains(err.Error(), `"b"`) {
		t.Errorf("o erro não nomeia o campo: %v", err)
	}
}

// TestTextoOuVazioEOIdiomaDoPython: `str(x or "")` é o mais comum na
// composição de chave, e o zero é o caso que quem escreve à mão erra.
func TestTextoOuVazioEOIdiomaDoPython(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("sem python3")
	}

	casos := []struct {
		valor   any
		literal string
	}{
		{nil, "None"}, {"", "''"}, {json.Number("0.0"), "0.0"}, {int64(0), "0"},
		{false, "False"}, {[]any{}, "[]"}, {map[string]any{}, "{}"},
		{"ola", "'ola'"}, {json.Number("19.0"), "19.0"}, {true, "True"},
	}

	var literais []string
	for _, c := range casos {
		literais = append(literais, c.literal)
	}
	script := "import sys\nfor v in [" + strings.Join(literais, ", ") +
		"]:\n    sys.stdout.write(str(v or '') + '\\x00')\n"
	b, err := exec.Command("python3", "-c", script).Output()
	if err != nil {
		t.Fatalf("python3: %v", err)
	}
	quero := strings.Split(strings.TrimSuffix(string(b), "\x00"), "\x00")

	for i, c := range casos {
		got, err := pycompat.TextoOuVazio(c.valor)
		if err != nil {
			t.Errorf("TextoOuVazio(%#v): %v", c.valor, err)
			continue
		}
		if got != quero[i] {
			t.Errorf("TextoOuVazio(%#v) = %q, e str(%s or '') = %q",
				c.valor, got, c.literal, quero[i])
		}
	}
}

// TestDivergenciaEntreOPadraoEOPython é a documentação da divergência, como
// teste.
//
// O SDK NÃO usa a renderização do Python por padrão, e esta tabela é o motivo
// por escrito: trocar o padrão mudaria o ingestion_id de toda linha que o Go
// já gravou.
func TestDivergenciaEntreOPadraoEOPython(t *testing.T) {
	casos := []struct {
		entrada any
		padrao  string
		python  string
	}{
		{nil, "", "None"},
		{true, "true", "True"},
		{false, "false", "False"},
		{json.Number("19.0"), "19.0", "19.0"},
		{json.Number("0.0"), "0.0", "0.0"},
		// Estes NÃO divergem, e é o que torna a lista acima curta.
		{"ola", "ola", "ola"},
		{json.Number("-20.04"), "-20.04", "-20.04"},
	}

	for _, c := range casos {
		// O padrão é exercitado pela porta da frente, com uma chave de um
		// campo só: asText é privado, e um teste que o alcançasse por dentro
		// deixaria de provar o que o consumidor vê.
		padrao, err := sdk.Key("v")(map[string]any{"v": c.entrada})
		if err != nil {
			t.Fatalf("Key(%#v): %v", c.entrada, err)
		}
		if padrao != c.padrao {
			t.Errorf("Key(%#v) = %q, a tabela diz %q", c.entrada, padrao, c.padrao)
		}

		python, err := sdk.KeyWith(pycompat.Texto, "v")(map[string]any{"v": c.entrada})
		if err != nil {
			t.Fatalf("KeyWith(%#v): %v", c.entrada, err)
		}
		if python != c.python {
			t.Errorf("KeyWith(%#v) = %q, a tabela diz %q", c.entrada, python, c.python)
		}
	}
}

// TestUserAgentENossoENaoDoGo: alguns provedores públicos limitam ou bloqueiam
// o UA padrão do Go, e isso aparece como 403 intermitente -- o tipo de falha
// que custa meia manhã para diagnosticar.
func TestUserAgentENossoENaoDoGo(t *testing.T) {
	var visto string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		visto = r.Header.Get("User-Agent")
		_, _ = fmt.Fprint(w, `{"ok":1}`)
	}))
	defer srv.Close()

	drenar(t, sdk.Source{From: from.HTTP{URL: srv.URL}})

	if strings.Contains(visto, "Go-http-client") {
		t.Errorf("o UA é o padrão do Go: %q", visto)
	}
	if !strings.Contains(visto, "brevis") {
		t.Errorf("o UA não identifica o SDK: %q", visto)
	}
}

// TestUserAgentDoChamadorVence: quem precisa se passar por outra coisa
// continua podendo.
func TestUserAgentDoChamadorVence(t *testing.T) {
	var visto string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		visto = r.Header.Get("User-Agent")
		_, _ = fmt.Fprint(w, `{"ok":1}`)
	}))
	defer srv.Close()

	drenar(t, sdk.Source{From: from.HTTP{
		URL:    srv.URL,
		Header: map[string][]string{"User-Agent": {"meu-fetcher/2.0"}},
	}})

	if visto != "meu-fetcher/2.0" {
		t.Errorf("UA = %q; o do chamador devia vencer", visto)
	}
}

// TestResponsePreserveNumbersChegaAoObject: quem define Records decodifica por
// conta própria, e esquecer o UseNumber é silencioso -- `1` e `1.0` viram o
// mesmo float64 e a chave sai diferente da que o Python compunha.
func TestResponsePreserveNumbersChegaAoObject(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"results":[{"id":19}]}`)
	}))
	defer srv.Close()

	tipos := map[bool]string{}
	for _, preservar := range []bool{false, true} {
		drenar(t, sdk.Source{From: from.HTTP{
			URL:             srv.URL,
			PreserveNumbers: preservar,
			Records: func(r sdk.Response) ([]any, error) {
				doc, err := r.Object()
				if err != nil {
					return nil, err
				}
				linhas := doc["results"].([]any)
				tipos[preservar] = fmt.Sprintf("%T", linhas[0].(map[string]any)["id"])
				return linhas, nil
			},
		}})
	}

	if tipos[false] != "float64" {
		t.Errorf("sem PreserveNumbers, o tipo é %s; esperado float64", tipos[false])
	}
	if tipos[true] != "json.Number" {
		t.Errorf("com PreserveNumbers, o tipo é %s; o Object devia honrar a Source", tipos[true])
	}
}

func drenar(t *testing.T, fonte sdk.Source) {
	t.Helper()
	dados, err := sdk.Extract(context.Background(), fonte)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	for _, err := range dados.Records {
		if err != nil {
			t.Fatalf("iterando: %v", err)
		}
	}
}
