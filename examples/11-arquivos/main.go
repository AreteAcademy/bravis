// Command 11-arquivos lê arquivos e escreve arquivos, sem nuvem nenhuma.
//
// Roda de primeira:
//
//	go run ./11-arquivos
//
// O mesmo pipeline atende S3 e GCS trocando uma linha -- o esquema do caminho
// diz o backend, e o Store é passado em vez de escolhido dentro do driver:
//
//	from.Files{Path: "s3://bucket/dia=1/*.ndjson", Store: s3.New(cliente)}
//	to.Files{Path: "gs://bucket/landing/", Store: gcs.New(cliente)}
//
// É isso que faz este programa não compilar uma linha da AWS nem do Google.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/AreteAcademy/bravis/sdk"
	"github.com/AreteAcademy/bravis/sdk/from"
	"github.com/AreteAcademy/bravis/sdk/to"
)

func main() {
	dir, err := os.MkdirTemp("", "bravis-arquivos-*")
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	entrada := filepath.Join(dir, "entrada")
	saida := filepath.Join(dir, "saida")
	if err := os.MkdirAll(entrada, 0o750); err != nil {
		log.Fatal(err)
	}

	// Duas "extrações" que já existiam em disco.
	semear(entrada, "2026-09-03.ndjson", `{"sku":"W-1","quantidade":3,"lixo":"x"}
{"sku":"W-2","quantidade":9,"lixo":"y"}`)
	semear(entrada, "2026-09-04.ndjson", `{"sku":"W-3","quantidade":1,"lixo":"z"}`)

	ctx := context.Background()

	// De onde vem. Os arquivos são lidos em ordem, sempre: um Key posicional
	// depende disso, e sem ordem o ingestion_id mudaria entre execuções.
	dados, err := sdk.Extract(ctx, sdk.Source{
		From:    from.Files{Path: filepath.Join(entrada, "*.ndjson")},
		Preview: 5,
	})
	if err != nil {
		log.Fatalf("extract: %v", err)
	}

	// Que linha monta. "lixo" não é declarado, então não sai.
	dados = sdk.Transform(dados, sdk.Accept("sku", "quantidade"))

	// Para onde vai, e com que colunas.
	res, err := sdk.Load(ctx, dados, sdk.Target{
		To: to.Files{
			Path:        saida + "/",
			PartitionBy: "ingestion_loaded_at",
			Compress:    true,
		},
		Columns:  []string{"ingestion_id", "ingestion_loaded_at", "sku", "quantidade"},
		Metadata: &sdk.Metadata{AutoID: true},
	})
	if err != nil {
		log.Fatalf("load: %v", err)
	}

	fmt.Println(res)
	fmt.Println("\nescrito em", saida)
	_ = filepath.Walk(saida, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			fmt.Printf("  %s  (%d bytes)\n", p[len(saida)+1:], info.Size())
		}
		return nil
	})
}

func semear(dir, nome, conteudo string) {
	if err := os.WriteFile(filepath.Join(dir, nome), []byte(conteudo+"\n"), 0o600); err != nil {
		log.Fatal(err)
	}
}
