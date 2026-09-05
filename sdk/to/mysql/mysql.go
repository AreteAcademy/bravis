// Package mysql writes records into MySQL.
package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql" // registra o driver "mysql"

	"github.com/AreteAcademy/brevis/sdk/internal/core"
)

// Table carrega registros numa tabela do MySQL.
//
//	To: mysql.Table{DSN: os.Getenv("MYSQL_DSN"), Name: "landing.pedidos"}
//
// A tabela precisa EXISTIR. Este driver nao a cria e nao infere tipo: deduzir
// DECIMAL(18,2) de um numero JSON e adivinhar, e adivinhar tipo e a unica
// coisa que este SDK decidiu nao fazer.
type Table struct {
	// DSN e a string de conexao. Obrigatoria, e nunca aparece em log.
	DSN string

	// Name e a tabela, com o banco quando ele nao for o do DSN.
	Name string

	// BatchSize e quantas linhas vao por INSERT. Zero usa 1000.
	//
	// Este campo existe aqui e NAO existe no driver do Postgres, e a diferenca
	// e real: o Postgres tem COPY FROM STDIN, que manda tudo em fluxo. O MySQL
	// nao tem equivalente confiavel -- LOAD DATA LOCAL INFILE costuma vir
	// desabilitado no servidor e no cliente --, entao a carga e INSERT
	// multi-linha, e o tamanho do lote e uma escolha de quem carrega:
	// pacotes grandes esbarram em max_allowed_packet.
	BatchSize int

	// DB reusa um pool. Nil abre um e o fecha ao fim.
	DB *sql.DB
}

const loteDefault = 1000

// Describe satisfaz core.Writer. Nomeia a tabela, nunca o DSN.
func (t Table) Describe() string { return "mysql:" + t.Name }

// Write satisfaz core.Writer.
func (t Table) Write(ctx context.Context, envelopes []core.Envelope, opt core.WriteOptions) (*core.LoadResult, error) {
	res := &core.LoadResult{Dedup: opt.Dedup, Strategy: "insert"}
	if opt.Dedup == "" {
		res.Dedup = core.DedupNone
	}
	inicio := time.Now()
	falhar := func(err error) (*core.LoadResult, error) {
		res.Duration = time.Since(inicio)
		return res, err
	}

	if t.DSN == "" && t.DB == nil {
		return falhar(fmt.Errorf("mysql.Table needs DSN (or DB)"))
	}
	if t.Name == "" {
		return falhar(fmt.Errorf("mysql.Table needs Name"))
	}
	if len(envelopes) == 0 {
		return falhar(nil)
	}
	if err := core.CheckColumns(opt.Columns, envelopes); err != nil {
		return falhar(err)
	}

	db, fechar, err := t.abrir()
	if err != nil {
		return falhar(err)
	}
	defer fechar()

	banco, tabela := partirNome(t.Name)
	colunasDaTabela, tipos, err := colunasDe(ctx, db, banco, tabela)
	if err != nil {
		return falhar(err)
	}
	if len(colunasDaTabela) == 0 {
		return falhar(fmt.Errorf("table %s does not exist. This driver does not create it and "+
			"does not infer types -- guessing DECIMAL(18,2) from a JSON number is the one thing "+
			"this SDK will not do. Create it with the columns the batch carries: %s",
			t.Name, strings.Join(camposDe(envelopes), ", ")))
	}

	colunas, err := core.Reconcile(colunasDaTabela, camposDe(envelopes), t.Name)
	if err != nil {
		return falhar(err)
	}

	if res.Dedup == core.DedupMerge {
		if err := conferirIndiceUnico(ctx, db, banco, tabela); err != nil {
			return falhar(err)
		}
	}

	linhas, err := t.inserir(ctx, db, colunas, tipos, envelopes, res.Dedup == core.DedupMerge)
	res.RowsLoaded = linhas
	res.RowsIgnored = int64(len(envelopes)) - linhas
	if res.Dedup != core.DedupMerge {
		res.RowsIgnored = 0
	}
	return falhar(err)
}

// inserir manda INSERT multi-linha em lotes, dentro de uma transacao.
//
// O SQL e montado uma vez por lote e reusado; os argumentos vao num slice
// reaproveitado. Montar a string por linha custaria uma concatenacao por
// registro, que numa carga de centenas de milhares e o custo dominante.
func (t Table) inserir(ctx context.Context, db *sql.DB, colunas []string, tipos map[string]string, envelopes []core.Envelope, ignorar bool) (int64, error) {
	tamanho := t.BatchSize
	if tamanho <= 0 {
		tamanho = loteDefault
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("mysql: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // no-op depois do commit

	var total int64
	args := make([]any, 0, tamanho*len(colunas))

	for inicio := 0; inicio < len(envelopes); inicio += tamanho {
		fim := min(inicio+tamanho, len(envelopes))
		bloco := envelopes[inicio:fim]

		args = args[:0]
		for i, e := range bloco {
			obj, err := core.AsObject(e.Payload)
			if err != nil {
				return total, fmt.Errorf("mysql: row %d: %w", inicio+i+1, err)
			}
			for _, c := range colunas {
				v, err := paraColuna(obj[c], tipos[c])
				if err != nil {
					return total, fmt.Errorf("mysql: row %d, column %q: %w", inicio+i+1, c, err)
				}
				args = append(args, v)
			}
		}

		tag, err := tx.ExecContext(ctx, InsertSQL(t.Name, colunas, len(bloco), ignorar), args...)
		if err != nil {
			return total, fmt.Errorf("mysql: insert into %s: %w", t.Name, err)
		}
		n, err := tag.RowsAffected()
		if err != nil {
			return total, fmt.Errorf("mysql: rows affected: %w", err)
		}
		total += n
	}

	if err := tx.Commit(); err != nil {
		return total, fmt.Errorf("mysql: commit: %w", err)
	}
	return total, nil
}

// InsertSQL monta o INSERT multi-linha.
//
// Exportada e pura porque SQL montado dentro de um metodo com cliente nunca foi
// visto por um teste -- foi assim que o MERGE do BigQuery saiu com casamento
// posicional e custou a v0.12.0. As colunas sao NOMEADAS, sempre.
//
// `ignorar` vira INSERT IGNORE, que e a dedup do MySQL: com indice unico em
// ingestion_id, a linha repetida e descartada em vez de derrubar o lote.
func InsertSQL(tabela string, colunas []string, linhas int, ignorar bool) string {
	nomes := make([]string, len(colunas))
	for i, c := range colunas {
		nomes[i] = citar(c)
	}

	umaLinha := "(" + strings.TrimSuffix(strings.Repeat("?,", len(colunas)), ",") + ")"
	valores := make([]string, linhas)
	for i := range valores {
		valores[i] = umaLinha
	}

	verbo := "INSERT"
	if ignorar {
		verbo = "INSERT IGNORE"
	}
	return fmt.Sprintf("%s INTO %s (%s) VALUES %s",
		verbo, qualificar(tabela), strings.Join(nomes, ", "), strings.Join(valores, ", "))
}

// citar usa crase, que e o delimitador do MySQL. Uma coluna chamada `order` e
// legitima, e sem crase ela vira erro de sintaxe no meio de uma carga.
func citar(s string) string { return "`" + strings.ReplaceAll(s, "`", "``") + "`" }

// qualificar cita cada parte de "banco.tabela" separadamente: citar o nome
// inteiro criaria uma tabela chamada "banco.tabela".
func qualificar(nome string) string {
	partes := strings.Split(nome, ".")
	for i, p := range partes {
		partes[i] = citar(p)
	}
	return strings.Join(partes, ".")
}

func partirNome(nome string) (banco, tabela string) {
	if i := strings.Index(nome, "."); i >= 0 {
		return nome[:i], nome[i+1:]
	}
	return "", nome
}

// colunasDe le o esquema real: os nomes na ordem declarada e o tipo de cada um.
func colunasDe(ctx context.Context, db *sql.DB, banco, tabela string) ([]string, map[string]string, error) {
	// DATABASE() cobre o caso de o banco vir do DSN e nao do Name.
	rows, err := db.QueryContext(ctx,
		`SELECT column_name, data_type FROM information_schema.columns
		 WHERE table_schema = COALESCE(NULLIF(?, ''), DATABASE()) AND table_name = ?
		 ORDER BY ordinal_position`, banco, tabela)
	if err != nil {
		return nil, nil, fmt.Errorf("mysql: reading the schema of %s: %w", tabela, err)
	}
	defer func() { _ = rows.Close() }()

	var out []string
	tipos := map[string]string{}
	for rows.Next() {
		var c, tipo string
		if err := rows.Scan(&c, &tipo); err != nil {
			return nil, nil, err
		}
		out = append(out, c)
		tipos[c] = strings.ToLower(tipo)
	}
	return out, tipos, rows.Err()
}

// conferirIndiceUnico exige o indice, e nao o cria.
func conferirIndiceUnico(ctx context.Context, db *sql.DB, banco, tabela string) error {
	var n int
	err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM information_schema.statistics s
		 WHERE s.table_schema = COALESCE(NULLIF(?, ''), DATABASE())
		   AND s.table_name = ? AND s.non_unique = 0 AND s.column_name = ?
		   AND s.seq_in_index = 1
		   AND (SELECT COUNT(*) FROM information_schema.statistics x
		        WHERE x.table_schema = s.table_schema AND x.table_name = s.table_name
		          AND x.index_name = s.index_name) = 1`,
		banco, tabela, core.MetadataID).Scan(&n)
	if err != nil {
		return fmt.Errorf("mysql: checking the unique index: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("dedup needs a unique index on %s, and %s does not have one -- "+
			"without it INSERT IGNORE has nothing to match and every run would insert "+
			"duplicates. This driver does not create indexes, because a loader that can "+
			"create one can lock a production table: "+
			"CREATE UNIQUE INDEX idx_%s ON %s (%s)",
			core.MetadataID, tabela, core.MetadataID, tabela, core.MetadataID)
	}
	return nil
}

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

func (t Table) abrir() (*sql.DB, func(), error) {
	if t.DB != nil {
		return t.DB, func() {}, nil
	}
	db, err := sql.Open("mysql", comParseTime(t.DSN))
	if err != nil {
		return nil, nil, fmt.Errorf("mysql: DSN is not valid")
	}
	return db, func() { _ = db.Close() }, nil
}

func comParseTime(dsn string) string {
	if strings.Contains(dsn, "parseTime=") {
		return dsn
	}
	if strings.Contains(dsn, "?") {
		return dsn + "&parseTime=true"
	}
	return dsn + "?parseTime=true"
}
