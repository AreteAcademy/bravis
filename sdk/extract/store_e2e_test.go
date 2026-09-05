package extract

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/sdk/internal/core"
)

// TestSegundaExecucaoUsaOQueVeioDoVolume e a prova do §7.13 da spec: rodar
// duas vezes com a semente REMOVIDA depois da primeira, e a segunda autenticar
// com o que veio do store.
//
// E literalmente o que o consumidor pediu -- parar de recolar o cookie por
// janela.
func TestSegundaExecucaoUsaOQueVeioDoVolume(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(core.EnvCredentialDir, dir)
	t.Setenv(core.EnvCredentialKey, "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")

	var mu sync.Mutex
	var vistos []string
	geracao := 0

	mux := http.NewServeMux()
	mux.HandleFunc("/api/auth/session", func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie("session")
		mu.Lock()
		if err == nil {
			vistos = append(vistos, c.Value)
		}
		mu.Unlock()
		if err != nil {
			_, _ = fmt.Fprint(w, `null`)
			return
		}
		geracao++
		http.SetCookie(w, &http.Cookie{Name: "session", Value: fmt.Sprintf("rotacionado-%d", geracao)})
		_, _ = fmt.Fprintf(w, `{"expires":%q}`, time.Now().Add(30*24*time.Hour).Format(time.RFC3339))
	})
	mux.HandleFunc("/api/proxy/occurrences", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"ok":1}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	rodar := func(semente string) error {
		fonte := core.Source{
			URL: srv.URL + "/api/proxy/occurrences",
			Auth: &core.Credential{
				Value: func(context.Context) (string, error) {
					if semente == "" {
						return "", fmt.Errorf("GABRIEL_SESSION_COOKIE nao esta definida")
					}
					return "session=" + semente, nil
				},
				Apply: core.AsCookie,
				Refresh: &core.Refresh{
					URL:       srv.URL + "/api/auth/session",
					ExpiresAt: core.JSONField("expires"),
					Store:     core.FileStore{Name: "gabriel-session"},
				},
			},
		}
		seq, err := JSON(context.Background(), fonte, nil)
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

	// Primeira: a semente existe, e a rotação é gravada.
	if err := rodar("colado-pelo-humano"); err != nil {
		t.Fatalf("primeira execucao: %v", err)
	}

	// Segunda: a semente FOI EMBORA. Só o volume tem credencial.
	if err := rodar(""); err != nil {
		t.Fatalf("segunda execucao, sem a semente: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(vistos) != 2 {
		t.Fatalf("a renovacao recebeu credencial %d vezes, esperado 2: %v", len(vistos), vistos)
	}
	if vistos[0] != "colado-pelo-humano" {
		t.Errorf("a primeira execucao usou %q", vistos[0])
	}
	if vistos[1] != "rotacionado-1" {
		t.Errorf("a segunda execucao usou %q; esperava o valor que a primeira gravou", vistos[1])
	}
}

// TestFalhaAoGravarNaoDerrubaAExecucao: a carga ja aconteceu; o que se perdeu
// foi a rotacao. Mas tem de gritar, e em Stats -- nao so no log.
func TestFalhaAoGravarNaoDerrubaAExecucao(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/auth", func(w http.ResponseWriter, _ *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "session", Value: "novo"})
		_, _ = fmt.Fprint(w, `{"ok":1}`)
	})
	mux.HandleFunc("/dados", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"ok":1}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	var stats core.Stats
	seq, err := JSON(context.Background(), core.Source{
		URL:   srv.URL + "/dados",
		Stats: &stats,
		Auth: &core.Credential{
			Value: func(context.Context) (string, error) { return "session=velho", nil },
			Apply: core.AsCookie,
			Refresh: &core.Refresh{
				URL:   srv.URL + "/auth",
				Store: storeQueNaoGrava{},
			},
		},
	}, nil)
	if err != nil {
		t.Fatalf("a execucao morreu por causa da escrita: %v", err)
	}
	linhas := 0
	for _, err := range seq {
		if err != nil {
			t.Fatalf("iterando: %v", err)
		}
		linhas++
	}
	if linhas != 1 {
		t.Errorf("linhas = %d; a extracao devia ter acontecido", linhas)
	}
	if stats.CredentialStoreError == "" {
		t.Error("a falha de escrita nao chegou a Stats; so o log a teria")
	}
}

type storeQueNaoGrava struct{}

func (storeQueNaoGrava) Load() (string, error) { return "", nil }
func (storeQueNaoGrava) Save(string) error     { return fmt.Errorf("disco cheio") }
func (storeQueNaoGrava) Describe() string      { return "store de teste" }

// TestACredencialNuncaAparecEmLog e o §7.8 da spec. Um valor que vaza para o
// log vaza para o agregador de logs, que muita gente le -- e seria repetir,
// noutro lugar, o erro de guardar credencial onde nao se guarda.
func TestACredencialNuncaAparecEmLog(t *testing.T) {
	const segredo = "eyJhbGciOiJkaXIiLCJlbmMiOiJBMjU2R0NNIn0..MUlTVFJP"
	const rotacionado = "ROTACIONADO-eyJhbGciOiJkaXI"

	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(core.EnvCredentialDir, dir)
	t.Setenv(core.EnvCredentialKey, "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")

	mux := http.NewServeMux()
	mux.HandleFunc("/api/auth/session", func(w http.ResponseWriter, _ *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "session", Value: rotacionado})
		_, _ = fmt.Fprintf(w, `{"expires":%q}`, time.Now().Add(time.Hour).Format(time.RFC3339))
	})
	mux.HandleFunc("/api/proxy/dados", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"ok":1}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	var buf bytes.Buffer
	anterior := slog.Default()
	// Debug: se algo vazasse so no nivel mais baixo, o teste tem de ver.
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(anterior)

	seq, err := JSON(context.Background(), core.Source{
		URL: srv.URL + "/api/proxy/dados",
		Auth: &core.Credential{
			Value: func(context.Context) (string, error) { return "session=" + segredo, nil },
			Apply: core.AsCookie,
			Refresh: &core.Refresh{
				URL:       srv.URL + "/api/auth/session",
				ExpiresAt: core.JSONField("expires"),
				Store:     core.FileStore{Name: "gabriel-session"},
			},
		},
	}, nil)
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	for _, err := range seq {
		if err != nil {
			t.Fatalf("iterando: %v", err)
		}
	}

	log := buf.String()
	for nome, valor := range map[string]string{"a semente": segredo, "o rotacionado": rotacionado} {
		if strings.Contains(log, valor) {
			t.Errorf("%s vazou para o log:\n%s", nome, log)
		}
		// Nem truncada: um prefixo de credencial ainda e credencial parcial.
		if strings.Contains(log, valor[:16]) {
			t.Errorf("%s vazou truncada para o log:\n%s", nome, log)
		}
	}
}
