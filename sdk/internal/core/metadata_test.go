package core

import (
	"log/slog"
	"strings"
	"testing"
)

func linhaCore(campos map[string]any) []Envelope {
	return []Envelope{{Payload: campos}}
}

func TestColumnsRecusaColunaQueNinguemEntregou(t *testing.T) {
	err := CheckColumns(
		[]string{"ingestion_id", "provider", "entity", "payload"},
		linhaCore(map[string]any{"ingestion_id": "x", "provider": "p", "payload": "{}"}),
	)
	if err == nil {
		t.Fatal("uma coluna declarada e não entregue landa NULL sem ninguém saber")
	}
	if !strings.Contains(err.Error(), "entity") {
		t.Errorf("o erro não nomeia a coluna: %v", err)
	}
	// E diz o que a linha de fato tem, para o conserto sair de uma leitura.
	if !strings.Contains(err.Error(), "provider") {
		t.Errorf("o erro não lista o que a linha tem: %v", err)
	}
}

// Critério 3: campo na linha que a declaração não lista.
func TestColumnsRecusaCampoNaoDeclarado(t *testing.T) {
	err := CheckColumns(
		[]string{"provider", "payload"},
		linhaCore(map[string]any{"provider": "p", "payload": "{}", "surpresa": 1}),
	)
	if err == nil {
		t.Fatal("um campo não declarado seria escrito numa tabela que nunca o mencionou")
	}
	if !strings.Contains(err.Error(), "surpresa") {
		t.Errorf("o erro não nomeia o campo: %v", err)
	}
}

// As duas colunas do Metadata podem ser declaradas, e é o ponto da spec:
// dentro do Transform elas jamais poderiam, porque ainda não existem lá.
func TestColumnsAceitaAsColunasDoMetadata(t *testing.T) {
	err := CheckColumns(
		[]string{"ingestion_id", "ingestion_loaded_at", "provider", "entity", "source_key", "payload"},
		linhaCore(map[string]any{
			"ingestion_id": "u", "ingestion_loaded_at": "2026-01-01T00:00:00Z",
			"provider": "p", "entity": "e", "source_key": "k", "payload": "{}",
		}),
	)
	if err != nil {
		t.Fatalf("as seis colunas do DDL deveriam passar: %v", err)
	}
}

func TestColumnsVazioNaoConfereNada(t *testing.T) {
	if err := CheckColumns(nil, linhaCore(map[string]any{"qualquer": 1})); err != nil {
		t.Errorf("sem declaração não há o que conferir: %v", err)
	}
}

// Critério 4: a declaração contra a tabela real.

// --- StampMetadata: a função mais consequente do SDK --------------------

// O id é determinístico por contrato: a mesma linha carregada duas vezes tem
// de sair com o mesmo ingestion_id, ou nenhuma deduplicação a jusante
// funciona -- e uma linha escrita aqui tem de casar com a que um fetcher
// Python escreve para o mesmo registro.
func TestStampMetadataEDeterministico(t *testing.T) {
	lote := []Envelope{{
		Provider: "acme", Entity: "widgets", SourceKey: "k-1",
		RecordTS: "2026-01-01T00:00:00Z",
		Payload:  map[string]any{"sku": "W-1"},
	}}

	a, err := StampMetadata(lote, WriteOptions{Metadata: true})
	if err != nil {
		t.Fatalf("StampMetadata: %v", err)
	}
	b, _ := StampMetadata(lote, WriteOptions{Metadata: true})

	idA := a[0].Payload.(map[string]any)[MetadataID]
	idB := b[0].Payload.(map[string]any)[MetadataID]
	if idA != idB {
		t.Errorf("o id mudou entre chamadas: %v != %v", idA, idB)
	}
}

// Load recebe a fatia do chamador. Escrever o metadado nela alteraria o que
// ele ainda segura -- e a segunda carga do mesmo lote falharia com "already
// has ingestion_id", que é exatamente o que um retry faz.
func TestStampMetadataNaoMutaOLoteDoChamador(t *testing.T) {
	original := map[string]any{"sku": "W-1"}
	lote := []Envelope{{
		Provider: "acme", Entity: "widgets", SourceKey: "k-1",
		RecordTS: "2026-01-01T00:00:00Z", Payload: original,
	}}

	if _, err := StampMetadata(lote, WriteOptions{Metadata: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := StampMetadata(lote, WriteOptions{Metadata: true}); err != nil {
		t.Fatalf("carregar o mesmo lote duas vezes tem de funcionar: %v", err)
	}

	if len(original) != 1 {
		t.Errorf("o mapa do chamador foi alterado: %v", original)
	}
	if lote[0].Payload.(map[string]any)[MetadataID] != nil {
		t.Error("o envelope do chamador foi carimbado")
	}
}

func TestStampMetadataDesligadoNaoTocaEmNada(t *testing.T) {
	lote := []Envelope{{Payload: map[string]any{"sku": "W-1"}}}
	got, err := StampMetadata(lote, WriteOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got[0].Payload.(map[string]any)) != 1 {
		t.Errorf("sem Metadata nada é acrescentado: %v", got[0].Payload)
	}
}

// Substituir em silêncio o valor de um fornecedor pelo nosso é a falha pior:
// ela é invisível.
func TestStampMetadataRecusaNomeJaOcupado(t *testing.T) {
	lote := []Envelope{{
		Provider: "acme", Entity: "widgets", SourceKey: "k", RecordTS: "2026-01-01T00:00:00Z",
		Payload: map[string]any{MetadataID: "meu-proprio-id"},
	}}
	_, err := StampMetadata(lote, WriteOptions{Metadata: true})
	if err == nil {
		t.Fatal("um registro que já tem ingestion_id não pode ser sobrescrito em silêncio")
	}
	if !strings.Contains(err.Error(), MetadataID) {
		t.Errorf("o erro precisa nomear o campo: %v", err)
	}
}

func TestStampMetadataAutoIDDaUmIDPorLinha(t *testing.T) {
	lote := []Envelope{
		{Payload: map[string]any{"a": 1}},
		{Payload: map[string]any{"a": 2}},
	}
	got, err := StampMetadata(lote, WriteOptions{Metadata: true, AutoID: true})
	if err != nil {
		t.Fatalf("AutoID não precisa de proveniência: %v", err)
	}
	if got[0].Payload.(map[string]any)[MetadataID] == got[1].Payload.(map[string]any)[MetadataID] {
		t.Error("AutoID tem de dar um id novo por linha")
	}
}

// Sem AutoID e sem SourceKey não há identidade estável, e um id que muda a
// cada execução é pior que nenhum: ele parece funcionar.
func TestStampMetadataRecusaSemIdentidade(t *testing.T) {
	_, err := StampMetadata([]Envelope{{Payload: map[string]any{"a": 1}}},
		WriteOptions{Metadata: true})
	if err == nil {
		t.Fatal("sem SourceKey e sem AutoID não há id estável a construir")
	}
}

// --- a resolução de configuração ---------------------------------------

func TestResolvePrecedencia(t *testing.T) {
	t.Setenv("BRAVIS_TESTE_X", "do-ambiente")

	if got := Resolve("explicito", "BRAVIS_TESTE_X", "padrao"); got.Value != "explicito" || got.Where != "explicit" {
		t.Errorf("o explícito tem de vencer: %+v", got)
	}
	if got := Resolve("", "BRAVIS_TESTE_X", "padrao"); got.Value != "do-ambiente" || got.Where != "BRAVIS_TESTE_X" {
		t.Errorf("o ambiente vem depois, e o log nomeia a variável: %+v", got)
	}
	if got := Resolve("", "BRAVIS_TESTE_AUSENTE", "padrao"); got.Value != "padrao" || got.Where != "default" {
		t.Errorf("o padrão fecha a lista: %+v", got)
	}
}

func TestEnvIntCaiNoPadraoEmVezDeQuebrar(t *testing.T) {
	t.Setenv("BRAVIS_TESTE_N", "nao-e-numero")
	if got := EnvInt("BRAVIS_TESTE_N", 7); got != 7 {
		t.Errorf("um valor ilegível não pode derrubar a pipeline: %d", got)
	}
	t.Setenv("BRAVIS_TESTE_N", "42")
	if got := EnvInt("BRAVIS_TESTE_N", 7); got != 42 {
		t.Errorf("EnvInt = %d", got)
	}
}

// Um nível de log inválido derruba a pipeline? Não: ele avisa e segue.
func TestLogLevelNaoDerrubaComValorInvalido(t *testing.T) {
	t.Setenv(EnvLogLevel, "nao-existe")
	if got := LogLevel(); got != slog.LevelInfo {
		t.Errorf("LogLevel = %v, esperado info", got)
	}
	t.Setenv(EnvLogLevel, "debug")
	if got := LogLevel(); got != slog.LevelDebug {
		t.Errorf("LogLevel = %v, esperado debug", got)
	}
}
