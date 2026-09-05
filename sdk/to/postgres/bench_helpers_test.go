package postgres_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func envOuPular(b *testing.B) string {
	b.Helper()
	d := os.Getenv("BREVIS_IT_PG_DSN")
	if d == "" {
		b.Skip("BREVIS_IT_PG_DSN não definida")
	}
	return d
}

func conectarBench(b *testing.B, dsn string) *pgx.Conn {
	b.Helper()
	conn, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
}

func tabelaBench(b *testing.B, conn *pgx.Conn) string {
	b.Helper()
	nome := fmt.Sprintf("bench_%d", time.Now().UnixNano())
	if _, err := conn.Exec(context.Background(), fmt.Sprintf(`CREATE TABLE %s (
		ingestion_id TEXT NOT NULL, ingestion_loaded_at TIMESTAMPTZ NOT NULL,
		provider TEXT, source_key TEXT, valor NUMERIC(18,2))`, nome)); err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _, _ = conn.Exec(context.Background(), "DROP TABLE IF EXISTS "+nome) })
	return nome
}
