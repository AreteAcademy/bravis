package redshift

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5"
)

// executor devolve como rodar SQL no cluster.
//
// O Redshift fala o protocolo do Postgres, entao o pgx serve -- e ele ja e
// dependencia do driver de Postgres. Um consumidor que importa este pacote
// paga o pgx e nao paga o driver do MySQL nem o do BigQuery.
func (t Table) executor(ctx context.Context) (SQLExecutor, func(), error) {
	if t.Executor != nil {
		return t.Executor, func() {}, nil
	}
	cfg, err := pgx.ParseConfig(t.DSN)
	if err != nil {
		return nil, nil, fmt.Errorf("redshift: DSN is not valid")
	}
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("redshift: connecting: %w", esconderDSN(err, t.DSN))
	}
	return conexao{conn}, func() { _ = conn.Close(context.WithoutCancel(ctx)) }, nil
}

type conexao struct{ conn *pgx.Conn }

func (c conexao) Exec(ctx context.Context, sql string) error {
	_, err := c.conn.Exec(ctx, sql)
	return err
}

// apagar remove o arquivo de staging.
//
// O core.Store nao tem Delete: ele foi desenhado para from.Files e to.Files,
// que nunca apagam. Em vez de acrescentar um metodo a interface -- e obrigar
// todo store de terceiro a implementa-lo por causa de um driver --, o driver
// pergunta se aquele store sabe apagar.
func (t Table) apagar(ctx context.Context, bucket, chave string) error {
	type apagador interface {
		Delete(ctx context.Context, bucket, key string) error
	}
	d, sabe := t.Store.(apagador)
	if !sabe {
		return fmt.Errorf("this store does not delete; set KeepStagedFile to silence this")
	}
	return d.Delete(ctx, bucket, chave)
}

func esconderDSN(err error, dsn string) error {
	if err == nil || dsn == "" || !strings.Contains(err.Error(), dsn) {
		return err
	}
	return fmt.Errorf("%s", strings.ReplaceAll(err.Error(), dsn, "REDACTED"))
}

// avisarSobra diz que o arquivo de staging ficou para tras.
//
// Nao derruba a carga: as linhas ja entraram, e trocar uma carga boa por um
// erro de limpeza seria trocar um problema pequeno por um grande. Mas nao fica
// calado -- um objeto orfao por execucao vira conta no fim do mes.
func avisarSobra(ctx context.Context, uri string, err error) {
	slog.WarnContext(ctx, "redshift: the staged file was left behind",
		"object", uri,
		"why", err,
		"effect", "it costs storage until something removes it")
}
