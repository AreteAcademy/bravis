package sdk

import (
	"fmt"
	"strconv"
	"time"

	core "github.com/AreteAcademy/bravis/sdk/internal/core"
)

// Target says where records land.
//
// The columns come from Transform: whatever shape your transformers compose
// is exactly what is written. The SDK adds nothing on its own, so the common
// case is one line:
//
//	sdk.Target{Table: "hourly_temperatures"}
//
// The one exception is Metadata, which adds two named columns and is opt-in.
// See Metadata.
type Target struct {
	// Driver selects the destination. Empty means DriverBigQuery, the only
	// one implemented today.
	//
	// Not to be confused with Provider: Driver is which system receives the
	// rows, Provider is which vendor the data came from. Provider feeds
	// ingestion_id; Driver does not.
	Driver core.Driver

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

	// CreateTable lets the SDK create the destination table when it is
	// absent. It never alters one that already exists.
	//
	// Three states, because two are not enough:
	//
	//	nil            you did not say. Inside Bravis the engine decides:
	//	               it creates on the step's first successful run, or
	//	               when the run was dispatched with create_table=true.
	//	               Outside Bravis, nothing is created.
	//	sdk.Bool(true)  always create when absent
	//	sdk.Bool(false) never create, and the engine does not override it
	//
	// A plain bool cannot carry this: its zero value would mean both "I do
	// not want a table" and "I said nothing", and the engine would have no
	// way to tell them apart. An explicit refusal has to win, or the same
	// code would behave differently inside and outside the engine with
	// nothing to warn you.
	CreateTable *bool

	// CreateSQL is your DDL, run instead of the built-in schema. The SDK
	// still checks afterwards that the table it produced can take the rows
	// being written.
	CreateSQL string

	// PartitionExpiration drops partitions older than this. Zero keeps them
	// forever, which is the default.
	PartitionExpiration time.Duration

	// RequirePartitionFilter makes BigQuery reject a query that does not
	// filter on the partition column, which stops an accidental full scan.
	// Incompatible with DedupMerge -- see the field on LoadConfig for why.
	RequirePartitionFilter bool

	// Metadata adds ingestion_id and ingestion_loaded_at, and is the only
	// thing the SDK writes that Transform did not compose. Nil adds nothing.
	//
	// It also carries the provenance those two are built from, so declaring
	// it is what makes Provider, Entity and Key necessary -- and the only
	// reason the SDK reads your record at all. See Metadata.
	//
	// Required by DedupMerge, which matches on ingestion_id, and by the
	// partition options, which partition on ingestion_loaded_at.
	Metadata *Metadata

	// ClusterBy names the columns a created table is clustered on. The SDK
	// cannot guess: it does not know your payload.
	ClusterBy []string
}

// defaultTable is the landing naming convention: vendors_<provider>_<entity>s.
//
// Empty when either half is missing. Provider and Entity are optional without
// a Metadata block, and "vendors__s" is not a table name -- it is two missing
// values pretending to be one.
func (d Target) defaultTable() string {
	if d.Metadata == nil || d.Metadata.Provider == "" || d.Metadata.Entity == "" {
		return ""
	}
	return fmt.Sprintf("vendors_%s_%ss", d.Metadata.Provider, d.Metadata.Entity)
}

// resolve turns a Target into a LoadConfig, applying the documented
// precedence and reporting where each value came from.
// resolveWith turns a Target into a LoadConfig, applying the documented
// precedence and reporting where each value came from.
//
// Precedence: what you set explicitly, then what the engine injected, then
// the environment, then the SDK default, then an error.
func (d Target) resolveWith(run RunContext) (*core.LoadConfig, map[string]origin, error) {
	// The provenance lives inside Metadata, so it is validated exactly when
	// there is something to build out of it. Without the block the record is
	// written as Transform composed it, and the SDK has no reason to know how
	// to read it.
	if d.Metadata != nil {
		if err := d.Metadata.validate(); err != nil {
			return nil, nil, err
		}
	}
	switch d.Driver {
	case "", core.DriverBigQuery:
		d.Driver = core.DriverBigQuery
	default:
		return nil, nil, fmt.Errorf("load driver %q is not implemented; use %q",
			d.Driver, core.DriverBigQuery)
	}

	projeto := resolve(d.Project, EnvProject, "")
	if projeto.value == "" {
		return nil, nil, fmt.Errorf("project not set: pass Target.Project or define %s", EnvProject)
	}

	dataset := resolve(d.Dataset, EnvDataset, "landing")
	table := resolve(d.Table, "", d.defaultTable())
	if table.value == "" {
		return nil, nil, fmt.Errorf("table not set: pass Target.Table, or a Metadata block " +
			"whose Provider and Entity give the default name vendors_<provider>_<entity>s")
	}
	bucket := resolve(d.StagingBucket, EnvBucket, projeto.value+"-bravis-staging")

	limite := d.InlineLimit
	if limite == 0 {
		limite = envInt("BRAVIS_SDK_LIMITE_INLINE", 5000)
	}

	create, createOrigin := d.resolveCreate(run)

	cfg := &core.LoadConfig{
		Driver:                 d.Driver,
		ProjectID:              projeto.value,
		Dataset:                dataset.value,
		Table:                  table.value,
		StagingBucket:          bucket.value,
		ThresholdForGCS:        limite,
		Format:                 "ndjson",
		Dedup:                  d.Dedup,
		Metadata:               d.Metadata != nil,
		ClusterBy:              d.ClusterBy,
		CreateTable:            create,
		CreateSQL:              d.CreateSQL,
		PartitionExpiration:    d.PartitionExpiration,
		RequirePartitionFilter: d.RequirePartitionFilter,
	}
	if d.Metadata != nil {
		cfg.Provider = d.Metadata.Provider
		cfg.Entity = d.Metadata.Entity
	}

	return cfg, map[string]origin{
		"project":      projeto,
		"dataset":      dataset,
		"table":        table,
		"bucket":       bucket,
		"create_table": createOrigin,
	}, nil
}

// resolve is resolveWith for a caller with no engine context.
func (d Target) resolve() (*core.LoadConfig, map[string]origin, error) {
	return d.resolveWith(RunContext{Params: map[string]string{}})
}

// resolveCreate settles the tri-state, and says where the answer came from.
//
// "Why did it create the table?" is a question someone will ask at three in
// the morning, and the log has to answer it without a rebuild.
func (d Target) resolveCreate(run RunContext) (bool, origin) {
	if d.CreateTable != nil {
		return *d.CreateTable, origin{strconv.FormatBool(*d.CreateTable), "explicit"}
	}

	switch {
	case run.First:
		return true, origin{"true", "the engine: first run of this step"}
	case run.Params[ParamCreateTable] == "true":
		return true, origin{"true", "the engine: " + ParamCreateTable + "=true"}
	}

	return false, origin{"false", "default"}
}

// Result describes what actually happened, end to end. Printing it is
// meant to be the whole of a fetcher's observability:
//
//	log.Info("pronto", res.Args()...)
type Result struct {
	// Extract
	Records      int64 // records that came out of extract, after expansion
	Pages        int   // pages fetched
	Attempts     int   // HTTP attempts spent, retries included
	ExtractBytes int64 // bytes read off the wire, before Transform
	ExtractTime  time.Duration

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
		"extract_bytes", r.ExtractBytes,
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
