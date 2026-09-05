package sdk

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// TextoPython renderiza um valor como o `str()` do Python renderiza.
//
// Ela existe para quem esta portando um fetcher de Python para Go mantendo a
// MESMA landing e o MESMO ingestion_id. Se o Python fazia `str(record["id"])`
// para compor a chave, o Go precisa produzir exatamente aquele texto -- senao
// a mesma leitura recebe um id diferente, e o que aparece do outro lado nao e
// um erro: e uma linha duplicada depois do merge do bronze.
//
// O SDK NAO usa esta funcao por padrao. Ver IngestionID e Key para o porque, e
// para como pedir que usem.
//
//	Go (asText, o padrao)      Python (str)
//	nil        ""              None       "None"
//	true       "true"          True       "True"
//	19.0       "19"            19.0       "19.0"
//
// # A faixa em que ela RECUSA, e por que recusar e melhor
//
// O `str()` do Python muda para notacao exponencial quando o expoente decimal
// sai de [-4, 16): `str(1e-5)` e `"1e-05"`, `str(1e16)` e `"1e+16"`. A forma
// exata desse texto -- quantos digitos no expoente, o sinal, o zero a esquerda
// -- e detalhe de implementacao do CPython, e imita-la seria apostar que a
// aposta esta certa numa CHAVE.
//
// Entao nessa faixa ela devolve erro nomeando o valor. Uma chave que falha alto
// e um problema de uma linha; uma chave que diverge em silencio e uma
// duplicata que aparece semanas depois, num relatorio, sem ninguem saber de
// onde veio.
//
// # O que ela NAO consegue recuperar
//
// O `encoding/json` decodifica todo numero como float64, entao `{"id": 19}` e
// `{"id": 19.0}` chegam identicos ao Go -- e no Python o primeiro era `int`
// (str = "19") e o segundo `float` (str = "19.0").
//
// Um float64 e tratado como o float do Python, que e a escolha certa para um
// numero que era float na origem e a ERRADA para um que era int. Para nao
// depender disso, ligue Source.PreserveNumbers: o literal chega intacto como
// json.Number, e aqui ele decide sozinho -- com ponto ou expoente e float, sem
// e int, exatamente como o json do Python decide.
func TextoPython(v any) (string, error) {
	switch t := v.(type) {
	case nil:
		return "None", nil

	case bool:
		if t {
			return "True", nil
		}
		return "False", nil

	case string:
		return t, nil

	case json.Number:
		// O literal preserva a distincao que o float64 perde: o json do Python
		// produz int quando nao ha ponto nem expoente, e float quando ha.
		texto := t.String()
		if !strings.ContainsAny(texto, ".eE") {
			// int do Python. O texto do literal ja e a forma canonica, exceto
			// pelo zero a esquerda e pelo mais, que JSON nao permite.
			return texto, nil
		}
		f, err := t.Float64()
		if err != nil {
			return "", fmt.Errorf("TextoPython: %q não é um número: %w", texto, err)
		}
		return floatPython(f)

	case int:
		return strconv.FormatInt(int64(t), 10), nil
	case int8:
		return strconv.FormatInt(int64(t), 10), nil
	case int16:
		return strconv.FormatInt(int64(t), 10), nil
	case int32:
		return strconv.FormatInt(int64(t), 10), nil
	case int64:
		return strconv.FormatInt(t, 10), nil
	case uint:
		return strconv.FormatUint(uint64(t), 10), nil
	case uint8:
		return strconv.FormatUint(uint64(t), 10), nil
	case uint16:
		return strconv.FormatUint(uint64(t), 10), nil
	case uint32:
		return strconv.FormatUint(uint64(t), 10), nil
	case uint64:
		return strconv.FormatUint(t, 10), nil

	case float32:
		// Convertido para float64 antes de formatar: o Python nao tem float de
		// 32 bits, e um float32 que virou float64 imprime os digitos do erro
		// de conversao. Formatar a partir do float32 e o que o Python veria.
		return floatPython(float64(t))
	case float64:
		return floatPython(t)

	default:
		return "", fmt.Errorf("TextoPython não sabe renderizar %T como o str() do Python "+
			"renderizaria. Ela cobre nil, bool, string, número e json.Number -- o resto o "+
			"Python formata com regras do tipo, e adivinhar numa chave produz duplicata "+
			"silenciosa", v)
	}
}

// floatPython e o str() do Python para float.
func floatPython(f float64) (string, error) {
	switch {
	case math.IsNaN(f):
		return "nan", nil
	case math.IsInf(f, 1):
		return "inf", nil
	case math.IsInf(f, -1):
		return "-inf", nil
	}

	if forcaExpoente(f) {
		return "", fmt.Errorf("TextoPython recusa %g: nessa faixa o str() do Python usa "+
			"notação exponencial (\"1e-05\", \"1e+16\"), cujo formato exato é detalhe do "+
			"CPython. Imitar seria apostar numa CHAVE -- e uma chave que diverge em silêncio "+
			"vira duplicata semanas depois. Componha esse campo você mesmo, ou tire-o da chave", f)
	}

	// 'f' com precisão -1 dá a representação decimal mais curta que faz o
	// round-trip, que é a mesma que o repr do Python usa dentro desta faixa.
	texto := strconv.FormatFloat(f, 'f', -1, 64)
	if !strings.ContainsAny(texto, ".") {
		// O float do Python sempre mostra a parte decimal: str(19.0) é "19.0",
		// e é exatamente essa a divergência que motivou esta função.
		texto += ".0"
	}
	return texto, nil
}

// forcaExpoente diz se o Python usaria notação exponencial.
//
// A regra do repr do CPython é o expoente decimal fora de [-4, 16): abaixo de
// 1e-4 ou a partir de 1e16. O zero está dentro (str(0.0) é "0.0").
func forcaExpoente(f float64) bool {
	if f == 0 {
		return false
	}
	abs := math.Abs(f)
	return abs < 1e-4 || abs >= 1e16
}
