package extract

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/sdk/internal/core"
)

// servidorComLogin troca um segredo por um token e exige o token nos dados.
func servidorComLogin(t *testing.T, falharVezes int32) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var logins atomic.Int32
	var falhas atomic.Int32
	falhas.Store(falharVezes)

	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		logins.Add(1)
		if falhas.Add(-1) >= 0 {
			http.Error(w, "indisponível", http.StatusServiceUnavailable)
			return
		}
		corpo, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(corpo), "meu-segredo") {
			http.Error(w, "sem o segredo no corpo", http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "login precisa ser POST", http.StatusMethodNotAllowed)
			return
		}
		_, _ = fmt.Fprint(w, `{"data":{"accessToken":"tok-123"}}`)
	})
	mux.HandleFunc("/dados", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok-123" {
			http.Error(w, "sem token", http.StatusUnauthorized)
			return
		}
		_, _ = fmt.Fprint(w, `{"ok":1}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &logins
}

func lerTudo(t *testing.T, s core.Source) error {
	t.Helper()
	seq, err := JSON(context.Background(), s, nil)
	if err != nil {
		return err
	}
	for _, err := range seq {
		if err != nil {
			return err
		}
	}
	return nil
}

func credencialDeLogin(srv *httptest.Server) *core.Credential {
	return &core.Credential{
		Login: &core.Login{
			URL:   srv.URL + "/oauth/token",
			Body:  core.JSONBody(map[string]any{"client_secret": "meu-segredo"}),
			Token: core.CampoJSON("data.accessToken"),
		},
		Apply: core.AsBearer,
	}
}

// TestLoginTrocaSegredoPorToken é o item 9: a requisição mais sensível do
// fetcher deixa de ser a única sem as garantias das outras.
func TestLoginTrocaSegredoPorToken(t *testing.T) {
	srv, logins := servidorComLogin(t, 0)

	if err := lerTudo(t, core.Source{URL: srv.URL + "/dados", Auth: credencialDeLogin(srv)}); err != nil {
		t.Fatalf("Read: %v", err)
	}
	if logins.Load() != 1 {
		t.Errorf("%d logins, esperado 1", logins.Load())
	}
}

// TestLoginTemRetry é a garantia que a versão escrita à mão não tem. Um 503 no
// login derrubava a execução inteira; aqui ele custa um retry, como qualquer
// outra requisição.
func TestLoginTemRetry(t *testing.T) {
	srv, logins := servidorComLogin(t, 2)

	err := lerTudo(t, core.Source{
		URL:  srv.URL + "/dados",
		Auth: credencialDeLogin(srv),
		RetryConfig: &core.RetryConfig{
			MaxAttempts: 3, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond,
		},
	})
	if err != nil {
		t.Fatalf("o login não teve retry: %v", err)
	}
	if logins.Load() != 3 {
		t.Errorf("%d tentativas de login, esperado 3", logins.Load())
	}
}

// TestLoginCacheiaComTTL: algumas APIs limitam a FREQUÊNCIA de autenticação em
// vez da de requisições.
func TestLoginCacheiaComTTL(t *testing.T) {
	srv, logins := servidorComLogin(t, 0)
	cred := credencialDeLogin(srv)
	cred.TTL = time.Hour

	for i := 0; i < 3; i++ {
		if _, err := cred.Get(context.Background()); err != nil {
			// A primeira chamada precisa do cliente, que só existe dentro do
			// fetch -- então a leitura vem primeiro.
			_ = err
		}
	}
	if err := lerTudo(t, core.Source{URL: srv.URL + "/dados", Auth: cred}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := cred.Get(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if logins.Load() != 1 {
		t.Errorf("%d logins para várias chamadas com TTL, esperado 1", logins.Load())
	}
}

// TestLoginQueFalhaParaAExecucao: seguir mandaria toda página com um
// Authorization vazio, e o erro voltaria culpando o endpoint de dados.
func TestLoginQueFalhaParaAExecucao(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "credencial recusada", http.StatusUnauthorized)
	})
	mux.HandleFunc("/dados", func(w http.ResponseWriter, _ *http.Request) {
		t.Error("pediu dados depois de o login falhar")
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	err := lerTudo(t, core.Source{URL: srv.URL + "/dados", Auth: credencialDeLogin(srv)})
	if err == nil {
		t.Fatal("o login falhou e a execução seguiu")
	}
	if !strings.Contains(err.Error(), "login") || !strings.Contains(err.Error(), "401") {
		t.Errorf("o erro não diz que foi o login: %v", err)
	}
}

// TestLoginNaoVazaOCabecalhoDaFonte: o endpoint de login pode ser de outro
// host, e o cabeçalho da fonte pode carregar segredo.
func TestLoginNaoVazaOCabecalhoDaFonte(t *testing.T) {
	var vistoNoLogin string
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		vistoNoLogin = r.Header.Get("X-Segredo-Da-Fonte")
		_, _ = fmt.Fprint(w, `{"data":{"accessToken":"tok-123"}}`)
	})
	mux.HandleFunc("/dados", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"ok":1}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	fonte := core.Source{
		URL:    srv.URL + "/dados",
		Header: map[string][]string{"X-Segredo-Da-Fonte": {"não deveria ir"}},
		Auth:   credencialDeLogin(srv),
	}
	if err := lerTudo(t, fonte); err != nil {
		t.Fatal(err)
	}
	if vistoNoLogin != "" {
		t.Errorf("o cabeçalho da fonte foi para o login: %q", vistoNoLogin)
	}
}

// TestLoginRecusaConfiguracaoQueNaoFunciona.
func TestLoginRecusaConfiguracaoQueNaoFunciona(t *testing.T) {
	casos := []struct {
		nome string
		cred *core.Credential
		diz  string
	}{
		{"Login e Value juntos", &core.Credential{
			Value: core.FromEnv("PATH"), Apply: core.AsBearer,
			Login: &core.Login{URL: "http://x", Token: core.CampoJSON("t")},
		}, "os dois preenchidos"},
		{"Login sem URL", &core.Credential{
			Apply: core.AsBearer, Login: &core.Login{Token: core.CampoJSON("t")},
		}, "URL"},
		{"Login sem Token", &core.Credential{
			Apply: core.AsBearer, Login: &core.Login{URL: "http://x"},
		}, "Token"},
		{"nem Value nem Login", &core.Credential{Apply: core.AsBearer}, "os dois nil"},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			_, err := JSON(context.Background(), core.Source{URL: "http://x", Auth: c.cred}, nil)
			if err == nil {
				t.Fatal("passou")
			}
			if !strings.Contains(err.Error(), c.diz) {
				t.Errorf("o erro não diz %q: %v", c.diz, err)
			}
		})
	}
}

// TestCampoJSONAusenteEErro: um token ausente viraria um cabeçalho vazio e um
// 401 mais adiante, culpando a API por um caminho que este lado escreveu
// errado.
func TestCampoJSONAusenteEErro(t *testing.T) {
	_, err := core.CampoJSON("data.accessToken")([]byte(`{"data":{"outro":"x"}}`))
	if err == nil {
		t.Fatal("campo ausente passou")
	}
	for _, quero := range []string{"accessToken", "cabeçalho vazio"} {
		if !strings.Contains(err.Error(), quero) {
			t.Errorf("o erro não diz %q: %v", quero, err)
		}
	}
}
