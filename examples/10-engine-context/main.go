// Command 10-engine-context is a fetcher that creates its table on the first
// run inside Brevis, and never mentions that it does.
//
// There is no flag for it, no argument, no environment read. The engine
// injects BREVIS_RUN_* into the step; the SDK picks it up; Target.CreateTable
// stays nil and lets it decide.
//
// Run by hand, none of that exists and nothing is created:
//
//	go run ./10-engine-context -dry-run
//
// Run by Brevis on the step's first successful execution, the table is
// created. To force it without pretending nothing ever ran -- the table was
// dropped by mistake -- dispatch with create_table=true:
//
//	brevis run wf.yaml --param create_table=true
package main

import (
	"context"
	"log/slog"

	"github.com/AreteAcademy/brevis/sdk"
	"github.com/AreteAcademy/brevis/sdk/from"
	"github.com/AreteAcademy/brevis/sdk/to/bigquery"
)

func main() {
	sdk.Run(sdk.Pipeline{
		Name: "open_meteo/hourly",

		Source: sdk.Source{
			From: from.HTTP{
				URL: "https://api.open-meteo.com/v1/forecast" +
					"?latitude=-23.55&longitude=-46.63&hourly=temperature_2m",

				// What a response means.
				Records: func(r sdk.Response) ([]any, error) {
					if err := sdk.RejectIf("error")(r); err != nil {
						return nil, err
					}
					doc, err := r.Object()
					if err != nil {
						return nil, err
					}
					return sdk.ParallelArrays("hourly", "time", "temperature_2m")(doc)
				},
			},
		},

		Transform: []sdk.Transformer{
			sdk.Accept("time", "temperature_2m", "latitude", "longitude"),
		},

		// Reading the run context is optional. This one uses it to widen the
		// window on a backfill; a fetcher that ignores it works the same.
		Before: func(ctx context.Context, p *sdk.Pipeline) error {
			if p.Run.Trigger != "backfill" {
				return nil
			}
			// The source is a value, so widening the window means replacing
			// it -- and the type says which driver you are replacing.
			origem := p.Source.From.(from.HTTP)
			origem.URL += "&past_days=7"
			p.Source.From = origem

			slog.InfoContext(ctx, "backfill: widening the window",
				"logical_date", p.Run.LogicalDate)
			return nil
		},

		Target: sdk.Target{
			To: bigquery.Table{
				ClusterBy: []string{"latitude", "longitude"},

				// Left nil on purpose. Inside Brevis the engine decides; outside,
				// nothing is created. sdk.Bool(false) here would refuse even on a
				// first run, and the engine would not override it.
				CreateTable: nil,
			},

			// O Schema é obrigatório justamente porque o engine PODE ligar o
			// CreateTable: sem ele, a criação seria recusada no primeiro run
			// dentro do Brevis, que é o único lugar onde ela acontece.
			Schema: sdk.Schema{
				{Name: "ingestion_id", Type: sdk.TypeString, Required: true},
				{Name: "ingestion_loaded_at", Type: sdk.TypeTimestamp, Required: true},
				{Name: "latitude", Type: sdk.TypeFloat64},
				{Name: "longitude", Type: sdk.TypeFloat64},
				{Name: "time", Type: sdk.TypeString},
				{Name: "temperature_2m", Type: sdk.TypeFloat64},
			},
		},
	})
}
