package mysql

import (
	"encoding/json"
	"fmt"
	"time"
)

// paraColuna converte o valor do registro no que o driver aceita.
//
// Pelo mesmo motivo do Postgres: o registro do SDK e JSON, e um instante nele
// e uma STRING RFC 3339. O driver do MySQL manda a string como texto, e o
// servidor recusa com "Incorrect datetime value" -- ou pior, aceita e guarda
// zero, dependendo do sql_mode.
//
//	coluna                  aceita
//	datetime/timestamp      string RFC 3339, time.Time, ou epoch
//	date                    string YYYY-MM-DD ou RFC 3339
//	decimal/numeric         string, preservada
//	json                    serializado
//	o resto                 passa como veio
func paraColuna(v any, tipo string) (any, error) {
	if v == nil {
		return nil, nil
	}

	switch tipo {
	case "datetime", "timestamp":
		return paraInstante(v, tipo)

	case "date":
		t, err := paraInstante(v, tipo)
		if err != nil {
			return nil, err
		}
		if ts, ok := t.(time.Time); ok {
			return ts.Format("2006-01-02"), nil
		}
		return t, nil

	case "json":
		switch t := v.(type) {
		case string:
			return t, nil
		case []byte:
			return string(t), nil
		default:
			b, err := json.Marshal(v)
			if err != nil {
				return nil, fmt.Errorf("encoding as json: %w", err)
			}
			return string(b), nil
		}

	default:
		return v, nil
	}
}

func paraInstante(v any, tipo string) (any, error) {
	switch t := v.(type) {
	case time.Time:
		return t, nil
	case string:
		for _, layout := range []string{
			time.RFC3339Nano, time.RFC3339,
			"2006-01-02 15:04:05.999999999", "2006-01-02 15:04:05", "2006-01-02",
		} {
			if ts, err := time.Parse(layout, t); err == nil {
				return ts.UTC(), nil
			}
		}
		return nil, fmt.Errorf("%q is not a timestamp this column (%s) accepts: use RFC 3339, "+
			"as in 2026-09-05T12:30:00Z", elidir(t), tipo)
	case float64:
		return time.Unix(int64(t), 0).UTC(), nil
	case int64:
		return time.Unix(t, 0).UTC(), nil
	case int:
		return time.Unix(int64(t), 0).UTC(), nil
	default:
		return nil, fmt.Errorf("a %T cannot go into a %s column", v, tipo)
	}
}

func elidir(s string) string {
	if len(s) <= 40 {
		return s
	}
	return s[:37] + "…"
}
