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

		// What row it builds. Accept says what we take from the source: a
		// field named here that the source stops sending is an error, not a
		// column that quietly goes NULL.
		Transform: []sdk.Transformer{
			sdk.Accept("id", "created_at", "kind", "amount"),
		},

		// Where it goes, and the columns it has -- the two the SDK fills in
		// included, so nothing lands in the table without being written here.
		Target: sdk.Target{
			To: bigquery.Table{
				Name: "events",
			},
			Columns: []string{
				"ingestion_id",        // from Metadata
				"ingestion_loaded_at", // from Metadata
				"id",
				"created_at",
				"kind",
				"amount",
			},
			Metadata: &sdk.Metadata{AutoID: true},
		},
	})
}
