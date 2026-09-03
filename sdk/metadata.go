package sdk

// Metadata asks the SDK to add exactly two fields to every record:
//
//	ingestion_id         deterministic UUID v5 over the provenance below
//	ingestion_loaded_at  when the row was written, RFC 3339
//
// Two columns, and only ever those two. They are named here, at the call
// site, so that no column ever appears in your table without being written in
// your fetcher.
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
	// Provider and Entity identify the source, and feed ingestion_id. Both
	// required.
	Provider string
	Entity   string

	// Key builds source_key from each record, after Transform. Required:
	// without it there is no stable identity, and ingestion_id would change
	// on every run.
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
	switch {
	case m.Provider == "":
		return errRequired("Metadata.Provider", "it feeds ingestion_id")
	case m.Entity == "":
		return errRequired("Metadata.Entity", "it feeds ingestion_id")
	case m.Key == nil:
		return errRequired("Metadata.Key", "without it there is no stable source_key, "+
			"and ingestion_id would change on every run")
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
