package jsontext

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// TestAppendJSONStringConcordaComOEncoder compara byte a byte com o
// encoding/json sem escape de HTML.
//
// A afirmação não é "o meu está certo": é "é idêntico ao do stdlib". Escrever
// JSON à mão é como se produz um arquivo que o servidor lê de outro jeito, e
// aqui é pior -- a mesma regra compõe uma CHAVE.
func TestAppendJSONStringConcordaComOEncoder(t *testing.T) {
	casos := []struct {
		s           string
		soSemantico bool
	}{
		{"", false}, {"simples", false}, {"com espaço", false},
		{`aspas "no meio"`, false},
		{`contrabarra \ e \\ dupla`, false},
		{"quebra\nde\rlinha\te tab", false},
		{"controle \x00\x01\x1f no meio", false},
		{"acentuação e emoji 🎉", false},
		{"html < > & sem escape", false},
		{string([]byte{0xff, 0xfe}), true},
		{"válido " + string([]byte{0xff}) + " misturado", true},
	}

	for _, c := range casos {
		nome := strings.Map(func(r rune) rune {
			if r < 0x20 {
				return '_'
			}
			return r
		}, c.s)
		t.Run(nome, func(t *testing.T) {
			var buf bytes.Buffer
			enc := json.NewEncoder(&buf)
			enc.SetEscapeHTML(false)
			if err := enc.Encode(c.s); err != nil {
				t.Fatal(err)
			}
			quero := strings.TrimSuffix(buf.String(), "\n")

			got := string(AppendJSONString(nil, c.s))
			if got == quero {
				return
			}
			if !c.soSemantico {
				t.Fatalf("%q:\n  meu      %s\n  Encoder  %s", c.s, got, quero)
			}

			// UTF-8 inválido: o encoding/json do 1.25 escapa o U+FFFD e o do
			// 1.27 escreve os bytes. As duas formas são o mesmo code point, e
			// a igualdade que vale ali é a do valor.
			var meu, dele string
			if err := json.Unmarshal([]byte(got), &meu); err != nil {
				t.Fatalf("a minha saída nem é JSON válido: %s", got)
			}
			if err := json.Unmarshal([]byte(quero), &dele); err != nil {
				t.Fatalf("a saída do encoder não decodifica: %s", quero)
			}
			if meu != dele {
				t.Errorf("%q decodifica diferente:\n  meu     %q\n  Encoder %q", c.s, meu, dele)
			}
		})
	}
}

// TestAppendJSONStringAcrescentaNoDestino: ela recebe o buffer e devolve o
// buffer, para não alocar um por string numa carga de milhões.
func TestAppendJSONStringAcrescentaNoDestino(t *testing.T) {
	dst := []byte("antes:")
	got := string(AppendJSONString(dst, "x"))
	if got != `antes:"x"` {
		t.Errorf("= %q", got)
	}
}
