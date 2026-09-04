// Command 08-fetcher-minimo is a whole fetcher, with nothing left out.
//
// Four questions, four places: where it comes from, what a response means,
// what row it builds, and where it goes with which columns.
//
// Flags, -dry-run, -preview, logging, retry, table creation and the exit code
// all come from sdk.Run. What is here is only what is specific to this source.
package main

import (
	"net/http"
	"time"

	"github.com/AreteAcademy/bravis/sdk"
	"github.com/AreteAcademy/bravis/sdk/from"
	"github.com/AreteAcademy/bravis/sdk/to/bigquery"
)

func main() {
	sdk.Run(sdk.Pipeline{
		// Where it comes from. Configuration, and only that.
		Source: sdk.Source{
			From: from.HTTP{
				URL:     "https://api.example.com/v1/events",
				Timeout: 15 * time.Second,

				// What a response means.
				Records: func(r sdk.Response) ([]any, error) {
					if r.Status == http.StatusNoContent {
						return nil, nil // an empty window is a result, not a failure
					}
					if err := sdk.RejectIf("error")(r); err != nil {
						return nil, err
					}
					doc, err := r.Object()
					if err != nil {
						return nil, err
					}
					return sdk.ArrayAt("results")(doc)
				},
			},
		},

		// What row it builds -- every column of it, including the two the SDK
		// knows how to write. Nothing is stamped on afterwards.
		//
		// Accept says what we take from the source: a field named here that
		// the source stops sending is an error, not a column that quietly
		// goes NULL.
		Transform: []sdk.Transformer{
			sdk.Accept("id", "created_at", "kind", "amount"),

			sdk.Compute("provider", func(map[string]any) (any, error) { return "example", nil }),
			sdk.Compute("entity", func(map[string]any) (any, error) { return "events", nil }),
			sdk.Compute("source_key", func(r map[string]any) (any, error) {
				return sdk.Key("id")(r)
			}),

			// As duas do SDK, na mesma cadeia que as outras.
			sdk.IngestionID("provider", "entity", "source_key", "created_at"),
			sdk.IngestionLoadedAt(),
		},

		// Where it goes, and the columns it has. Count them: nine helpers in
		// the chain above, nine columns here. Nothing happens outside it.
		Target: sdk.Target{
			To: bigquery.Table{Name: "events"},
			Columns: []string{
				"ingestion_id",
				"ingestion_loaded_at",
				"provider",
				"entity",
				"source_key",
				"id",
				"created_at",
				"kind",
				"amount",
			},
		},
	})
}
