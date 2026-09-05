// Package pycompat renderiza valores como o Python renderiza, para quem esta
// portando um fetcher de Python mantendo a MESMA landing e os MESMOS ids.
//
// Ele vive num subpacote, e nao no nucleo, porque e uma PONTE PARA UMA
// MIGRACAO e nao um conceito de ETL. Um time que comeca um pipeline novo em Go
// nao tem com o que casar, e a estrutura diz isso: quem precisa importa, quem
// nao precisa nem sabe que existe.
//
//	Key:       sdk.KeyWith(pycompat.Texto, "provider", "id"),
//	Transform: []sdk.Transformer{sdk.IngestionIDWith(pycompat.Texto)},
package pycompat

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Texto renderiza um valor como o `str()` do Python renderiza.
//
// Ela existe para quem esta portando um fetcher de Python para Go mantendo a
// MESMA landing e o MESMO ingestion_id. Se o Python fazia `str(record["id"])`
// para compor a chave, o Go precisa produzir exatamente aquele texto -- senao
// a mesma leitura recebe um id diferente, e o que aparece do outro lado nao e
// um erro: e uma linha duplicada depois do merge do bronze.
//
// O SDK NAO usa esta funcao por padrao. Ver sdk.IngestionIDWith e sdk.KeyWith, e
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
func Texto(v any) (string, error) {
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
		// Um float32 NAO vem de JSON decodificado -- o encoding/json nunca
		// produz um --, entao ele so chega aqui se o consumidor o pos no
		// registro, e nesse caso ele e float sem ambiguidade. E o unico
		// flutuante que da para renderizar sem adivinhar.
		//
		// Convertido para float64 antes de formatar: o Python nao tem float de
		// 32 bits, e formatar a partir do float32 e o que o Python veria.
		return floatPython(float64(t))

	case float64:
		// inf e nan passam: nenhum literal JSON produz um deles -- o JSON nem
		// os representa --, entao a ambiguidade int/float nao existe aqui.
		// Recusa-los seria aplicar a regra onde ela nao tem razao.
		if math.IsNaN(t) || math.IsInf(t, 0) {
			return floatPython(t)
		}

		// RECUSA, pela mesma razao que o default recusa.
		//
		// Um float64 so chega aqui quando o literal JA SE PERDEU: o
		// encoding/json decodifica `1` e `1.0` no mesmo float64, e o Python
		// via int num caso e float no outro. Escolher uma das duas acerta
		// metade das vezes, e a metade errada e uma linha duplicada semanas
		// depois -- que e exatamente o que esta funcao existe para evitar.
		//
		// Ate a v0.39.0 ela escolhia "1.0" em silencio, e a limitacao estava
		// DOCUMENTADA. Documentar uma divergencia nao e o mesmo que impedi-la,
		// e o default logo abaixo recusava pelo mesmo motivo -- a incoerencia
		// era minha.
		return "", fmt.Errorf("pycompat.Texto recebeu um float64 (%v), e a essa altura o "+
			"literal do JSON já se perdeu: `1` e `1.0` viram o mesmo float64, e o Python "+
			"via int num caso e float no outro. Ligue Source.PreserveNumbers para o número "+
			"chegar como json.Number com o literal intacto. Se a origem era mesmo float e "+
			"você quer renderizar como float, diga isso: pycompat.TextoAceitandoFloat64", t)

	default:
		return "", fmt.Errorf("pycompat.Texto não sabe renderizar %T como o str() do Python "+
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
		return "", fmt.Errorf("pycompat.Texto recusa %g: nessa faixa o str() do Python usa "+
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

// TextoOuVazio e o idioma `str(x or "")` do Python, que e o mais comum na
// composicao de chave -- 14 dos fetchers levantados o usam.
//
// O `or ""` e a verdade-falsidade do Python, e ela nao e a do Go: `None`, `""`,
// `0`, `0.0`, `[]` e `{}` sao todos falsos la, e viram string vazia. Escrever
// isso a mao da cerca de 25 linhas por consumidor, e errar um dos seis casos e
// silencioso -- o valor falso vira texto na chave e o id sai diferente.
//
// Repare que `0` e `0.0` viram "" e nao "0": e contraintuitivo, e e o que o
// Python faz. Um port que escrever isso a mao vai acertar None e errar o zero.
func TextoOuVazio(v any) (string, error) {
	if falsoNoPython(v) {
		return "", nil
	}
	return Texto(v)
}

// falsoNoPython implementa a verdade-falsidade dos tipos que um registro JSON
// produz. O resto -- um objeto qualquer -- e verdadeiro no Python por padrao, e
// aqui cai no Texto, que recusa o que nao sabe renderizar.
func falsoNoPython(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case bool:
		return !t
	case string:
		return t == ""
	case json.Number:
		f, err := t.Float64()
		return err == nil && f == 0
	case int:
		return t == 0
	case int8:
		return t == 0
	case int16:
		return t == 0
	case int32:
		return t == 0
	case int64:
		return t == 0
	case uint:
		return t == 0
	case uint8:
		return t == 0
	case uint16:
		return t == 0
	case uint32:
		return t == 0
	case uint64:
		return t == 0
	case float32:
		return t == 0
	case float64:
		return t == 0
	case []any:
		return len(t) == 0
	case map[string]any:
		return len(t) == 0
	default:
		return false
	}
}

// TextoAceitandoFloat64 e Texto tratando float64 como o float do Python.
//
// Use quando voce SABE que o numero era float na origem -- e nao um int que o
// encoding/json colapsou -- e nao pode ligar Source.PreserveNumbers.
//
//	Key: sdk.KeyWith(pycompat.TextoAceitandoFloat64, "lat", "lon")
//
// O nome e comprido de proposito. Ele e a afirmacao "eu conferi": num campo que
// era int no Python, isto produz "19.0" onde o Python produziu "19", e o
// resultado nao e um erro -- e uma linha duplicada depois do merge.
//
// Onde der para ligar PreserveNumbers, ligue: o literal decide sozinho, e nao ha
// o que conferir.
func TextoAceitandoFloat64(v any) (string, error) {
	if f, ehFloat := v.(float64); ehFloat {
		return floatPython(f)
	}
	return Texto(v)
}
