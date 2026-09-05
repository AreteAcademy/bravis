package sdk

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// KeySelector builds the source_key of a record from its payload.
// FieldSelector pulls a single value out of a record.
type FieldSelector func(payload any) (string, error)

// KeySelector builds a record's source_key.
//
// An alias of FieldSelector, not a separate type. The two always had the same
// signature and the same meaning: read a string out of the record.
//
//	sdk.Compute("source_key", sdk.Key("latitude", "longitude", "time"))
//
// One place produces the key, so the column and the ingestion_id cannot
// diverge.
type KeySelector = FieldSelector

// keySeparator joins the fields of a composite source_key.
//
// FROZEN. This character and the field order given to Key both feed
// source_key, which feeds ingestion_id. Change either and the same reading
// produces a different id, so it lands twice and stops matching the row a
// Python fetcher writes for it. Fixed in v0.3.0; never change it.
const keySeparator = "|"

// Key builds source_key by joining the named payload fields, in the order
// given, separated by "|".
//
//	Key("latitude", "longitude", "time")
//	// {"latitude": -23.55, "longitude": -46.63, "time": "2026-01-01T00:00"}
//	// -> "-23.55|-46.63|2026-01-01T00:00"
//
// The order is part of the contract: it feeds ingestion_id, so reordering the
// arguments makes the same reading land a second time. Pick an order once and
// keep it.
//
// A missing field is an error naming the field and listing what the payload
// actually has. Silently skipping it would produce a short key that still
// looks valid, and collisions would only surface as lost rows much later.
func Key(fields ...string) KeySelector {
	return func(payload any) (string, error) {
		if len(fields) == 0 {
			return "", fmt.Errorf("Key precisa from ao menos um campo")
		}

		obj, err := asObject(payload)
		if err != nil {
			return "", err
		}

		parts := make([]string, 0, len(fields))
		for _, campo := range fields {
			v, ok := obj[campo]
			if !ok {
				return "", fmt.Errorf("field %q is not in the payload; available: %s",
					campo, availableKeys(obj))
			}
			parts = append(parts, asText(v))
		}

		return strings.Join(parts, keySeparator), nil
	}
}

// KeyPython e Key rendendo cada campo como o str() do Python renderiza.
//
// Existe para o porte: se o fetcher em Python compunha a chave com
// `str(record["id"])`, esta e a unica forma de o Go produzir o MESMO
// ingestion_id -- e "o mesmo" e o que decide se a linha portada casa com a que
// ja esta na landing ou vira uma duplicata depois do merge.
//
// Um valor que TextoPython recusa -- a faixa exponencial, um mapa, uma lista --
// vira erro nomeando o campo. Recusar e melhor que divergir numa chave: a
// divergencia so aparece semanas depois, num relatorio, sem ninguem saber de
// onde veio.
//
// Ligue Source.PreserveNumbers junto. Sem ele, `{"id": 19}` e `{"id": 19.0}`
// chegam identicos ao Go, e esta funcao rende os dois como o float do Python
// ("19.0") -- certo para o segundo, errado para o primeiro.
func KeyPython(fields ...string) KeySelector {
	return chaveCom(TextoPython, fields...)
}

// chaveCom e Key com a renderizacao injetada, para que as duas variantes nao
// sejam duas copias que envelhecem separadas.
func chaveCom(render func(any) (string, error), fields ...string) KeySelector {
	return func(payload any) (string, error) {
		if len(fields) == 0 {
			return "", fmt.Errorf("Key precisa from ao menos um campo")
		}

		obj, err := asObject(payload)
		if err != nil {
			return "", err
		}

		parts := make([]string, 0, len(fields))
		for _, campo := range fields {
			v, ok := obj[campo]
			if !ok {
				return "", fmt.Errorf("field %q is not in the payload; available: %s",
					campo, availableKeys(obj))
			}
			texto, err := render(v)
			if err != nil {
				return "", fmt.Errorf("field %q: %w", campo, err)
			}
			parts = append(parts, texto)
		}

		return strings.Join(parts, keySeparator), nil
	}
}

// FixedKey uses a constant source_key. Only correct when the source yields a
// single record per run -- otherwise every row collapses onto one id.
func FixedKey(value string) KeySelector {
	return func(any) (string, error) { return value, nil }
}

// Field reads one payload field as the record timestamp.
//
//	When: Field("time")
//
// A missing field is an error naming it, for the same reason as Key.
func Field(name string) FieldSelector {
	return func(payload any) (string, error) {
		obj, err := asObject(payload)
		if err != nil {
			return "", err
		}
		v, ok := obj[name]
		if !ok {
			return "", fmt.Errorf("field %q is not in the payload; available: %s",
				name, availableKeys(obj))
		}
		return asText(v), nil
	}
}

// Now stamps every record with the time the run started. Use it when the
// source carries no timestamp of its own -- but note that it makes
// ingestion_id vary between runs, so the same reading will not deduplicate.
func Now() FieldSelector {
	instant := time.Now().UTC().Format(time.RFC3339)
	return func(any) (string, error) { return instant, nil }
}

// asObject narrows a payload to a JSON object, which is what the selectors
// can address.
func asObject(payload any) (map[string]any, error) {
	obj, ok := payload.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("payload precisa ser um objeto JSON para selecionar fields, veio %T", payload)
	}
	return obj, nil
}

// asText renders a payload value for use inside a key.
//
// Floats are formatted with the shortest representation that round-trips, so
// -23.55 stays "-23.55" rather than becoming "-23.550000". JSON numbers all
// arrive as float64, so an integer id must not pick up a ".0" tail.
//
// # Ele NAO e o str() do Python, e a diferenca importa
//
//	valor    asText     str() do Python
//	nil      ""         "None"
//	true     "true"     "True"
//	19.0     "19"       "19.0"
//
// Isto e o padrao e continua sendo, por um motivo que nao e preferencia:
// trocar a renderizacao muda o ingestion_id de TODA linha que o Go ja gravou.
// Um fetcher em producao passaria a escrever ids novos para as mesmas
// leituras, e o resultado nao e um erro -- e a tabela inteira duplicada no
// proximo merge.
//
// Quem esta portando um fetcher de Python e precisa casar com o que ja esta na
// landing usa KeyPython e IngestionIDPython, que rendem com TextoPython. A
// escolha e por fetcher, escrita no fetcher, e nao um padrao global que muda
// o significado de quem nao pediu.
func asText(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	case nil:
		return ""
	default:
		return fmt.Sprint(t)
	}
}

func availableKeys(obj map[string]any) string {
	nomes := make([]string, 0, len(obj))
	for k := range obj {
		nomes = append(nomes, k)
	}
	sort.Strings(nomes)
	if len(nomes) == 0 {
		return "(empty payload)"
	}
	return strings.Join(nomes, ", ")
}
