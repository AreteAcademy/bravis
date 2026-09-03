package sdk

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"
)

// Pipeline is a whole fetcher as a value. Rodar takes it from here: flags,
// -dry-run, logging and the exit code.
//
//	func main() {
//		sdk.Rodar(sdk.Pipeline{
//			Fonte: sdk.Fonte{
//				URL:      "https://api.example.com/events",
//				Expandir: sdk.ArrayEm("results"),
//			},
//			Destino: sdk.Destino{
//				Provider: "example",
//				Entity:   "events",
//				Chave:    sdk.Chave("id"),
//				Quando:   sdk.Campo("created_at"),
//			},
//		})
//	}
//
// Anything this does not cover is still reachable by calling Extract and Load
// directly.
type Pipeline struct {
	Fonte   Fonte
	Destino Destino

	// Nome appears in logs. Defaults to provider/entity.
	Nome string

	// Flags registers extra command-line flags before parsing, for a fetcher
	// that takes parameters of its own.
	Flags func(*flag.FlagSet)

	// Antes runs after flags are parsed and before the fetch, for a source
	// whose URL depends on those flags.
	Antes func(ctx context.Context, p *Pipeline) error
}

func (p Pipeline) nome() string {
	if p.Nome != "" {
		return p.Nome
	}
	return fmt.Sprintf("%s/%s", p.Destino.Provider, p.Destino.Entity)
}

// Rodar runs a Pipeline as a command: it parses flags, sets up logging,
// honours -dry-run, prints the result and exits non-zero on failure.
//
// It calls os.Exit, so it belongs in main. Use Executar to keep control.
func Rodar(p Pipeline) {
	if err := Executar(context.Background(), &p, os.Args[1:]); err != nil {
		slog.Error("falhou", "pipeline", p.nome(), "erro", err)
		os.Exit(1)
	}
}

// Executar is Rodar without the exit: it parses the arguments given and
// returns the error instead of terminating. This is what tests call.
func Executar(ctx context.Context, p *Pipeline, args []string) error {
	fs := flag.NewFlagSet(p.nome(), flag.ContinueOnError)

	var (
		dryRun  = fs.Bool("dry-run", false, "extrai, mapeia e imprime os primeiros registros, sem escrever")
		amostra = fs.Int("amostra", 5, "quantos registros o -dry-run imprime")
		dataset = fs.String("dataset", "", "dataset do BigQuery (padrão: "+EnvDataset+", ou landing)")
		tabela  = fs.String("tabela", "", "tabela de destino (padrão: vendors_<provider>_<entity>s)")
		verbose = fs.Bool("v", false, "log em debug")
	)
	if p.Flags != nil {
		p.Flags(fs)
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	nivel := NivelDeLog()
	if *verbose {
		nivel = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: nivel})))

	if *dataset != "" {
		p.Destino.Dataset = *dataset
	}
	if *tabela != "" {
		p.Destino.Tabela = *tabela
	}

	if p.Antes != nil {
		if err := p.Antes(ctx, p); err != nil {
			return err
		}
	}

	if *dryRun {
		return dryRunPipeline(ctx, p, *amostra)
	}

	dados, err := Extract(ctx, p.Fonte)
	if err != nil {
		return err
	}

	res, err := Load(ctx, dados, p.Destino)
	if res != nil {
		slog.Info("carregado", append([]any{"pipeline", p.nome()}, res.Args()...)...)
		for _, linha := range res.ErrosPorLinha {
			slog.Error("linha recusada", "detalhe", linha)
		}
	}
	return err
}

// dryRunPipeline extracts and maps without writing, printing the first n
// records with the ingestion_id each would get.
//
// Every fetcher needs this on day one, and every fetcher used to rewrite it.
// It is also the cheapest way to see that Chave picks the fields you meant:
// a wrong key is invisible until rows start duplicating.
func dryRunPipeline(ctx context.Context, p *Pipeline, n int) error {
	inicio := time.Now()

	dados, err := Extract(ctx, p.Fonte)
	if err != nil {
		return err
	}

	// Provenance must be stamped the same way Load would, or the printed
	// ingestion_id would not be the one that lands.
	envelopes, err := coletar(dados, p.Destino)
	if err != nil {
		return err
	}

	tabela := p.Destino.Tabela
	if tabela == "" {
		tabela = p.Destino.tabelaPadrao()
	}

	_, _ = fmt.Fprintf(os.Stdout, "dry-run %s -> %s (%d registros, %s)\n\n",
		p.nome(), tabela, len(envelopes), time.Since(inicio).Round(time.Millisecond))

	for i, env := range envelopes {
		if i == n {
			_, _ = fmt.Fprintf(os.Stdout, "... e mais %d\n", len(envelopes)-n)
			break
		}
		id, err := env.IngestionID()
		if err != nil {
			return fmt.Errorf("registro %d: %w", i, err)
		}
		corpo, err := json.Marshal(env.Payload)
		if err != nil {
			return fmt.Errorf("registro %d: %w", i, err)
		}
		_, _ = fmt.Fprintf(os.Stdout, "%s  key=%s  ts=%s\n  %s\n", id, env.SourceKey, env.RecordTS, corpo)
	}

	if len(envelopes) == 0 {
		_, _ = fmt.Fprintln(os.Stdout, "nenhum registro — a fonte respondeu, mas sem dados")
	}
	return nil
}
