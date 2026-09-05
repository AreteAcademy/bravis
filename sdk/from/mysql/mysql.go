// Package mysql reads records out of MySQL.
//
// Importa o driver do MySQL sobre database/sql. Um fetcher que le HTTP,
// arquivos ou Postgres nunca o compila.
package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"iter"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql" // registra o driver "mysql"

	"github.com/AreteAcademy/brevis/sdk/internal/core"
)

// Query le o resultado de um SELECT, uma linha por registro.
//
//	From: mysql.Query{
//	    DSN: os.Getenv("MYSQL_DSN"),
//	    SQL: "SELECT * FROM pedidos WHERE id > ? ORDER BY id LIMIT ?",
//	    Args: []any{ultimoID, 50000},
//	}
//
// O driver acrescenta `parseTime=true` ao DSN se voce esquecer. O resultado e
// o mesmo sem ele -- ha caminho para o texto cru --, mas com ele o instante
// nao precisa ser reparseado em Go uma vez por linha.
type Query struct {
	// DSN e a string de conexao, no formato do driver:
	//
	//	usuario:senha@tcp(host:3306)/banco
	//
	// Carrega senha, entao nunca aparece em log nem em Describe().
	DSN string

	// SQL e a consulta. Os parametros sao `?`, e nao $1.
	//
	// Pagine por CHAVE e nao por OFFSET: OFFSET numa tabela grande e O(n^2),
	// porque o servidor conta as linhas descartadas a cada pagina.
	SQL string

	// Args sao os parametros de `?`. Opcional.
	Args []any

	// Timeout limita a consulta inteira. Zero significa sem limite.
	Timeout time.Duration

	// DB reusa um pool que voce ja tem. Nil abre um e o fecha ao fim.
	DB *sql.DB
}

// Describe satisfaz core.Reader. Diz a consulta, nunca o DSN.
func (q Query) Describe() string { return "mysql: " + resumir(q.SQL) }

// Read satisfaz core.Reader.
func (q Query) Read(ctx context.Context, opt core.ReadOptions) (iter.Seq2[core.Envelope, error], error) {
	if q.DSN == "" && q.DB == nil {
		return nil, fmt.Errorf("mysql.Query needs DSN (or DB)")
	}
	if q.SQL == "" {
		return nil, fmt.Errorf("mysql.Query needs SQL")
	}

	if q.Timeout > 0 {
		var cancelar context.CancelFunc
		ctx, cancelar = context.WithTimeout(ctx, q.Timeout)
		defer cancelar()
	}

	db, fechar, err := q.abrir()
	if err != nil {
		return nil, err
	}

	// A consulta e disparada aqui, e nao dentro do iterador: um DSN errado ou
	// uma tabela inexistente voltam como erro de Extract, e nao como o
	// primeiro item de uma sequencia que o chamador precisa drenar.
	rows, err := db.QueryContext(ctx, q.SQL, q.Args...)
	if err != nil {
		fechar()
		return nil, fmt.Errorf("mysql: %w", esconderDSN(err, q.DSN))
	}

	tipos, err := rows.ColumnTypes()
	if err != nil {
		_ = rows.Close()
		fechar()
		return nil, fmt.Errorf("mysql: reading column types: %w", err)
	}

	inicio := time.Now()
	return func(yield func(core.Envelope, error) bool) {
		defer fechar()
		defer func() { _ = rows.Close() }()

		nomes := make([]string, len(tipos))
		declarados := make([]string, len(tipos))
		for i, t := range tipos {
			nomes[i], declarados[i] = t.Name(), strings.ToUpper(t.DatabaseTypeName())
		}

		// Os destinos sao reusados entre linhas: um Scan por linha alocando
		// N ponteiros e N valores multiplicaria por dois o custo de cada
		// registro, e o Scan copia para os alvos antes de devolver.
		alvos := make([]any, len(tipos))
		celulas := make([]any, len(tipos))
		for i := range alvos {
			alvos[i] = &celulas[i]
		}

		linhas := 0
		var amostra []any

		defer func() {
			if opt.Stats != nil {
				opt.Stats.Pages, opt.Stats.Attempts = 1, 1
			}
			decorrido := time.Since(inicio)
			core.LogExtract(ctx, "mysql", q.Describe(), core.PreviewStats{
				Rows: linhas, Pages: 1, Duration: decorrido,
			})
			if opt.Preview > 0 {
				core.WritePreview(opt.PreviewWriter, amostra, opt.PreviewBytes, core.PreviewStats{
					Rows: linhas, Pages: 1, Duration: decorrido,
				})
			}
		}()

		for rows.Next() {
			if err := rows.Scan(alvos...); err != nil {
				yield(core.Envelope{}, fmt.Errorf("mysql: reading row %d: %w", linhas+1, err))
				return
			}

			registro := make(map[string]any, len(nomes))
			for i, nome := range nomes {
				registro[nome] = ParaJSON(celulas[i], declarados[i])
			}

			linhas++
			if opt.Preview > 0 && len(amostra) < opt.Preview {
				amostra = append(amostra, registro)
			}
			if !yield(core.Envelope{Payload: registro}, nil) {
				return
			}
		}

		if err := rows.Err(); err != nil {
			yield(core.Envelope{}, fmt.Errorf("mysql: after %d row(s): %w", linhas, err))
		}
	}, nil
}

func (q Query) abrir() (*sql.DB, func(), error) {
	if q.DB != nil {
		return q.DB, func() {}, nil
	}
	db, err := sql.Open("mysql", ComParseTime(q.DSN))
	if err != nil {
		return nil, nil, fmt.Errorf("mysql: DSN is not valid")
	}
	return db, func() { _ = db.Close() }, nil
}

// ComParseTime garante parseTime=true no DSN.
//
// Sem ele o driver devolve DATETIME e TIMESTAMP como []byte. O ParaJSON tem
// caminho para isso e produz o mesmo RFC 3339 -- entao o resultado nao muda, e
// ha teste provando que nao muda.
//
// O que muda e o CUSTO: sem parseTime, cada instante e reparseado de texto em
// Go, uma vez por linha, depois de o driver ja ter feito o trabalho. Numa
// carga de centenas de milhares de linhas isso e uma alocacao e um parse por
// registro, de graca.
//
// Acrescentar e melhor que recusar: quem esqueceu nao tem como saber que era
// isso, e o resultado seria identico de qualquer forma.
func ComParseTime(dsn string) string {
	if strings.Contains(dsn, "parseTime=") {
		return dsn
	}
	if strings.Contains(dsn, "?") {
		return dsn + "&parseTime=true"
	}
	return dsn + "?parseTime=true"
}

func resumir(sql string) string {
	s := strings.TrimSpace(sql)
	if i := strings.IndexAny(s, "\n\r"); i >= 0 {
		s = strings.TrimSpace(s[:i]) + " …"
	}
	if len(s) > 120 {
		s = s[:117] + "…"
	}
	return s
}

func esconderDSN(err error, dsn string) error {
	if err == nil || dsn == "" || !strings.Contains(err.Error(), dsn) {
		return err
	}
	return fmt.Errorf("%s", strings.ReplaceAll(err.Error(), dsn, "REDACTED"))
}
