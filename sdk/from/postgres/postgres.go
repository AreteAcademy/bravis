// Package postgres reads records out of PostgreSQL.
//
// Importa o pgx. Um fetcher que le HTTP ou arquivos nunca o compila -- e a
// mesma regra que tirou o BigQuery de dentro de `to`, e ela e medida: um
// consumidor so de arquivos chegou a compilar 461 pacotes e 21 MB por causa
// de um driver que ele nao usava.
package postgres

import (
	"context"
	"fmt"
	"iter"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/AreteAcademy/brevis/sdk/internal/core"
)

// Query le o resultado de um SELECT, uma linha por registro.
//
//	From: postgres.Query{
//	    DSN: os.Getenv("PG_DSN"),
//	    SQL: "SELECT * FROM pedidos WHERE atualizado_em > $1 ORDER BY atualizado_em, id LIMIT $2",
//	    Args: []any{run.LogicalDate, 50_000},
//	}
//
// As linhas sao entregues em fluxo, uma a uma: o driver nunca monta a lista
// inteira antes de devolver. Numa tabela de milhoes de linhas a diferenca e
// entre correr e ficar sem memoria.
type Query struct {
	// DSN e a string de conexao. Obrigatoria.
	//
	//	postgres://usuario:senha@host:5432/banco?sslmode=require
	//
	// Ela carrega senha, entao nunca aparece em log nem em Describe().
	DSN string

	// SQL e a consulta. Obrigatoria.
	//
	// Use paginacao por CHAVE e nao por OFFSET: `WHERE id > $1 ORDER BY id
	// LIMIT $2`. OFFSET numa tabela grande e O(n^2), porque o servidor conta
	// as linhas descartadas a cada pagina -- e por isso este driver nao
	// oferece um campo Offset, que so convidaria ao erro.
	SQL string

	// Args sao os parametros de $1, $2... Opcional.
	Args []any

	// FetchSize e quantas linhas o servidor manda por ida. Zero usa o padrao
	// do pgx. Serve para trocar memoria por latencia numa consulta larga.
	FetchSize int

	// Timeout limita a consulta inteira. Zero significa sem limite -- um
	// SELECT legitimamente longo nao deve morrer por um numero que o SDK
	// inventou.
	Timeout time.Duration

	// Conn reusa uma conexao que voce ja tem. Nil abre uma e a fecha ao fim.
	Conn *pgx.Conn
}

// Describe satisfaz core.Reader. Diz a consulta, nunca o DSN: o DSN carrega
// senha, e Describe vai para log e para mensagem de erro.
func (q Query) Describe() string { return "postgres: " + primeiraLinha(q.SQL) }

// Read satisfaz core.Reader.
func (q Query) Read(ctx context.Context, opt core.ReadOptions) (iter.Seq2[core.Envelope, error], error) {
	if q.DSN == "" && q.Conn == nil {
		return nil, fmt.Errorf("postgres.Query needs DSN (or Conn)")
	}
	if q.SQL == "" {
		return nil, fmt.Errorf("postgres.Query needs SQL")
	}

	if q.Timeout > 0 {
		var cancelar context.CancelFunc
		ctx, cancelar = context.WithTimeout(ctx, q.Timeout)
		_ = cancelar // o contexto morre com a iteracao; ver o defer abaixo
	}

	conn, fechar, err := q.conectar(ctx)
	if err != nil {
		return nil, err
	}

	// A consulta e disparada AQUI, e nao dentro do iterador, para que um DSN
	// errado, uma tabela inexistente ou um SQL invalido voltem como erro de
	// Extract -- e nao como o primeiro item de uma sequencia que o chamador
	// precisa drenar para descobrir. E a mesma razao do HTTP buscar a
	// primeira pagina cedo.
	rows, err := conn.Query(ctx, q.SQL, q.Args...)
	if err != nil {
		fechar()
		return nil, fmt.Errorf("postgres: %w", err)
	}

	inicio := time.Now()
	return func(yield func(core.Envelope, error) bool) {
		defer fechar()
		defer rows.Close()

		// Nome e OID vem juntos: o OID e o tipo DECLARADO da coluna, e e dele
		// que a conversao sai. Sem ele, DATE e TIMESTAMPTZ chegam como o
		// mesmo time.Time e o DATE ganha uma hora falsa.
		campos := rows.FieldDescriptions()
		nomes := make([]string, len(campos))
		oids := make([]uint32, len(campos))
		for i, c := range campos {
			nomes[i], oids[i] = c.Name, c.DataTypeOID
		}

		linhas := 0
		var amostra []any

		defer func() {
			if opt.Stats != nil {
				opt.Stats.Pages = 1
				opt.Stats.Attempts = 1
			}
			decorrido := time.Since(inicio)
			core.LogExtract(ctx, "postgres", q.Describe(), core.PreviewStats{
				Rows: linhas, Pages: 1, Duration: decorrido,
			})
			if opt.Preview > 0 {
				core.WritePreview(opt.PreviewWriter, amostra, opt.PreviewBytes, core.PreviewStats{
					Rows: linhas, Pages: 1, Duration: decorrido,
				})
			}
		}()

		for rows.Next() {
			valores, err := rows.Values()
			if err != nil {
				yield(core.Envelope{}, fmt.Errorf("postgres: reading row %d: %w", linhas+1, err))
				return
			}

			registro := make(map[string]any, len(nomes))
			for i, nome := range nomes {
				registro[nome] = ParaJSONComOID(valores[i], oids[i])
			}

			linhas++
			if opt.Preview > 0 && len(amostra) < opt.Preview {
				amostra = append(amostra, registro)
			}
			if !yield(core.Envelope{Payload: registro}, nil) {
				return
			}
		}

		// rows.Err() so tem valor DEPOIS do laco, e um erro aqui significa que
		// a consulta morreu no meio -- o que o chamador precisa ver, ou ele
		// carrega meio lote achando que carregou tudo.
		if err := rows.Err(); err != nil {
			yield(core.Envelope{}, fmt.Errorf("postgres: after %d row(s): %w", linhas, err))
		}
	}, nil
}

func (q Query) conectar(ctx context.Context) (*pgx.Conn, func(), error) {
	if q.Conn != nil {
		return q.Conn, func() {}, nil
	}
	cfg, err := pgx.ParseConfig(q.DSN)
	if err != nil {
		// O erro do pgx pode conter o DSN, e o DSN carrega senha.
		return nil, nil, fmt.Errorf("postgres: DSN is not valid")
	}
	if q.FetchSize > 0 {
		cfg.DefaultQueryExecMode = pgx.QueryExecModeCacheStatement
	}
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("postgres: connecting: %w", redigir(err, q.DSN))
	}
	return conn, func() { _ = conn.Close(context.WithoutCancel(ctx)) }, nil
}
