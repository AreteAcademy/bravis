package gcs

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"cloud.google.com/go/storage"
	"google.golang.org/api/option"

	"github.com/AreteAcademy/brevis/sdk/internal/core"
)

// gcsFalso e um GCS de mentira com o que importa aqui: geracao por objeto, e
// ifGenerationMatch de verdade.
type gcsFalso struct {
	mu        sync.Mutex
	conteudo  []byte
	geracao   int64
	existe    bool
	conflitos int
}

func (g *gcsFalso) servidor(t *testing.T) *storage.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()

		// O cliente baixa o objeto por GET no caminho XML (/bucket/objeto),
		// e nao pela API JSON -- foi assim que a primeira versao deste falso
		// errou.
		if r.Method == http.MethodGet {
			if !g.existe {
				http.Error(w, `{"error":{"code":404}}`, http.StatusNotFound)
				return
			}
			w.Header().Set("x-goog-generation", fmt.Sprint(g.geracao))
			w.Header().Set("Content-Length", fmt.Sprint(len(g.conteudo)))
			_, _ = w.Write(g.conteudo)
			return
		}

		if r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/upload/") {
			q := r.URL.Query()
			if v := q.Get("ifGenerationMatch"); v != "" {
				esperado := v == fmt.Sprint(g.geracao)
				if v == "0" {
					esperado = !g.existe
				}
				if !esperado {
					g.conflitos++
					http.Error(w, `{"error":{"code":412,"message":"generation mismatch"}}`,
						http.StatusPreconditionFailed)
					return
				}
			}
			corpo, _ := io.ReadAll(r.Body)
			g.conteudo = extrairCorpoMultipart(corpo)
			g.geracao++
			g.existe = true
			_ = json.NewEncoder(w).Encode(map[string]any{
				"name": "obj", "bucket": "b", "generation": fmt.Sprint(g.geracao),
			})
			return
		}
		http.Error(w, "nao esperado: "+r.Method+" "+r.URL.Path, http.StatusBadRequest)
	}))
	t.Cleanup(srv.Close)

	cli, err := storage.NewClient(context.Background(),
		option.WithEndpoint(srv.URL), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cli.Close() })
	return cli
}

// extrairCorpoMultipart pega a segunda parte do upload multipart do GCS.
func extrairCorpoMultipart(b []byte) []byte {
	partes := strings.Split(string(b), "\r\n\r\n")
	if len(partes) < 3 {
		return b
	}
	corpo := partes[2]
	if i := strings.LastIndex(corpo, "\r\n--"); i >= 0 {
		corpo = corpo[:i]
	}
	return []byte(corpo)
}

func credencial(t *testing.T, g *gcsFalso) Credential {
	t.Helper()
	t.Setenv(core.EnvCredentialKey, "")
	geracoes.Delete("gs://b/obj")
	return Credential{Bucket: "b", Object: "obj", Client: g.servidor(t)}
}

// TestGuardaEDevolve: o caminho feliz.
func TestGuardaEDevolve(t *testing.T) {
	g := &gcsFalso{}
	c := credencial(t, g)

	if v, err := c.Load(); err != nil || v != "" {
		t.Fatalf("objeto ausente = (%q, %v); devia ser vazio sem erro", v, err)
	}
	if err := c.Save("session=abc=="); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := c.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got != "session=abc==" {
		t.Errorf("Load = %q", got)
	}
}

// TestEscritaCondicional: quem leu a geracao 1 e tenta gravar depois de outro
// ter gravado a 2 recebe 412 -- e NAO sobrescreve. E a diferenca entre CAS de
// verdade e ultimo-vence, que era o que um volume permitiria.
func TestEscritaCondicional(t *testing.T) {
	g := &gcsFalso{}
	c := credencial(t, g)

	if err := c.Save("primeiro"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Load(); err != nil { // lembra a geracao 1
		t.Fatal(err)
	}

	// Outro processo grava por fora, avancando a geracao.
	g.mu.Lock()
	g.geracao++
	g.conteudo = []byte("brevis-cred/1p\nde-outro-processo")
	g.mu.Unlock()

	// A gravacao desta execucao estava condicionada a geracao que ela leu.
	if err := c.Save("meu-valor"); err != nil {
		t.Fatalf("o conflito virou erro: %v", err)
	}
	g.mu.Lock()
	conflitos, conteudo := g.conflitos, string(g.conteudo)
	g.mu.Unlock()

	if conflitos != 1 {
		t.Errorf("conflitos = %d, esperado 1 -- a escrita nao foi condicional", conflitos)
	}
	if !strings.Contains(conteudo, "de-outro-processo") {
		t.Errorf("sobrescreveu o valor mais novo: %q", conteudo)
	}
}

// TestPrimeiraGravacaoUsaDoesNotExist: sem isso, duas primeiras execucoes
// simultaneas gravariam as duas, e a mais velha poderia chegar por ultimo.
func TestPrimeiraGravacaoUsaDoesNotExist(t *testing.T) {
	g := &gcsFalso{}
	c := credencial(t, g)

	if _, err := c.Load(); err != nil { // objeto ausente, geracao 0
		t.Fatal(err)
	}
	// Outro processo cria o objeto antes.
	g.mu.Lock()
	g.existe, g.geracao, g.conteudo = true, 7, []byte("brevis-cred/1p\nde-outro")
	g.mu.Unlock()

	if err := c.Save("meu"); err != nil {
		t.Fatalf("o conflito virou erro: %v", err)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.conflitos != 1 {
		t.Errorf("conflitos = %d; a primeira gravacao nao usou DoesNotExist", g.conflitos)
	}
	if !strings.Contains(string(g.conteudo), "de-outro") {
		t.Errorf("sobrescreveu: %q", g.conteudo)
	}
}

// TestBucketEObjetoSaoObrigatorios.
func TestBucketEObjetoSaoObrigatorios(t *testing.T) {
	for _, c := range []Credential{{Object: "o"}, {Bucket: "b"}, {}} {
		if err := c.CheckStore(); err == nil {
			t.Errorf("aceitou %+v", c)
		}
	}
}

// TestDescribeNaoRevelaNada: Describe vai para log.
func TestDescribeNaoRevelaNada(t *testing.T) {
	c := Credential{Bucket: "b", Object: "o", Key: "chave-secreta-aqui"}
	if strings.Contains(c.Describe(), "chave-secreta") {
		t.Errorf("Describe vaza a chave: %q", c.Describe())
	}
	if c.Describe() != "gs://b/o" {
		t.Errorf("Describe = %q", c.Describe())
	}
}
