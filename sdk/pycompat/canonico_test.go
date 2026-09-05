package pycompat

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

// TestJSONCanonicoContraOPythonDeVerdade é o teste diferencial: a afirmação não
// é "está certo", é "é byte a byte o que o json.dumps produz".
//
// Reproduzir isso à mão custou ~90 linhas no consumidor, e as três armadilhas
// mudam a chave sem erro.
func TestJSONCanonicoContraOPythonDeVerdade(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("sem python3")
	}

	casos := []struct {
		nome    string
		valor   any
		literal string
	}{
		{"objeto simples", obj(`{"b":1,"a":2}`), `{"b":1,"a":2}`},
		{"chaves ordenadas", obj(`{"z":1,"a":2,"m":3}`), `{"z":1,"a":2,"m":3}`},
		{"aninhado", obj(`{"c":{"z":"x","y":"w"},"a":1}`), `{"c":{"z":"x","y":"w"},"a":1}`},
		{"lista", obj(`{"a":[1,2.0,null,true]}`), `{"a":[1,2.0,null,true]}`},
		{"html não escapado", obj(`{"h":"<a href='x'>&amp;</a>"}`), `{"h":"<a href='x'>&amp;</a>"}`},
		{"unicode cru", obj(`{"d":"acentuação e 🎉"}`), `{"d":"acentuação e 🎉"}`},
		{"int e float distintos", obj(`{"f":19.0,"i":19}`), `{"f":19.0,"i":19}`},
		{"inteiro grande", obj(`{"n":9007199254740993}`), `{"n":9007199254740993}`},
		{"negativo", obj(`{"n":-20.04}`), `{"n":-20.04}`},
		{"vazio", obj(`{}`), `{}`},
		{"lista vazia", obj(`{"a":[]}`), `{"a":[]}`},
		{"aspas e barra", obj(`{"s":"com \"aspas\" e \\ barra"}`), `{"s":"com \"aspas\" e \\ barra"}`},
		{"controle", obj(`{"s":"quebra\nlinha\ttab"}`), `{"s":"quebra\nlinha\ttab"}`},
	}

	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			got, err := JSONCanonico(c.valor)
			if err != nil {
				t.Fatalf("JSONCanonico: %v", err)
			}

			script := "import json,sys\n" +
				"v = json.loads(r'''" + c.literal + "''')\n" +
				"sys.stdout.write(json.dumps(v, sort_keys=True, separators=(',',':'), ensure_ascii=False))"
			b, err := exec.Command("python3", "-c", script).Output()
			if err != nil {
				t.Fatalf("python3: %v", err)
			}

			if string(got) != string(b) {
				t.Errorf("divergiu:\n  meu     %s\n  Python  %s", got, b)
			}
		})
	}
}

// obj decodifica preservando o literal do número, que é o que
// Source.PreserveNumbers entrega.
func obj(s string) map[string]any {
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		panic(err)
	}
	return m
}

// TestJSONCanonicoRecusaFloat64: a armadilha 2, e ela é erro e não palpite.
func TestJSONCanonicoRecusaFloat64(t *testing.T) {
	var m map[string]any
	if err := json.Unmarshal([]byte(`{"n":19}`), &m); err != nil {
		t.Fatal(err)
	}
	_, err := JSONCanonico(m)
	if err == nil {
		t.Fatal("um float64 passou; o literal já se perdeu e ele adivinhou")
	}
	for _, quero := range []string{`"n"`, "PreserveNumbers"} {
		if !strings.Contains(err.Error(), quero) {
			t.Errorf("o erro não diz %q: %v", quero, err)
		}
	}
}

// TestJSONCanonicoPreservaInteiroGrande: a armadilha 3 -- 2^53+1 não sobrevive
// a um float64.
func TestJSONCanonicoPreservaInteiroGrande(t *testing.T) {
	got, err := JSONCanonico(obj(`{"n":9007199254740993}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"n":9007199254740993}` {
		t.Errorf("= %s; a precisão se perdeu", got)
	}
}

// TestJSONCanonicoNaoEscapaHTML: a armadilha 1, e o SDK já sabia dela -- no
// driver do Redshift, sem compartilhar.
func TestJSONCanonicoNaoEscapaHTML(t *testing.T) {
	got, err := JSONCanonico(obj(`{"h":"<&>"}`))
	if err != nil {
		t.Fatal(err)
	}
	// A asserção é sobre a AUSÊNCIA do escape: o encoding/json escreve
	// \u003c, e o Python escreve o caractere. Escrita ao contrário, ela
	// falhava justamente quando o comportamento estava certo.
	if strings.Contains(string(got), `\u003c`) {
		t.Errorf("escapou HTML como o encoding/json faz: %s", got)
	}
	if string(got) != `{"h":"<&>"}` {
		t.Errorf("= %s", got)
	}
}

// TestJSONCanonicoEDeterministico: a ordem de um mapa em Go é embaralhada de
// propósito, e sem sort_keys a chave mudaria a cada execução.
func TestJSONCanonicoEDeterministico(t *testing.T) {
	m := obj(`{"z":1,"a":2,"m":3,"b":4,"y":5}`)
	var primeiro string
	for i := 0; i < 50; i++ {
		got, err := JSONCanonico(m)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			primeiro = string(got)
			continue
		}
		if string(got) != primeiro {
			t.Fatalf("variou entre execuções:\n  %s\n  %s", primeiro, got)
		}
	}
	if primeiro != `{"a":2,"b":4,"m":3,"y":5,"z":1}` {
		t.Errorf("= %s; as chaves não estão ordenadas", primeiro)
	}
}

// TestJSONCanonicoRecusaOQueNaoSabe: um tipo que um registro JSON não produz é
// erro nomeando o caminho, e não um palpite dentro de uma chave.
func TestJSONCanonicoRecusaOQueNaoSabe(t *testing.T) {
	_, err := JSONCanonico(map[string]any{"a": map[string]any{"b": struct{}{}}})
	if err == nil {
		t.Fatal("um struct passou")
	}
	if !strings.Contains(err.Error(), `"a"`) || !strings.Contains(err.Error(), `"b"`) {
		t.Errorf("o erro não diz o caminho: %v", err)
	}
}
