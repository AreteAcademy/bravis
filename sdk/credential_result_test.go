package sdk

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/sdk/from"
)

// TestResultCarregaAValidadeDaCredencial: o aviso de validade tem que chegar
// ao Result, e nao so a um slog.Warn. O motivo esta escrito no campo: quem
// roda o pipeline e quem precisa recolar o cookie, e um aviso que so existe
// no log e como a morte silenciosa comeca.
func TestResultCarregaAValidadeDaCredencial(t *testing.T) {
	vence := time.Now().Add(10 * 24 * time.Hour).Truncate(time.Second)

	mux := http.NewServeMux()
	mux.HandleFunc("/auth", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `{"expires":%q}`, vence.Format(time.RFC3339))
	})
	mux.HandleFunc("/dados", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"results":[{"id":"1"}]}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	dados, err := Extract(context.Background(), Source{
		From: from.HTTP{
			URL:     srv.URL + "/dados",
			DataKey: "results",
			Auth: &from.Credential{
				Value: func(context.Context) (string, error) { return "t", nil },
				Apply: from.AsBearer,
				Refresh: &from.Refresh{
					URL:       srv.URL + "/auth",
					ExpiresAt: from.JSONField("expires"),
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}

	res, err := loadWith(context.Background(), dados,
		Target{To: destinoFalso{}, Columns: []string{"id"}}, RunContext{})
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if !res.CredentialExpiry.Equal(vence) {
		t.Errorf("Result.CredentialExpiry = %v, esperado %v", res.CredentialExpiry, vence)
	}
	if !strings.Contains(fmt.Sprint(res.Args()...), "credential_expires") {
		t.Error("Args() nao carrega a validade; a linha do pipeline nao a mostraria")
	}
}

// TestArgsOmiteCredencialQuandoNaoHa: uma chave sempre zerada em toda linha
// ensina quem le a pular ela, e ai ela some justo na linha que importa.
func TestArgsOmiteCredencialQuandoNaoHa(t *testing.T) {
	if got := fmt.Sprint((&Result{}).Args()...); strings.Contains(got, "credential") {
		t.Errorf("Args() carrega a chave sem validade nenhuma: %s", got)
	}
}
