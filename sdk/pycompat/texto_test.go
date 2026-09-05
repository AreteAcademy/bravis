package pycompat

import (
	"encoding/json"
	"math"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// A tabela é gerada do Python e fixada aqui, e o teste seguinte a confere
// contra um python3 de verdade quando ele existe.
//
// Duas redes porque elas pegam coisas diferentes: a tabela roda em qualquer
// máquina e documenta a verdade por escrito; o diferencial pega o dia em que
// o CPython mudar uma regra de formatação sob nossos pés.
var comoOPythonRenderiza = []struct {
	entrada any
	python  string
	// literal é o que o Python passaria ao str(), quando o valor Go não
	// consegue expressá-lo (int contra float).
	literal string
}{
	{nil, "None", "None"},
	{true, "True", "True"},
	{false, "False", "False"},
	{"", "", "''"},
	{"ola", "ola", "'ola'"},
	// Os floats entram como json.Number, que e o caminho de verdade: um
	// float64 cru recusa, porque a essa altura o literal ja se perdeu. Ver
	// TestTextoRecusaFloat64.
	{json.Number("0.0"), "0.0", "0.0"},
	{json.Number("19.0"), "19.0", "19.0"},
	{json.Number("-20.04"), "-20.04", "-20.04"},
	{json.Number("0.1"), "0.1", "0.1"},
	{json.Number("0.3333333333333333"), "0.3333333333333333", "1/3"},
	{json.Number("1000000000000000.0"), "1000000000000000.0", "1e15"},
	{json.Number("9999000000000000.0"), "9999000000000000.0", "9.999e15"},
	{json.Number("0.0001"), "0.0001", "0.0001"},
	{int64(0), "0", "0"},
	{int64(19), "19", "19"},
	{int64(-7), "-7", "-7"},
	{json.Number("19"), "19", "19"},
	{json.Number("19.0"), "19.0", "19.0"},
	{json.Number("-20.04"), "-20.04", "-20.04"},
	{json.Number("0"), "0", "0"},
}

// TestTextoContraATabela é a rede que roda em qualquer lugar.
func TestTextoContraATabela(t *testing.T) {
	for _, c := range comoOPythonRenderiza {
		got, err := Texto(c.entrada)
		if err != nil {
			t.Errorf("Texto(%#v): %v", c.entrada, err)
			continue
		}
		if got != c.python {
			t.Errorf("Texto(%#v) = %q, o str() do Python dá %q", c.entrada, got, c.python)
		}
	}
}

// TestTextoContraOPythonDeVerdade é o teste diferencial pedido.
//
// Ele roda o str() de um python3 e compara. Pula quando não há python3 -- e
// pular é honesto aqui: a tabela acima cobre os mesmos casos por escrito, e um
// teste que inventasse um resultado sem o Python não seria diferencial de
// nada.
func TestTextoContraOPythonDeVerdade(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("sem python3; a tabela fixada cobre os mesmos casos")
	}

	var literais []string
	for _, c := range comoOPythonRenderiza {
		literais = append(literais, c.literal)
	}
	script := "import sys\nfor v in [" + strings.Join(literais, ", ") + "]:\n    sys.stdout.write(str(v) + '\\x00')\n"

	saida, err := exec.Command("python3", "-c", script).Output()
	if err != nil {
		t.Fatalf("rodando python3: %v", err)
	}
	linhas := strings.Split(strings.TrimSuffix(string(saida), "\x00"), "\x00")
	if len(linhas) != len(comoOPythonRenderiza) {
		t.Fatalf("o python devolveu %d valores para %d casos", len(linhas), len(comoOPythonRenderiza))
	}

	for i, c := range comoOPythonRenderiza {
		if c.python != linhas[i] {
			t.Errorf("a tabela fixada diz que str(%s) é %q, e este python3 dá %q",
				c.literal, c.python, linhas[i])
		}
		got, err := Texto(c.entrada)
		if err != nil {
			t.Errorf("Texto(%#v): %v", c.entrada, err)
			continue
		}
		if got != linhas[i] {
			t.Errorf("Texto(%#v) = %q, e str(%s) = %q", c.entrada, got, c.literal, linhas[i])
		}
	}
}

// TestTextoRecusaAFaixaExponencial: recusar é melhor que divergir numa
// chave. O formato exato do expoente é detalhe do CPython, e apostar nele
// produz duplicata em silêncio.
func TestTextoRecusaAFaixaExponencial(t *testing.T) {
	// Pela porta que aceita float: a política do expoente é sobre floats, e o
	// Texto recusa float64 antes de chegar nela.
	recusados := []float64{1e-5, 9.99e-5, -1e-5, 1e16, -1e16, 1e300, math.SmallestNonzeroFloat64}
	for _, f := range recusados {
		if got, err := TextoAceitandoFloat64(f); err == nil {
			t.Errorf("TextoAceitandoFloat64(%g) devolveu %q em vez de recusar", f, got)
		}
	}

	// E as bordas de dentro passam: a faixa é [1e-4, 1e16).
	aceitos := []float64{0, 1e-4, -1e-4, 9.999999999999998e15, 0.1}
	for _, f := range aceitos {
		if _, err := TextoAceitandoFloat64(f); err != nil {
			t.Errorf("TextoAceitandoFloat64(%g) recusou dentro da faixa: %v", f, err)
		}
	}
}

// TestTextoRecusaFloat64 é o item 11 da segunda rodada, e conserta uma
// incoerência que era minha.
//
// O `default` do Texto recusava dizendo que "adivinhar numa chave produz
// duplicata silenciosa", e o `case float64` logo acima adivinhava em silêncio.
// A limitação estava DOCUMENTADA -- e documentar uma divergência não é o mesmo
// que impedi-la.
//
// Um float64 só chega aqui quando o literal já se perdeu: o encoding/json
// decodifica `1` e `1.0` no mesmo float64, e o Python via int num caso e float
// no outro. Escolher uma das duas acerta metade das vezes, e a metade errada é
// uma linha duplicada.
func TestTextoRecusaFloat64(t *testing.T) {
	// inf e nan ficam de fora: nenhum literal JSON produz um deles, então a
	// ambiguidade int/float não existe ali e a regra não teria razão.
	for _, f := range []float64{0, 1, 19.0, -20.04} {
		got, err := Texto(f)
		if err == nil {
			t.Errorf("Texto(%v) devolveu %q; o literal já se perdeu e ele adivinhou", f, got)
			continue
		}
		for _, quero := range []string{"PreserveNumbers", "TextoAceitandoFloat64"} {
			if !strings.Contains(err.Error(), quero) {
				t.Errorf("o erro não oferece a saída %q: %v", quero, err)
			}
		}
	}
}

// TestTextoAceitaFloat32: um float32 nunca vem de JSON decodificado, então ele
// é float sem ambiguidade -- é o único flutuante que dá para renderizar sem
// adivinhar.
func TestTextoAceitaFloat32(t *testing.T) {
	got, err := Texto(float32(19))
	if err != nil {
		t.Fatalf("float32 recusado: %v", err)
	}
	if got != "19.0" {
		t.Errorf("= %q, esperado \"19.0\"", got)
	}
}

// TestTextoAceitandoFloat64CasaComOPython: a saída de escape produz o mesmo
// texto que o str() de um float do Python.
func TestTextoAceitandoFloat64CasaComOPython(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("sem python3")
	}
	casos := map[float64]string{0: "0.0", 19: "19.0", -20.04: "-20.04", 0.1: "0.1"}

	var literais []string
	var valores []float64
	for f := range casos {
		valores = append(valores, f)
	}
	sort.Float64s(valores)
	for _, f := range valores {
		// float(...) explicito: str(0) do Python e "0" e str(0.0) e "0.0", e
		// um literal sem ponto viraria int -- que e outro teste.
		literais = append(literais, "float("+strconv.FormatFloat(f, 'f', -1, 64)+")")
	}
	script := "import sys\nfor v in [" + strings.Join(literais, ", ") +
		"]:\n    sys.stdout.write(str(v) + '\\x00')\n"
	b, err := exec.Command("python3", "-c", script).Output()
	if err != nil {
		t.Fatalf("python3: %v", err)
	}
	quero := strings.Split(strings.TrimSuffix(string(b), "\x00"), "\x00")

	for i, f := range valores {
		got, err := TextoAceitandoFloat64(f)
		if err != nil {
			t.Errorf("TextoAceitandoFloat64(%v): %v", f, err)
			continue
		}
		if got != quero[i] {
			t.Errorf("TextoAceitandoFloat64(%v) = %q, e str(%v) = %q", f, got, f, quero[i])
		}
	}
}

// TestTextoRecusaOQueNaoSabe: um mapa ou uma lista tem str() com regras
// do tipo, e adivinhar numa chave é o defeito que esta função existe para
// evitar.
func TestTextoRecusaOQueNaoSabe(t *testing.T) {
	for _, v := range []any{map[string]any{"a": 1}, []any{1, 2}, struct{}{}} {
		if got, err := Texto(v); err == nil {
			t.Errorf("Texto(%#v) devolveu %q em vez de recusar", v, got)
		}
	}
}

// TestTextoEspeciais: inf e nan têm str() e não expoente.
func TestTextoEspeciais(t *testing.T) {
	casos := map[float64]string{
		math.Inf(1):  "inf",
		math.Inf(-1): "-inf",
	}
	for f, quero := range casos {
		got, err := Texto(f)
		if err != nil || got != quero {
			t.Errorf("Texto(%v) = (%q, %v), esperado %q", f, got, err, quero)
		}
	}
	if got, _ := Texto(math.NaN()); got != "nan" {
		t.Errorf("NaN = %q", got)
	}
}

// TestPreserveNumbersRecuperaADistincaoQueOFloatPerde é o que torna o pycompat
// utilizável de verdade.
//
// Sem ele, {"id": 19} e {"id": 19.0} chegam idênticos como float64(19), e o
// Python via int num caso e float no outro. Desde a v0.40.0 o Texto RECUSA esse
// float64 em vez de escolher uma das duas -- porque escolher acerta metade das
// vezes, e a metade errada é uma duplicata.
func TestPreserveNumbersRecuperaADistincaoQueOFloatPerde(t *testing.T) {
	var semPreservar map[string]any
	if err := json.Unmarshal([]byte(`{"inteiro":19,"decimal":19.0}`), &semPreservar); err != nil {
		t.Fatal(err)
	}
	if _, err := Texto(semPreservar["inteiro"]); err == nil {
		t.Error("sem preservar, o Texto adivinhou em vez de recusar")
	}

	// Preservando: o literal decide, exatamente como o json do Python decide.
	dec := json.NewDecoder(strings.NewReader(`{"inteiro":19,"decimal":19.0}`))
	dec.UseNumber()
	var comPreservar map[string]any
	if err := dec.Decode(&comPreservar); err != nil {
		t.Fatal(err)
	}
	inteiro, err := Texto(comPreservar["inteiro"])
	if err != nil {
		t.Fatal(err)
	}
	decimal, err := Texto(comPreservar["decimal"])
	if err != nil {
		t.Fatal(err)
	}
	if inteiro != "19" || decimal != "19.0" {
		t.Errorf("com PreserveNumbers = (%q, %q), esperado (\"19\", \"19.0\")", inteiro, decimal)
	}
}
