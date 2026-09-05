// Command 07-own-shape builds the row shape the warehouse expects.
//
// The SDK writes your payload as Transform left it and imposes nothing. When
// the destination has a contract -- here the six-column landing shape a
// Python fetcher also writes -- you build it, in one Transformer.
//
// Only ingestion_id and ingestion_loaded_at come from the SDK, and only
// because you asked with a Metadata block.
package main

import (
	"flag"
	"log"

	"github.com/AreteAcademy/brevis/sdk"
	"github.com/AreteAcademy/brevis/sdk/from"
	"github.com/AreteAcademy/brevis/sdk/to/bigquery"
)

const (
	provider = "open_meteo"
	entity   = "hourly_temperature"
)

func main() {
	var project string

	sdk.Run(sdk.Pipeline{
		Name: provider + "/" + entity,

		Flags: func(fs *flag.FlagSet) {
			fs.StringVar(&project, "project", "", "GCP project")
		},

		// Configuration, and only that.
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
			// What we take from the source. Not the table -- that is below.
			sdk.Accept("time", "temperature_2m", "latitude", "longitude"),

			// The contract, built here because it is yours, not the SDK's.
			// ingestion_id and ingestion_loaded_at arrive on top of this from
			// the Metadata block below.
			func(payload any) (any, error) {
				return map[string]any{
					"provider":   provider,
					"entity":     entity,
					"source_key": payload.(map[string]any)["time"],
					"payload":    payload,
				}, nil
			},
		},

		Target: sdk.Target{
			To: bigquery.Table{
				Project: project,
				Dataset: "landing",
				Name:    "vendors_" + provider + "_" + entity + "s",

				// First run creates the table. Clustering has to be named:
				// the SDK does not know what is in your payload.
				CreateTable: sdk.Bool(true),
				ClusterBy:   []string{"provider", "entity"},
			},

			// The table, in the order of its DDL -- and this IS the DDL:
			// with CreateTable set, the SDK writes the CREATE from this list
			// and from nothing else. It does not infer a type from the data,
			// because the type would then come from the first batch and
			// change the day a field arrives whole instead of fractional.
			//
			// One list, and it names the two the SDK fills in too.
			Schema: sdk.Schema{
				{Name: "ingestion_id", Type: sdk.TypeString, Required: true},
				{Name: "ingestion_loaded_at", Type: sdk.TypeTimestamp, Required: true},
				{Name: "provider", Type: sdk.TypeString, Required: true},
				{Name: "entity", Type: sdk.TypeString, Required: true},
				{Name: "source_key", Type: sdk.TypeString},
				{Name: "payload", Type: sdk.TypeJSON, Required: true},
			},

			// A partição é declarada, e não escolhida pelo SDK. Vazio mantém
			// o padrão -- diária em ingestion_loaded_at -- e escrevê-la é
			// como se diz outra coisa.
			PartitionBy: "ingestion_loaded_at",

			// Re-running the same window is a no-op. Costs one scan of the
			// destination per load, which is why it is never on by default.
			Dedup: sdk.DedupMerge,
		},
	})

	log.SetFlags(0)
}
