package pycompat

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/AreteAcademy/brevis/sdk/internal/core"
)

// JSONCanonico serializa o registro como o Python serializa em
//
//	json.dumps(v, sort_keys=True, separators=(",", ":"), ensure_ascii=False)
//
// que e a forma com que boa parte dos fetchers em Python deriva a chave quando a
// origem nao tem id estavel.
//
//	Key: func(p any) (string, error) {
//	    b, err := pycompat.JSONCanonico(p)
//	    return string(b), err
//	}
//
// # As tres armadilhas, e por que uma funcao
//
// Reproduzir isto a mao custa ~90 linhas, e cada uma das tres muda a chave SEM
// ERRO:
//
//  1. o encoding/json escapa `<`, `>` e `&`; o Python nao escapa nenhum dos
//     tres. A regra vive em core.AppendJSONString, num lugar so;
//  2. sem Source.PreserveNumbers, `1` e `1.0` chegam como o mesmo float64 --
//     e aqui isso e ERRO, nao um palpite. Ver Texto;
//  3. inteiro de precisao arbitraria perde precisao ao passar por float64, e o
//     json.Number preserva o literal.
//
// # O que ela NAO e
//
// Ela casa com o PYTHON, e nao com um padrao. Quem esta comecando um ETL novo e
// so quer uma chave estavel quer outra coisa -- o RFC 8785 (JCS) -- e as duas
// nao devem ser a mesma funcao: confundi-las seria pior que nao ter nenhuma.
func JSONCanonico(v any) ([]byte, error) {
	return escrever(make([]byte, 0, 256), v)
}

func escrever(dst []byte, v any) ([]byte, error) {
	switch t := v.(type) {
	case nil:
		// null, e nao "None": isto e JSON, e o json.dumps escreve JSON. So os
		// NUMEROS seguem o repr do Python, que e onde as duas linguagens
		// divergem.
		return append(dst, "null"...), nil

	case bool:
		if t {
			return append(dst, "true"...), nil
		}
		return append(dst, "false"...), nil

	case string:
		return core.AppendJSONString(dst, t), nil

	case map[string]any:
		// sort_keys=True. A ordem de um mapa em Go e embaralhada de proposito,
		// entao sem isto a chave mudaria a cada execucao -- e a identidade com
		// ela.
		chaves := make([]string, 0, len(t))
		for k := range t {
			chaves = append(chaves, k)
		}
		sort.Strings(chaves)

		dst = append(dst, '{')
		for i, k := range chaves {
			if i > 0 {
				dst = append(dst, ',') // separators=(",", ":"): sem espacos
			}
			dst = core.AppendJSONString(dst, k)
			dst = append(dst, ':')
			var err error
			if dst, err = escrever(dst, t[k]); err != nil {
				return nil, fmt.Errorf("em %q: %w", k, err)
			}
		}
		return append(dst, '}'), nil

	case []any:
		dst = append(dst, '[')
		for i, e := range t {
			if i > 0 {
				dst = append(dst, ',')
			}
			var err error
			if dst, err = escrever(dst, e); err != nil {
				return nil, fmt.Errorf("no índice %d: %w", i, err)
			}
		}
		return append(dst, ']'), nil

	default:
		// Numero. O Texto ja e o repr do Python para float e o literal para
		// int, que e exatamente o que o json.dumps escreve -- e ele RECUSA um
		// float64 cru, pelo mesmo motivo que esta funcao nao pode adivinhar.
		texto, err := Texto(v)
		if err != nil {
			return nil, err
		}
		if _, ehTexto := v.(string); ehTexto {
			return core.AppendJSONString(dst, texto), nil
		}
		if !ehNumero(texto) {
			return nil, fmt.Errorf("JSONCanonico não sabe serializar %T. Ela cobre o que um "+
				"registro JSON produz: nil, bool, string, número, objeto e lista", v)
		}
		return append(dst, texto...), nil
	}
}

// ehNumero confere que o texto e um literal JSON de numero.
//
// A conferencia existe porque o Texto rende bool como "True" e nil como "None",
// que sao Python e nao JSON -- e os dois ja foram tratados acima. Ela e a rede
// que impede um tipo novo de escapar pelo default e sair como literal invalido.
func ehNumero(s string) bool {
	if s == "" {
		return false
	}
	n := json.Number(s)
	if _, err := n.Float64(); err != nil {
		return false
	}
	return !strings.ContainsAny(s, `"' `)
}
