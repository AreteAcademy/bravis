package sdk

import (
	"fmt"
	"time"

	core "github.com/AreteAcademy/bravis/sdk/internal/core"
)

// Target says where records land and how to identify one.
//
// Everything except Provider and Entity has a default or reads the
// environment, so the common case is four lines:
//
//	sdk.Target{
//		Provider: "open_meteo",
//		Entity:   "hourly_temperature",
//		Key:    sdk.Key("latitude", "longitude", "time"),
//		When:   sdk.Field("time"),
//	}
type Target struct {
	// Driver selects the destination. Empty means DriverBigQuery, the only
	// one implemented today.
	//
	// Not to be confused with Provider: Driver is which system receives the
	// rows, Provider is which vendor the data came from. Provider feeds
	// ingestion_id; Driver does not.
	Driver core.Driver

	// Provider and Entity identify the source. They feed ingestion_id and the
	// default table name, so they are fixed once and not changed after.
	Provider string
	Entity   string

	// Key builds source_key from each payload. Required: without it there
	// is no stable identity, and ingestion_id would vary between runs.
	Key KeySelector

	// When reads the record's own timestamp from the payload. Defaults to
	// Now(), which stamps the run time -- fine for a source with no
	// timestamp, but it makes ingestion_id vary between runs, so the same
	// reading will not deduplicate.
	When FieldSelector

	// Project, Dataset and Table default from the environment; see the Env
	// constants. Table defaults to vendors_<provider>_<entity>s.
	Project string
	Dataset string
	Table   string

	// StagingBucket is used above InlineLimit rows. Defaults to
	// <projeto>-bravis-staging.
	StagingBucket string

	// InlineLimit is the row count above which the load stages through GCS.
	// Zero uses the SDK default of 5000.
	InlineLimit int

	// Dedup selects deduplication. Zero value appends, which is free;
	// DedupMerge costs one scan of the destination per load, so it is never
	// enabled on your behalf.
	Dedup core.Dedup

	// NoCreateTable stops the SDK from creating the landing table. By
	// default it creates one with the six-column contract, partitioned by
	// ingestion_loaded_at and clustered by provider and entity. It never
	// alters an existing table.
	NoCreateTable bool

	// RawPayload writes the payload flat instead of wrapping it in the six
	// landing columns. Turning this on also turns off table creation, since
	// the SDK then does not know the schema.
	RawPayload bool
}

// defaultTable is the landing naming convention: vendors_<provider>_<entity>s.
func (d Target) defaultTable() string {
	return fmt.Sprintf("vendors_%s_%ss", d.Provider, d.Entity)
}

// resolve turns a Target into a LoadConfig, applying the documented
// precedence and reporting where each value came from.
func (d Target) resolve() (*core.LoadConfig, map[string]origin, error) {
	if d.Provider == "" {
		return nil, nil, fmt.Errorf("Target.Provider is required: it feeds ingestion_id")
	}
	if d.Entity == "" {
		return nil, nil, fmt.Errorf("Target.Entity is required: it feeds ingestion_id")
	}
	switch d.Driver {
	case "", core.DriverBigQuery:
		d.Driver = core.DriverBigQuery
	default:
		return nil, nil, fmt.Errorf("load driver %q is not implemented; use %q",
			d.Driver, core.DriverBigQuery)
	}

	if d.Key == nil {
		return nil, nil, fmt.Errorf("Target.Key is required: without it there is no stable source_key, " +
			"and ingestion_id would change on every run")
	}

	projeto := resolve(d.Project, EnvProject, "")
	if projeto.value == "" {
		return nil, nil, fmt.Errorf("project not set: pass Target.Project or define %s", EnvProject)
	}

	dataset := resolve(d.Dataset, EnvDataset, "landing")
	table := resolve(d.Table, "", d.defaultTable())
	bucket := resolve(d.StagingBucket, EnvBucket, projeto.value+"-bravis-staging")

	limite := d.InlineLimit
	if limite == 0 {
		limite = envInt("BRAVIS_SDK_LIMITE_INLINE", 5000)
	}

	cfg := &core.LoadConfig{
		Driver:               d.Driver,
		ProjectID:            projeto.value,
		Dataset:              dataset.value,
		Table:                table.value,
		StagingBucket:        bucket.value,
		ThresholdForGCS:      limite,
		Format:               "ndjson",
		DeleteAfterLoad:      true,
		Dedup:                d.Dedup,
		WriteEnvelopeColumns: !d.RawPayload,
		CreateTable:          !d.NoCreateTable && !d.RawPayload,
	}

	return cfg, map[string]origin{
		"projeto": projeto,
		"dataset": dataset,
		"table":   table,
		"bucket":  bucket,
	}, nil
}

// Result describes what actually happened, end to end. Printing it is
// meant to be the whole of a fetcher's observability:
//
//	log.Info("pronto", res.Args()...)
type Result struct {
	// Extract
	Records     int64 // records that came out of extract, after expansion
	Pages       int   // pages fetched
	Attempts    int   // HTTP attempts spent, retries included
	ExtractTime time.Duration

	// Load
	Rows         int64      // rows written
	Ignored      int64      // rows deduplication matched as already present
	Bytes        int64      // bytes in the staged format
	Strategy     string     // "inline" or "gcs"
	Format       string     // the format actually written
	Dedup        core.Dedup // the deduplication that actually ran
	TableCreated bool       // whether this run created the table
	Table        string     // dataset.table written to
	LoadTime     time.Duration

	// Diagnostics BigQuery reported per row, when it refused any.
	RowErrors []string

	Duration time.Duration
}

// Args renders the result as slog key-value pairs.
func (r *Result) Args() []any {
	return []any{
		"records", r.Records,
		"lines", r.Rows,
		"ignored", r.Ignored,
		"paginas", r.Pages,
		"attempts", r.Attempts,
		"bytes", r.Bytes,
		"table", r.Table,
		"estrategia", r.Strategy,
		"formato", r.Format,
		"dedup", r.Dedup,
		"tabela_criada", r.TableCreated,
		"extract", r.ExtractTime,
		"load", r.LoadTime,
		"duracao", r.Duration,
	}
}

func (r *Result) String() string {
	return fmt.Sprintf("%d records -> %d lines (%d ignored) em %s via %s, dedup %s, %s",
		r.Records, r.Rows, r.Ignored, r.Table, r.Strategy, r.Dedup, r.Duration)
}
