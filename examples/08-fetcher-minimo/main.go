// Command 08-fetcher-minimo is a whole fetcher, with nothing left out.
//
// Flags, -dry-run, -preview, logging, retry, table creation and the exit code
// all come from sdk.Run. What is here is only what is specific to this source.
package main

import (
	"time"

	"github.com/AreteAcademy/bravis/sdk"
)

func main() {
	sdk.Run(sdk.Pipeline{
		Source: sdk.Source{
			URL:     "https://api.example.com/v1/events",
			Timeout: 15 * time.Second,
			Guard:   sdk.RejectIf("error"),
			Expand:  sdk.ArrayAt("results"),
		},

		// The columns of the destination table, and the only place they are
		// decided. Read this line and you know what the table holds.
		//
		// A field named here that the source stops sending is an error, not a
		// column that quietly goes NULL.
		Transform: []sdk.Transformer{
			sdk.Schema("id", "created_at", "kind", "amount"),
		},

		// Where it goes. Nothing about the shape, because the shape is above.
		Target: sdk.Target{Table: "events"},
	})
}
