package sdk

import (
	"fmt"
	"time"

	core "github.com/AreteAcademy/brevis/sdk/internal/core"
)

// Target says where records land, and what every destination honours.
//
// The destination itself is To -- to.BigQuery, to.Postgres, to.Files. What
// lives here instead of in the driver is what is true of all of them: the
// columns declared, the metadata asked for, the deduplication wanted.
//
//	Target: sdk.Target{
//		To:      to.BigQuery{Dataset: "bronze", Table: "pedidos"},
//		Columns: []string{"ingestion_id", "ingestion_loaded_at", "payload"},
//		Columns: []string{"ingestion_id", "ingestion_loaded_at", "payload"},
//	}
type Target struct {
	// To is the destination. Required.
	To Writer

	// Columns declares the destination's columns, in the order of its DDL,
	// including the ones the SDK fills in:
	//
	//	Columns: []string{
	//		"ingestion_id",         // from sdk.IngestionID()
	//		"ingestion_loaded_at",  // from sdk.IngestionLoadedAt()
	//		"provider",
	//		"entity",
	//		"source_key",
	//		"payload",
	//	}
	//
	// One declaration, and it names every column -- including the two that
	// the ingestion transformers write, so nothing lands that the chain did
	// not compose.
	//
	// Checked against the row the Transform chain composed, so a declared
	// column the chain did not deliver is an error naming the column, and a
	// field the row carries that this list does not declare is an error naming
	// the field. Checked again against the real destination,
	// where a declared column it lacks is an error naming both sides.
	//
	// Nil declares nothing and checks nothing.
	//
	// Use Schema instead when the destination has to CREATE the table: names
	// alone cannot say what type each column is, and the SDK does not guess.
	// Setting both is an error.
	Columns []string

	// Schema is Columns with a type on each column, and it is what a
	// destination needs in order to create the table.
	//
	//	Schema: sdk.Schema{
	//		{Name: "ingestion_id",        Type: sdk.TypeString,    Required: true},
	//		{Name: "ingestion_loaded_at", Type: sdk.TypeTimestamp, Required: true},
	//		{Name: "temperatura",         Type: sdk.TypeFloat64},
	//	}
	//
	// Everything Columns does, Schema does -- it is the same declaration
	// carrying more. Setting both is an error: two lists of columns are two
	// sources of truth, and the one that loses does so silently.
	Schema core.Schema

	// PartitionBy names the column a created table is partitioned on.
	//
	// Empty keeps the SDK's default, which is daily on ingestion_loaded_at --
	// the column that says when the row was written, and the one a landing
	// table is almost always read by. Declaring it is how you say otherwise.
	//
	// Only consulted when the destination creates the table.
	PartitionBy string

	// Dedup selects deduplication. Zero value appends, which is free. What
	// DedupMerge costs, and whether a destination supports it at all, is the
	// driver's to say.
	Dedup core.Dedup

	// FlushEvery escreve a cada N registros lidos, em vez de acumular a
	// leitura inteira em memoria. Zero acumula tudo, que continua sendo o
	// padrao.
	//
	// Uma leitura de milhares de origens nao cabe necessariamente na memoria:
	// o lote inteiro fica vivo, e o destino monta uma SEGUNDA copia dele para
	// serializar. Com FlushEvery, o teto e N registros mais a copia de N.
	//
	// O que se paga por isso, e precisa ser dito:
	//
	//   - a carga deixa de ser ATOMICA. Uma falha na terceira leva deixa as
	//     duas primeiras gravadas, e a re-execucao depende de Dedup para nao
	//     duplicar. Sem DedupMerge, uma falha no meio duplica o que ja entrou.
	//   - com DedupMerge cada leva paga o proprio MERGE, entao N pequeno
	//     multiplica o custo no destino.
	//
	// O Result soma as levas: Rows, Ignored e Bytes sao o total, e RowErrors
	// junta as de todas.
	FlushEvery int
}

// ValidateTarget confere o que a fachada consegue conferir sem tocar o
// destino: a declaracao consigo mesma.
//
// Exportada porque um fetcher pode querer falhar cedo, num teste ou num
// -dry-run, sem cliente de nuvem nenhum -- e porque um invariante que so da
// para exercitar com servidor de pe e um invariante que ninguem exercita.
func ValidateTarget(t Target) error { return t.validate() }

// validate checks what the facade owns. What the destination needs is the
// destination's to check, and it does so in Write.
func (d Target) validate() error {
	if d.To == nil {
		return fmt.Errorf("Target.To is required: pass a destination, such as " +
			"bigquery.Table{Dataset: \"bronze\", Name: \"pedidos\"}")
	}
	if len(d.Columns) > 0 && len(d.Schema) > 0 {
		return fmt.Errorf("Target declares both Columns and Schema, and they are two " +
			"lists of the same thing -- the one that loses would do so silently. " +
			"Schema is Columns with a type on each column: keep it and drop Columns")
	}
	if err := d.Schema.Check(); err != nil {
		return err
	}
	if d.PartitionBy != "" && len(d.Schema) > 0 {
		if !d.Schema.Has(d.PartitionBy) {
			return fmt.Errorf("Target.PartitionBy names %q, which Schema does not declare. "+
				"A table cannot be partitioned on a column it does not have", d.PartitionBy)
		}
	}
	return nil
}

// colunas devolve a declaracao efetiva, venha de Columns ou de Schema.
func (d Target) colunas() []string {
	if len(d.Schema) > 0 {
		return d.Schema.Names()
	}
	return d.Columns
}

// options folds the Target into what every driver receives.
func (d Target) options(run RunContext) core.WriteOptions {
	return core.WriteOptions{
		Columns:     d.colunas(),
		Schema:      d.Schema,
		PartitionBy: d.PartitionBy,
		Dedup:       d.Dedup,
		Run:         run,
	}
}

// Result describes what actually happened, end to end. Printing it is meant
// to be the whole of a fetcher's observability:
//
//	log.Info("pronto", res.Args()...)
type Result struct {
	// Extract
	Records      int64 // records that came out of the source, after expansion
	Pages        int   // pages fetched
	Attempts     int   // HTTP attempts spent, retries included
	ExtractBytes int64 // bytes read off the wire, before Transform
	ExtractTime  time.Duration

	// Load
	Rows         int64      // rows written
	Ignored      int64      // rows deduplication matched as already present
	Bytes        int64      // bytes in the staged format
	Strategy     string     // how the driver wrote: "inline", "gcs", "copy"
	Format       string     // the format actually written
	Dedup        core.Dedup // the deduplication that actually ran
	TableCreated bool       // whether this run created the destination
	Table        string     // the destination written to
	LoadTime     time.Duration

	// CredentialExpiry is when the source credential stops working, when the
	// source renews one that says so. Zero otherwise.
	//
	// It is on Result and not only in a log line because the credential this
	// tracks is renewed by a human: whoever runs this pipeline is the one who
	// has to act, and a warning that only exists in the logs is how the
	// silent death happens in the first place.
	CredentialExpiry time.Time

	// CredentialStoreError diz que a credencial rotacionada nao foi guardada.
	// A carga aconteceu; o que se perdeu foi a rotacao, e a próxima execucao
	// cai na semente. Vazio quando nao ha store ou quando gravou.
	CredentialStoreError string

	// FailedSources sao as origens que falharam e foram toleradas por
	// from.Many com ContinueOnError. Vazio quando nao houve.
	//
	// Ele esta aqui, e nao so no log, porque e a unica coisa que permite
	// reprocessar o que faltou. Um fan-out que perde 3.000 de 4.803 origens e
	// nao diz quais obriga a proxima execucao a refazer tudo.
	FailedSources []core.SourceFailure

	// Objects são os objetos que a carga escreveu e que continuam lá.
	//
	// Com to.Files, o arquivo. Com um destino que estagia e apaga, vazio -- um
	// caminho reportado que já não existe é pior que nenhum.
	//
	// Ele existe para o caso de um passo escrever o arquivo e outro lê-lo: sem
	// isto, quem escreveu não sabe o que escreveu, porque o nome carrega um
	// carimbo de tempo que o driver escolhe.
	Objects []string

	// Diagnostics the destination reported per row, when it refused any.
	RowErrors []string

	Duration time.Duration
}

// Args renders the result as slog key-value pairs.
func (r *Result) Args() []any {
	args := []any{
		"records", r.Records,
		"lines", r.Rows,
		"ignored", r.Ignored,
		"paginas", r.Pages,
		"attempts", r.Attempts,
		"table", r.Table,
		"estrategia", r.Strategy,
		"dedup", r.Dedup,
		"tabela_criada", r.TableCreated,
		"extract", r.ExtractTime,
		"load", r.LoadTime,
		"duracao", r.Duration,
	}

	// Contadores que nem todo driver preenche saem quando estao zerados.
	//
	// "um numero que e sempre zero e pior que numero nenhum" e principio deste
	// projeto, e a linha de um pipeline SQL o violava: os drivers de banco nao
	// contam bytes, entao `extract_bytes=0 bytes=0 formato=""` aparecia em
	// toda execucao, ensinando quem le a pular esses campos -- e quando o
	// pipeline de HTTP mostrasse zero de verdade, ninguem veria.
	if r.ExtractBytes > 0 {
		args = append(args, "extract_bytes", r.ExtractBytes)
	}
	if r.Bytes > 0 {
		args = append(args, "bytes", r.Bytes)
	}
	if r.Format != "" {
		args = append(args, "formato", r.Format)
	}
	// Only when there is one: a key that is always the zero time on every
	// line teaches people to skip it, and then it is invisible on the one
	// line that matters.
	if !r.CredentialExpiry.IsZero() {
		args = append(args,
			"credential_expires", r.CredentialExpiry.Format(time.RFC3339),
			"credential_left", core.RoundDuration(time.Until(r.CredentialExpiry)))
	}
	if r.CredentialStoreError != "" {
		args = append(args, "credential_not_saved", r.CredentialStoreError)
	}
	if n := len(r.FailedSources); n > 0 {
		args = append(args, "fontes_falharam", n)
	}
	if len(r.Objects) == 1 {
		args = append(args, "objeto", r.Objects[0])
	} else if len(r.Objects) > 1 {
		// Com FlushEvery sao varios, e despejar cinquenta caminhos numa linha
		// de log a torna ilegivel. A lista inteira esta em Result.Objects.
		args = append(args, "objetos", len(r.Objects), "primeiro", r.Objects[0])
	}
	return args
}

func (r *Result) String() string {
	return fmt.Sprintf("%d records -> %d lines (%d ignored) em %s via %s, dedup %s, %s",
		r.Records, r.Rows, r.Ignored, r.Table, r.Strategy, r.Dedup, r.Duration)
}
