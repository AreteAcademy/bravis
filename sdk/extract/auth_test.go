package extract

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/sdk/internal/core"
)

// TestAuthAplicaOSegredo: as quatro formas de por a credencial na requisicao.
func TestAuthAplicaOSegredo(t *testing.T) {
	casos := []struct {
		nome      string
		aplicar   core.Applier
		segredo   string
		cabecalho string
		esperado  string
	}{
		{"bearer", core.AsBearer, "abc", "Authorization", "Bearer abc"},
		{"cookie inteiro", core.AsCookie, "session=abc==", "Cookie", "session=abc=="},
		{"cookie nomeado", core.AsCookieNamed("session"), "abc==", "Cookie", "session=abc=="},
		{"header proprio", core.AsHeader("X-API-Key"), "abc", "X-API-Key", "abc"},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			var visto string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				visto = r.Header.Get(c.cabecalho)
				_, _ = fmt.Fprint(w, `{"ok":1}`)
			}))
			defer srv.Close()

			segredo := c.segredo
			drenar(t, core.Source{URL: srv.URL, Auth: &core.Credential{
				Value: func(context.Context) (string, error) { return segredo, nil },
				Apply: c.aplicar,
			}})

			if visto != c.esperado {
				t.Errorf("%s = %q, esperado %q", c.cabecalho, visto, c.esperado)
			}
		})
	}
}

// TestRefreshRenovaOCookieParaAsPaginas: o mecanismo inteiro. A renovacao
// reemite o cookie, o jar absorve, e as paginas seguintes vao com o novo --
// sem nada ser gravado em lugar nenhum.
func TestRefreshRenovaOCookieParaAsPaginas(t *testing.T) {
	var mu sync.Mutex
	var dados []string

	mux := http.NewServeMux()
	mux.HandleFunc("/auth/session", func(w http.ResponseWriter, r *http.Request) {
		if c, _ := r.Cookie("session"); c == nil || c.Value != "colado==" {
			http.Error(w, "sem o cookie colado", http.StatusUnauthorized)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "session", Value: "renovado==", Path: "/"})
		_, _ = fmt.Fprint(w, `{"expires":"`+time.Now().Add(30*24*time.Hour).Format(time.RFC3339)+`"}`)
	})
	mux.HandleFunc("/dados", func(w http.ResponseWriter, r *http.Request) {
		c, _ := r.Cookie("session")
		mu.Lock()
		if c != nil {
			dados = append(dados, c.Value)
		}
		mu.Unlock()
		_, _ = fmt.Fprint(w, `{"ok":1}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	var stats core.Stats
	drenar(t, core.Source{
		URL:   srv.URL + "/dados",
		Stats: &stats,
		Auth: &core.Credential{
			Value: func(context.Context) (string, error) { return "session=colado==", nil },
			Apply: core.AsCookie,
			Refresh: &core.Refresh{
				URL:       srv.URL + "/auth/session",
				ExpiresAt: core.JSONField("expires"),
			},
		},
	})

	if len(dados) != 1 || dados[0] != "renovado==" {
		t.Errorf("a pagina foi com %v; esperava o cookie reemitido pela renovacao", dados)
	}
	if stats.CredentialExpiry.IsZero() {
		t.Error("Stats.CredentialExpiry ficou zerado; a validade nao chegou a quem observa")
	}
}

// TestRefreshQueFalhaParaARun: seguir depois de uma renovacao recusada manda
// todas as paginas com uma credencial que a API acabou de negar, e o erro
// aparece culpando o endpoint de dados.
func TestRefreshQueFalhaParaARun(t *testing.T) {
	var pediuDados atomic.Bool
	mux := http.NewServeMux()
	mux.HandleFunc("/auth/session", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "sessao expirada", http.StatusUnauthorized)
	})
	mux.HandleFunc("/dados", func(w http.ResponseWriter, r *http.Request) {
		pediuDados.Store(true)
		_, _ = fmt.Fprint(w, `{"ok":1}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	_, err := JSON(context.Background(), core.Source{
		URL: srv.URL + "/dados",
		Auth: &core.Credential{
			Value:   core.FromEnv("PATH"), // qualquer env que exista
			Apply:   core.AsBearer,
			Refresh: &core.Refresh{URL: srv.URL + "/auth/session"},
		},
	}, nil)

	if err == nil {
		t.Fatal("renovacao recusada passou batido")
	}
	if !strings.Contains(err.Error(), "refresh") || !strings.Contains(err.Error(), "401") {
		t.Errorf("o erro nao diz que foi a renovacao: %v", err)
	}
	if pediuDados.Load() {
		t.Error("pediu dados depois da renovacao falhar")
	}
}

// TestTTLCacheiaOLogin: a API do ana bloqueia por FREQUENCIA de auth, nao por
// requisicao. Sem cache, cada pipeline no processo faz um login novo.
func TestTTLCacheiaOLogin(t *testing.T) {
	var logins atomic.Int32
	cred := &core.Credential{
		TTL:   time.Hour,
		Apply: core.AsBearer,
		Value: func(context.Context) (string, error) {
			logins.Add(1)
			return "token", nil
		},
	}

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := cred.Get(context.Background()); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()

	if n := logins.Load(); n != 1 {
		t.Errorf("%d logins para 20 chamadas concorrentes; TTL deveria deixar em 1", n)
	}
}

// TestSemTTLNaoCacheia: um TTL zerado precisa continuar chamando Value, ou o
// campo estaria cacheando sem ninguem pedir.
func TestSemTTLNaoCacheia(t *testing.T) {
	var n int
	cred := &core.Credential{
		Apply: core.AsBearer,
		Value: func(context.Context) (string, error) { n++; return "t", nil },
	}
	for i := 0; i < 3; i++ {
		_, _ = cred.Get(context.Background())
	}
	if n != 3 {
		t.Errorf("Value chamado %d vezes sem TTL, esperado 3", n)
	}
}

// TestAuthRecusaConfiguracaoQueNaoFunciona: cada uma destas passaria e viraria
// 401, ou um campo escrito que nao faz nada.
func TestAuthRecusaConfiguracaoQueNaoFunciona(t *testing.T) {
	casos := []struct {
		nome string
		cred *core.Credential
		diz  string
	}{
		{"sem Value", &core.Credential{Apply: core.AsBearer}, "Value"},
		{"sem Apply", &core.Credential{Value: core.FromEnv("PATH")}, "Apply"},
		{"Refresh sem URL", &core.Credential{
			Value: core.FromEnv("PATH"), Apply: core.AsBearer,
			Refresh: &core.Refresh{},
		}, "URL"},
		{"WarnAfter sem ExpiresAt", &core.Credential{
			Value: core.FromEnv("PATH"), Apply: core.AsBearer,
			Refresh: &core.Refresh{URL: "http://x", WarnAfter: time.Hour},
		}, "ExpiresAt"},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			_, err := JSON(context.Background(), core.Source{URL: "http://x", Auth: c.cred}, nil)
			if err == nil {
				t.Fatal("passou")
			}
			if !strings.Contains(err.Error(), c.diz) {
				t.Errorf("o erro nao aponta %q: %v", c.diz, err)
			}
		})
	}
}

// TestEnvAusenteFalaONome: senao vira header vazio e 401 culpando a API.
func TestEnvAusenteFalaONome(t *testing.T) {
	_, err := core.FromEnv("BREVIS_ENV_QUE_NAO_EXISTE")(context.Background())
	if err == nil || !strings.Contains(err.Error(), "BREVIS_ENV_QUE_NAO_EXISTE") {
		t.Errorf("erro nao nomeia a variavel: %v", err)
	}
}

// TestAuthNaoMutaOHeaderDoCaller: o mapa e do consumidor e nao pode voltar
// carregando o segredo.
func TestAuthNaoMutaOHeaderDoCaller(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"ok":1}`)
	}))
	defer srv.Close()

	h := map[string][]string{"X-Trace": {"1"}}
	drenar(t, core.Source{URL: srv.URL, Header: h, Auth: &core.Credential{
		Value: func(context.Context) (string, error) { return "segredo", nil },
		Apply: core.AsBearer,
	}})

	if _, tem := h["Authorization"]; tem {
		t.Errorf("o segredo ficou no header do caller: %v", h)
	}
}
