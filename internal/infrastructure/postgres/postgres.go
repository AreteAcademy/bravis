// Package postgres e o adaptador de persistencia.
//
// O plano (secao 22) define o banco como fonte da verdade operacional. Aqui vive
// so o encanamento: pool de conexoes, migrations e o health check. As queries de
// dominio entram nas fases que as usam.
package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/zarvhq/bravis/migrations"
)

// Pool envolve o pgxpool. O tipo existe para que o resto do sistema dependa de
// algo nosso, e nao do driver diretamente.
type Pool struct {
	*pgxpool.Pool
}

// New abre o pool e verifica a conexao antes de devolver. Um pool que so falha
// no primeiro uso transforma erro de configuracao em erro de request.
func New(ctx context.Context, url string) (*Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse da BRAVIS_DATABASE_URL: %w", err)
	}
	cfg.MaxConnLifetime = time.Hour
	cfg.HealthCheckPeriod = 30 * time.Second

	p, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("abrindo o pool: %w", err)
	}

	ctxPing, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := p.Ping(ctxPing); err != nil {
		p.Close()
		return nil, fmt.Errorf("conectando ao postgres: %w", err)
	}
	return &Pool{Pool: p}, nil
}

// Check e o contrato de health: um ping com prazo. Sem timeout, um banco lento
// faria o readiness pendurar em vez de reprovar.
func (p *Pool) Check(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return p.Ping(ctx)
}

// Migrate aplica as migrations embutidas. Roda pelo subcomando `bravis migrate`,
// nunca no `serve`: subir a aplicacao e migrar o schema tem blast radius
// diferente, e juntar as duas faz um restart casual virar um DDL.
func Migrate(ctx context.Context, url, direcao string) error {
	cfg, err := pgx.ParseConfig(url)
	if err != nil {
		return fmt.Errorf("parse da BRAVIS_DATABASE_URL: %w", err)
	}

	db := stdlib.OpenDB(*cfg)
	defer db.Close()

	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}

	// "." porque o embed.FS tem a raiz no proprio diretorio migrations/.
	const dir = "."
	switch direcao {
	case "up":
		return goose.UpContext(ctx, db, dir)
	case "down":
		return goose.DownContext(ctx, db, dir)
	case "status":
		return goose.StatusContext(ctx, db, dir)
	default:
		return fmt.Errorf("direcao desconhecida: %q (use up, down ou status)", direcao)
	}
}
