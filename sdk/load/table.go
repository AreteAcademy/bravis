package load

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"cloud.google.com/go/bigquery"
	core "github.com/AreteAcademy/brevis/sdk/internal/core"
	"google.golang.org/api/googleapi"
)

// prepareTable makes sure the destination is ready to receive the batch, and
// reports whether the table already existed.
//
// The SDK does not know your schema -- the payload is yours -- so it cannot
// write a CREATE for you. Two ways to get one anyway:
//
//   - Schema: the columns with a type on each one, and the SDK writes the DDL
//   - CreateSQL: your DDL, run once when the table is absent
//
// CreateTable with neither is an error naming what is missing. It used to fall
// back to BigQuery's autodetect, and that was the last place in this SDK where
// a type came from the data instead of from a declaration -- the invariant I2
// of plan/2026-09-03-sdk-schema-declarado.md. What it cost is not theoretical:
// the type of a column came from the FIRST batch, so a field that arrived whole
// today and fractional tomorrow changed the column's type with nobody writing
// anything.
//
// It never alters a table that already exists. A loader that can ALTER or
// DROP is a loader that can erase history.
func (l *Loader) prepareTable(ctx context.Context, table *bigquery.Table, data []byte, prov provenance) (bool, error) {
	_, err := table.Metadata(ctx)
	if err == nil {
		return true, nil
	}
	if !isNotFound(err) {
		return false, fmt.Errorf("looking up %s: %w", nameOf(table), err)
	}

	if !l.cfg.CreateTable {
		return false, fmt.Errorf("table %s does not exist. Set CreateTable to let the SDK "+
			"create it, or create it yourself", nameOf(table))
	}

	comoCriar, err := PlanoDeCriacao(l.cfg, nameOf(table))
	if err != nil {
		return false, err
	}
	if comoCriar == CriarPorSQL {
		return false, l.createFromSQL(ctx, table)
	}
	return false, l.createFromSchema(ctx, table, prov)
}

// ComoCriar diz de onde sai a forma da tabela.
type ComoCriar int

const (
	// CriarPorSQL roda o DDL do consumidor.
	CriarPorSQL ComoCriar = iota
	// CriarPorSchema monta o DDL a partir da declaracao tipada.
	CriarPorSchema
)

// PlanoDeCriacao decide como criar a tabela, ou recusa.
//
// Funcao PURA, e exportada, pelo motivo que este SDK ja pagou uma vez: uma
// decisao tomada dentro de um metodo com cliente nunca e vista por um teste.
// O mergeSQL e o reconcile existem por isso, e o invariante I2 -- "o SDK nunca
// infere schema" -- so vira propriedade verificavel se der para exercita-lo
// sem um projeto do BigQuery.
func PlanoDeCriacao(cfg *core.LoadConfig, tabela string) (ComoCriar, error) {
	if cfg.CreateSQL != "" {
		return CriarPorSQL, nil
	}
	if len(cfg.Schema) > 0 {
		return CriarPorSchema, nil
	}
	return 0, fmt.Errorf("table %s does not exist and CreateTable is set, but nothing says "+
		"what type each column is. Declare Target.Schema -- the same list as Columns, with a "+
		"Type on each entry -- or pass CreateSQL with your own DDL. The SDK does not infer: a "+
		"type taken from the first batch changes the day a field arrives whole instead of "+
		"fractional, and nobody writes anything", tabela)
}

// createFromSchema cria a tabela a partir da declaracao, e de nada mais.
func (l *Loader) createFromSchema(ctx context.Context, table *bigquery.Table, prov provenance) error {
	esquema, err := bigquerySchema(l.cfg.Schema)
	if err != nil {
		return err
	}
	meta := typedTable(l.cfg, esquema, prov)
	if err := table.Create(ctx, meta); err != nil {
		return fmt.Errorf("creating %s: %w", nameOf(table), err)
	}
	return nil
}

// bigquerySchema traduz a declaracao para o dialeto do BigQuery.
//
// A tabela e curta e escrita, e nao um mapeamento esperto: quem precisa de
// NUMERIC(18,2), de um REPEATED ou de um RECORD escreve o DDL em CreateSQL,
// que continua existindo exatamente para isso.
func bigquerySchema(s core.Schema) (bigquery.Schema, error) {
	tipos := map[core.ColumnType]bigquery.FieldType{
		core.TypeString:    bigquery.StringFieldType,
		core.TypeInt64:     bigquery.IntegerFieldType,
		core.TypeFloat64:   bigquery.FloatFieldType,
		core.TypeNumeric:   bigquery.NumericFieldType,
		core.TypeBool:      bigquery.BooleanFieldType,
		core.TypeTimestamp: bigquery.TimestampFieldType,
		core.TypeDate:      bigquery.DateFieldType,
		core.TypeJSON:      bigquery.JSONFieldType,
		core.TypeBytes:     bigquery.BytesFieldType,
	}

	out := make(bigquery.Schema, 0, len(s))
	for _, c := range s {
		// As duas colunas do SDK tem forma propria, e ela vence a declaracao:
		// ingestion_id e ingestion_loaded_at sao dele, e um NULLABLE ali
		// deixaria a dedup casar com nulo.
		if propria, minha := metadataSchema[c.Name]; minha {
			out = append(out, propria)
			continue
		}
		t, conhecido := tipos[c.Type]
		if !conhecido {
			return nil, fmt.Errorf("column %q has type %q, which BigQuery has no equivalent for",
				c.Name, c.Type)
		}
		out = append(out, &bigquery.FieldSchema{
			Name: c.Name, Type: t, Required: c.Required,
		})
	}
	return out, nil
}

// createFromSQL runs the caller's DDL and confirms it produced the table the
// load is about to write to.
//
// Running someone's statement and trusting it would move the failure to the
// load, where the error is about a missing column rather than about the DDL
// that forgot it.
func (l *Loader) createFromSQL(ctx context.Context, table *bigquery.Table) error {
	job, err := l.bq.Query(l.cfg.CreateSQL).Run(ctx)
	if err != nil {
		return fmt.Errorf("running CreateSQL: %w", err)
	}

	status, err := job.Wait(ctx)
	if err != nil {
		return fmt.Errorf("waiting for CreateSQL: %w", err)
	}
	if err := status.Err(); err != nil {
		return fmt.Errorf("CreateSQL failed: %w", err)
	}

	if _, err := table.Metadata(ctx); err != nil {
		if isNotFound(err) {
			return fmt.Errorf("CreateSQL ran without error but %s still does not exist; "+
				"does the statement name that table?", nameOf(table))
		}
		return fmt.Errorf("checking what CreateSQL produced: %w", err)
	}

	return nil
}

// applyLayout configures a load job that may create the table.
//
// Partitioning is the one layout decision the SDK can make on its own, and
// only with Metadata: ingestion_loaded_at is the only column it knows
// exists. An unpartitioned landing table costs a full scan on every MERGE the
// bronze layer runs -- measured on a consumer, one entity spent 58.96 GiB of
// MERGE against 0.0 GiB of SELECT.
//
// Clustering has to be named: the SDK does not know your payload.
func (l *Loader) applyLayout(loader *bigquery.Loader, file *bigquery.FileConfig) {
	if !l.cfg.CreateTable {
		loader.CreateDisposition = bigquery.CreateNever
		return
	}

	// The load job creates the table only when nobody else did. CreateSQL and
	// the typed-metadata path both create it first, and pointing autodetect
	// at a table that already has a schema is how a REQUIRED column gets
	// relaxed back to NULLABLE -- BigQuery refuses outright, which is the
	// good outcome, but it refuses the whole load.
	if l.cfg.CreateSQL != "" || typesAnything(l.cfg.Columns) {
		loader.CreateDisposition = bigquery.CreateNever
		return
	}

	loader.CreateDisposition = bigquery.CreateIfNeeded
	file.AutoDetect = true

	if len(l.cfg.ClusterBy) > 0 {
		loader.Clustering = &bigquery.Clustering{Fields: l.cfg.ClusterBy}
	}
}

// describeTable attaches a description and labels once the table exists.
//
// Best effort: a table that loaded fine must not be reported as a failure
// because a label did not stick. It answers "what writes here?" six months
// later, which is worth attempting and not worth failing over.
func (l *Loader) describeTable(ctx context.Context, table *bigquery.Table, prov provenance) {
	md, err := table.Metadata(ctx)
	if err != nil {
		return
	}
	if md.Description != "" || len(md.Labels) > 0 {
		return // someone already said something; leave it
	}

	update := bigquery.TableMetadataToUpdate{Description: tableDescription(l.cfg, prov)}
	for k, v := range tableLabels(prov) {
		update.SetLabel(k, v)
	}

	if _, err := table.Update(ctx, update, md.ETag); err != nil {
		return
	}
}

// provenance labels the created table, for cost attribution and for answering
// "what writes here?" six months later.
//
// It comes from the batch, not from configuration. There is no second place
// for a fetcher to say it, and a second place would be a second chance for the
// two to disagree.
type provenance struct{ Provider, Entity string }

func provenanceOf(records []core.Envelope) provenance {
	if len(records) == 0 {
		return provenance{}
	}

	// From the row's own provider and entity columns when it has them: that
	// is where a fetcher composes them, and reading them anywhere else would
	// be a second place for the two to disagree.
	if row, err := core.AsObject(records[0].Payload); err == nil {
		if p, e := text(row["provider"]), text(row["entity"]); p != "" || e != "" {
			return provenance{Provider: p, Entity: e}
		}
	}

	// The low-level API hands envelopes with provenance on them instead.
	return provenance{Provider: records[0].Provider, Entity: records[0].Entity}
}

func text(v any) string {
	s, _ := v.(string)
	return s
}

func tableDescription(cfg *core.LoadConfig, prov provenance) string {
	who := "the Brevis SDK"
	if prov.Provider != "" && prov.Entity != "" {
		who = fmt.Sprintf("%s/%s via the Brevis SDK", prov.Provider, prov.Entity)
	}
	if declares(cfg.Columns, core.MetadataID) {
		return fmt.Sprintf("Written by %s since %s. Rows carry ingestion_id; deduplicate "+
			"on it downstream. The SDK never alters this table.",
			who, time.Now().UTC().Format("2006-01-02"))
	}
	return fmt.Sprintf("Written by %s since %s. The SDK never alters this table.",
		who, time.Now().UTC().Format("2006-01-02"))
}

// tableLabels attach the source to the table for cost attribution in billing.
//
// BigQuery takes lowercase letters, digits, dashes and underscores, up to 63
// characters, starting with a letter. A value that does not fit is dropped
// rather than failing: a naming rule is not worth losing the load over.
func tableLabels(prov provenance) map[string]string {
	labels := map[string]string{}
	for key, raw := range map[string]string{"provider": prov.Provider, "entity": prov.Entity} {
		if v := sanitiseLabel(raw); v != "" {
			labels[key] = v
		}
	}
	return labels
}

var labelAllowed = regexp.MustCompile(`[^a-z0-9_-]+`)

func sanitiseLabel(v string) string {
	v = labelAllowed.ReplaceAllString(strings.ToLower(v), "_")
	v = strings.Trim(v, "_-")
	if len(v) > 63 {
		v = v[:63]
	}
	if v == "" || v[0] < 'a' || v[0] > 'z' {
		return ""
	}
	return v
}

func nameOf(t *bigquery.Table) string {
	return fmt.Sprintf("%s.%s", t.DatasetID, t.TableID)
}

// isNotFound distinguishes "the table is not there" from "we could not ask" --
// creating a table because of a permissions blip would be wrong.
func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	var apiErr *googleapi.Error
	if errors.As(err, &apiErr) {
		return apiErr.Code == 404
	}
	return false
}

// The declared shape of the two columns the SDK writes. Everything else in
// the table is the caller's, and its types come from BigQuery.
var metadataSchema = map[string]*bigquery.FieldSchema{
	core.MetadataID:       {Name: core.MetadataID, Type: bigquery.StringFieldType, Required: true},
	core.MetadataLoadedAt: {Name: core.MetadataLoadedAt, Type: bigquery.TimestampFieldType, Required: true},
}

// typesAnything reports whether the declaration names a column the SDK knows
// the shape of.
//
// This is the whole trigger for the typed-creation path, and it is the
// caller's own list -- which is what keeps it from being a default deciding
// the table's shape without appearing in the fetcher. Declare the column, get
// the guarantee; declare nothing, and autodetect infers everything nullable.
func typesAnything(columns []string) bool {
	for _, c := range columns {
		if _, mine := metadataSchema[c]; mine {
			return true
		}
	}
	return false
}

// typedTable is the destination's declaration: the caller's columns as
// BigQuery typed them, with the SDK's two overridden to their declared shape,
// plus the layout.
//
// Pure, so a test can read the schema this produces without a BigQuery
// client -- which is where the NOT NULL either survives or quietly does not.
func typedTable(cfg *core.LoadConfig, inferred bigquery.Schema, prov provenance) *bigquery.TableMetadata {
	schema := make(bigquery.Schema, 0, len(inferred))
	for _, f := range inferred {
		if own, mine := metadataSchema[f.Name]; mine {
			schema = append(schema, own)
			continue
		}
		schema = append(schema, f)
	}

	meta := &bigquery.TableMetadata{
		Schema:      schema,
		Description: tableDescription(cfg, prov),
		Labels:      tableLabels(prov),
		TimePartitioning: &bigquery.TimePartitioning{
			Type: bigquery.DayPartitioningType,
			// Declarada quando o consumidor declara; senao, a coluna que diz
			// quando a linha foi escrita -- que e por onde uma landing e lida
			// quase sempre, e a unica que o SDK sabe que existe.
			Field:                  particaoDe(cfg),
			Expiration:             cfg.PartitionExpiration,
			RequirePartitionFilter: cfg.RequirePartitionFilter,
		},
	}
	if len(cfg.ClusterBy) > 0 {
		meta.Clustering = &bigquery.Clustering{Fields: cfg.ClusterBy}
	}
	return meta
}

// particaoDe resolve a coluna de particionamento.
func particaoDe(cfg *core.LoadConfig) string {
	if cfg.PartitionBy != "" {
		return cfg.PartitionBy
	}
	return core.MetadataLoadedAt
}
