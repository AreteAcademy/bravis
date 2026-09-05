package extract

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/sdk/internal/core"
)

// storeEspiao registra o que foi gravado, sem tocar em disco nem em nuvem.
type storeEspiao struct {
	mu        sync.Mutex
	guardado  string
	gravacoes int
}

func (s *storeEspiao) Load() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.guardado, nil
}

func (s *storeEspiao) Save(v string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.guardado = v
	s.gravacoes++
	return nil
}

func (s *storeEspiao) Describe() string { return "store espiao" }

func (s *storeEspiao) estado() (string, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.guardado, s.gravacoes
}

// sessaoDeslogada imita o NextAuth para uma sessao que nao autenticou: HTTP
// 200, corpo `null`, e Set-Cookie LIMPANDO os valores. Nao ha status de erro
// nenhum -- o corpo e o unico lugar onde a diferenca aparece.
func sessaoDeslogada(t *testing.T, cookieDeSaida string) (*httptest.Server, *int) {
	t.Helper()
	var paginas int
	mux := http.NewServeMux()
	mux.HandleFunc("/api/auth/session", func(w http.ResponseWriter, _ *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "session", Value: cookieDeSaida})
		_, _ = fmt.Fprint(w, `null`)
	})
	mux.HandleFunc("/api/proxy/dados", func(w http.ResponseWriter, _ *http.Request) {
		paginas++
		_, _ = fmt.Fprint(w, `{"ok":1}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &paginas
}

func rodarComStore(t *testing.T, srv *httptest.Server, store core.CredentialStore, expiresAt func([]byte) (time.Time, error)) error {
	t.Helper()
	seq, err := JSON(context.Background(), core.Source{
		URL: srv.URL + "/api/proxy/dados",
		Auth: &core.Credential{
			Value: func(context.Context) (string, error) { return "session=colado-por-um-humano", nil },
			Apply: core.AsCookie,
			Refresh: &core.Refresh{
				URL:       srv.URL + "/api/auth/session",
				ExpiresAt: expiresAt,
				Store:     store,
			},
		},
	}, nil)
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

// TestRenovacaoQueNaoAutenticaNaoGrava e o §10 do SDK_V9.md.
//
// O NextAuth responde 200 com corpo null e Set-Cookie ESVAZIANDO os valores
// para uma sessao nao autenticada. Gravando isso, e com a ordem de leitura
// sendo store-antes-da-semente, trocar a env por uma credencial boa deixa de
// resolver: o valor morto vence sempre, e a unica saida e apagar o objeto a
// mao. O sintoma para quem opera e 401 sem explicacao.
func TestRenovacaoQueNaoAutenticaNaoGrava(t *testing.T) {
	srv, _ := sessaoDeslogada(t, "")
	store := &storeEspiao{}

	err := rodarComStore(t, srv, store, core.JSONField("expires"))
	if err == nil {
		t.Fatal("a renovacao nao autenticada devia falhar a execucao")
	}
	if !strings.Contains(err.Error(), "expires") {
		t.Errorf("o erro nao aponta a validade ausente: %v", err)
	}

	guardado, gravacoes := store.estado()
	if gravacoes != 0 {
		t.Errorf("gravou %d vez(es) numa renovacao que nao autenticou; guardou %q",
			gravacoes, guardado)
	}
}

// TestRenovacaoBoaContinuaGravando: o outro lado do critério 2. Sem isto o
// conserto viraria "nunca grava", que resolve o defeito e apaga a feature.
func TestRenovacaoBoaContinuaGravando(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/auth/session", func(w http.ResponseWriter, _ *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "session", Value: "rotacionado"})
		_, _ = fmt.Fprintf(w, `{"expires":%q}`, time.Now().Add(30*24*time.Hour).Format(time.RFC3339))
	})
	mux.HandleFunc("/api/proxy/dados", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"ok":1}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	store := &storeEspiao{}
	if err := rodarComStore(t, srv, store, core.JSONField("expires")); err != nil {
		t.Fatalf("execucao boa falhou: %v", err)
	}

	guardado, gravacoes := store.estado()
	if gravacoes != 1 {
		t.Fatalf("gravacoes = %d, esperado 1", gravacoes)
	}
	if !strings.Contains(guardado, "rotacionado") {
		t.Errorf("guardou %q, esperava o valor rotacionado", guardado)
	}
}

// TestSemExpiresAtGrava: com ExpiresAt nil o SDK nao tem sinal nenhum de que a
// renovacao autenticou -- o status e 200 nos dois casos. Entao ele grava, e o
// aviso na montagem e que diz a quem configurou o que isso custa.
func TestSemExpiresAtGrava(t *testing.T) {
	srv, _ := sessaoDeslogada(t, "seja-la-o-que-for")
	store := &storeEspiao{}

	if err := rodarComStore(t, srv, store, nil); err != nil {
		t.Fatalf("sem ExpiresAt a execucao devia seguir: %v", err)
	}
	if _, gravacoes := store.estado(); gravacoes != 1 {
		t.Errorf("gravacoes = %d, esperado 1", gravacoes)
	}
}

// TestARotacaoValeParaAsPaginasMesmoQuandoExpiresAtFalha e o critério 5: o
// `aplicarRotacao` nao se move. A credencial reemitida tem de valer para esta
// execucao mesmo que a validade nao venha -- o que muda e so quando ela e
// PERSISTIDA.
func TestARotacaoValeParaAsPaginasMesmoQuandoExpiresAtFalha(t *testing.T) {
	var mu sync.Mutex
	var cookieNaPagina string

	mux := http.NewServeMux()
	mux.HandleFunc("/api/auth/session", func(w http.ResponseWriter, _ *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "session", Value: "rotacionado"})
		_, _ = fmt.Fprint(w, `null`) // sem "expires"
	})
	mux.HandleFunc("/api/proxy/dados", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		cookieNaPagina = r.Header.Get("Cookie")
		mu.Unlock()
		_, _ = fmt.Fprint(w, `{"ok":1}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	fonte := core.Source{
		URL: srv.URL + "/api/proxy/dados",
		Auth: &core.Credential{
			Value: func(context.Context) (string, error) { return "session=velho", nil },
			Apply: core.AsCookie,
			Refresh: &core.Refresh{
				URL: srv.URL + "/api/auth/session",
				// Sem ExpiresAt a execucao segue, e e ai que da para observar
				// se a rotacao chegou as paginas.
			},
		},
	}
	seq, err := JSON(context.Background(), fonte, nil)
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	for _, err := range seq {
		if err != nil {
			t.Fatalf("iterando: %v", err)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(cookieNaPagina, "rotacionado") {
		t.Errorf("a pagina foi com %q; a rotacao deixou de valer para a execucao", cookieNaPagina)
	}
}

// TestStoreSemExpiresAtAvisaNaMontagem e o critério 4. Nao e recusa: ha fontes
// cuja renovacao nao devolve validade, e para elas o store ainda vale. Mas o
// limite tem de ser dito a quem configurou -- nessa combinacao o store
// continua envenenavel, e nada em runtime vai revelar isso.
func TestStoreSemExpiresAtAvisaNaMontagem(t *testing.T) {
	var buf bytes.Buffer
	anterior := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(anterior)

	c := &core.Credential{
		Value:   func(context.Context) (string, error) { return "x", nil },
		Apply:   core.AsBearer,
		Refresh: &core.Refresh{URL: "http://x", Store: &storeEspiao{}},
	}
	if err := c.Check(); err != nil {
		t.Fatalf("virou erro em vez de aviso: %v", err)
	}

	log := buf.String()
	for _, exigido := range []string{"ExpiresAt", "store espiao", "200"} {
		if !strings.Contains(log, exigido) {
			t.Errorf("o aviso nao diz %q:\n%s", exigido, log)
		}
	}
}

// TestStoreComExpiresAtNaoAvisa: um aviso que aparece na configuracao certa
// ensina a ignorar avisos.
func TestStoreComExpiresAtNaoAvisa(t *testing.T) {
	var buf bytes.Buffer
	anterior := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(anterior)

	c := &core.Credential{
		Value: func(context.Context) (string, error) { return "x", nil },
		Apply: core.AsBearer,
		Refresh: &core.Refresh{
			URL: "http://x", Store: &storeEspiao{}, ExpiresAt: core.JSONField("expires"),
		},
	}
	if err := c.Check(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "without ExpiresAt") {
		t.Errorf("avisou na configuracao certa:\n%s", buf.String())
	}
}
