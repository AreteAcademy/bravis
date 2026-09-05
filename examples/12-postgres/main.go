// Um fetcher completo de Postgres para Postgres.
//
// Existe porque a fase 5 do plano dos drivers pede um exemplo executável por
// driver -- e o motivo é concreto: foi um exemplo que não rodava que achou o
// buraco do 03-basic-load.
//
//	docker compose -f docker-compose.drivers.yml up -d postgres
//	export PG_DSN='postgres://brevis:brevis@localhost:55432/brevis_it'
//	go run ./12-postgres -criar-tabelas
//	go run ./12-postgres
//	go run ./12-postgres          # a segunda execução carrega zero linhas
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/AreteAcademy/brevis/sdk"
	frompg "github.com/AreteAcademy/brevis/sdk/from/postgres"
	topg "github.com/AreteAcademy/brevis/sdk/to/postgres"

	"github.com/jackc/pgx/v5"
)

const (
	origem  = "exemplo_pedidos"
	destino = "landing_pedidos"
)

func main() {
	var criar bool

	sdk.Run(sdk.Pipeline{
		Name:  "exemplo_postgres",
		Flags: func(fs *flag.FlagSet) { fs.BoolVar(&criar, "criar-tabelas", false, "cria as tabelas e sai") },

		Before: func(ctx context.Context, _ *sdk.Pipeline) error {
			if !criar {
				return nil
			}
			if err := criarTabelas(ctx); err != nil {
				return err
			}
			fmt.Println("tabelas criadas; rode de novo sem -criar-tabelas")
			os.Exit(0)
			return nil
		},

		Source: sdk.Source{
			From: frompg.Query{
				DSN: os.Getenv("PG_DSN"),
				// Paginação por CHAVE. OFFSET numa tabela grande é O(n²),
				// porque o servidor conta as linhas que descarta.
				SQL: "SELECT id, nome, valor, atualizado_em FROM " + origem +
					" WHERE id > $1 ORDER BY id LIMIT $2",
				Args: []any{0, 10_000},
			},
			Preview: 3,
		},

		Transform: []sdk.Transformer{
			// A chave do ingestion_id é texto: um número e a string dele têm
			// de produzir o mesmo id.
			sdk.Compute("source_key", func(r map[string]any) (any, error) {
				return fmt.Sprint(r["id"]), nil
			}),
			sdk.Without("id"),
			sdk.Rename(map[string]string{"atualizado_em": "record_ts"}),
			sdk.Compute("provider", func(map[string]any) (any, error) { return "exemplo", nil }),
			sdk.Compute("entity", func(map[string]any) (any, error) { return "pedidos", nil }),
			sdk.IngestionID(),
			sdk.IngestionLoadedAt(),
		},

		Target: sdk.Target{
			To: topg.Table{DSN: os.Getenv("PG_DSN"), Name: destino},
			Columns: []string{
				"ingestion_id", "ingestion_loaded_at", "provider", "entity",
				"source_key", "record_ts", "nome", "valor",
			},
			// Exige índice único em ingestion_id; o -criar-tabelas o cria.
			Dedup: sdk.DedupMerge,
		},
	})
}

// criarTabelas escreve o DDL à mão, de propósito: o driver não cria tabela e
// não infere tipo, então o DDL é do consumidor -- e é ele quem sabe que
// `valor` é NUMERIC(18,2) e não um float.
func criarTabelas(ctx context.Context) error {
	conn, err := pgx.Connect(ctx, os.Getenv("PG_DSN"))
	if err != nil {
		return fmt.Errorf("conectando: %w", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	ddl := []string{
		`CREATE TABLE IF NOT EXISTS ` + origem + ` (
			id INT PRIMARY KEY, nome TEXT, valor NUMERIC(18,2), atualizado_em TIMESTAMPTZ)`,
		`INSERT INTO ` + origem + `
			SELECT g, 'pedido ' || g, (g * 1.5)::numeric, now() FROM generate_series(1, 500) g
			ON CONFLICT (id) DO NOTHING`,
		`CREATE TABLE IF NOT EXISTS ` + destino + ` (
			ingestion_id TEXT NOT NULL,
			ingestion_loaded_at TIMESTAMPTZ NOT NULL,
			provider TEXT NOT NULL,
			entity TEXT NOT NULL,
			source_key TEXT NOT NULL,
			record_ts TEXT NOT NULL,
			nome TEXT,
			valor NUMERIC(18,2))`,
		`CREATE UNIQUE INDEX IF NOT EXISTS ` + destino + `_ingestion_id
			ON ` + destino + ` (ingestion_id)`,
	}
	for _, sql := range ddl {
		if _, err := conn.Exec(ctx, sql); err != nil {
			return fmt.Errorf("ddl: %w", err)
		}
	}
	return nil
}
