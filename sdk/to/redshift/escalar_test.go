package redshift

import (
	"bytes"
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/sdk/internal/core"
)

// TestEscalarConcordaComOEncoder compara byte a byte com o encoding/json,
// configurado como o EncodeNDJSON o configura.
//
// Escrever JSON à mão é como se produz um arquivo que o servidor lê de outro
// jeito -- e um COPY que aceita um valor errado é pior que um que falha. Então
// a afirmação não é "o meu está certo": é "o meu é idêntico ao do stdlib".
//
// A referência é o Encoder com SetEscapeHTML(false), e NÃO o json.Marshal: o
// Marshal escapa <, > e & por padrão, e o caminho dos compostos neste arquivo
// não escapa. Comparar com o Marshal faria os dois caminhos do MESMO arquivo
// divergirem -- que foi o que este teste pegou na primeira execução.
func TestEscalarConcordaComOEncoder(t *testing.T) {
	// soSemantico marca os casos em que os PRÓPRIOS toolchains discordam entre
	// si, e por isso o meu código não pode ser igual aos dois.
	//
	// Aconteceu com UTF-8 inválido: o encoding/json do Go 1.25 escreve
	// `\ufffd` escapado, e o do 1.27 escreve os bytes de U+FFFD. As duas
	// formas são JSON válido e o mesmo code point -- o COPY lê a mesma coisa
	// --, então ali a igualdade que vale é a do VALOR, e não a dos bytes.
	//
	// A exceção é declarada caso a caso, e não um "se der diferente, decodifica
	// e compara": uma tolerância genérica esconderia um escape errado de
	// verdade.
	casos := []struct {
		valor       any
		soSemantico bool
	}{
		{nil, false}, {true, false}, {false, false},
		{"", false}, {"simples", false}, {"com espaço", false},
		{`aspas "no meio"`, false},
		{`contrabarra \ e \\ dupla`, false},
		{"quebra\nde\rlinha\te tab", false},
		{"controle \x00\x01\x1f no meio", false},
		{"acentuação e emoji 🎉", false},
		{"barra / e menor < maior > e comercial &", false},
		{string([]byte{0xff, 0xfe}), true},
		{"válido " + string([]byte{0xff}) + " misturado", true},
		{int(0), false}, {int(-1), false}, {int(42), false},
		{int32(-2147483648), false}, {int64(9007199254740993), false},
		{uint64(18446744073709551615), false},
		{float64(0), false}, {float64(-1.5), false}, {float64(1e21), false},
		{float64(0.1), false},
	}

	for _, c := range casos {
		nome := strings.ReplaceAll(nomeDe(c.valor), " ", "_")
		t.Run(nome, func(t *testing.T) {
			quero, err := comoOEncoder(c.valor)
			if err != nil {
				t.Skipf("o stdlib recusa %v", c.valor)
			}

			var buf bytes.Buffer
			if !escreverEscalar(&buf, c.valor) {
				t.Fatalf("escreverEscalar recusou %#v, que o encoder aceita", c.valor)
			}
			got := buf.String()

			if got == quero {
				return
			}
			if !c.soSemantico {
				t.Fatalf("%#v:\n  meu      %s\n  Encoder  %s", c.valor, got, quero)
			}

			// Bytes diferentes: os valores ainda têm de ser o mesmo.
			var meu, dele any
			if err := json.Unmarshal([]byte(got), &meu); err != nil {
				t.Fatalf("a minha saída nem é JSON válido: %s", got)
			}
			if err := json.Unmarshal([]byte(quero), &dele); err != nil {
				t.Fatalf("a saída do encoder não decodifica: %s", quero)
			}
			if meu != dele {
				t.Errorf("%#v decodifica diferente:\n  meu     %#v\n  Encoder %#v",
					c.valor, meu, dele)
			}
		})
	}
}

// TestEscalarRecusaOQueNaoSabe: recusar é o contrato -- o chamador cai no
// encoder, que resolve. Aceitar e escrever errado seria o defeito.
func TestEscalarRecusaOQueNaoSabe(t *testing.T) {
	naoEscalares := []any{
		map[string]any{"a": 1},
		[]any{1, 2},
		[]byte{1, 2},
		math.NaN(),
		math.Inf(1),
		struct{ A int }{1},
	}
	for _, v := range naoEscalares {
		var buf bytes.Buffer
		if escreverEscalar(&buf, v) {
			t.Errorf("aceitou %#v e escreveu %q", v, buf.String())
		}
	}
}

// TestCompostoAindaSaiIgual: o caminho lento continua produzindo o mesmo
// documento, e é o teste que impede o fast path de mudar a saída do resto.
func TestCompostoAindaSaiIgual(t *testing.T) {
	got, err := EncodeNDJSON(envelopesDeTeste(), []string{"texto", "documento", "lista", "numero"})
	if err != nil {
		t.Fatal(err)
	}

	var doc map[string]any
	linha := strings.TrimSpace(string(got))
	if err := json.Unmarshal([]byte(linha), &doc); err != nil {
		t.Fatalf("saída não é JSON válido: %q\n%v", linha, err)
	}
	if doc["texto"] != `com "aspas"` {
		t.Errorf("texto = %#v", doc["texto"])
	}
	if m, ok := doc["documento"].(map[string]any); !ok || m["a"] != float64(1) {
		t.Errorf("documento = %#v", doc["documento"])
	}
	if l, ok := doc["lista"].([]any); !ok || len(l) != 2 {
		t.Errorf("lista = %#v", doc["lista"])
	}
}

// comoOEncoder serializa como o EncodeNDJSON serializa os compostos.
func comoOEncoder(v any) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	return strings.TrimSuffix(buf.String(), "\n"), nil
}

func nomeDe(v any) string {
	s := strings.TrimSpace(strings.Map(func(r rune) rune {
		if r < 0x20 {
			return '_'
		}
		return r
	}, jsonOuTipo(v)))
	if len(s) > 30 {
		s = s[:30]
	}
	if s == "" {
		s = "vazio"
	}
	return s
}

func jsonOuTipo(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "invalido"
	}
	return string(b)
}

func envelopesDeTeste() []core.Envelope {
	return []core.Envelope{{Payload: map[string]any{
		"texto":     `com "aspas"`,
		"documento": map[string]any{"a": 1},
		"lista":     []any{1, "dois"},
		"numero":    42,
	}}}
}
