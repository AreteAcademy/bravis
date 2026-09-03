package sdk

import (
	"encoding/json"
	"fmt"
)

// Expansor turns one decoded document into the records it contains.
//
// Many APIs answer with a single object wrapping the actual readings, so the
// decoder yields one Envelope holding the whole document. An Expansor says how
// to get from that to one record per reading.
//
// When the vendor's shape does not fit a helper here, write the function
// yourself -- Expandir takes any func with this signature. The hard case has
// to stay possible; what must not happen is the common case costing a hundred
// lines.
type Expansor func(payload any) ([]any, error)

// ArraysParalelos expands the widespread "columns as parallel arrays" shape,
// where index i of every array describes the same reading:
//
//	{"latitude": -23.55, "hourly": {"time": [...], "temperature_2m": [...]}}
//
//	ArraysParalelos("hourly", "time", "temperature_2m")
//	// -> {"time": ..., "temperature_2m": ..., "latitude": -23.55}
//
// bloco names the object holding the arrays, or "" when they sit at the top
// level. Fields outside that block are copied onto every record, which is how
// latitude and longitude above end up on each reading.
//
// Arrays of differing lengths are an error: pairing them by index would
// silently mismatch readings.
func ArraysParalelos(bloco string, campos ...string) Expansor {
	return func(payload any) ([]any, error) {
		if len(campos) == 0 {
			return nil, fmt.Errorf("ArraysParalelos precisa de ao menos um campo")
		}

		doc, err := comoObjeto(payload)
		if err != nil {
			return nil, err
		}

		fonte := doc
		if bloco != "" {
			b, ok := doc[bloco]
			if !ok {
				return nil, fmt.Errorf("bloco %q não existe na resposta; disponíveis: %s",
					bloco, chavesDisponiveis(doc))
			}
			fonte, err = comoObjeto(b)
			if err != nil {
				return nil, fmt.Errorf("bloco %q: %w", bloco, err)
			}
		}

		arrays := make(map[string][]any, len(campos))
		tamanho := -1
		for _, campo := range campos {
			v, ok := fonte[campo]
			if !ok {
				return nil, fmt.Errorf("campo %q não existe em %q; disponíveis: %s",
					campo, bloco, chavesDisponiveis(fonte))
			}
			arr, ok := v.([]any)
			if !ok {
				return nil, fmt.Errorf("campo %q não é um array, veio %T", campo, v)
			}
			if tamanho == -1 {
				tamanho = len(arr)
			} else if len(arr) != tamanho {
				return nil, fmt.Errorf("arrays de tamanhos diferentes: %q tem %d, esperado %d — "+
					"parear por índice juntaria leituras erradas", campo, len(arr), tamanho)
			}
			arrays[campo] = arr
		}

		// Everything outside the block describes the series as a whole and is
		// copied onto every record.
		comuns := map[string]any{}
		if bloco != "" {
			for k, v := range doc {
				if k == bloco {
					continue
				}
				if _, aninhado := v.(map[string]any); aninhado {
					continue
				}
				comuns[k] = v
			}
		}

		registros := make([]any, 0, tamanho)
		for i := 0; i < tamanho; i++ {
			r := make(map[string]any, len(campos)+len(comuns))
			for k, v := range comuns {
				r[k] = v
			}
			for _, campo := range campos {
				r[campo] = arrays[campo][i]
			}
			registros = append(registros, r)
		}

		return registros, nil
	}
}

// ArrayEm expands an array nested under a path, the other common shape:
//
//	{"data": {"results": [ {...}, {...} ]}}
//	ArrayEm("data", "results")
func ArrayEm(caminho ...string) Expansor {
	return func(payload any) ([]any, error) {
		atual := payload
		for _, passo := range caminho {
			obj, err := comoObjeto(atual)
			if err != nil {
				return nil, fmt.Errorf("caminho %v: %w", caminho, err)
			}
			v, ok := obj[passo]
			if !ok {
				return nil, fmt.Errorf("caminho %v: %q não existe; disponíveis: %s",
					caminho, passo, chavesDisponiveis(obj))
			}
			atual = v
		}

		arr, ok := atual.([]any)
		if !ok {
			return nil, fmt.Errorf("caminho %v não termina num array, veio %T", caminho, atual)
		}
		return arr, nil
	}
}

// RecusarSe rejects a 200 response whose body carries one of these fields set
// to a truthy value. Plenty of APIs answer 200 with {"error": true} -- without
// a guard that document lands in the warehouse as if it were data.
//
//	Guarda: RecusarSe("error")
//
// It only inspects top-level fields of a JSON object; a body that is not one
// passes through, because a non-JSON body is the decoder's problem to report.
func RecusarSe(campos ...string) func(status int, corpo []byte) error {
	return func(status int, corpo []byte) error {
		var doc map[string]any
		if err := json.Unmarshal(corpo, &doc); err != nil {
			return nil
		}

		for _, campo := range campos {
			v, ok := doc[campo]
			if !ok || !verdadeiro(v) {
				continue
			}
			// Surface the reason when the API gives one; it is the difference
			// between "the API said no" and knowing why.
			for _, motivo := range []string{"reason", "message", "detail", "error_description"} {
				if m, ok := doc[motivo].(string); ok && m != "" {
					return fmt.Errorf("resposta %d marcada com %q: %s", status, campo, m)
				}
			}
			return fmt.Errorf("resposta %d marcada com %q: %v", status, campo, v)
		}
		return nil
	}
}

// ExigirCampos rejects a response missing any of the named top-level fields,
// which catches a truncated or restructured payload before it is decoded.
func ExigirCampos(campos ...string) func(status int, corpo []byte) error {
	return func(status int, corpo []byte) error {
		var doc map[string]any
		if err := json.Unmarshal(corpo, &doc); err != nil {
			return fmt.Errorf("resposta %d não é um objeto JSON", status)
		}
		for _, campo := range campos {
			if _, ok := doc[campo]; !ok {
				return fmt.Errorf("resposta %d sem o campo %q; disponíveis: %s",
					status, campo, chavesDisponiveis(doc))
			}
		}
		return nil
	}
}

func verdadeiro(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return t != "" && t != "false" && t != "0"
	case float64:
		return t != 0
	case nil:
		return false
	default:
		return true
	}
}

// Somente keeps just the named fields of each record an Expansor produces.
//
// ArraysParalelos copies every scalar outside the block onto each record,
// which is usually what you want -- latitude and longitude describe the
// reading. But a response often also carries per-request metadata, and
// Open-Meteo's generationtime_ms is the cautionary case: it changes on every
// call, so it does not belong in a row that is supposed to be the same
// reading each time you fetch it.
//
//	Expandir: sdk.Somente(
//		sdk.ArraysParalelos("hourly", "time", "temperature_2m"),
//		"time", "temperature_2m", "latitude", "longitude",
//	)
//
// A field named here but absent from a record is skipped rather than an
// error: this is a projection, and Chave is where a missing field has to be
// loud, because that one changes the identity of the row.
func Somente(e Expansor, campos ...string) Expansor {
	return func(payload any) ([]any, error) {
		registros, err := e(payload)
		if err != nil {
			return nil, err
		}

		saida := make([]any, 0, len(registros))
		for _, r := range registros {
			obj, ok := r.(map[string]any)
			if !ok {
				saida = append(saida, r)
				continue
			}
			filtrado := make(map[string]any, len(campos))
			for _, campo := range campos {
				if v, existe := obj[campo]; existe {
					filtrado[campo] = v
				}
			}
			saida = append(saida, filtrado)
		}
		return saida, nil
	}
}
