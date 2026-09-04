package load

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"cloud.google.com/go/bigquery"
	core "github.com/AreteAcademy/bravis/sdk/internal/core"
)

// checkColumns confirms the row matches the declaration, in both directions.
//
// Strict on purpose, and this is the half that makes the declaration worth
// writing. If a declared column could quietly be absent, the list would drift
// from the table it claims to describe and nothing would say so -- which is
// the state this check exists to end.
//
//   - a declared column that neither Transform nor Metadata delivered is an
//     error naming the column. It would land NULL, and a column that is NULL
//     because nobody filled it looks exactly like one that is NULL on purpose.
//   - a field the row carries that the declaration does not list is an error
//     naming the field. Dropping data in silence is the worst way to fail.
//
// Checked on the first record. Every record in a batch comes out of the same
// Transform chain, so a second one that differed would be caught by the load
// itself; checking all of them would cost a full scan to say the same thing.
func checkColumns(declared []string, envelopes []core.Envelope) error {
	if len(declared) == 0 || len(envelopes) == 0 {
		return nil
	}

	row, err := asObject(envelopes[0].Payload)
	if err != nil {
		return err
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
			"Compose them in Transform, or declare a Metadata block if they are "+
			"ingestion_id and ingestion_loaded_at. The row has: %s",
			strings.Join(missing, ", "), keysOf(row))
	}
	if len(undeclared) > 0 {
		sort.Strings(undeclared)
		return fmt.Errorf("the row carries %s, which Columns does not declare. "+
			"They would be written to a table that never mentioned them, so the "+
			"load stops here: add them to Columns, or drop them in Transform",
			strings.Join(undeclared, ", "))
	}

	return nil
}

// checkDeclaredAgainstTable confirms the declaration describes the table that
// is actually there.
//
// Asymmetric, the same way reconcile is and for the same reason: a declared
// column the table lacks is a load that cannot work, while a table column the
// declaration omits stays NULL, which a landing table legitimately does.
func checkDeclaredAgainstTable(declared []string, schema bigquery.Schema, table string) error {
	if len(declared) == 0 {
		return nil
	}

	has := make(map[string]bool, len(schema))
	for _, f := range schema {
		has[f.Name] = true
	}

	var absent []string
	for _, c := range declared {
		if !has[c] {
			absent = append(absent, c)
		}
	}
	if len(absent) == 0 {
		return nil
	}

	sort.Strings(absent)
	return fmt.Errorf("the Columns declaration lists %s, which %s does not have. The table has: %s",
		strings.Join(absent, ", "), table, namesOf(schema))
}

// asObject is the record as a map, whatever shape it arrived in.
func asObject(payload any) (map[string]any, error) {
	if m, ok := payload.(map[string]any); ok {
		return m, nil
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("the record does not encode to JSON: %w", err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("the Columns declaration can only be checked against a JSON object; "+
			"this record is %s", truncate(data, 80))
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
