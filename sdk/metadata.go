package sdk

import (
	"fmt"
	"strings"
)

// Metadata asks the SDK to add exactly two fields to every record:
//
//	ingestion_id         deterministic UUID v5 over the provenance below
//	ingestion_loaded_at  when the row was written, RFC 3339
//
// Two columns, and only ever those two:
//
//	ingestion_id         STRING    NOT NULL
//	ingestion_loaded_at  TIMESTAMP NOT NULL
//
// It is a switch for those two columns, not a place to put data. Nothing you
// write in this block becomes a column: Provider, Entity, Key and When are
// read to build the id and are never written. Columns come from Transform.
//
// They are named here, at the call site, so that no column ever appears in
// your table without being written in your fetcher.
//
// Everything else about the row comes from Transform. This block is the one
// exception, and it is opt-in:
//
//	Target: sdk.Target{
//		Table: "vendors_open_meteo_hourly_temperatures",
//		Metadata: &sdk.Metadata{
//			Provider: "open_meteo",
//			Entity:   "hourly_temperature",
//			Key:      sdk.Key("latitude", "longitude", "time"),
//			When:     sdk.Field("time"),
//		},
//	}
//
// A nil *Metadata is the default and adds nothing. The four fields below
// exist only to build ingestion_id -- they are provenance, never columns --
// which is why they live here and not on Target: without this block the SDK
// has no use for them and never reads your record.
type Metadata struct {
	// AutoID makes ingestion_id a fresh random UUID per row, and is the whole
	// declaration when you just want a row identifier:
	//
	//	Metadata: &sdk.Metadata{AutoID: true}
	//
	// Nothing else is needed, because nothing about the record goes into the
	// id. That is also what it costs: the same reading loaded twice gets two
	// different ids, so nothing downstream can tell the copies apart, and
	// DedupMerge is refused alongside it -- a merge on a random id matches
	// nothing and would write the duplicate it exists to prevent.
	//
	// Leave it off and ingestion_id is deterministic: the same record always
	// gets the same id, which is what makes a re-run safe. That is built from
	// the four fields below, and they become required.
	AutoID bool

	// Provider and Entity identify the source, and feed ingestion_id. Both
	// required unless AutoID is set.
	Provider string
	Entity   string

	// Key builds source_key from each record, after Transform. Required
	// unless AutoID is set: without it there is no stable identity, and
	// ingestion_id would change on every run.
	Key KeySelector

	// When reads the record's own timestamp. Defaults to Now(), which stamps
	// the run time -- fine for a source with no timestamp of its own, but it
	// makes ingestion_id vary between runs, so the same reading will not
	// deduplicate.
	When FieldSelector
}

// validate is called by Target.resolveWith. The errors name the field,
// because "provenance is incomplete" sends nobody anywhere.
func (m *Metadata) validate() error {
	if m.AutoID {
		// A field that is set and never read is the defect this SDK keeps
		// finding in itself. With AutoID nothing about the record reaches the
		// id, so provenance here would be exactly that: written, ignored, and
		// looking for all the world like it still made the id deterministic.
		var unused []string
		if m.Provider != "" {
			unused = append(unused, "Provider")
		}
		if m.Entity != "" {
			unused = append(unused, "Entity")
		}
		if m.Key != nil {
			unused = append(unused, "Key")
		}
		if m.When != nil {
			unused = append(unused, "When")
		}
		if len(unused) > 0 {
			return fmt.Errorf("Metadata.AutoID makes ingestion_id random, so %s would be "+
				"set and never read. Drop them, or drop AutoID to get the deterministic id "+
				"they build", strings.Join(unused, ", "))
		}
		return nil
	}

	switch {
	case m.Provider == "":
		return errRequired("Metadata.Provider", "it feeds ingestion_id")
	case m.Entity == "":
		return errRequired("Metadata.Entity", "it feeds ingestion_id")
	case m.Key == nil:
		return errRequired("Metadata.Key", "without it there is no stable source_key, "+
			"and ingestion_id would change on every run. Set AutoID for a random id instead")
	}
	return nil
}

func errRequired(field, why string) error {
	return &requiredError{field: field, why: why}
}

type requiredError struct {
	field string
	why   string
}

func (e *requiredError) Error() string {
	return e.field + " is required: " + e.why
}
