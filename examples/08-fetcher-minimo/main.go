// Command 08-fetcher-minimo is a whole fetcher, with nothing left out.
//
// Flags, -dry-run, logging, retry, provenance, table creation and the exit
// code all come from sdk.Run. What is here is only what is specific to this
// source.
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
		Target: sdk.Target{
			Provider: "example",
			Entity:   "events",
			Key:      sdk.Key("id"),
			When:     sdk.Field("created_at"),
		},
	})
}
