package mysql_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	frommy "github.com/AreteAcademy/brevis/sdk/from/mysql"
	tomy "github.com/AreteAcademy/brevis/sdk/to/mysql"

	"github.com/AreteAcademy/brevis/sdk"
)

// docker compose -f docker-compose.drivers.yml up -d mysql
// BREVIS_IT_MYSQL_DSN='root:brevis@tcp(localhost:53306)/brevis_it' go test ./sdk/to/mysql/
func dsn(t *testing.T) string {
	t.Helper()
	d := os.Getenv("BREVIS_IT_MYSQL_DSN")
	if d == "" {
		t.Skip("BREVIS_IT_MYSQL_DSN não definida; suba o docker-compose.drivers.yml")
	}
	return d
}

func abrir(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("mysql", frommy.ComParseTime(dsn(t)))
	if err != nil {
		t.Fatalf("abrindo: %v", err)
	}
	if err := db.Ping(); err != nil {
		t.Fatalf("ping: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func tabela(t *testing.T, db *sql.DB, ddl string) string {
	t.Helper()
	nome := fmt.Sprintf("t_%d", time.Now().UnixNano())
	if _, err := db.Exec(fmt.Sprintf("CREATE TABLE %s (%s)", nome, ddl)); err != nil {
		t.Fatalf("criando %s: %v", nome, err)
	}
	t.Cleanup(func() { _, _ = db.Exec("DROP TABLE IF EXISTS " + nome) })
	return nome
}

const colunasPadrao = "" +
	"ingestion_id VARCHAR(36) NOT NULL," +
	"ingestion_loaded_at DATETIME(6) NOT NULL," +
	"provider VARCHAR(64)," +
	"source_key VARCHAR(64)," +
	"valor DECIMAL(18,2)"

func lote(n int) []sdk.Envelope {
	out := make([]sdk.Envelope, n)
	agora := time.Now().UTC().Format(time.RFC3339)
	for i := range out {
		out[i] = sdk.Envelope{Payload: map[string]any{
			"ingestion_id":        fmt.Sprintf("id-%04d", i),
			"ingestion_loaded_at": agora,
			"provider":            "teste",
			"source_key":          fmt.Sprintf("k%d", i),
			"valor":               "10.50",
		}}
	}
	return out
}

// TestIntegrationUmaLinhaRealmenteEntra: os testes em memória provam os bytes
// que montamos, não o que o servidor aceita.
func TestIntegrationUmaLinhaRealmenteEntra(t *testing.T) {
	db := abrir(t)
	nome := tabela(t, db, colunasPadrao)

	res, err := tomy.Table{DSN: dsn(t), Name: nome}.Write(
		context.Background(), lote(3), sdk.WriteOptions{})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if res.RowsLoaded != 3 {
		t.Errorf("RowsLoaded = %d, esperado 3", res.RowsLoaded)
	}

	var n int
	if err := db.QueryRow("SELECT count(*) FROM " + nome).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("o servidor tem %d linhas", n)
	}
}

// TestIntegrationOrdemDaTabela: cada valor tem de pousar na coluna certa
// quando a ordem da tabela não é a do registro.
func TestIntegrationOrdemDaTabela(t *testing.T) {
	db := abrir(t)
	nome := tabela(t, db, "valor DECIMAL(18,2), provider VARCHAR(64), "+
		"ingestion_loaded_at DATETIME(6) NOT NULL, ingestion_id VARCHAR(36) NOT NULL")

	l := []sdk.Envelope{{Payload: map[string]any{
		"ingestion_id":        "abc",
		"ingestion_loaded_at": time.Now().UTC().Format(time.RFC3339),
		"provider":            "acme",
		"valor":               "99.90",
	}}}
	if _, err := (tomy.Table{DSN: dsn(t), Name: nome}).Write(
		context.Background(), l, sdk.WriteOptions{}); err != nil {
		t.Fatalf("Write: %v", err)
	}

	var id, prov, valor string
	if err := db.QueryRow("SELECT ingestion_id, provider, valor FROM "+nome).
		Scan(&id, &prov, &valor); err != nil {
		t.Fatal(err)
	}
	if id != "abc" || prov != "acme" || valor != "99.90" {
		t.Errorf("valores trocados: id=%q provider=%q valor=%q", id, prov, valor)
	}
}

// TestIntegrationDedupCarregaOMesmoLoteDuasVezes é o critério de pronto da
// fase 3: o mesmo pipeline da fase 2, com uma linha trocada.
func TestIntegrationDedupCarregaOMesmoLoteDuasVezes(t *testing.T) {
	db := abrir(t)
	nome := tabela(t, db, colunasPadrao)
	if _, err := db.Exec(fmt.Sprintf("CREATE UNIQUE INDEX u ON %s (ingestion_id)", nome)); err != nil {
		t.Fatal(err)
	}

	destino := tomy.Table{DSN: dsn(t), Name: nome}
	l := lote(5)
	opt := sdk.WriteOptions{Dedup: sdk.DedupMerge}

	primeira, err := destino.Write(context.Background(), l, opt)
	if err != nil {
		t.Fatalf("primeira: %v", err)
	}
	if primeira.RowsLoaded != 5 {
		t.Errorf("primeira: %d carregadas", primeira.RowsLoaded)
	}

	segunda, err := destino.Write(context.Background(), l, opt)
	if err != nil {
		t.Fatalf("segunda: %v", err)
	}
	if segunda.RowsLoaded != 0 || segunda.RowsIgnored != 5 {
		t.Errorf("segunda: %d carregadas, %d ignoradas -- esperava 0 e 5",
			segunda.RowsLoaded, segunda.RowsIgnored)
	}

	var n int
	if err := db.QueryRow("SELECT count(*) FROM " + nome).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 5 {
		t.Errorf("a tabela tem %d linhas depois de duas cargas do mesmo lote", n)
	}
}

// TestIntegrationDedupSemIndiceRecusa: sem índice único, INSERT IGNORE não tem
// o que casar e toda execução inseriria duplicatas.
func TestIntegrationDedupSemIndiceRecusa(t *testing.T) {
	db := abrir(t)
	nome := tabela(t, db, colunasPadrao)

	_, err := tomy.Table{DSN: dsn(t), Name: nome}.Write(
		context.Background(), lote(1), sdk.WriteOptions{Dedup: sdk.DedupMerge})
	if err == nil {
		t.Fatal("dedup sem índice único passou")
	}
	if !strings.Contains(err.Error(), "CREATE UNIQUE INDEX") {
		t.Errorf("o erro não diz o comando: %v", err)
	}
}

// TestIntegrationCampoQueATabelaNaoTemRecusa: recusar ANTES do servidor, com a
// saída escrita.
func TestIntegrationCampoQueATabelaNaoTemRecusa(t *testing.T) {
	db := abrir(t)
	nome := tabela(t, db, colunasPadrao)

	l := []sdk.Envelope{{Payload: map[string]any{
		"ingestion_id":        "x",
		"ingestion_loaded_at": time.Now().UTC().Format(time.RFC3339),
		"coluna_inexistente":  1,
	}}}
	_, err := tomy.Table{DSN: dsn(t), Name: nome}.Write(context.Background(), l, sdk.WriteOptions{})
	if err == nil {
		t.Fatal("campo sem coluna passou")
	}
	for _, exigido := range []string{"coluna_inexistente", "remove the field in Transform"} {
		if !strings.Contains(err.Error(), exigido) {
			t.Errorf("o erro não diz %q: %v", exigido, err)
		}
	}
}

// TestIntegrationLeituraEmFluxo falha se o driver bufferizar.
func TestIntegrationLeituraEmFluxo(t *testing.T) {
	db := abrir(t)
	nome := tabela(t, db, "i INT, texto TEXT")
	// 20 mil linhas via recursão: o MySQL não tem generate_series, e o limite
	// padrão de recursão é 1000.
	//
	// A conexão é fixada: SET SESSION num *sql.DB vale para a conexão que o
	// pool escolheu, e o INSERT seguinte pode sair por outra -- um teste que
	// passa por sorte é pior que um teste lento.
	conexao, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conexao.Close() }()

	if _, err := conexao.ExecContext(context.Background(),
		"SET SESSION cte_max_recursion_depth = 50000"); err != nil {
		t.Fatal(err)
	}
	if _, err := conexao.ExecContext(context.Background(), fmt.Sprintf(`INSERT INTO %s
		WITH RECURSIVE s(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM s WHERE n < 20000)
		SELECT n, REPEAT('x', 500) FROM s`, nome)); err != nil {
		t.Fatal(err)
	}

	seq, err := frommy.Query{DSN: dsn(t), SQL: "SELECT i, texto FROM " + nome + " ORDER BY i"}.
		Read(context.Background(), sdk.ReadOptions{})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	var n int
	if err := db.QueryRow("SELECT count(*) FROM " + nome).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 20000 {
		t.Fatalf("a tabela tem %d linhas; o teste precisa das 20 mil para significar algo", n)
	}

	inicio := time.Now()
	recebeu := false
	for _, err := range seq {
		if err != nil {
			t.Fatal(err)
		}
		recebeu = true
		break
	}
	if !recebeu {
		t.Fatal("nenhuma linha")
	}
	if d := time.Since(inicio); d > 2*time.Second {
		t.Errorf("a primeira linha levou %s; o driver parece bufferizar", d)
	}
}

// TestIntegrationTiposVemDoServidor prova a tabela de tipos contra o MySQL de
// verdade. O database/sql devolve []byte para quase tudo quando se lê em any,
// então sem o tipo declarado todo DECIMAL viraria base64 no JSON.
func TestIntegrationTiposVemDoServidor(t *testing.T) {
	db := abrir(t)
	nome := tabela(t, db, `
		numerico DECIMAL(20,2),
		data DATE,
		instante DATETIME(6),
		documento JSON,
		bytes VARBINARY(16),
		inteiro BIGINT,
		texto VARCHAR(32),
		vazio VARCHAR(8)`)
	if _, err := db.Exec(fmt.Sprintf(`INSERT INTO %s VALUES
		('123456789012345678.99', '2026-09-05', '2026-09-05 12:30:00',
		 '{"a":[1,2]}', 0xDEADBEEF, 42, 'ola', NULL)`, nome)); err != nil {
		t.Fatal(err)
	}

	seq, err := frommy.Query{DSN: dsn(t), SQL: "SELECT * FROM " + nome}.
		Read(context.Background(), sdk.ReadOptions{})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	var linha map[string]any
	for e, err := range seq {
		if err != nil {
			t.Fatal(err)
		}
		linha = e.Payload.(map[string]any)
	}
	if linha == nil {
		t.Fatal("nenhuma linha")
	}

	esperado := map[string]any{
		"numerico": "123456789012345678.99",
		"data":     "2026-09-05",
		"instante": "2026-09-05T12:30:00Z",
		"inteiro":  int64(42),
		"texto":    "ola",
		"vazio":    nil,
	}
	for campo, quero := range esperado {
		if got := linha[campo]; got != quero {
			t.Errorf("%s = %#v (%T), esperado %#v", campo, got, got, quero)
		}
	}
	if _, ok := linha["documento"].(map[string]any); !ok {
		t.Errorf("documento = %#v; JSON devia chegar aninhado", linha["documento"])
	}
	if b, ok := linha["bytes"].([]byte); !ok || len(b) != 4 {
		t.Errorf("bytes = %#v; VARBINARY devia chegar como []byte", linha["bytes"])
	}
}

// TestIntegrationMySQLParaMySQL é o critério de pronto da fase 3: o mesmo
// pipeline da fase 2, com uma linha trocada.
func TestIntegrationMySQLParaMySQL(t *testing.T) {
	db := abrir(t)

	origem := tabela(t, db, "id INT, nome VARCHAR(64), valor DECIMAL(18,2), atualizado_em DATETIME(6)")
	if _, err := db.Exec(fmt.Sprintf(`INSERT INTO %s
		WITH RECURSIVE s(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM s WHERE n < 100)
		SELECT n, CONCAT('registro ', n), n * 1.5, NOW(6) FROM s`, origem)); err != nil {
		t.Fatal(err)
	}

	destino := tabela(t, db, `
		ingestion_id VARCHAR(36) NOT NULL,
		ingestion_loaded_at DATETIME(6) NOT NULL,
		provider VARCHAR(32) NOT NULL,
		entity VARCHAR(32) NOT NULL,
		source_key VARCHAR(32) NOT NULL,
		record_ts VARCHAR(64) NOT NULL,
		nome VARCHAR(64),
		valor DECIMAL(18,2)`)
	if _, err := db.Exec(fmt.Sprintf("CREATE UNIQUE INDEX u ON %s (ingestion_id)", destino)); err != nil {
		t.Fatal(err)
	}

	rodar := func() *sdk.Result {
		t.Helper()
		dados, err := sdk.Extract(context.Background(), sdk.Source{
			From: frommy.Query{DSN: dsn(t),
				SQL: "SELECT id, nome, valor, atualizado_em FROM " + origem + " ORDER BY id"},
		})
		if err != nil {
			t.Fatalf("Extract: %v", err)
		}
		dados = sdk.Transform(dados,
			sdk.Compute("source_key", func(r map[string]any) (any, error) {
				return fmt.Sprint(r["id"]), nil
			}),
			sdk.Without("id"),
			sdk.Rename(map[string]string{"atualizado_em": "record_ts"}),
			sdk.Compute("provider", func(map[string]any) (any, error) { return "mysql", nil }),
			sdk.Compute("entity", func(map[string]any) (any, error) { return "registros", nil }),
			sdk.IngestionID(),
			sdk.IngestionLoadedAt(),
		)
		res, err := sdk.Load(context.Background(), dados, sdk.Target{
			To: tomy.Table{DSN: dsn(t), Name: destino},
			Columns: []string{"ingestion_id", "ingestion_loaded_at", "provider", "entity",
				"source_key", "record_ts", "nome", "valor"},
			Dedup: sdk.DedupMerge,
		})
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		return res
	}

	if primeira := rodar(); primeira.Rows != 100 {
		t.Errorf("primeira carga: %d linhas, esperado 100", primeira.Rows)
	}
	if segunda := rodar(); segunda.Rows != 0 || segunda.Ignored != 100 {
		t.Errorf("segunda carga: %d carregadas e %d ignoradas", segunda.Rows, segunda.Ignored)
	}

	var n int
	if err := db.QueryRow("SELECT count(*) FROM " + destino).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 100 {
		t.Errorf("o destino tem %d linhas depois de duas execuções idênticas", n)
	}

	var valor string
	if err := db.QueryRow("SELECT valor FROM " + destino + " WHERE source_key = '2'").Scan(&valor); err != nil {
		t.Fatal(err)
	}
	if valor != "3.00" {
		t.Errorf("valor = %q, esperado \"3.00\"", valor)
	}
}
