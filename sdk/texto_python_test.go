package sdk

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

// TestTextoPythonContraATabela é a rede que roda em qualquer lugar.
func TestTextoPythonContraATabela(t *testing.T) {
	for _, c := range comoOPythonRenderiza {
		got, err := TextoPython(c.entrada)
		if err != nil {
			t.Errorf("TextoPython(%#v): %v", c.entrada, err)
			continue
		}
		if got != c.python {
			t.Errorf("TextoPython(%#v) = %q, o str() do Python dá %q", c.entrada, got, c.python)
		}
	}
}

// TestTextoPythonContraOPythonDeVerdade é o teste diferencial pedido.
//
// Ele roda o str() de um python3 e compara. Pula quando não há python3 -- e
// pular é honesto aqui: a tabela acima cobre os mesmos casos por escrito, e um
// teste que inventasse um resultado sem o Python não seria diferencial de
// nada.
func TestTextoPythonContraOPythonDeVerdade(t *testing.T) {
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
		got, err := TextoPython(c.entrada)
		if err != nil {
			t.Errorf("TextoPython(%#v): %v", c.entrada, err)
			continue
		}
		if got != linhas[i] {
			t.Errorf("TextoPython(%#v) = %q, e str(%s) = %q", c.entrada, got, c.literal, linhas[i])
		}
	}
}

// TestTextoPythonRecusaAFaixaExponencial: recusar é melhor que divergir numa
// chave. O formato exato do expoente é detalhe do CPython, e apostar nele
// produz duplicata em silêncio.
func TestTextoPythonRecusaAFaixaExponencial(t *testing.T) {
	recusados := []float64{1e-5, 9.99e-5, -1e-5, 1e16, -1e16, 1e300, math.SmallestNonzeroFloat64}
	for _, f := range recusados {
		if got, err := TextoPython(f); err == nil {
			t.Errorf("TextoPython(%g) devolveu %q em vez de recusar", f, got)
		}
	}

	// E as bordas de dentro passam: a faixa é [1e-4, 1e16).
	aceitos := []float64{0, 1e-4, -1e-4, 9.999999999999998e15, 0.1}
	for _, f := range aceitos {
		if _, err := TextoPython(f); err != nil {
			t.Errorf("TextoPython(%g) recusou dentro da faixa: %v", f, err)
		}
	}
}

// TestTextoPythonRecusaOQueNaoSabe: um mapa ou uma lista tem str() com regras
// do tipo, e adivinhar numa chave é o defeito que esta função existe para
// evitar.
func TestTextoPythonRecusaOQueNaoSabe(t *testing.T) {
	for _, v := range []any{map[string]any{"a": 1}, []any{1, 2}, struct{}{}} {
		if got, err := TextoPython(v); err == nil {
			t.Errorf("TextoPython(%#v) devolveu %q em vez de recusar", v, got)
		}
	}
}

// TestTextoPythonEspeciais: inf e nan têm str() e não expoente.
func TestTextoPythonEspeciais(t *testing.T) {
	casos := map[float64]string{
		math.Inf(1):  "inf",
		math.Inf(-1): "-inf",
	}
	for f, quero := range casos {
		got, err := TextoPython(f)
		if err != nil || got != quero {
			t.Errorf("TextoPython(%v) = (%q, %v), esperado %q", f, got, err, quero)
		}
	}
	if got, _ := TextoPython(math.NaN()); got != "nan" {
		t.Errorf("NaN = %q", got)
	}
}

// TestDivergenciaEntreAsTextEOPython é a documentação da divergência, como
// teste.
//
// O SDK NÃO usa TextoPython por padrão, e esta tabela é o motivo por escrito:
// trocar o padrão mudaria o ingestion_id de toda linha que o Go já gravou.
func TestDivergenciaEntreAsTextEOPython(t *testing.T) {
	casos := []struct {
		entrada     any
		asText      string
		textoPython string
	}{
		{nil, "", "None"},
		{true, "true", "True"},
		{false, "false", "False"},
		{float64(19), "19", "19.0"},
		{float64(0), "0", "0.0"},
		// Estes NÃO divergem, e é o que torna a lista acima curta.
		{"ola", "ola", "ola"},
		{float64(-20.04), "-20.04", "-20.04"},
	}

	for _, c := range casos {
		if got := asText(c.entrada); got != c.asText {
			t.Errorf("asText(%#v) = %q, a tabela diz %q", c.entrada, got, c.asText)
		}
		got, err := TextoPython(c.entrada)
		if err != nil {
			t.Fatalf("TextoPython(%#v): %v", c.entrada, err)
		}
		if got != c.textoPython {
			t.Errorf("TextoPython(%#v) = %q, a tabela diz %q", c.entrada, got, c.textoPython)
		}
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
	a, _ := TextoPython(semPreservar["inteiro"])
	b, _ := TextoPython(semPreservar["decimal"])
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
	inteiro, err := TextoPython(comPreservar["inteiro"])
	if err != nil {
		t.Fatal(err)
	}
	decimal, err := TextoPython(comPreservar["decimal"])
	if err != nil {
		t.Fatal(err)
	}
	if inteiro != "19" || decimal != "19.0" {
		t.Errorf("com PreserveNumbers = (%q, %q), esperado (\"19\", \"19.0\")", inteiro, decimal)
	}
}

// TestIngestionIDPythonCasaComOPython é a prova que interessa a quem porta: o
// id que o Go compõe é o mesmo que o Python compunha, para os três casos que
// divergem.
func TestIngestionIDPythonCasaComOPython(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("sem python3")
	}

	registro := func() map[string]any {
		return map[string]any{
			"provider": "acme", "entity": nil,
			"source_key": float64(19), "record_ts": true,
		}
	}

	saida, err := IngestionIDPython()(registro())
	if err != nil {
		t.Fatal(err)
	}
	got := saida.(map[string]any)[ColumnIngestionID].(string)

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
		t.Errorf("IngestionIDPython = %s, e o Python dá %s", got, quero)
	}

	// E o padrão NÃO casa. É essa a divergência que motivou tudo isto, e
	// mantê-la é decisão escrita -- trocar o padrão reescreveria o id de toda
	// linha que o Go já gravou.
	saidaPadrao, err := IngestionID()(registro())
	if err != nil {
		t.Fatal(err)
	}
	if padrao := saidaPadrao.(map[string]any)[ColumnIngestionID].(string); padrao == quero {
		t.Error("o padrão passou a casar com o Python; se foi intencional, a decisão " +
			"documentada em asText mudou e este teste precisa ser reescrito")
	}
}

// TestKeyPythonCasaComOPython: o mesmo, para a chave.
func TestKeyPythonCasaComOPython(t *testing.T) {
	registro := map[string]any{"a": nil, "b": float64(19), "c": true}

	got, err := KeyPython("a", "b", "c")(registro)
	if err != nil {
		t.Fatal(err)
	}
	if got != "None|19.0|True" {
		t.Errorf("KeyPython = %q, o Python daria \"None|19.0|True\"", got)
	}

	padrao, err := Key("a", "b", "c")(registro)
	if err != nil {
		t.Fatal(err)
	}
	if padrao != "|19|true" {
		t.Errorf("Key = %q, esperado \"|19|true\" -- se mudou, o padrão mudou", padrao)
	}
}

// TestKeyPythonRecusaOQueNaoSabeRenderizar: a recusa nomeia o campo, senão
// quem lê o erro não sabe qual dos seis é.
func TestKeyPythonRecusaOQueNaoSabeRenderizar(t *testing.T) {
	_, err := KeyPython("a", "b")(map[string]any{"a": "ok", "b": 1e-5})
	if err == nil {
		t.Fatal("a faixa exponencial passou")
	}
	if !strings.Contains(err.Error(), `"b"`) {
		t.Errorf("o erro não nomeia o campo: %v", err)
	}
}
