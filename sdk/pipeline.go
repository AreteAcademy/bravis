package sdk

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/AreteAcademy/brevis/sdk/internal/core"
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
	// Source is where records come from: the driver in From, plus the preview
	// and the counters that every origin honours.
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

	// Run is what the Brevis engine knows about this execution: whether it is
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
	if p.Target.To != nil {
		return p.Target.To.Describe()
	}
	return "pipeline"
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
	if p.Run.FromEngine() {
		slog.InfoContext(ctx, "running under Brevis",
			append([]any{"pipeline", p.name()}, p.Run.Args()...)...)
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

	return runPipeline(ctx, p)
}

// runPipeline is Execute after the flags: extract, transform, load, and the
// one line of log that says what happened.
//
// Separate from Execute because Execute installs the default logger, and a
// test that wants to read what was logged cannot do that through a function
// that replaces the logger first. The log line here is the whole of a
// fetcher's observability, so it is the part that most needs a test.
func runPipeline(ctx context.Context, p *Pipeline) error {
	data, err := Extract(ctx, p.Source)
	if err != nil {
		return err
	}
	data = Transform(data, p.Transform...)

	res, err := loadWith(ctx, data, p.Target, p.Run)

	// The credential expiry rides the pipeline's own line, because the one
	// the extract emits is at the wrong end of a run somebody only reads the
	// tail of -- and this is the number that says when the fetcher stops
	// working for a reason no retry fixes.
	if exp := data.Stats().CredentialExpiry; !exp.IsZero() {
		slog.Info("credential",
			"pipeline", p.name(),
			"expires", exp.Format(time.RFC3339),
			"left", core.RoundDuration(time.Until(exp)))
	}
	if res != nil {
		// The result comes back on the failure path too, by design, so that
		// RowErrors is readable after a refusal. That makes the message the
		// one thing that has to tell the two apart: "loaded" on a load that
		// wrote nothing is a line somebody will grep for and believe, and at
		// INFO it does not even reach whoever watches for errors.
		args := append([]any{"pipeline", p.name()}, res.Args()...)
		if err != nil {
			slog.Error("load failed", append(args, "error", err)...)
		} else {
			slog.Info("loaded", args...)
		}

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

	stats := data.Stats()
	_, _ = fmt.Fprintf(os.Stdout, "dry-run %s -> %s (%d records, %d page(s), %d attempt(s), %s)\n\n",
		p.name(), p.Target.To.Describe(), len(envelopes), stats.Pages, stats.Attempts,
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

		// The row is printed whole. Whatever the chain composed is what
		// lands, ingestion_id included -- so there is nothing left to compute
		// here, and nothing that could be printed and then not written.
		_, _ = fmt.Fprintf(os.Stdout, "%s\n", body)
	}

	if len(envelopes) == 0 {
		_, _ = fmt.Fprintln(os.Stdout, "no records -- the source answered, but with no data")
	}
	return nil
}
