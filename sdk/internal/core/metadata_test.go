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

func TestResolvePrecedencia(t *testing.T) {
	t.Setenv("BREVIS_TESTE_X", "do-ambiente")

	if got := Resolve("explicito", "BREVIS_TESTE_X", "padrao"); got.Value != "explicito" || got.Where != "explicit" {
		t.Errorf("o explícito tem de vencer: %+v", got)
	}
	if got := Resolve("", "BREVIS_TESTE_X", "padrao"); got.Value != "do-ambiente" || got.Where != "BREVIS_TESTE_X" {
		t.Errorf("o ambiente vem depois, e o log nomeia a variável: %+v", got)
	}
	if got := Resolve("", "BREVIS_TESTE_AUSENTE", "padrao"); got.Value != "padrao" || got.Where != "default" {
		t.Errorf("o padrão fecha a lista: %+v", got)
	}
}

func TestEnvIntCaiNoPadraoEmVezDeQuebrar(t *testing.T) {
	t.Setenv("BREVIS_TESTE_N", "nao-e-numero")
	if got := EnvInt("BREVIS_TESTE_N", 7); got != 7 {
		t.Errorf("um valor ilegível não pode derrubar a pipeline: %d", got)
	}
	t.Setenv("BREVIS_TESTE_N", "42")
	if got := EnvInt("BREVIS_TESTE_N", 7); got != 42 {
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
