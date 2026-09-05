// Package postgres writes records into PostgreSQL.
//
// Importa o pgx. Um fetcher que carrega em arquivo ou no BigQuery nunca o
// compila -- a mesma regra medida que tirou o BigQuery de dentro de `to`.
package postgres

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/AreteAcademy/brevis/sdk/internal/core"
)

// Table carrega registros numa tabela do Postgres.
//
//	To: postgres.Table{DSN: os.Getenv("PG_DSN"), Name: "landing.pedidos"}
//
// A tabela precisa EXISTIR. Este driver nao a cria e nao infere tipo nenhum:
// deduzir NUMERIC(18,2) de um float64 do encoding/json seria adivinhar, e
// adivinhar tipo e a unica coisa que este SDK decidiu nao fazer. O erro diz
// isso, e diz as colunas que o lote traz, para o DDL sair de uma leitura.
type Table struct {
	// DSN e a string de conexao. Obrigatoria, e nunca aparece em log.
	DSN string

	// Name e a tabela, com esquema: "landing.pedidos". Obrigatoria.
	Name string

	// Conn reusa uma conexao. Nil abre uma e a fecha ao fim.
	Conn *pgx.Conn
}

// Describe satisfaz core.Writer. Nomeia a tabela, nunca o DSN.
func (t Table) Describe() string { return "postgres:" + t.Name }

// Write satisfaz core.Writer.
func (t Table) Write(ctx context.Context, envelopes []core.Envelope, opt core.WriteOptions) (*core.LoadResult, error) {
	res := &core.LoadResult{Dedup: opt.Dedup, Strategy: "copy"}
	if opt.Dedup == "" {
		res.Dedup = core.DedupNone
	}
	inicio := time.Now()
	falhar := func(err error) (*core.LoadResult, error) {
		res.Duration = time.Since(inicio)
		return res, err
	}

	if t.DSN == "" && t.Conn == nil {
		return falhar(fmt.Errorf("postgres.Table needs DSN (or Conn)"))
	}
	if t.Name == "" {
		return falhar(fmt.Errorf("postgres.Table needs Name, with the schema: \"landing.pedidos\""))
	}
	if len(envelopes) == 0 {
		return falhar(nil)
	}

	// O registro e exatamente o que a cadeia de Transform compos, e a
	// declaracao e conferida contra ele inteiro -- ingestion_id incluido.
	if err := core.CheckColumns(opt.Columns, envelopes); err != nil {
		return falhar(err)
	}

	conn, fechar, err := t.conectar(ctx)
	if err != nil {
		return falhar(err)
	}
	defer fechar()

	esquema, tabela, err := partirNome(t.Name)
	if err != nil {
		return falhar(err)
	}

	colunasDaTabela, tipos, err := colunasDe(ctx, conn, esquema, tabela)
	if err != nil {
		return falhar(err)
	}
	if len(colunasDaTabela) == 0 {
		return falhar(fmt.Errorf("table %s does not exist. This driver does not create it and "+
			"does not infer types -- guessing NUMERIC(18,2) from a JSON number is the one "+
			"thing this SDK will not do. Create it with the columns the batch carries: %s",
			t.Name, strings.Join(camposDe(envelopes), ", ")))
	}

	// O que o Reconcile compra AQUI nao e o casamento posicional: o CopyFrom
	// do pgx manda a lista de colunas junto, entao valor e coluna nao se
	// desencontram como se desencontraram no INSERT ROW do BigQuery.
	//
	// O que ele compra e a recusa ANTES de tocar o servidor, com a mensagem
	// que resolve: um campo que a tabela nao tem faria o Postgres devolver
	// `column "x" of relation "y" does not exist` no meio de um COPY -- depois
	// do extract inteiro, e sem dizer o que fazer. E a ordem da tabela deixa a
	// lista de colunas estavel entre execucoes, em vez de depender da ordem em
	// que um mapa foi percorrido.
	colunas, err := core.Reconcile(colunasDaTabela, camposDe(envelopes), t.Name)
	if err != nil {
		return falhar(err)
	}

	if res.Dedup == core.DedupMerge {
		if err := conferirIndiceUnico(ctx, conn, esquema, tabela); err != nil {
			return falhar(err)
		}
		linhas, ignoradas, err := t.carregarComDedup(ctx, conn, colunas, tipos, envelopes)
		res.RowsLoaded, res.RowsIgnored = linhas, ignoradas
		return falhar(err)
	}

	linhas, err := t.copiar(ctx, conn, colunas, tipos, envelopes)
	res.RowsLoaded = linhas
	return falhar(err)
}

// copiar usa COPY FROM STDIN, que e o caminho rapido do Postgres.
//
// O pgx puxa as linhas do CopyFromSource sob demanda, entao o lote nao e
// materializado numa segunda estrutura: o que existe em memoria e o slice de
// envelopes que ja chegou, mais uma linha de cada vez.
func (t Table) copiar(ctx context.Context, conn *pgx.Conn, colunas []string, tipos map[string]string, envelopes []core.Envelope) (int64, error) {
	fonte := &linhas{colunas: colunas, tipos: tipos, envelopes: envelopes}
	n, err := conn.CopyFrom(ctx, pgx.Identifier(strings.Split(t.Name, ".")), colunas, fonte)
	if err != nil {
		if fonte.err != nil {
			return 0, fonte.err
		}
		return 0, fmt.Errorf("postgres: COPY into %s: %w", t.Name, err)
	}
	return n, nil
}

// linhas alimenta o CopyFrom uma linha por vez.
//
// Implementa pgx.CopyFromSource em vez de montar [][]any de uma vez: num lote
// de 500 mil registros a diferenca e entre um slice de ponteiros e meio giga
// de valores vivos ao mesmo tempo. O buffer de valores e reusado entre
// linhas, porque o pgx consome cada um antes de pedir o proximo.
type linhas struct {
	colunas   []string
	tipos     map[string]string
	envelopes []core.Envelope
	i         int
	buf       []any
	err       error
}

func (l *linhas) Next() bool { return l.i < len(l.envelopes) && l.err == nil }

func (l *linhas) Values() ([]any, error) {
	obj, err := core.AsObject(l.envelopes[l.i].Payload)
	if err != nil {
		l.err = fmt.Errorf("postgres: row %d: %w", l.i+1, err)
		return nil, l.err
	}
	if l.buf == nil {
		l.buf = make([]any, len(l.colunas))
	}
	for j, c := range l.colunas {
		// Ausente vira nil, que o COPY escreve como NULL.
		v, err := paraColuna(obj[c], l.tipos[c])
		if err != nil {
			l.err = fmt.Errorf("postgres: row %d, column %q: %w", l.i+1, c, err)
			return nil, l.err
		}
		l.buf[j] = v
	}
	l.i++
	return l.buf, nil
}

func (l *linhas) Err() error { return l.err }

// carregarComDedup passa por uma tabela temporaria e um ON CONFLICT DO
// NOTHING, que e o equivalente barato do MERGE do BigQuery.
//
// A temporaria e da SESSAO e some sozinha; nao ha limpeza a esquecer.
func (t Table) carregarComDedup(ctx context.Context, conn *pgx.Conn, colunas []string, tipos map[string]string, envelopes []core.Envelope) (int64, int64, error) {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("postgres: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op depois do commit

	const tmp = "brevis_stage"
	if _, err := tx.Exec(ctx, fmt.Sprintf(
		`CREATE TEMP TABLE %s (LIKE %s INCLUDING DEFAULTS) ON COMMIT DROP`, tmp, t.Name)); err != nil {
		return 0, 0, fmt.Errorf("postgres: staging table: %w", err)
	}

	fonte := &linhas{colunas: colunas, tipos: tipos, envelopes: envelopes}
	if _, err := tx.CopyFrom(ctx, pgx.Identifier{tmp}, colunas, fonte); err != nil {
		if fonte.err != nil {
			return 0, 0, fonte.err
		}
		return 0, 0, fmt.Errorf("postgres: COPY into staging: %w", err)
	}

	tag, err := tx.Exec(ctx, InsertSQL(t.Name, tmp, colunas))
	if err != nil {
		return 0, 0, fmt.Errorf("postgres: insert into %s: %w", t.Name, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, 0, fmt.Errorf("postgres: commit: %w", err)
	}

	inseridas := tag.RowsAffected()
	return inseridas, int64(len(envelopes)) - inseridas, nil
}

// InsertSQL monta o INSERT ... ON CONFLICT da dedup.
//
// Exportada e pura porque SQL montado dentro de um metodo com cliente nunca
// foi visto por um teste -- foi assim que o MERGE do BigQuery saiu com casamento
// posicional e custou a v0.12.0. As colunas sao NOMEADAS, sempre.
func InsertSQL(destino, origem string, colunas []string) string {
	nomes := make([]string, len(colunas))
	for i, c := range colunas {
		nomes[i] = citar(c)
	}
	lista := strings.Join(nomes, ", ")
	return fmt.Sprintf(
		"INSERT INTO %s (%s) SELECT %s FROM %s ON CONFLICT (%s) DO NOTHING",
		destino, lista, lista, origem, citar(core.MetadataID))
}

// citar poe aspas no identificador. Uma coluna chamada "order" ou "select" e
// legitima, e sem aspas ela vira erro de sintaxe no meio de uma carga.
func citar(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

func partirNome(nome string) (esquema, tabela string, err error) {
	partes := strings.Split(nome, ".")
	switch len(partes) {
	case 1:
		return "public", partes[0], nil
	case 2:
		return partes[0], partes[1], nil
	default:
		return "", "", fmt.Errorf("postgres.Table.Name %q has too many parts; use \"schema.table\"", nome)
	}
}

// colunasDe le o esquema real: os nomes na ordem em que a tabela os declara, e
// o tipo de cada um.
//
// A ORDEM importa porque COPY casa por posicao. Os TIPOS importam porque o
// registro do SDK e JSON, e um timestamp nele e uma string -- ver paraColuna.
func colunasDe(ctx context.Context, conn *pgx.Conn, esquema, tabela string) ([]string, map[string]string, error) {
	rows, err := conn.Query(ctx,
		`SELECT column_name, data_type FROM information_schema.columns
		 WHERE table_schema = $1 AND table_name = $2
		 ORDER BY ordinal_position`, esquema, tabela)
	if err != nil {
		return nil, nil, fmt.Errorf("postgres: reading the schema of %s.%s: %w", esquema, tabela, err)
	}
	defer rows.Close()

	var out []string
	tipos := map[string]string{}
	for rows.Next() {
		var c, tipo string
		if err := rows.Scan(&c, &tipo); err != nil {
			return nil, nil, err
		}
		out = append(out, c)
		tipos[c] = tipo
	}
	return out, tipos, rows.Err()
}

// conferirIndiceUnico exige o indice, e nao o cria.
//
// Um loader que sabe criar indice sabe travar uma tabela de producao no meio
// do expediente. A recusa nomeia o indice que falta e mostra o comando.
func conferirIndiceUnico(ctx context.Context, conn *pgx.Conn, esquema, tabela string) error {
	var existe bool
	err := conn.QueryRow(ctx,
		`SELECT EXISTS (
		   SELECT 1 FROM pg_index i
		   JOIN pg_class c   ON c.oid = i.indrelid
		   JOIN pg_namespace n ON n.oid = c.relnamespace
		   JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum = ANY(i.indkey)
		   WHERE n.nspname = $1 AND c.relname = $2
		     AND i.indisunique AND i.indnatts = 1 AND a.attname = $3)`,
		esquema, tabela, core.MetadataID).Scan(&existe)
	if err != nil {
		return fmt.Errorf("postgres: checking the unique index: %w", err)
	}
	if !existe {
		return fmt.Errorf("dedup needs a unique index on %s, and %s.%s does not have one -- "+
			"without it ON CONFLICT has nothing to match and every run would insert duplicates. "+
			"This driver does not create indexes, because a loader that can create one can lock "+
			"a production table: CREATE UNIQUE INDEX CONCURRENTLY ON %s.%s (%s)",
			core.MetadataID, esquema, tabela, esquema, tabela, core.MetadataID)
	}
	return nil
}

// camposDe devolve a uniao ordenada dos campos do lote.
func camposDe(envelopes []core.Envelope) []string {
	vistos := map[string]bool{}
	for _, e := range envelopes {
		obj, err := core.AsObject(e.Payload)
		if err != nil {
			continue
		}
		for k := range obj {
			vistos[k] = true
		}
	}
	out := make([]string, 0, len(vistos))
	for k := range vistos {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (t Table) conectar(ctx context.Context) (*pgx.Conn, func(), error) {
	if t.Conn != nil {
		return t.Conn, func() {}, nil
	}
	cfg, err := pgx.ParseConfig(t.DSN)
	if err != nil {
		return nil, nil, fmt.Errorf("postgres: DSN is not valid")
	}
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("postgres: connecting: %w", esconderDSN(err, t.DSN))
	}
	return conn, func() { _ = conn.Close(context.WithoutCancel(ctx)) }, nil
}

func esconderDSN(err error, dsn string) error {
	if err == nil || dsn == "" || !strings.Contains(err.Error(), dsn) {
		return err
	}
	return fmt.Errorf("%s", strings.ReplaceAll(err.Error(), dsn, "REDACTED"))
}

// CheckDestination satisfaz core.DestinationChecker: confere a declaracao
// contra a tabela real, antes de a extracao acontecer.
//
// Num vendor com cota, a diferenca entre conferir aqui e conferir no Write e a
// diferenca entre uma consulta ao information_schema e a janela inteira de
// quota gasta para descobrir que uma coluna nao bate.
//
// Uma tabela que nao existe NAO e erro aqui, e sim no Write -- que e onde a
// mensagem tambem lista as colunas do lote, para o DDL sair de uma leitura.
func (t Table) CheckDestination(ctx context.Context, columns []string) error {
	if len(columns) == 0 || (t.DSN == "" && t.Conn == nil) || t.Name == "" {
		return nil
	}

	conn, fechar, err := t.conectar(ctx)
	if err != nil {
		return err
	}
	defer fechar()

	esquema, tabela, err := partirNome(t.Name)
	if err != nil {
		return err
	}
	daTabela, _, err := colunasDe(ctx, conn, esquema, tabela)
	if err != nil || len(daTabela) == 0 {
		return err
	}

	tem := make(map[string]bool, len(daTabela))
	for _, c := range daTabela {
		tem[c] = true
	}
	var ausentes []string
	for _, c := range columns {
		if !tem[c] {
			ausentes = append(ausentes, c)
		}
	}
	if len(ausentes) == 0 {
		return nil
	}
	sort.Strings(ausentes)
	return fmt.Errorf("the declaration lists %s, which %s does not have. The table has: %s. "+
		"Caught before the extract, so no source quota was spent",
		strings.Join(ausentes, ", "), t.Name, strings.Join(daTabela, ", "))
}
