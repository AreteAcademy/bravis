package sdk

import (
	"encoding/json"
	"fmt"
)

// Expander turns one decoded document into the records it contains.
//
// Many APIs answer with a single object wrapping the actual readings, so the
// decoder yields one Envelope holding the whole document. An Expander says how
// to get from that to one record per reading.
//
// When the vendor's shape does not fit a helper here, write the function
// yourself -- Expand takes any func with this signature. The hard case has
// to stay possible; what must not happen is the common case costing a hundred
// lines.
type Expander func(payload any) ([]any, error)

// ParallelArrays expands the widespread "columns as parallel arrays" shape,
// where index i of every array describes the same reading:
//
//	{"latitude": -23.55, "hourly": {"time": [...], "temperature_2m": [...]}}
//
//	ParallelArrays("hourly", "time", "temperature_2m")
//	// -> {"time": ..., "temperature_2m": ..., "latitude": -23.55}
//
// block names the object holding the arrays, or "" when they sit at the top
// level. Fields outside that block are copied onto every record, which is how
// latitude and longitude above end up on each reading.
//
// Arrays of differing lengths are an error: pairing them by index would
// silently mismatch readings.
func ParallelArrays(block string, fields ...string) Expander {
	return func(payload any) ([]any, error) {
		if len(fields) == 0 {
			return nil, fmt.Errorf("ParallelArrays precisa from ao menos um campo")
		}

		doc, err := asObject(payload)
		if err != nil {
			return nil, err
		}

		source := doc
		if block != "" {
			b, ok := doc[block]
			if !ok {
				return nil, fmt.Errorf("block %q is not in the response; available: %s",
					block, availableKeys(doc))
			}
			source, err = asObject(b)
			if err != nil {
				return nil, fmt.Errorf("block %q: %w", block, err)
			}
		}

		arrays := make(map[string][]any, len(fields))
		size := -1
		for _, campo := range fields {
			v, ok := source[campo]
			if !ok {
				return nil, fmt.Errorf("field %q is not in %q; available: %s",
					campo, block, availableKeys(source))
			}
			arr, ok := v.([]any)
			if !ok {
				return nil, fmt.Errorf("field %q is not an array, got %T", campo, v)
			}
			if size == -1 {
				size = len(arr)
			} else if len(arr) != size {
				return nil, fmt.Errorf("arrays of different lengths: %q has %d, expected %d -- "+
					"pairing them by index would join the wrong readings", campo, len(arr), size)
			}
			arrays[campo] = arr
		}

		// Everything outside the block describes the series as a whole and is
		// copied onto every record.
		common := map[string]any{}
		if block != "" {
			for k, v := range doc {
				if k == block {
					continue
				}
				if _, nested := v.(map[string]any); nested {
					continue
				}
				common[k] = v
			}
		}

		records := make([]any, 0, size)
		for i := 0; i < size; i++ {
			r := make(map[string]any, len(fields)+len(common))
			for k, v := range common {
				r[k] = v
			}
			for _, campo := range fields {
				r[campo] = arrays[campo][i]
			}
			records = append(records, r)
		}

		return records, nil
	}
}

// ArrayAt expands an array nested under a path, the other common shape:
//
//	{"data": {"results": [ {...}, {...} ]}}
//	ArrayAt("data", "results")
func ArrayAt(path ...string) Expander {
	return func(payload any) ([]any, error) {
		current := payload
		for _, step := range path {
			obj, err := asObject(current)
			if err != nil {
				return nil, fmt.Errorf("path %v: %w", path, err)
			}
			v, ok := obj[step]
			if !ok {
				return nil, fmt.Errorf("path %v: %q does not exist; available: %s",
					path, step, availableKeys(obj))
			}
			current = v
		}

		arr, ok := current.([]any)
		if !ok {
			return nil, fmt.Errorf("path %v does not end in an array, got %T", path, current)
		}
		return arr, nil
	}
}

// RejectIf rejects a 200 response whose body carries one of these fields set
// to a truthy value. Plenty of APIs answer 200 with {"error": true} -- without
// a guard that document lands in the warehouse as if it were data.
//
//	Guard: RejectIf("error")
//
// It only inspects top-level fields of a JSON object; a body that is not one
// passes through, because a non-JSON body is the decoder's problem to report.
func RejectIf(fields ...string) func(status int, body []byte) error {
	return func(status int, body []byte) error {
		var doc map[string]any
		if err := json.Unmarshal(body, &doc); err != nil {
			return nil
		}

		for _, campo := range fields {
			v, ok := doc[campo]
			if !ok || !truthy(v) {
				continue
			}
			// Surface the reason when the API gives one; it is the difference
			// between "the API said no" and knowing why.
			for _, reason := range []string{"reason", "message", "detail", "error_description"} {
				if m, ok := doc[reason].(string); ok && m != "" {
					return fmt.Errorf("response %d flagged with %q: %s", status, campo, m)
				}
			}
			return fmt.Errorf("response %d flagged with %q: %v", status, campo, v)
		}
		return nil
	}
}

// RequireFields rejects a response missing any of the named top-level fields,
// which catches a truncated or restructured payload before it is decoded.
func RequireFields(fields ...string) func(status int, body []byte) error {
	return func(status int, body []byte) error {
		var doc map[string]any
		if err := json.Unmarshal(body, &doc); err != nil {
			return fmt.Errorf("response %d is not a JSON object", status)
		}
		for _, campo := range fields {
			if _, ok := doc[campo]; !ok {
				return fmt.Errorf("response %d is missing field %q; available: %s",
					status, campo, availableKeys(doc))
			}
		}
		return nil
	}
}

func truthy(v any) bool {
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

// Only keeps just the named fields of each record an Expander produces.
//
// ParallelArrays copies every scalar outside the block onto each record,
// which is usually what you want -- latitude and longitude describe the
// reading. But a response often also carries per-request metadata, and
// Open-Meteo's generationtime_ms is the cautionary case: it changes on every
// call, so it does not belong in a row that is supposed to be the same
// reading each time you fetch it.
//
//	Expand: sdk.Only(
//		sdk.ParallelArrays("hourly", "time", "temperature_2m"),
//		"time", "temperature_2m", "latitude", "longitude",
//	)
//
// A field named here but absent from a record is skipped rather than an
// error: this is a projection, and Key is where a missing field has to be
// loud, because that one changes the identity of the row.
func Only(e Expander, fields ...string) Expander {
	return func(payload any) ([]any, error) {
		records, err := e(payload)
		if err != nil {
			return nil, err
		}

		out := make([]any, 0, len(records))
		for _, r := range records {
			obj, ok := r.(map[string]any)
			if !ok {
				out = append(out, r)
				continue
			}
			filtered := make(map[string]any, len(fields))
			for _, campo := range fields {
				if v, exists := obj[campo]; exists {
					filtered[campo] = v
				}
			}
			out = append(out, filtered)
		}
		return out, nil
	}
}
