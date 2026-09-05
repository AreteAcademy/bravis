package redshift

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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

// TestEncodeNDJSONNaoPagaOCaminhoDoMapa compara os DOIS caminhos no mesmo
// processo, e essa é a única forma que se sustenta.
//
// Este teste já esteve errado duas vezes, e as duas por medir a coisa errada:
//
//  1. a primeira versão comparava 2000 linhas com 200 e exigia razão abaixo de
//     10 -- que com qualquer custo linear dá exatamente 10, impossível de
//     passar de um jeito e de falhar do outro;
//  2. a segunda fixou um teto absoluto por linha, medido com o toolchain
//     local. A CI roda outro, e a análise de escape mudou entre eles: 0,005
//     por linha no 1.25 viraram 2,00 no 1.27, sem nada no código mudar.
//
// Um número absoluto de alocações não é propriedade do código; é propriedade
// do código MAIS o compilador. O que é do código é a diferença entre as duas
// estratégias -- e medindo as duas sob o mesmo compilador, ela se sustenta em
// qualquer um.
func TestEncodeNDJSONNaoPagaOCaminhoDoMapa(t *testing.T) {
	const linhas = 2000
	envelopes := make([]core.Envelope, linhas)
	for i := range envelopes {
		envelopes[i] = core.Envelope{Payload: map[string]any{"a": i, "b": "texto"}}
	}
	colunas := []string{"a", "b"}

	direto := testing.AllocsPerRun(5, func() {
		if _, err := EncodeNDJSON(envelopes, colunas); err != nil {
			t.Fatal(err)
		}
	})
	viaMapa := testing.AllocsPerRun(5, func() {
		if _, err := encodeViaMapa(envelopes, colunas); err != nil {
			t.Fatal(err)
		}
	})

	// A margem é folgada de propósito: o que se afirma é que o caminho direto
	// é substancialmente mais barato, não um número exato que o próximo Go
	// invalidaria.
	if direto*2 > viaMapa {
		t.Errorf("o caminho direto custa %.0f alocações e o do mapa %.0f para %d linhas; "+
			"a vantagem sumiu -- ou o EncodeNDJSON voltou a montar um map por registro",
			direto, viaMapa, linhas)
	}
	t.Logf("direto %.0f, via mapa %.0f (%.1fx) para %d linhas",
		direto, viaMapa, viaMapa/direto, linhas)
}

// encodeViaMapa é o caminho que EncodeNDJSON tinha antes: um map[string]any
// por registro, entregue ao json.Encoder.
//
// Vive no teste, e não no código de produção, porque é a referência contra a
// qual o ganho é medido -- e porque uma referência que mora no teste não pode
// ser usada por engano.
func encodeViaMapa(envelopes []core.Envelope, colunas []string) ([]byte, error) {
	var buf bytes.Buffer
	buf.Grow(len(envelopes) * 128)
	enc := json.NewEncoder(&buf)

	for i, e := range envelopes {
		obj, err := core.AsObject(e.Payload)
		if err != nil {
			return nil, fmt.Errorf("row %d: %w", i+1, err)
		}
		linha := make(map[string]any, len(colunas))
		for _, c := range colunas {
			if v, tem := obj[c]; tem {
				linha[c] = v
			}
		}
		if err := enc.Encode(linha); err != nil {
			return nil, err
		}
	}
	return buf.Bytes(), nil
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
