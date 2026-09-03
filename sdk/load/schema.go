package load

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"cloud.google.com/go/bigquery"
	"google.golang.org/api/googleapi"
)

// esquemaLanding is the six-column landing contract, the only schema the SDK
// knows well enough to create.
//
// Partitioning and clustering ship by default and are not optional: an
// unpartitioned landing table costs a full scan on every MERGE the bronze
// layer runs. Measured on a consumer, one entity spent 58.96 GiB of MERGE
// against 0.0 GiB of SELECT.
func esquemaLanding() bigquery.Schema {
	return bigquery.Schema{
		{Name: "ingestion_id", Type: bigquery.StringFieldType, Required: true},
		{Name: "ingestion_loaded_at", Type: bigquery.TimestampFieldType, Required: true},
		{Name: "provider", Type: bigquery.StringFieldType, Required: true},
		{Name: "entity", Type: bigquery.StringFieldType, Required: true},
		{Name: "source_key", Type: bigquery.StringFieldType},
		{Name: "payload", Type: bigquery.JSONFieldType, Required: true},
	}
}

func metadataLanding() *bigquery.TableMetadata {
	return &bigquery.TableMetadata{
		Schema: esquemaLanding(),
		TimePartitioning: &bigquery.TimePartitioning{
			Type:  bigquery.DayPartitioningType,
			Field: "ingestion_loaded_at",
		},
		Clustering: &bigquery.Clustering{
			Fields: []string{"provider", "entity"},
		},
	}
}

// garantirTabela makes sure the destination exists and matches the contract.
//
// It creates the table when absent and, when present, compares and refuses on
// any difference. It never alters: a loader that can ALTER or DROP on its own
// is a loader that can erase history, and no convenience is worth that.
//
// Reports whether it created the table.
func (l *Loader) garantirTabela(ctx context.Context, table *bigquery.Table) (bool, error) {
	md, err := table.Metadata(ctx)
	if err == nil {
		return false, conferirEsquema(table, md)
	}

	if !ehNaoEncontrado(err) {
		return false, fmt.Errorf("consultando %s: %w", nomeDe(table), err)
	}

	if !l.cfg.CriarTabela {
		return false, fmt.Errorf("tabela %s não existe. Crie-a, ou use CriarTabela para o SDK criá-la "+
			"com o contrato de seis colunas", nomeDe(table))
	}
	if !l.cfg.WriteEnvelopeColumns {
		return false, fmt.Errorf("CriarTabela exige WriteEnvelopeColumns: o SDK só conhece o schema " +
			"do contrato de landing, não o seu")
	}

	if err := table.Create(ctx, metadataLanding()); err != nil {
		// Another process may have created it between our check and this call.
		if md, segunda := table.Metadata(ctx); segunda == nil {
			return false, conferirEsquema(table, md)
		}
		return false, fmt.Errorf("criando %s: %w", nomeDe(table), err)
	}

	return true, nil
}

// conferirEsquema refuses a table that does not match the contract, naming the
// difference. A vague "schema mismatch" costs an investigation; the caller
// needs to know which column is wrong.
func conferirEsquema(table *bigquery.Table, md *bigquery.TableMetadata) error {
	esperado := esquemaLanding()

	tipos := map[string]bigquery.FieldType{}
	for _, f := range md.Schema {
		tipos[f.Name] = f.Type
	}

	var faltando, divergentes []string
	for _, f := range esperado {
		tipo, ok := tipos[f.Name]
		if !ok {
			faltando = append(faltando, f.Name)
			continue
		}
		if tipo != f.Type {
			divergentes = append(divergentes, fmt.Sprintf("%s é %s, esperado %s", f.Name, tipo, f.Type))
		}
	}

	if len(faltando) == 0 && len(divergentes) == 0 {
		return nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "tabela %s existe mas não bate com o contrato de landing", nomeDe(table))
	if len(faltando) > 0 {
		sort.Strings(faltando)
		fmt.Fprintf(&b, "; colunas faltando: %s", strings.Join(faltando, ", "))
	}
	if len(divergentes) > 0 {
		sort.Strings(divergentes)
		fmt.Fprintf(&b, "; tipos divergentes: %s", strings.Join(divergentes, "; "))
	}
	b.WriteString(". O SDK não altera tabela existente — ajuste-a, ou aponte para outra")

	return fmt.Errorf("%s", b.String())
}

func nomeDe(t *bigquery.Table) string {
	return fmt.Sprintf("%s.%s", t.DatasetID, t.TableID)
}

// ehNaoEncontrado distinguishes "the table is not there" from "we could not
// ask" -- creating a table because of a permissions blip would be wrong, so
// this checks for a 404 specifically rather than any failure.
func ehNaoEncontrado(err error) bool {
	if err == nil {
		return false
	}
	var apiErr *googleapi.Error
	if errors.As(err, &apiErr) {
		return apiErr.Code == 404
	}
	return false
}
