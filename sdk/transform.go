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
// Transformer that renames the field Target.Key reads must run before Load
// sees it, and Target.Key must name the new name.
func Transform(data *Data, fns ...Transformer) *Data {
	if data == nil || len(fns) == 0 {
		return data
	}

	source := data.source
	upstream := data.Records

	return &Data{
		source: source,
		start:  data.start,
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
						URL: redact(source.URL), Format: string(source.Format),
						Line: i, Cause: err,
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
func applyAll(fns []Transformer, payload any) (any, bool, error) {
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

// Only keeps just the named fields.
//
// ParallelArrays copies every scalar outside the block onto each record,
// which is usually what you want -- latitude and longitude describe the
// reading. But a response also carries per-request metadata, and Open-Meteo's
// generationtime_ms is the cautionary case: it changes on every call, so
// keeping it makes the same reading write a different payload every run.
//
//	sdk.Only("time", "temperature_2m", "latitude", "longitude")
//
// A name that is not in the record is skipped rather than an error: this is a
// projection. Target.Key is where a missing field has to be loud, because
// that one decides the row's identity.
func Only(fields ...string) Transformer {
	keep := make(map[string]bool, len(fields))
	for _, f := range fields {
		keep[f] = true
	}

	return func(payload any) (any, error) {
		obj, ok := payload.(map[string]any)
		if !ok {
			return payload, nil
		}
		out := make(map[string]any, len(fields))
		for k, v := range obj {
			if keep[k] {
				out[k] = v
			}
		}
		return out, nil
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
		out := make(map[string]any, len(obj))
		for k, v := range obj {
			if !drop[k] {
				out[k] = v
			}
		}
		return out, nil
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

		out := make(map[string]any, len(obj))
		for k, v := range obj {
			if to, renamed := names[k]; renamed {
				out[to] = v
				continue
			}
			out[k] = v
		}
		return out, nil
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
