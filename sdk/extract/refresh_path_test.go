package extract

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/sdk/internal/core"
)

// TestRefreshRecebeACredencialEmOutroPrefixo e o §9 do SDK_V9.md.
//
// AsCookie semeia o jar a partir da URL da FONTE, e o cookiejar do Go, quando
// o cookie nao traz Path, usa o diretorio dessa URL. Com a fonte em
// /api/proxy/... e a renovacao em /api/auth/session, o jar nao envia nada --
// o endpoint responde null para nao autenticado, ExpiresAt nao acha "expires",
// e a execucao morre antes da primeira pagina.
//
// O teste que existia usava srv.URL + "/dados", cujo diretorio e "/", que casa
// com tudo. Passava porque a fonte estava na raiz, e nenhuma API de verdade
// esta.
func TestRefreshRecebeACredencialEmOutroPrefixo(t *testing.T) {
	casos := []struct {
		nome    string
		fonte   string
		renovar string
	}{
		{"prefixos diferentes", "/api/proxy/occurrences", "/api/auth/session"},
		{"mesmo prefixo", "/api/proxy/occurrences", "/api/proxy/session"},
		{"fonte na raiz", "/dados", "/auth/session"},
		{"renovacao mais funda", "/api/dados", "/api/v2/interno/auth/session"},
	}

	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			var mu sync.Mutex
			var cookieNaRenovacao string
			var cookieNasPaginas []string

			mux := http.NewServeMux()
			mux.HandleFunc(c.renovar, func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				cookieNaRenovacao = r.Header.Get("Cookie")
				mu.Unlock()

				// Como a API real: sem credencial, responde null.
				if _, err := r.Cookie("session"); err != nil {
					_, _ = fmt.Fprint(w, `null`)
					return
				}
				http.SetCookie(w, &http.Cookie{Name: "session", Value: "renovado=="})
				_, _ = fmt.Fprintf(w, `{"expires":%q}`, time.Now().Add(30*24*time.Hour).Format(time.RFC3339))
			})
			mux.HandleFunc(c.fonte, func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				cookieNasPaginas = append(cookieNasPaginas, r.Header.Get("Cookie"))
				mu.Unlock()
				_, _ = fmt.Fprint(w, `{"ok":1}`)
			})
			srv := httptest.NewServer(mux)
			defer srv.Close()

			var stats core.Stats
			seq, err := JSON(context.Background(), core.Source{
				URL:   srv.URL + c.fonte,
				Stats: &stats,
				Auth: &core.Credential{
					Value: func(context.Context) (string, error) { return "session=colado==", nil },
					Apply: core.AsCookie,
					Refresh: &core.Refresh{
						URL:       srv.URL + c.renovar,
						ExpiresAt: core.JSONField("expires"),
					},
				},
			}, nil)
			if err != nil {
				t.Fatalf("a execucao morreu: %v", err)
			}
			for _, err := range seq {
				if err != nil {
					t.Fatalf("iterando: %v", err)
				}
			}

			if cookieNaRenovacao == "" {
				t.Error("a renovacao foi SEM a credencial")
			}
			// E o cookie REEMITIDO tem de valer para as paginas, senao a
			// renovacao renovou para ninguem: o valor novo ficaria preso ao
			// diretorio da URL de renovacao e as paginas seguiriam com o
			// antigo, que e o mesmo defeito na direcao oposta.
			//
			// O servidor de teste reemite SEM Path, de proposito: e o padrao
			// do RFC 6265 e o caso que quebra.
			for _, got := range cookieNasPaginas {
				if got == "" {
					t.Error("a pagina foi sem credencial nenhuma")
				}
				if !strings.Contains(got, "renovado==") {
					t.Errorf("a pagina foi com %q; esperava o valor reemitido pela renovacao", got)
				}
			}
			if stats.CredentialExpiry.IsZero() {
				t.Error("a validade nao chegou; a renovacao nao autenticou")
			}
		})
	}
}
