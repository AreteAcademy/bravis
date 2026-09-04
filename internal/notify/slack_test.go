package notify_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/internal/notify"
)

func capturar(t *testing.T, status int, resposta string) (*notify.Slack, *string) {
	t.Helper()
	var recebido string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		recebido = string(b)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(resposta))
	}))
	t.Cleanup(srv.Close)

	s := notify.NovoSlack(srv.URL, "prod")
	return s, &recebido
}

func alerta() notify.Alerta {
	quando := time.Date(2026, 9, 1, 4, 0, 0, 0, time.UTC)
	return notify.Alerta{
		Workflow: "id_verification", RunID: "1f2e3d4c-0000-0000-0000-000000000000",
		Status: "failed", Trigger: "schedule", Tentativas: 3, LogicalDate: &quando,
		Erro:    "nivel 1: step \"run\": saiu com codigo 2\nDatabase Error in model x",
		Tags:    []string{"zarv", "id", "dbt"},
		URLBase: "https://brevis.zarv.net",
	}
}

func TestMensagemTemOContextoDaFalha(t *testing.T) {
	s, recebido := capturar(t, 200, "ok")
	if err := s.Falhou(context.Background(), alerta()); err != nil {
		t.Fatal(err)
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(*recebido), &payload); err != nil {
		t.Fatalf("payload nao e JSON: %v", err)
	}

	// `text` fora dos blocos e o que aparece na notificacao do celular; sem ele
	// o Slack mostra "This content can't be displayed" no preview.
	texto, _ := payload["text"].(string)
	if !strings.Contains(texto, "id_verification") {
		t.Errorf("texto de preview = %q", texto)
	}

	corpo := *recebido
	for _, esperado := range []string{
		"id_verification",               // pipeline
		"`id`",                          // dominio, vindo das tags
		"FAILED",                        // status
		"schedule",                      // origem
		"saiu com codigo 2",             // a causa
		"brevis.zarv.net/runs/1f2e3d4c", // link direto
		"01/09/2026 01:00",              // data logica no fuso local
	} {
		if !strings.Contains(corpo, esperado) {
			t.Errorf("mensagem sem %q:\n%s", esperado, corpo)
		}
	}
}

// A mensagem de erro carrega stderr; o bloco do Slack tem limite de 3000
// caracteres e RECUSA a mensagem inteira quando estoura — truncar e o que
// garante que o alerta chegue.
func TestErroLongoEhTruncado(t *testing.T) {
	s, recebido := capturar(t, 200, "ok")
	a := alerta()
	a.Erro = strings.Repeat("linha muito comprida de stack trace ", 200)

	if err := s.Falhou(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if len(*recebido) > 3000 {
		t.Errorf("payload com %d bytes; o bloco do Slack recusaria", len(*recebido))
	}
	if !strings.Contains(*recebido, "truncado") {
		t.Error("truncou sem avisar que truncou")
	}
}

// Ambiente no cabecalho: um alerta de homologacao as tres da manha nao pode ser
// indistinguivel de um de producao.
func TestAmbienteApareceNoCabecalho(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(b), "(dev)") {
			t.Errorf("cabecalho sem o ambiente: %s", b)
		}
	}))
	defer srv.Close()

	s := notify.NovoSlack(srv.URL, "dev")
	if err := s.Falhou(context.Background(), alerta()); err != nil {
		t.Fatal(err)
	}
}

// O Slack responde texto puro em erro ("invalid_payload"), nao JSON. Repassar o
// corpo distingue webhook revogado de payload malformado sem abrir o navegador.
func TestErroDoSlackCarregaOMotivo(t *testing.T) {
	s, _ := capturar(t, 403, "invalid_token")
	err := s.Falhou(context.Background(), alerta())
	if err == nil {
		t.Fatal("esperava erro")
	}
	if !strings.Contains(err.Error(), "invalid_token") {
		t.Errorf("erro sem o motivo do Slack: %v", err)
	}
}

// Sem webhook configurado, nao e erro: a instalacao simplesmente nao alerta.
func TestSemWebhookNaoFazNada(t *testing.T) {
	if err := (&notify.Slack{}).Falhou(context.Background(), alerta()); err != nil {
		t.Errorf("instalacao sem webhook virou erro: %v", err)
	}
}

// Sem tags, o dominio sai do prefixo do slug em vez de ficar anonimo.
func TestDominioCaiParaOPrefixoDoSlug(t *testing.T) {
	s, recebido := capturar(t, 200, "ok")
	a := alerta()
	a.Tags = nil
	a.Workflow = "platform_workspace"

	if err := s.Falhou(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(*recebido, "`platform`") {
		t.Errorf("dominio nao derivado do slug:\n%s", *recebido)
	}
}
