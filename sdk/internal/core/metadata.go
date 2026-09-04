package core

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// The names of the two columns sdk.IngestionID and sdk.IngestionLoadedAt
// write. They live here because the destination needs to recognise them: a
// declaration that names ingestion_loaded_at is what tells BigQuery to
// partition on it.
const (
	MetadataID       = "ingestion_id"
	MetadataLoadedAt = "ingestion_loaded_at"
)

// CheckColumns confirms the row matches the declaration, in both directions.
//
// Strict on purpose, and this is the half that makes the declaration worth
// writing. If a declared column could quietly be absent, the list would drift
// from the table it claims to describe and nothing would say so.
//
//   - a declared column the Transform chain did not deliver is an
//     error naming the column. It would land NULL, and a column that is NULL
//     because nobody filled it looks exactly like one that is NULL on purpose.
//   - a field the row carries that the declaration does not list is an error
//     naming the field. Dropping data in silence is the worst way to fail.
//
// Checked on the first record: every record in a batch comes out of the same
// Transform chain, so checking all of them would cost a full scan to say the
// same thing.
func CheckColumns(declared []string, records []Envelope) error {
	if len(declared) == 0 || len(records) == 0 {
		return nil
	}

	row, err := AsObject(records[0].Payload)
	if err != nil {
		return fmt.Errorf("the Columns declaration can only be checked against a JSON "+
			"object: %w", err)
	}

	want := make(map[string]bool, len(declared))
	var missing []string
	for _, c := range declared {
		want[c] = true
		if _, present := row[c]; !present {
			missing = append(missing, c)
		}
	}

	var undeclared []string
	for f := range row {
		if !want[f] {
			undeclared = append(undeclared, f)
		}
	}

	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("the Columns declaration lists %s, which the row does not have. "+
			"Compose them in Transform -- sdk.IngestionID() and sdk.IngestionLoadedAt() "+
			"write the two the SDK knows. The row has: %s",
			strings.Join(missing, ", "), keysOf(row))
	}
	if len(undeclared) > 0 {
		sort.Strings(undeclared)
		return fmt.Errorf("the row carries %s, which Columns does not declare. "+
			"They would be written to a destination that never mentioned them, so the "+
			"load stops here: add them to Columns, or drop them in Transform",
			strings.Join(undeclared, ", "))
	}

	return nil
}

// AsObject is the record as a map, whatever shape it arrived in.
func AsObject(payload any) (map[string]any, error) {
	if m, ok := payload.(map[string]any); ok {
		return m, nil
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("the record does not encode to JSON: %w", err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("this record is %s", truncate(data, 80))
	}
	return m, nil
}

func keysOf(row map[string]any) string {
	names := make([]string, 0, len(row))
	for k := range row {
		names = append(names, k)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

func truncate(b []byte, max int) string {
	if len(b) <= max {
		return string(b)
	}
	return string(b[:max]) + "..."
}
