#!/usr/bin/env bash
# Compila um consumidor limpo do SDK -- sem replace, sem os testes do repo,
# do jeito que alguém que faz `go get` o vê.
#
# Um argumento: a versão a exigir. Passe "local" para apontar o replace para a
# árvore de trabalho (o gate antes da tag); passe uma versão publicada para
# provar o que o proxy serve (a verificação depois da tag).
#
#   .github/scripts/consumer-check.sh local
#   .github/scripts/consumer-check.sh v0.25.0
#
# Existe num arquivo só, e não inline em dois jobs, porque as duas cópias
# ficaram para trás na API da v0.17.1 e reprovaram nove publicações seguidas.
# Eu consertei uma delas e a outra seguiu vermelha -- que é o argumento contra
# duas cópias, escrito por elas mesmas.
set -euo pipefail

MODULO="github.com/AreteAcademy/brevis/sdk"
VERSAO="${1:?uso: consumer-check.sh <versão|local>}"
ARVORE="${2:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../../sdk" && pwd)}"

DIR="$(mktemp -d)"
trap 'rm -rf "$DIR"' EXIT
cd "$DIR"

if [ "$VERSAO" = "local" ]; then
  cat > go.mod <<EOF
module example.com/consumidor

go 1.23

require $MODULO v0.0.0

replace $MODULO => $ARVORE
EOF
else
  cat > go.mod <<EOF
module example.com/consumidor

go 1.23

require $MODULO $VERSAO
EOF
fi

cat > main.go <<EOF
package main

import (
	"context"
	"fmt"
	"time"

	"$MODULO"
	"$MODULO/from"
	"$MODULO/to"
	"$MODULO/to/bigquery"
)

// Toca a porta da frente e um driver de cada lado, para que um rename que
// quebre quem chama falhe aqui e não depois da release.
func main() {
	dados, err := sdk.Extract(context.Background(), sdk.Source{
		From: from.HTTP{
			URL:     "http://x",
			PageKey: "page",
			DataKey: "results",
			// Um Applier declarado como func em vez de var nao seria
			// atribuivel aqui -- e so um consumidor de fora pega isso.
			Auth: &from.Credential{
				Value: from.FromEnv("APP_SESSION"),
				Apply: from.AsCookie,
				TTL:   time.Hour,
				Refresh: &from.Refresh{
					URL:       "http://x/session",
					ExpiresAt: from.JSONField("expires"),
					WarnAfter: 7 * 24 * time.Hour,
				},
			},
			Records: func(r sdk.Response) ([]any, error) {
				doc, err := r.Object()
				if err != nil {
					return nil, err
				}
				return sdk.ArrayAt("results")(doc)
			},
		},
		Preview: 5,
	})
	if err != nil {
		return
	}

	dados = sdk.Transform(dados,
		sdk.Accept("id"),
		sdk.Rename(map[string]string{"id": "chave"}),
		sdk.Compute("provider", func(map[string]any) (any, error) { return "p", nil }),
		sdk.Compute("entity", func(map[string]any) (any, error) { return "e", nil }),
		sdk.Compute("source_key", func(r map[string]any) (any, error) { return sdk.Key("chave")(r) }),
		sdk.IngestionID("provider", "entity", "source_key", "chave"),
		sdk.IngestionLoadedAt(),
	)

	_, _ = sdk.Load(context.Background(), dados, sdk.Target{
		To:      bigquery.Table{Dataset: "bronze", Name: "t"},
		Columns: []string{"ingestion_id", "ingestion_loaded_at", "provider", "entity", "source_key", "chave"},
		Dedup:   sdk.DedupNone,
	})

	// O outro destino, que não pode arrastar o BigQuery junto.
	_, _ = sdk.Load(context.Background(), dados, sdk.Target{To: to.Files{Path: "./saida/"}})

	env := sdk.Envelope{Provider: "p", Entity: "e", SourceKey: "k"}
	id, err := env.IngestionID()
	fmt.Println(id, err, sdk.LogLevel())
}
EOF

GOFLAGS=-mod=mod go mod tidy
go build ./...
echo "✅ consumidor limpo compila contra $VERSAO"
