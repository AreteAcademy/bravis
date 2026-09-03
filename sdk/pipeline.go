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

// Pipeline is a whole fetcher as a value. Run takes it from here: flags,
// -dry-run, logging and the exit code.
//
//	func main() {
//		sdk.Run(sdk.Pipeline{
//			Source: sdk.Source{
//				URL:    "https://api.example.com/events",
//				Expand: sdk.ArrayAt("results"),
//			},
//			Transform: []sdk.Transformer{
//				sdk.Without("generationtime_ms"),
//			},
//			Target: sdk.Target{
//				Provider: "example",
//				Entity:   "events",
//				Key:    sdk.Key("id"),
//				When:   sdk.Field("created_at"),
//			},
//		})
//	}
//
// Anything this does not cover is still reachable by calling Extract and Load
// directly.
type Pipeline struct {
	Source Source

	// Transform reshapes each record between Extract and Load, in order. See
	// Transformer.
	Transform []Transformer

	Target Target

	// Name appears in logs. Defaults to provider/entity.
	Name string

	// Flags registers extra command-line flags before parsing, for a fetcher
	// that takes parameters of its own.
	Flags func(*flag.FlagSet)

	// Before runs after flags are parsed and before the fetch, for a source
	// whose URL depends on those flags.
	Before func(ctx context.Context, p *Pipeline) error
}

func (p Pipeline) name() string {
	if p.Name != "" {
		return p.Name
	}
	return fmt.Sprintf("%s/%s", p.Target.Provider, p.Target.Entity)
}

// Run runs a Pipeline as a command: it parses flags, sets up logging,
// honours -dry-run, prints the result and exits non-zero on failure.
//
// It calls os.Exit, so it belongs in main. Use Execute to keep control.
func Run(p Pipeline) {
	if err := Execute(context.Background(), &p, os.Args[1:]); err != nil {
		slog.Error("failed", "pipeline", p.name(), "error", err)
		os.Exit(1)
	}
}

// Execute is Run without the exit: it parses the arguments given and
// returns the error instead of terminating. This is what tests call.
func Execute(ctx context.Context, p *Pipeline, args []string) error {
	fs := flag.NewFlagSet(p.name(), flag.ContinueOnError)

	var (
		dryRun  = fs.Bool("dry-run", false, "extract, map and print the first records without writing")
		sample  = fs.Int("sample", 5, "how many records -dry-run prints")
		dataset = fs.String("dataset", "", "BigQuery dataset (default: "+EnvDataset+", or landing)")
		table   = fs.String("table", "", "destination table (default: vendors_<provider>_<entity>s)")
		verbose = fs.Bool("v", false, "log at debug level")
	)
	if p.Flags != nil {
		p.Flags(fs)
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	level := LogLevel()
	if *verbose {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))

	if *dataset != "" {
		p.Target.Dataset = *dataset
	}
	if *table != "" {
		p.Target.Table = *table
	}

	if p.Before != nil {
		if err := p.Before(ctx, p); err != nil {
			return err
		}
	}

	if *dryRun {
		return runDryRun(ctx, p, *sample)
	}

	data, err := Extract(ctx, p.Source)
	if err != nil {
		return err
	}
	data = Transform(data, p.Transform...)

	res, err := Load(ctx, data, p.Target)
	if res != nil {
		slog.Info("loaded", append([]any{"pipeline", p.name()}, res.Args()...)...)
		for _, line := range res.RowErrors {
			slog.Error("row rejected", "detail", line)
		}
	}
	return err
}

// runDryRun extracts and maps without writing, printing the first n
// records with the ingestion_id each would get.
//
// Every fetcher needs this on day one, and every fetcher used to rewrite it.
// It is also the cheapest way to see that Key picks the fields you meant:
// a wrong key is invisible until rows start duplicating.
func runDryRun(ctx context.Context, p *Pipeline, n int) error {
	start := time.Now()

	data, err := Extract(ctx, p.Source)
	if err != nil {
		return err
	}
	// Transform runs here too: a dry-run that printed untransformed records
	// would show a payload -- and an ingestion_id -- that is not what lands.
	data = Transform(data, p.Transform...)

	// Provenance must be stamped the same way Load would, or the printed
	// ingestion_id would not be the one that lands.
	envelopes, err := collect(data, p.Target)
	if err != nil {
		return err
	}

	table := p.Target.Table
	if table == "" {
		table = p.Target.defaultTable()
	}

	stats := data.Stats()
	_, _ = fmt.Fprintf(os.Stdout, "dry-run %s -> %s (%d records, %d page(s), %d attempt(s), %s)\n\n",
		p.name(), table, len(envelopes), stats.Pages, stats.Attempts,
		time.Since(start).Round(time.Millisecond))

	for i, env := range envelopes {
		if i == n {
			_, _ = fmt.Fprintf(os.Stdout, "... and %d more\n", len(envelopes)-n)
			break
		}
		id, err := env.IngestionID()
		if err != nil {
			return fmt.Errorf("record %d: %w", i, err)
		}
		body, err := json.Marshal(env.Payload)
		if err != nil {
			return fmt.Errorf("record %d: %w", i, err)
		}
		_, _ = fmt.Fprintf(os.Stdout, "%s  key=%s  ts=%s\n  %s\n", id, env.SourceKey, env.RecordTS, body)
	}

	if len(envelopes) == 0 {
		_, _ = fmt.Fprintln(os.Stdout, "no records -- the source answered, but with no data")
	}
	return nil
}
