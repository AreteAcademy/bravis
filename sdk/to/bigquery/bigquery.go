// Package bigquery writes to BigQuery.
//
// It lives in its own package because it carries the Google client, and Go
// prunes dependencies by package imported: a fetcher that only writes files
// must not compile this. That is the rule for every driver with a vendor SDK
// behind it.
package bigquery

import (
	"context"
	"fmt"
	"strconv"
	"time"

	core "github.com/AreteAcademy/brevis/sdk/internal/core"
	"github.com/AreteAcademy/brevis/sdk/load"
)

// Table writes to a BigQuery table.
//
//	To: bigquery.Table{
//		Dataset: "bronze",
//		Name:    "vendors_open_meteo_hourly_temperatures",
//	}
//
// Project, Dataset and StagingBucket read the environment when left empty;
// see the Env constants. Everything here is BigQuery's -- partitioning,
// clustering, the GCS staging threshold -- and none of it appears on a
// destination that has no such thing.
type Table struct {
	// Project, Dataset and Name. Project and Dataset default from the
	// environment; Name has no default.
	Project string
	Dataset string
	Name    string

	// StagingBucket is used above InlineLimit rows. Defaults to
	// <project>-brevis-staging.
	StagingBucket string

	// InlineLimit is the row count above which the load stages through GCS.
	// Zero uses the SDK default of 5000.
	InlineLimit int

	// CreateTable lets the SDK create the table when it is absent. It never
	// alters one that already exists.
	//
	// Three states, because two are not enough: nil means you did not say and
	// the engine decides (first successful run of the step, or a dispatch
	// carrying create_table=true); sdk.Bool(true) always creates; sdk.Bool(false)
	// refuses, and the engine does not override it.
	CreateTable *bool

	// CreateSQL is your DDL, run instead of the built-in schema.
	CreateSQL string

	// ClusterBy names the columns a created table is clustered on.
	ClusterBy []string

	// PartitionExpiration drops partitions older than this. Zero keeps them.
	PartitionExpiration time.Duration

	// RequirePartitionFilter makes BigQuery reject a query that does not
	// filter on the partition column. Incompatible with DedupMerge.
	RequirePartitionFilter bool

	// StagingPrefix is where staged objects go inside the bucket. Empty uses
	// "extracts/".
	StagingPrefix string

	// KeepStagedFile leaves the staged object in the bucket after a load.
	KeepStagedFile bool
}

// Write satisfies core.Writer.
func (b Table) Write(ctx context.Context, records []core.Envelope, opt core.WriteOptions) (*core.LoadResult, error) {
	cfg, origins, err := b.config(opt)
	if err != nil {
		return nil, err
	}
	core.LogResolution(ctx, origins)

	loader, err := load.New(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return loader.Load(ctx, records...)
}

// Describe satisfies core.Writer.
func (b Table) Describe() string {
	dataset := core.Resolve(b.Dataset, core.EnvDataset, "landing").Value
	return fmt.Sprintf("%s.%s", dataset, b.Name)
}

// config applies the documented precedence -- what you set, then the engine,
// then the environment, then the default, then an error -- and reports where
// each value came from, because "why did it write there?" is a question
// somebody asks at three in the morning.
func (b Table) config(opt core.WriteOptions) (*core.LoadConfig, map[string]core.Origin, error) {
	project := core.Resolve(b.Project, core.EnvProject, "")
	if project.Value == "" {
		return nil, nil, fmt.Errorf("project not set: pass bigquery.Table.Project or define %s",
			core.EnvProject)
	}

	dataset := core.Resolve(b.Dataset, core.EnvDataset, "landing")
	table := core.Resolve(b.Name, "", "")
	if table.Value == "" {
		return nil, nil, fmt.Errorf("table not set: pass bigquery.Table.Name")
	}
	bucket := core.Resolve(b.StagingBucket, core.EnvBucket, project.Value+"-brevis-staging")

	limit := b.InlineLimit
	if limit == 0 {
		limit = core.EnvInt("BREVIS_SDK_LIMITE_INLINE", 5000)
	}

	create, createOrigin := b.resolveCreate(opt.Run)

	cfg := &core.LoadConfig{
		ProjectID:              project.Value,
		Dataset:                dataset.Value,
		Table:                  table.Value,
		StagingBucket:          bucket.Value,
		StagingPrefix:          b.StagingPrefix,
		ThresholdForGCS:        limit,
		Format:                 "ndjson",
		Columns:                opt.Columns,
		Schema:                 opt.Schema,
		PartitionBy:            opt.PartitionBy,
		Dedup:                  opt.Dedup,
		ClusterBy:              b.ClusterBy,
		CreateTable:            create,
		CreateSQL:              b.CreateSQL,
		PartitionExpiration:    b.PartitionExpiration,
		RequirePartitionFilter: b.RequirePartitionFilter,
		KeepStagedFile:         b.KeepStagedFile,
	}

	return cfg, map[string]core.Origin{
		"project":      project,
		"dataset":      dataset,
		"table":        table,
		"bucket":       bucket,
		"create_table": createOrigin,
	}, nil
}

// resolveCreate settles the tri-state, and says where the answer came from.
func (b Table) resolveCreate(run core.RunContext) (bool, core.Origin) {
	if b.CreateTable != nil {
		return *b.CreateTable, core.Origin{
			Value: strconv.FormatBool(*b.CreateTable), Where: "explicit",
		}
	}
	switch {
	case run.First:
		return true, core.Origin{Value: "true", Where: "the engine: first run of this step"}
	case run.Params[core.ParamCreateTable] == "true":
		return true, core.Origin{Value: "true", Where: "the engine: " + core.ParamCreateTable + "=true"}
	}
	return false, core.Origin{Value: "false", Where: "default"}
}
