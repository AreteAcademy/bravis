// Command 10-engine-context is a fetcher that creates its table on the first
// run inside Bravis, and never mentions that it does.
//
// There is no flag for it, no argument, no environment read. The engine
// injects BRAVIS_RUN_* into the step; the SDK picks it up; Target.CreateTable
// stays nil and lets it decide.
//
// Run by hand, none of that exists and nothing is created:
//
//	go run ./10-engine-context -dry-run
//
// Run by Bravis on the step's first successful execution, the table is
// created. To force it without pretending nothing ever ran -- the table was
// dropped by mistake -- dispatch with create_table=true:
//
//	bravis run wf.yaml --param create_table=true
package main

import (
	"context"
	"log/slog"

	"github.com/AreteAcademy/bravis/sdk"
)

func main() {
	sdk.Run(sdk.Pipeline{
		Name: "open_meteo/hourly",

		Source: sdk.Source{
			URL: "https://api.open-meteo.com/v1/forecast" +
				"?latitude=-23.55&longitude=-46.63&hourly=temperature_2m",
			Guard:  sdk.RejectIf("error"),
			Expand: sdk.ParallelArrays("hourly", "time", "temperature_2m"),
		},

		Transform: []sdk.Transformer{
			sdk.Schema("time", "temperature_2m", "latitude", "longitude"),
		},

		// Reading the run context is optional. This one uses it to widen the
		// window on a backfill; a fetcher that ignores it works the same.
		Before: func(ctx context.Context, p *sdk.Pipeline) error {
			if p.Run.Trigger == "backfill" {
				p.Source.URL += "&past_days=7"
				slog.InfoContext(ctx, "backfill: widening the window",
					"logical_date", p.Run.LogicalDate)
			}
			return nil
		},

		Target: sdk.Target{
			Metadata: &sdk.Metadata{
				Provider: "open_meteo",
				Entity:   "hourly_temperature",
				Key:      sdk.Key("latitude", "longitude", "time"),
				When:     sdk.Field("time"),
			},
			ClusterBy: []string{"latitude", "longitude"},

			// Left nil on purpose. Inside Bravis the engine decides; outside,
			// nothing is created. sdk.Bool(false) here would refuse even on a
			// first run, and the engine would not override it.
			CreateTable: nil,
		},
	})
}
