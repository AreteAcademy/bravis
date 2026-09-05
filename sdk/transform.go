package sdk

import (
	"errors"
	"fmt"
	"iter"
	"sort"
	"strings"
)

// Transformer reshapes one record on its way from Extract to Load.
//
// It receives the payload as decoded -- a map[string]any for JSON, the usual
// case -- and returns what should be loaded in its place. Returning
// SkipRecord drops the record.
//
// # The map you receive belongs to the chain
//
// Transform hands you a copy it made for this record, and nothing outside the
// chain holds it. You may modify it in place and return it -- that is what the
// built-in Transformers do, and it is why a chain of six costs one map instead
// of six.
//
// What you must not do is RETAIN it: the transformers after yours will write
// into the same map, and the loader reads it after the chain finishes. If you
// need the record to outlive your function, copy it.
//
// This is a seam, not a transformation engine. The SDK does not know your
// business rules and does not try to: heavy reshaping belongs downstream, in
// dbt. What belongs here is the shaping a row needs before it is worth
// storing at all -- dropping request metadata, renaming a field to the name
// the warehouse uses, deriving a value the source gives in pieces.
type Transformer func(payload any) (any, error)

// SkipRecord, returned by a Transformer, drops the record without failing the
// load. Use it to filter.
//
//	sdk.Transform(data, func(p any) (any, error) {
//		if p.(map[string]any)["temperature_2m"] == nil {
//			return nil, sdk.SkipRecord
//		}
//		return p, nil
//	})
//
// Named without the Err prefix on purpose: this is a control signal, not a
// failure, and the stdlib spells the same thing fs.SkipDir and fs.SkipAll.
var SkipRecord = errors.New("skip record") //nolint:staticcheck // ST1012: control signal, see above

// Transform applies fns to every record, in the order given, between Extract
// and Load:
//
//	data, err := sdk.Extract(ctx, source)
//	data = sdk.Transform(data,
//		sdk.Without("generationtime_ms"),
//		sdk.Rename(map[string]string{"temperature_2m": "temperature_c"}),
//	)
//	res, err := sdk.Load(ctx, data, target)
//
// It stays lazy: records are transformed as they are pulled, so a paginated
// source still does not have to fit in memory.
//
// Provenance is not available here. Provider, Entity, SourceKey and RecordTS
// are stamped at Load, from Target, after every Transformer has run -- so a
// Transformer that renames a field sdk.IngestionID reads must run before it,
// and IngestionID must name the new name.
func Transform(data *Data, fns ...Transformer) *Data {
	if data == nil || len(fns) == 0 {
		return data
	}

	source := data.source
	upstream := data.Records

	return &Data{
		source: source,
		start:  data.start,
		stats:  data.stats,
		Records: func(yield func(Envelope, error) bool) {
			i := 0
			for env, err := range upstream {
				if err != nil {
					if !yield(Envelope{}, err) {
						return
					}
					continue
				}

				payload, skip, err := applyAll(fns, env.Payload)
				if err != nil {
					yield(Envelope{}, &FormatError{
						URL: source.From.Describe(), Line: i, Cause: err,
					})
					return
				}
				i++
				if skip {
					continue
				}

				env.Payload = payload
				if !yield(env, nil) {
					return
				}
			}
		},
	}
}

// applyAll runs the chain, reporting whether the record was skipped.
//
// Ele faz UMA copia do registro antes da cadeia, e a partir dai os
// transformers escrevem no lugar.
//
// Antes, cada transformer devolvia um mapa novo, "porque o chamador ainda pode
// estar segurando o mapa". Isso e verdade exatamente uma vez -- para o mapa que
// o decodificador acabou de entregar, e que o preview do extract guarda para
// mostrar o que a FONTE mandou. Depois da primeira copia, o unico que segura o
// registro e a propria cadeia, e as outras seis copias eram trabalho identico
// repetido por registro: seis mapas por linha, numa carga de milhoes.
//
// A copia fica AQUI, num lugar so, e nao dentro de cada transformer -- que e o
// que torna a economia estrutural em vez de uma otimizacao a ser lembrada em
// cada driver novo.
func applyAll(fns []Transformer, payload any) (any, bool, error) {
	if obj, ehObjeto := payload.(map[string]any); ehObjeto && len(fns) > 0 {
		copia := make(map[string]any, len(obj))
		for k, v := range obj {
			copia[k] = v
		}
		payload = copia
	}

	for n, fn := range fns {
		if fn == nil {
			continue
		}
		out, err := fn(payload)
		if errors.Is(err, SkipRecord) {
			return nil, true, nil
		}
		if err != nil {
			return nil, false, fmt.Errorf("transformer %d: %w", n, err)
		}
		payload = out
	}
	return payload, false, nil
}

// Accept names what you take from the source: exactly these fields, and an
// error naming any that is missing.
//
//	Transform: []sdk.Transformer{
//		sdk.Accept("time", "temperature_2m", "latitude", "longitude"),
//		sdk.Compute("payload", func(r map[string]any) (any, error) { ... }),
//	}
//
// Fields not named are dropped -- saying which four you want is saying it out
// loud. A field that is named and not there is an error, because that one is
// the source changing shape under you, and it must not reach the warehouse as
// a column that quietly went NULL.
//
// This is not the destination's shape. Accept answers "does the source still
// send what I read?"; Target.Columns answers "does the row have the columns
// the table has?". Both are worth checking and they catch different things,
// which is why they are two calls with two names -- an earlier version called
// this one Schema, and a fetcher then had two Schema lines meaning two
// different things.
//
// A record that is not a JSON object is passed through untouched: there are
// no fields to name.
func Accept(fields ...string) Transformer {
	keep := make(map[string]bool, len(fields))
	for _, f := range fields {
		keep[f] = true
	}

	return func(payload any) (any, error) {
		obj, ok := payload.(map[string]any)
		if !ok {
			return payload, nil
		}

		// Os ausentes sao conferidos ANTES de apagar qualquer coisa: recusar
		// depois de ter mexido no registro deixaria o erro descrevendo um
		// registro que ja nao existe.
		var missing []string
		for _, f := range fields {
			if _, present := obj[f]; !present {
				missing = append(missing, f)
			}
		}
		if len(missing) > 0 {
			return nil, fmt.Errorf("Accept names %s, which this record does not have. "+
				"It has: %s", strings.Join(missing, ", "), availableKeys(obj))
		}

		for k := range obj {
			if !keep[k] {
				delete(obj, k)
			}
		}
		return obj, nil
	}
}

// Without drops the named fields and keeps everything else. The inverse of
// Only, and the better choice when a response has many useful fields and one
// or two you do not want.
//
//	sdk.Without("generationtime_ms", "timezone_abbreviation")
func Without(fields ...string) Transformer {
	drop := make(map[string]bool, len(fields))
	for _, f := range fields {
		drop[f] = true
	}

	return func(payload any) (any, error) {
		obj, ok := payload.(map[string]any)
		if !ok {
			return payload, nil
		}
		for f := range drop {
			delete(obj, f)
		}
		return obj, nil
	}
}

// Rename maps field names from what the source calls them to what you do.
//
//	sdk.Rename(map[string]string{"temperature_2m": "temperature_c"})
//
// Renaming onto a name the record already has is an error: one of the two
// values would be lost, and which one would depend on map iteration order.
func Rename(names map[string]string) Transformer {
	return func(payload any) (any, error) {
		obj, ok := payload.(map[string]any)
		if !ok {
			return payload, nil
		}

		var clashes []string
		for from, to := range names {
			if _, present := obj[from]; !present {
				continue
			}
			if _, taken := obj[to]; taken && from != to {
				clashes = append(clashes, fmt.Sprintf("%s -> %s", from, to))
			}
		}
		if len(clashes) > 0 {
			sort.Strings(clashes)
			return nil, fmt.Errorf("rename would overwrite an existing field: %s",
				strings.Join(clashes, ", "))
		}

		// Dois passos, e nao um: aplicar as trocas uma a uma no lugar faria
		// {a: b, b: c} sobre um registro que so tem `a` mover o valor para
		// `c` ou parar em `b`, dependendo da ordem em que o mapa foi
		// percorrido. Lendo tudo antes de escrever, o resultado e o mesmo que
		// montar um mapa novo -- que era o comportamento anterior.
		type troca struct {
			para  string
			valor any
		}
		var trocas []troca
		for de, para := range names {
			if v, present := obj[de]; present {
				trocas = append(trocas, troca{para, v})
				delete(obj, de)
			}
		}
		for _, t := range trocas {
			obj[t.para] = t.valor
		}
		return obj, nil
	}
}

// Compute adds a field derived from the record.
//
//	sdk.Compute("temperature_f", func(r map[string]any) (any, error) {
//		c, ok := r["temperature_2m"].(float64)
//		if !ok {
//			return nil, fmt.Errorf("temperature_2m missing or not a number")
//		}
//		return c*9/5 + 32, nil
//	})
//
// Overwriting an existing field is an error. Silently replacing a value the
// source gave you is the kind of thing nobody notices until the numbers are
// wrong; to replace deliberately, Without it first.
func Compute(name string, fn func(record map[string]any) (any, error)) Transformer {
	return func(payload any) (any, error) {
		obj, ok := payload.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("Compute needs a JSON object, got %T", payload)
		}
		if _, taken := obj[name]; taken {
			return nil, fmt.Errorf("Compute(%q) would overwrite a field the record already has; "+
				"drop it first with Without(%q) if that is what you mean", name, name)
		}

		v, err := fn(obj)
		if err != nil {
			return nil, fmt.Errorf("computing %q: %w", name, err)
		}

		out := make(map[string]any, len(obj)+1)
		for k, val := range obj {
			out[k] = val
		}
		out[name] = v
		return out, nil
	}
}

// ensure Transform's iterator type matches Data.Records.
var _ func(func(Envelope, error) bool) = iter.Seq2[Envelope, error](nil)
