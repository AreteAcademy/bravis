package core

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

// The two fields a Metadata block adds. Only two: provider, entity and
// source_key are provenance the SDK uses to build the id, not columns it
// imposes.
const (
	MetadataID       = "ingestion_id"
	MetadataLoadedAt = "ingestion_loaded_at"
)

var metadataFields = []string{MetadataID, MetadataLoadedAt}

// StampMetadata adds ingestion_id and ingestion_loaded_at to every record,
// when the WriteOptions ask for them.
//
// It lives here, not in a driver, because the id has to be computed in exactly
// one place: a row written to Postgres and the same row written to BigQuery
// must carry the same ingestion_id, or nothing downstream can reconcile them.
//
// Returns a copy. `Write(ctx, batch, opt)` hands the driver the caller's own
// slice, so writing the metadata back into it would alter what they still
// hold -- loading the same batch twice then failed on the second try with
// "payload already has ingestion_id", which is exactly what a retry does.
func StampMetadata(records []Envelope, opt WriteOptions) ([]Envelope, error) {
	if !opt.Metadata {
		return records, nil
	}

	out := make([]Envelope, len(records))
	copy(out, records)

	for i := range out {
		id, err := ingestionID(&out[i], opt.AutoID)
		if err != nil {
			return nil, err
		}

		payload, err := AsObject(out[i].Payload)
		if err != nil {
			return nil, fmt.Errorf("a Metadata block adds two fields to the record, so it has "+
				"to be a JSON object: %w", err)
		}

		// Copy: the caller may still hold this map.
		stamped := make(map[string]any, len(payload)+len(metadataFields))
		for k, v := range payload {
			stamped[k] = v
		}

		var clashes []string
		for _, f := range metadataFields {
			if _, taken := stamped[f]; taken {
				clashes = append(clashes, f)
			}
		}
		if len(clashes) > 0 {
			return nil, fmt.Errorf("record %d already has the field(s) %s, which Metadata would "+
				"overwrite. Rename them in Transform, or drop the Metadata block",
				i, strings.Join(clashes, ", "))
		}

		stamped[MetadataID] = id
		stamped[MetadataLoadedAt] = time.Now().UTC().Format(time.RFC3339)
		out[i].Payload = stamped
	}

	return out, nil
}

// ingestionID is the one place that decides which id a row gets.
//
// Deterministic by default, so a re-run writes the same id for the same record
// and a merge can recognise it. Random with AutoID, which is a row identifier
// and nothing more.
func ingestionID(env *Envelope, auto bool) (string, error) {
	if auto {
		id, err := uuid.NewRandom()
		if err != nil {
			return "", fmt.Errorf("generating a random ingestion_id: %w", err)
		}
		return id.String(), nil
	}
	return env.IngestionID()
}

// CheckColumns confirms the row matches the declaration, in both directions.
//
// Strict on purpose, and this is the half that makes the declaration worth
// writing. If a declared column could quietly be absent, the list would drift
// from the table it claims to describe and nothing would say so.
//
//   - a declared column that neither Transform nor Metadata delivered is an
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
			"Compose them in Transform, or declare a Metadata block if they are "+
			"ingestion_id and ingestion_loaded_at. The row has: %s",
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
