package sdk

import (
	"fmt"
	"time"

	core "github.com/AreteAcademy/bravis/sdk/internal/core"
)

// Destino says where records land and how to identify one.
//
// Everything except Provider and Entity has a default or reads the
// environment, so the common case is four lines:
//
//	sdk.Destino{
//		Provider: "open_meteo",
//		Entity:   "hourly_temperature",
//		Chave:    sdk.Chave("latitude", "longitude", "time"),
//		Quando:   sdk.Campo("time"),
//	}
type Destino struct {
	// Provider and Entity identify the source. They feed ingestion_id and the
	// default table name, so they are fixed once and not changed after.
	Provider string
	Entity   string

	// Chave builds source_key from each payload. Required: without it there
	// is no stable identity, and ingestion_id would vary between runs.
	Chave SeletorDeChave

	// Quando reads the record's own timestamp from the payload. Defaults to
	// Agora(), which stamps the run time -- fine for a source with no
	// timestamp, but it makes ingestion_id vary between runs, so the same
	// reading will not deduplicate.
	Quando SeletorDeCampo

	// Projeto, Dataset and Tabela default from the environment; see the Env
	// constants. Tabela defaults to vendors_<provider>_<entity>s.
	Projeto string
	Dataset string
	Tabela  string

	// BucketDeStaging is used above LimiteInline rows. Defaults to
	// <projeto>-bravis-staging.
	BucketDeStaging string

	// LimiteInline is the row count above which the load stages through GCS.
	// Zero uses the SDK default of 5000.
	LimiteInline int

	// Dedup selects deduplication. Zero value appends, which is free;
	// DedupMerge costs one scan of the destination per load, so it is never
	// enabled on your behalf.
	Dedup core.Dedup

	// SemCriarTabela stops the SDK from creating the landing table. By
	// default it creates one with the six-column contract, partitioned by
	// ingestion_loaded_at and clustered by provider and entity. It never
	// alters an existing table.
	SemCriarTabela bool

	// PayloadCru writes the payload flat instead of wrapping it in the six
	// landing columns. Turning this on also turns off table creation, since
	// the SDK then does not know the schema.
	PayloadCru bool
}

// tabelaPadrao is the landing naming convention: vendors_<provider>_<entity>s.
func (d Destino) tabelaPadrao() string {
	return fmt.Sprintf("vendors_%s_%ss", d.Provider, d.Entity)
}

// resolver turns a Destino into a LoadConfig, applying the documented
// precedence and reporting where each value came from.
func (d Destino) resolver() (*core.LoadConfig, map[string]origem, error) {
	if d.Provider == "" {
		return nil, nil, fmt.Errorf("Destino.Provider é obrigatório: ele entra no ingestion_id")
	}
	if d.Entity == "" {
		return nil, nil, fmt.Errorf("Destino.Entity é obrigatório: ele entra no ingestion_id")
	}
	if d.Chave == nil {
		return nil, nil, fmt.Errorf("Destino.Chave é obrigatório: sem ela não há source_key estável, " +
			"e o ingestion_id mudaria a cada execução")
	}

	projeto := resolver(d.Projeto, EnvProjeto, "")
	if projeto.valor == "" {
		return nil, nil, fmt.Errorf("projeto não definido: passe Destino.Projeto ou defina %s", EnvProjeto)
	}

	dataset := resolver(d.Dataset, EnvDataset, "landing")
	tabela := resolver(d.Tabela, "", d.tabelaPadrao())
	bucket := resolver(d.BucketDeStaging, EnvBucket, projeto.valor+"-bravis-staging")

	limite := d.LimiteInline
	if limite == 0 {
		limite = envInt("BRAVIS_SDK_LIMITE_INLINE", 5000)
	}

	cfg := &core.LoadConfig{
		ProjectID:            projeto.valor,
		Dataset:              dataset.valor,
		Table:                tabela.valor,
		StagingBucket:        bucket.valor,
		ThresholdForGCS:      limite,
		Format:               "ndjson",
		DeleteAfterLoad:      true,
		Dedup:                d.Dedup,
		WriteEnvelopeColumns: !d.PayloadCru,
		CriarTabela:          !d.SemCriarTabela && !d.PayloadCru,
	}

	return cfg, map[string]origem{
		"projeto": projeto,
		"dataset": dataset,
		"tabela":  tabela,
		"bucket":  bucket,
	}, nil
}

// Resultado describes what actually happened, end to end. Printing it is
// meant to be the whole of a fetcher's observability:
//
//	log.Info("pronto", res.Args()...)
type Resultado struct {
	// Extract
	Registros    int64 // records that came out of extract, after expansion
	Paginas      int   // pages fetched
	Tentativas   int   // HTTP attempts spent, retries included
	TempoExtract time.Duration

	// Load
	Linhas       int64      // rows written
	Ignoradas    int64      // rows deduplication matched as already present
	Bytes        int64      // bytes in the staged format
	Estrategia   string     // "inline" or "gcs"
	Formato      string     // the format actually written
	Dedup        core.Dedup // the deduplication that actually ran
	TabelaCriada bool       // whether this run created the table
	Tabela       string     // dataset.table written to
	TempoLoad    time.Duration

	// Diagnostics BigQuery reported per row, when it refused any.
	ErrosPorLinha []string

	Duracao time.Duration
}

// Args renders the result as slog key-value pairs.
func (r *Resultado) Args() []any {
	return []any{
		"registros", r.Registros,
		"linhas", r.Linhas,
		"ignoradas", r.Ignoradas,
		"paginas", r.Paginas,
		"tentativas", r.Tentativas,
		"bytes", r.Bytes,
		"tabela", r.Tabela,
		"estrategia", r.Estrategia,
		"formato", r.Formato,
		"dedup", r.Dedup,
		"tabela_criada", r.TabelaCriada,
		"extract", r.TempoExtract,
		"load", r.TempoLoad,
		"duracao", r.Duracao,
	}
}

func (r *Resultado) String() string {
	return fmt.Sprintf("%d registros -> %d linhas (%d ignoradas) em %s via %s, dedup %s, %s",
		r.Registros, r.Linhas, r.Ignoradas, r.Tabela, r.Estrategia, r.Dedup, r.Duracao)
}
