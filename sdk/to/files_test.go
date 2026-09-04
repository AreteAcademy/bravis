package to

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	core "github.com/AreteAcademy/bravis/sdk/internal/core"
)

func lote(n int) []core.Envelope {
	out := make([]core.Envelope, n)
	for i := range out {
		out[i] = core.Envelope{
			Provider: "acme", Entity: "widgets",
			SourceKey: string(rune('a' + i)), RecordTS: "2026-01-01T00:00:00Z",
			Payload: map[string]any{"sku": string(rune('A' + i)), "quantidade": i},
		}
	}
	return out
}

func unico(t *testing.T, dir string) string {
	t.Helper()
	var achados []string
	_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			achados = append(achados, p)
		}
		return nil
	})
	if len(achados) != 1 {
		t.Fatalf("%d arquivos em %s, esperado 1: %v", len(achados), dir, achados)
	}
	return achados[0]
}

func TestFilesEscreveNDJSON(t *testing.T) {
	dir := t.TempDir()

	res, err := Files{Path: dir + "/"}.Write(context.Background(), lote(3), core.WriteOptions{})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if res.RowsLoaded != 3 {
		t.Errorf("RowsLoaded = %d", res.RowsLoaded)
	}

	corpo, err := os.ReadFile(unico(t, dir))
	if err != nil {
		t.Fatal(err)
	}
	linhas := strings.Split(strings.TrimSpace(string(corpo)), "\n")
	if len(linhas) != 3 {
		t.Fatalf("%d linhas, esperado 3", len(linhas))
	}
	var row map[string]any
	if err := json.Unmarshal([]byte(linhas[0]), &row); err != nil {
		t.Fatalf("a primeira linha não é JSON: %v", err)
	}
	if row["sku"] != "A" {
		t.Errorf("o registro não sobreviveu: %v", row)
	}
}

// Nada é acrescentado: o registro sai como o Transform o deixou, igual em
// todo destino.
func TestFilesNaoAcrescentaNadaSemMetadata(t *testing.T) {
	dir := t.TempDir()
	if _, err := (Files{Path: dir + "/"}).Write(context.Background(), lote(1), core.WriteOptions{}); err != nil {
		t.Fatal(err)
	}

	corpo, _ := os.ReadFile(unico(t, dir))
	var row map[string]any
	_ = json.Unmarshal([]byte(strings.TrimSpace(string(corpo))), &row)

	if len(row) != 2 {
		t.Errorf("o registro tem %d campos, esperado os 2 do chamador: %v", len(row), row)
	}
	for _, proibido := range []string{"ingestion_id", "provider", "entity", "source_key", "payload"} {
		if _, tem := row[proibido]; tem {
			t.Errorf("o SDK escreveu %q sem ser pedido", proibido)
		}
	}
}

func TestFilesComprime(t *testing.T) {
	dir := t.TempDir()
	if _, err := (Files{Path: dir + "/", Compress: true}).
		Write(context.Background(), lote(2), core.WriteOptions{}); err != nil {
		t.Fatal(err)
	}

	caminho := unico(t, dir)
	if !strings.HasSuffix(caminho, ".gz") {
		t.Errorf("o arquivo não terminou em .gz: %s", caminho)
	}
	f, err := os.Open(caminho) //nolint:gosec
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if _, err := gzip.NewReader(f); err != nil {
		t.Errorf("o conteúdo não é gzip: %v", err)
	}
}

// Um diretório não tem chave para casar, e uma flag ignorada em silêncio é
// pior que um erro.
func TestFilesRecusaFormatoQueNaoEscreve(t *testing.T) {
	_, err := Files{Path: t.TempDir() + "/", Format: "parquet"}.
		Write(context.Background(), lote(1), core.WriteOptions{})
	if err == nil {
		t.Fatal("to.Files escreve NDJSON e CSV; parquet tem de ser recusado")
	}
	if !strings.Contains(err.Error(), "parquet") {
		t.Errorf("o erro precisa nomear o formato: %v", err)
	}
}

// A declaração vale em todo destino, não só no BigQuery.
func TestFilesConfereColumns(t *testing.T) {
	_, err := Files{Path: t.TempDir() + "/"}.Write(context.Background(), lote(1),
		core.WriteOptions{Columns: []string{"sku", "quantidade", "faltando"}})
	if err == nil {
		t.Fatal("uma coluna declarada e não entregue tem de ser erro aqui também")
	}
	if !strings.Contains(err.Error(), "faltando") {
		t.Errorf("o erro precisa nomear a coluna: %v", err)
	}
}

func TestFilesEscreveCSVComUniaoDosCampos(t *testing.T) {
	dir := t.TempDir()
	registros := []core.Envelope{
		{Payload: map[string]any{"a": 1, "b": 2}},
		{Payload: map[string]any{"a": 3}}, // sem "b"
	}
	if _, err := (Files{Path: dir + "/", Format: core.FormatCSV}).
		Write(context.Background(), registros, core.WriteOptions{}); err != nil {
		t.Fatal(err)
	}

	corpo, _ := os.ReadFile(unico(t, dir))
	linhas := strings.Split(strings.TrimSpace(string(corpo)), "\n")
	if linhas[0] != "a,b" {
		t.Errorf("cabeçalho = %q, esperado a união ordenada", linhas[0])
	}
	if linhas[2] != "3," {
		t.Errorf("a linha sem b = %q; o campo ausente tem de ficar vazio na coluna certa", linhas[2])
	}
}

// Dois lotes não se sobrescrevem: um diretório não tem noção de "as mesmas
// linhas de novo".
func TestFilesNaoSobrescreveOLoteAnterior(t *testing.T) {
	dir := t.TempDir()
	d := Files{Path: dir + "/"}
	for i := 0; i < 2; i++ {
		if _, err := d.Write(context.Background(), lote(1), core.WriteOptions{}); err != nil {
			t.Fatal(err)
		}
	}

	entradas, _ := os.ReadDir(dir)
	if len(entradas) != 2 {
		t.Errorf("%d arquivos depois de duas cargas, esperado 2", len(entradas))
	}
}

// Nenhum arquivo temporário sobra: a escrita é temp + rename, e o rename é
// atômico no mesmo sistema de arquivos.
func TestFilesNaoDeixaTemporario(t *testing.T) {
	dir := t.TempDir()
	if _, err := (Files{Path: dir + "/"}).Write(context.Background(), lote(1), core.WriteOptions{}); err != nil {
		t.Fatal(err)
	}
	entradas, _ := os.ReadDir(dir)
	for _, e := range entradas {
		if strings.HasPrefix(e.Name(), ".bravis-") {
			t.Errorf("sobrou um temporário: %s", e.Name())
		}
	}
}

func TestFilesRecusaCaminhoSemStore(t *testing.T) {
	_, err := Files{Path: "gs://b/x/"}.Write(context.Background(), lote(1), core.WriteOptions{})
	if err == nil {
		t.Fatal("um caminho gs:// sem Store não tem como ser escrito")
	}
	if !strings.Contains(err.Error(), "gs") || !strings.Contains(err.Error(), "Store") {
		t.Errorf("o erro precisa nomear os dois: %v", err)
	}
}

// Um diretório não tem chave para casar, e uma flag ignorada em silêncio é
// pior que um erro.
func TestFilesRecusaDedup(t *testing.T) {
	_, err := Files{Path: t.TempDir() + "/"}.Write(context.Background(), lote(1),
		core.WriteOptions{Dedup: core.DedupMerge})
	if err == nil {
		t.Fatal("to.Files não sabe deduplicar; aceitar a flag seria ignorá-la")
	}
	for _, quer := range []string{"to.Files", "Dedup"} {
		if !strings.Contains(err.Error(), quer) {
			t.Errorf("o erro precisa nomear %q: %v", quer, err)
		}
	}
}

// O particionamento lê a coluna que a cadeia compôs, não uma que o destino
// acrescenta.
func TestFilesParticionaPelaColunaDaLinha(t *testing.T) {
	dir := t.TempDir()
	registros := []core.Envelope{{Payload: map[string]any{
		"sku": "W-1", "ingestion_loaded_at": "2026-09-04T10:00:00Z",
	}}}

	if _, err := (Files{Path: dir + "/", PartitionBy: "ingestion_loaded_at"}).
		Write(context.Background(), registros, core.WriteOptions{}); err != nil {
		t.Fatal(err)
	}
	if caminho := unico(t, dir); !strings.Contains(caminho, "ingestion_loaded_at=2026-09-04") {
		t.Errorf("o caminho não foi particionado: %s", caminho)
	}
}
