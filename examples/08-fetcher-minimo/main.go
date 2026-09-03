// Command 08-fetcher-minimo is a whole fetcher, with nothing left out.
//
// Flags, -dry-run, logging, retry, provenance, table creation and the exit
// code all come from sdk.Rodar. What is here is only what is specific to this
// source.
package main

import (
	"time"

	"github.com/AreteAcademy/bravis/sdk"
)

func main() {
	sdk.Rodar(sdk.Pipeline{
		Fonte: sdk.Fonte{
			URL:      "https://api.example.com/v1/events",
			Timeout:  15 * time.Second,
			Guarda:   sdk.RecusarSe("error"),
			Expandir: sdk.ArrayEm("results"),
		},
		Destino: sdk.Destino{
			Provider: "example",
			Entity:   "events",
			Chave:    sdk.Chave("id"),
			Quando:   sdk.Campo("created_at"),
		},
	})
}
