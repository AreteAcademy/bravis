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

	"github.com/AreteAcademy/bravis/sdk"
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
			URL: "https://api.open-meteo.com/v1/forecast" +
				"?latitude=-23.55&longitude=-46.63&hourly=temperature_2m",
		},

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
			// The table, in the order of its DDL:
			//
			//	CREATE TABLE ... (
			//	  ingestion_id        STRING NOT NULL,
			//	  ingestion_loaded_at TIMESTAMP NOT NULL,
			//	  provider            STRING NOT NULL,
			//	  entity              STRING NOT NULL,
			//	  source_key          STRING,
			//	  payload             JSON   NOT NULL
			//	)
			//
			// One list, and it names the two the SDK fills in too. Put this
			// next to the DDL and the question "do these describe the same
			// table?" is answered by reading, not by tracing.
			Columns: []string{
				"ingestion_id",        // from Metadata
				"ingestion_loaded_at", // from Metadata
				"provider",
				"entity",
				"source_key",
				"payload",
			},

			Metadata: &sdk.Metadata{
				Provider: provider,
				Entity:   entity,
				Key:      sdk.Key("source_key"),
				When:     sdk.Field("source_key"),
			},

			// First run creates the table, inferring the columns from the
			// rows above. Clustering has to be named: the SDK does not know
			// what is in your payload.
			CreateTable: sdk.Bool(true),
			ClusterBy:   []string{"provider", "entity"},

			// Re-running the same window is a no-op. Costs one scan of the
			// destination per load, which is why it is never on by default.
			Dedup: sdk.DedupMerge,
		},
	})

	log.SetFlags(0)
}
