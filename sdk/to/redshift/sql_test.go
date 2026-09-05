package redshift

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/sdk/internal/core"
)

// Este arquivo é o que dá para testar sem cluster, e é deliberadamente o
// grosso do driver: a geração do SQL como função pura.
//
// O motivo está escrito na v0.12.0 -- SQL montado dentro de um método com
// cliente nunca tinha sido visto por um teste, e saiu com casamento posicional.

func TestCopySQLUsaRoleENaoChave(t *testing.T) {
	got := CopySQL("landing.pedidos", "s3://b/k.ndjson", "arn:aws:iam::1:role/r")
	for _, exigido := range []string{
		"COPY landing.pedidos FROM 's3://b/k.ndjson'",
		"IAM_ROLE 'arn:aws:iam::1:role/r'",
		"FORMAT AS JSON 'auto'",
	} {
		if !strings.Contains(got, exigido) {
			t.Errorf("falta %q:\n%s", exigido, got)
		}
	}
}

// TestMergeSQLNomeiaAsColunas: nomeadas sempre. A alternativa já aconteceu --
// o INSERT ROW do BigQuery casa por posição, e a v0.12.0 saiu com as colunas
// trocadas de lugar porque ninguém tinha visto o SQL gerado.
func TestMergeSQLNomeiaAsColunas(t *testing.T) {
	got := MergeSQL("destino", "brevis_stage", []string{"ingestion_id", "valor"})

	esperado := `MERGE INTO destino USING brevis_stage ` +
		`ON destino."ingestion_id" = brevis_stage."ingestion_id" ` +
		`WHEN NOT MATCHED THEN INSERT ("ingestion_id", "valor") ` +
		`VALUES (brevis_stage."ingestion_id", brevis_stage."valor")`
	if got != esperado {
		t.Errorf("SQL:\n  got  %s\n  want %s", got, esperado)
	}
}

// TestMergeSQLCasaPeloIngestionID: trocar a coluna de junção faria a dedup
// casar pela coisa errada, em silêncio.
func TestMergeSQLCasaPeloIngestionID(t *testing.T) {
	got := MergeSQL("d", "s", []string{"a"})
	if !strings.Contains(got, `d."`+core.MetadataID+`" = s."`+core.MetadataID+`"`) {
		t.Errorf("a junção não é por %s:\n%s", core.MetadataID, got)
	}
}

// TestMergeSQLCitaPalavraReservada.
func TestMergeSQLCitaPalavraReservada(t *testing.T) {
	got := MergeSQL("d", "s", []string{"order"})
	if strings.Count(got, `"order"`) < 2 {
		t.Errorf("a palavra reservada não está citada nos dois lados:\n%s", got)
	}
}

// TestStagingTableUsaLike: uma lista de colunas escrita à mão é a que faz o
// MERGE falhar meses depois, quando alguém acrescenta uma coluna ao destino.
func TestStagingTableUsaLike(t *testing.T) {
	got := StagingTableSQL("landing.pedidos", "brevis_stage")
	if !strings.Contains(got, "LIKE landing.pedidos") {
		t.Errorf("a temporária não acompanha o destino:\n%s", got)
	}
	if !strings.Contains(got, "TEMP") {
		t.Errorf("a tabela de staging não é temporária:\n%s", got)
	}
}

// TestEncodeNDJSONUmaLinhaPorRegistro.
func TestEncodeNDJSONUmaLinhaPorRegistro(t *testing.T) {
	envelopes := []core.Envelope{
		{Payload: map[string]any{"a": 1, "b": "x", "sobra": true}},
		{Payload: map[string]any{"a": 2}},
	}
	b, err := EncodeNDJSON(envelopes, []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}

	linhas := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(linhas) != 2 {
		t.Fatalf("%d linhas, esperado 2:\n%s", len(linhas), b)
	}

	var primeira map[string]any
	if err := json.Unmarshal([]byte(linhas[0]), &primeira); err != nil {
		t.Fatal(err)
	}
	// Só as colunas declaradas entram: um campo a mais faria o COPY com
	// 'auto' tentar uma coluna que não existe.
	if _, tem := primeira["sobra"]; tem {
		t.Errorf("campo fora da declaração foi para o arquivo: %v", primeira)
	}
	if primeira["a"] != float64(1) || primeira["b"] != "x" {
		t.Errorf("linha 1 = %v", primeira)
	}

	// Coluna que o registro não traz simplesmente não aparece; o COPY com
	// 'auto' deixa a coluna NULL, que é legítimo numa landing.
	var segunda map[string]any
	if err := json.Unmarshal([]byte(linhas[1]), &segunda); err != nil {
		t.Fatal(err)
	}
	if _, tem := segunda["b"]; tem {
		t.Errorf("linha 2 inventou a coluna b: %v", segunda)
	}
}

// TestEncodeNDJSONTemOrcamentoPorLinha fixa um teto de alocações POR LINHA.
//
// A primeira versão deste teste comparava 2000 linhas com 200 e exigia razão
// abaixo de 10 -- o que é impossível de falhar de um jeito e impossível de
// passar do outro: com qualquer custo por linha a razão é exatamente 10. Um
// teste que mede a coisa errada é pior que nenhum, porque dá confiança.
//
// O que vale é o número por linha, com um teto escrito. Passar um
// map[string]any ao json.Encoder por registro custava cinco.
func TestEncodeNDJSONTemOrcamentoPorLinha(t *testing.T) {
	const linhas = 2000
	envelopes := make([]core.Envelope, linhas)
	for i := range envelopes {
		envelopes[i] = core.Envelope{Payload: map[string]any{"a": i, "b": "texto"}}
	}
	colunas := []string{"a", "b"}

	total := testing.AllocsPerRun(5, func() {
		if _, err := EncodeNDJSON(envelopes, colunas); err != nil {
			t.Fatal(err)
		}
	})

	// O teto é apertado de propósito: hoje são ~10 alocações para 2000 linhas
	// (o buffer que dobra, e mais nada). Um teto frouxo deixaria o caminho
	// antigo -- cinco por linha -- voltar sem ninguém ver.
	const teto = 0.05
	if porLinha := total / linhas; porLinha > teto {
		t.Errorf("%.2f alocações por linha (teto %.1f); ao todo %.0f para %d linhas",
			porLinha, teto, total, linhas)
	}
}

// executorFalso registra o SQL, e é como o driver inteiro é testável sem
// cluster -- que é a única forma, porque não existe imagem do Redshift.
type executorFalso struct{ sqls []string }

func (e *executorFalso) Exec(_ context.Context, sql string) error {
	e.sqls = append(e.sqls, sql)
	return nil
}

type storeFalso struct {
	bucket, chave string
	apagou        bool
}

func (s *storeFalso) Scheme() string { return "s3" }
func (s *storeFalso) List(context.Context, string, string) ([]string, error) {
	return nil, nil
}
func (s *storeFalso) Open(context.Context, string, string) (io.ReadCloser, error) {
	return nil, nil
}
func (s *storeFalso) Create(_ context.Context, bucket, chave string, r io.Reader) error {
	s.bucket, s.chave = bucket, chave
	_, _ = io.ReadAll(r)
	return nil
}
func (s *storeFalso) Delete(context.Context, string, string) error {
	s.apagou = true
	return nil
}

// TestOrdemDosComandos prova a sequência inteira sem cluster: staging, COPY
// para a temporária, MERGE, DROP.
func TestOrdemDosComandos(t *testing.T) {
	exec := &executorFalso{}
	store := &storeFalso{}

	tabela := Table{
		Name: "landing.pedidos", Staging: "s3://b/stage/",
		IAMRole: "arn:aws:iam::1:role/r", Store: store, Executor: exec,
	}
	_, err := tabela.Write(context.Background(),
		[]core.Envelope{{Payload: map[string]any{core.MetadataID: "x", "a": 1}}},
		core.WriteOptions{Dedup: core.DedupMerge, Columns: []string{core.MetadataID, "a"}})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}

	if len(exec.sqls) != 4 {
		t.Fatalf("%d comandos, esperado 4:\n%s", len(exec.sqls), strings.Join(exec.sqls, "\n"))
	}
	prefixos := []string{"CREATE TEMP TABLE", "COPY brevis_stage", "MERGE INTO", "DROP TABLE"}
	for i, p := range prefixos {
		if !strings.HasPrefix(exec.sqls[i], p) {
			t.Errorf("comando %d = %q, esperado começar com %q", i, exec.sqls[i], p)
		}
	}
	if !store.apagou {
		t.Error("o arquivo de staging não foi apagado")
	}
}

// TestSemDedupCopiaDiretoNoDestino: sem dedup não há temporária nem MERGE, e
// uma temporária criada à toa é trabalho que ninguém pediu.
func TestSemDedupCopiaDiretoNoDestino(t *testing.T) {
	exec := &executorFalso{}
	tabela := Table{
		Name: "landing.pedidos", Staging: "s3://b/stage/",
		IAMRole: "arn:aws:iam::1:role/r", Store: &storeFalso{}, Executor: exec,
	}
	if _, err := tabela.Write(context.Background(),
		[]core.Envelope{{Payload: map[string]any{"a": 1}}},
		core.WriteOptions{Columns: []string{"a"}}); err != nil {
		t.Fatal(err)
	}
	if len(exec.sqls) != 1 || !strings.HasPrefix(exec.sqls[0], "COPY landing.pedidos") {
		t.Errorf("comandos = %v", exec.sqls)
	}
}

// TestKeepStagedFileMantem.
func TestKeepStagedFileMantem(t *testing.T) {
	store := &storeFalso{}
	tabela := Table{
		Name: "t", Staging: "s3://b/stage/", IAMRole: "arn:aws:iam::1:role/r",
		Store: store, Executor: &executorFalso{}, KeepStagedFile: true,
	}
	if _, err := tabela.Write(context.Background(),
		[]core.Envelope{{Payload: map[string]any{"a": 1}}},
		core.WriteOptions{Columns: []string{"a"}}); err != nil {
		t.Fatal(err)
	}
	if store.apagou {
		t.Error("apagou mesmo com KeepStagedFile")
	}
}

// TestChaveDeAcessoERecusada: uma chave na string do COPY acaba no log de
// query do cluster, que muita gente lê.
func TestChaveDeAcessoERecusada(t *testing.T) {
	tabela := Table{
		Name: "t", Staging: "s3://b/s/", Store: &storeFalso{}, Executor: &executorFalso{},
		IAMRole: "aws_access_key_id=AKIA;aws_secret_access_key=xyz",
	}
	_, err := tabela.Write(context.Background(),
		[]core.Envelope{{Payload: map[string]any{"a": 1}}}, core.WriteOptions{})
	if err == nil {
		t.Fatal("chave de acesso passou")
	}
	if !strings.Contains(err.Error(), "query log") {
		t.Errorf("o erro não diz por quê: %v", err)
	}
}

// TestCamposObrigatoriosSaoNomeados: não há caminho inline no Redshift, e o
// erro tem de dizer isso em vez de listar campos sem contexto.
func TestCamposObrigatoriosSaoNomeados(t *testing.T) {
	_, err := Table{}.Write(context.Background(),
		[]core.Envelope{{Payload: map[string]any{"a": 1}}}, core.WriteOptions{})
	if err == nil {
		t.Fatal("configuração vazia passou")
	}
	for _, exigido := range []string{"DSN", "Name", "Staging", "IAMRole", "Store", "no inline path"} {
		if !strings.Contains(err.Error(), exigido) {
			t.Errorf("o erro não diz %q: %v", exigido, err)
		}
	}
}
