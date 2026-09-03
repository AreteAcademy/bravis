package sdk

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// SeletorDeChave builds the source_key of a record from its payload.
type SeletorDeChave func(payload any) (string, error)

// SeletorDeCampo pulls a single value out of a payload.
type SeletorDeCampo func(payload any) (string, error)

// separadorDeChave joins the fields of a composite source_key.
//
// FROZEN. This character and the field order given to Chave both feed
// source_key, which feeds ingestion_id. Change either and the same reading
// produces a different id, so it lands twice and stops matching the row a
// Python fetcher writes for it. Fixed in v0.3.0; never change it.
const separadorDeChave = "|"

// Chave builds source_key by joining the named payload fields, in the order
// given, separated by "|".
//
//	Chave("latitude", "longitude", "time")
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
func Chave(campos ...string) SeletorDeChave {
	return func(payload any) (string, error) {
		if len(campos) == 0 {
			return "", fmt.Errorf("Chave precisa de ao menos um campo")
		}

		obj, err := comoObjeto(payload)
		if err != nil {
			return "", err
		}

		partes := make([]string, 0, len(campos))
		for _, campo := range campos {
			v, ok := obj[campo]
			if !ok {
				return "", fmt.Errorf("campo %q não existe no payload; disponíveis: %s",
					campo, chavesDisponiveis(obj))
			}
			partes = append(partes, comoTexto(v))
		}

		return strings.Join(partes, separadorDeChave), nil
	}
}

// ChaveFixa uses a constant source_key. Only correct when the source yields a
// single record per run -- otherwise every row collapses onto one id.
func ChaveFixa(valor string) SeletorDeChave {
	return func(any) (string, error) { return valor, nil }
}

// Campo reads one payload field as the record timestamp.
//
//	Quando: Campo("time")
//
// A missing field is an error naming it, for the same reason as Chave.
func Campo(nome string) SeletorDeCampo {
	return func(payload any) (string, error) {
		obj, err := comoObjeto(payload)
		if err != nil {
			return "", err
		}
		v, ok := obj[nome]
		if !ok {
			return "", fmt.Errorf("campo %q não existe no payload; disponíveis: %s",
				nome, chavesDisponiveis(obj))
		}
		return comoTexto(v), nil
	}
}

// Agora stamps every record with the time the run started. Use it when the
// source carries no timestamp of its own -- but note that it makes
// ingestion_id vary between runs, so the same reading will not deduplicate.
func Agora() SeletorDeCampo {
	instante := time.Now().UTC().Format(time.RFC3339)
	return func(any) (string, error) { return instante, nil }
}

// comoObjeto narrows a payload to a JSON object, which is what the selectors
// can address.
func comoObjeto(payload any) (map[string]any, error) {
	obj, ok := payload.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("payload precisa ser um objeto JSON para selecionar campos, veio %T", payload)
	}
	return obj, nil
}

// comoTexto renders a payload value for use inside a key.
//
// Floats are formatted with the shortest representation that round-trips, so
// -23.55 stays "-23.55" rather than becoming "-23.550000". JSON numbers all
// arrive as float64, so an integer id must not pick up a ".0" tail.
func comoTexto(v any) string {
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

func chavesDisponiveis(obj map[string]any) string {
	nomes := make([]string, 0, len(obj))
	for k := range obj {
		nomes = append(nomes, k)
	}
	sort.Strings(nomes)
	if len(nomes) == 0 {
		return "(payload vazio)"
	}
	return strings.Join(nomes, ", ")
}
