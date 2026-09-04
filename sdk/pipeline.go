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
//				Records: minhaLeitura,
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
	// Source is configuration, and only that: URL, headers, timeouts, retry,
	// pagination, format. Nothing in it decides what the data means.
	Source Source

	// Records decides what each successful response means -- the records it
	// carries, or a refusal saying why. Nil decodes the body and treats each
	// document as one record. See Reading.
	//
	// It sits here rather than inside Source because it is the one thing in a
	// fetcher that is about the data instead of the transport, and it belongs
	// next to Transform, which is the other step that runs over what was
	// extracted.
	Records Reading

	// Transform reshapes each record between Extract and Load, in order. See
	// Transformer.
	Transform []Transformer

	Target Target

	// Name appears in logs. Defaults to provider/entity.
	Name string

	// Flags registers extra command-line flags before parsing, for a fetcher
	// that takes parameters of its own.
	Flags func(*flag.FlagSet)

	// Run is what the Bravis engine knows about this execution: whether it is
	// the first, the parameters it was dispatched with, which run it is.
	//
	// Filled in from the environment before Before runs, and zero when the
	// fetcher runs by hand. Read it if it helps; ignoring it costs nothing.
	//
	//	Before: func(ctx context.Context, p *sdk.Pipeline) error {
	//		if p.Run.Params["load_full"] == "true" {
	//			p.Source.URL += "&full=1"
	//		}
	//		return nil
	//	}
	Run RunContext

	// Before runs after flags are parsed and before the fetch, for a source
	// whose URL depends on those flags or on Run.
	Before func(ctx context.Context, p *Pipeline) error
}

func (p Pipeline) name() string {
	if p.Name != "" {
		return p.Name
	}
	if m := p.Target.Metadata; m != nil && m.Provider != "" {
		return fmt.Sprintf("%s/%s", m.Provider, m.Entity)
	}
	return p.Target.Table
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
		preview = fs.Int("preview", 0, "print the first N records as a table once the extract finishes")
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

	// Read before Before, so a hook can act on it.
	p.Run = runContextFromEnv()
	if p.Run.fromEngine() {
		slog.InfoContext(ctx, "running under Bravis",
			append([]any{"pipeline", p.name()}, p.Run.Args()...)...)
	}

	if *dataset != "" {
		p.Target.Dataset = *dataset
	}
	if *table != "" {
		p.Target.Table = *table
	}
	// The flag only turns the preview on; a pipeline that asked for one in
	// code keeps it, so running without the flag does not silently disable
	// what the fetcher configured.
	if *preview > 0 {
		p.Source.Preview = *preview
	}

	if p.Before != nil {
		if err := p.Before(ctx, p); err != nil {
			return err
		}
	}

	if *dryRun {
		return runDryRun(ctx, p, *sample)
	}

	data, err := Extract(ctx, p.Source, p.Records)
	if err != nil {
		return err
	}
	data = Transform(data, p.Transform...)

	res, err := loadWith(ctx, data, p.Target, p.Run)
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

	data, err := Extract(ctx, p.Source, p.Records)
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
		body, err := json.Marshal(env.Payload)
		if err != nil {
			return fmt.Errorf("record %d: %w", i, err)
		}

		// Without a Metadata block there is no ingestion_id to print, because
		// the load will not write one. Printing a computed id here would
		// show a column that never lands.
		if p.Target.Metadata == nil {
			_, _ = fmt.Fprintf(os.Stdout, "%s\n", body)
			continue
		}

		id, err := env.IngestionID()
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
