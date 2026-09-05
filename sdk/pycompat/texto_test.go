package pycompat

import (
	"encoding/json"
	"math"
	"os/exec"
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
	{float64(0), "0.0", "0.0"},
	{float64(19), "19.0", "19.0"},
	{float64(-20.04), "-20.04", "-20.04"},
	{float64(0.1), "0.1", "0.1"},
	{float64(1) / 3, "0.3333333333333333", "1/3"},
	{float64(1e15), "1000000000000000.0", "1e15"},
	{float64(9.999e15), "9999000000000000.0", "9.999e15"},
	{float64(0.0001), "0.0001", "0.0001"},
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
	recusados := []float64{1e-5, 9.99e-5, -1e-5, 1e16, -1e16, 1e300, math.SmallestNonzeroFloat64}
	for _, f := range recusados {
		if got, err := Texto(f); err == nil {
			t.Errorf("Texto(%g) devolveu %q em vez de recusar", f, got)
		}
	}

	// E as bordas de dentro passam: a faixa é [1e-4, 1e16).
	aceitos := []float64{0, 1e-4, -1e-4, 9.999999999999998e15, 0.1}
	for _, f := range aceitos {
		if _, err := Texto(f); err != nil {
			t.Errorf("Texto(%g) recusou dentro da faixa: %v", f, err)
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

// TestPreserveNumbersRecuperaADistincaoQueOFloatPerde é o que torna
// IngestionIDPython utilizável de verdade.
//
// Sem ele, {"id": 19} e {"id": 19.0} chegam idênticos como float64(19), e o
// Python via int num caso e float no outro -- str() "19" contra "19.0". A
// função renderiza os dois como float, e acerta metade.
func TestPreserveNumbersRecuperaADistincaoQueOFloatPerde(t *testing.T) {
	var semPreservar map[string]any
	if err := json.Unmarshal([]byte(`{"inteiro":19,"decimal":19.0}`), &semPreservar); err != nil {
		t.Fatal(err)
	}
	a, _ := Texto(semPreservar["inteiro"])
	b, _ := Texto(semPreservar["decimal"])
	if a != b {
		t.Fatalf("o float64 distinguiu %q de %q, e não deveria conseguir", a, b)
	}
	if a != "19.0" {
		t.Errorf("sem preservar = %q; a documentação diz que rende como float", a)
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
