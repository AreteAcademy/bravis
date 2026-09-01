// Package migrations embute o SQL de schema no binario.
//
// O embed vive aqui, e nao no pacote postgres, porque `//go:embed` so alcanca
// arquivos do proprio diretorio do pacote — nao aceita `../`.
package migrations

import "embed"

// FS contem as migrations versionadas, aplicadas pelo goose.
//
//go:embed *.sql
var FS embed.FS
