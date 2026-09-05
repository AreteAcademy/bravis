package extract

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/sdk/internal/core"
)

// paginadorComMeta serve N páginas e diz, no corpo, se há próxima.
func paginadorComMeta(t *testing.T, paginas int, mente bool) (*httptest.Server, *int) {
	t.Helper()
	var pedidas int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pedidas++
		p := 1
		if v := r.URL.Query().Get("page"); v != "" {
			_, _ = fmt.Sscanf(v, "%d", &p)
		}
		temMais := p < paginas
		if mente {
			// Mente dizendo que sempre há mais; a rede de segurança (página
			// vazia) tem de encerrar mesmo assim.
			temMais = true
		}
		linhas := `[{"n":1}]`
		if p > paginas {
			linhas = `[]`
		}
		_, _ = fmt.Fprintf(w, `{"pageMeta":{"hasNextPage":%t},"results":%s}`, temMais, linhas)
	}))
	t.Cleanup(srv.Close)
	return srv, &pedidas
}

func contar(t *testing.T, s core.Source) int {
	t.Helper()
	seq, err := JSON(context.Background(), s, nil)
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	n := 0
	for _, err := range seq {
		if err != nil {
			t.Fatalf("iterando: %v", err)
		}
		n++
	}
	return n
}

// TestMoreKeyParaSemPedirAPaginaVazia é o item 12: a parada por página vazia
// custa uma requisição a mais POR ORIGEM, e num fan-out de centenas de origens
// isso é centenas de requisições por execução.
func TestMoreKeyParaSemPedirAPaginaVazia(t *testing.T) {
	srv, pedidas := paginadorComMeta(t, 3, false)

	linhas := contar(t, core.Source{
		URL: srv.URL, PageKey: "page", DataKey: "results", MoreKey: "pageMeta.hasNextPage",
	})
	if linhas != 3 {
		t.Errorf("%d linhas, esperado 3", linhas)
	}
	if *pedidas != 3 {
		t.Errorf("%d requisições, esperado 3 -- a quarta é a que o MoreKey economiza", *pedidas)
	}
}

// TestSemMoreKeyPedeAPaginaVazia é o outro lado: sem ele, a quarta requisição
// acontece. É a medida do que o item economiza.
func TestSemMoreKeyPedeAPaginaVazia(t *testing.T) {
	srv, pedidas := paginadorComMeta(t, 3, false)

	contar(t, core.Source{URL: srv.URL, PageKey: "page", DataKey: "results"})
	if *pedidas != 4 {
		t.Errorf("%d requisições, esperado 4 -- a quarta volta vazia e é o que encerra", *pedidas)
	}
}

// TestMoreKeyNaoSubstituiARedeDeSeguranca: uma API que mente no campo não pode
// virar laço infinito.
func TestMoreKeyNaoSubstituiARedeDeSeguranca(t *testing.T) {
	srv, pedidas := paginadorComMeta(t, 2, true)

	linhas := contar(t, core.Source{
		URL: srv.URL, PageKey: "page", DataKey: "results", MoreKey: "pageMeta.hasNextPage",
	})
	if linhas != 2 {
		t.Errorf("%d linhas, esperado 2", linhas)
	}
	// A terceira volta vazia e encerra, apesar de o campo dizer que há mais.
	if *pedidas != 3 {
		t.Errorf("%d requisições; a parada por página vazia devia ter encerrado", *pedidas)
	}
}

// TestMoreKeyAusenteNaoEFimDaPaginacao: tratar ausente como fim faria a
// paginação parar na primeira página em silêncio, que é pior que não ter a
// otimização.
func TestMoreKeyAusenteNaoEFimDaPaginacao(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"results":[{"n":1}]}`)
	}))
	defer srv.Close()

	// O erro vem do JSON e não da iteração: a primeira página é buscada cedo,
	// de propósito, para que um caminho errado falhe antes de o consumidor
	// drenar a sequência.
	_, err := JSON(context.Background(), core.Source{
		URL: srv.URL, DataKey: "results", MoreKey: "pageMeta.hasNextPage",
	}, nil)
	if err == nil {
		t.Fatal("um campo ausente passou como 'não há mais'")
	}
	for _, quero := range []string{"pageMeta", "confira o caminho"} {
		if !strings.Contains(err.Error(), quero) {
			t.Errorf("o erro não diz %q: %v", quero, err)
		}
	}
}

// TestMoreKeyNaRaiz: o caminho sem ponto também vale -- has_more é comum na
// raiz.
func TestMoreKeyNaRaiz(t *testing.T) {
	var pedidas int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pedidas++
		p := 1
		if v := r.URL.Query().Get("page"); v != "" {
			_, _ = fmt.Sscanf(v, "%d", &p)
		}
		_, _ = fmt.Fprintf(w, `{"has_more":%t,"data":[{"n":%d}]}`, p < 2, p)
	}))
	defer srv.Close()

	if n := contar(t, core.Source{
		URL: srv.URL, PageKey: "page", DataKey: "data", MoreKey: "has_more",
	}); n != 2 {
		t.Errorf("%d linhas, esperado 2", n)
	}
	if pedidas != 2 {
		t.Errorf("%d requisições, esperado 2", pedidas)
	}
}

// TestMoreKeyNuloEFim: null é a forma que várias APIs usam para "acabou".
func TestMoreKeyNuloEFim(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"next":null,"data":[{"n":1}]}`)
	}))
	defer srv.Close()

	if n := contar(t, core.Source{URL: srv.URL, DataKey: "data", MoreKey: "next"}); n != 1 {
		t.Errorf("%d linhas, esperado 1", n)
	}
}

// TestMoreKeyQueNaoLevaABooleano: um campo que existe e não é booleano é erro
// nomeando o tipo, e não um palpite sobre a verdade-falsidade dele.
func TestMoreKeyQueNaoLevaABooleano(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"meta":{"next":"sim"},"data":[{"n":1}]}`)
	}))
	defer srv.Close()

	_, err := JSON(context.Background(), core.Source{
		URL: srv.URL, DataKey: "data", MoreKey: "meta.next",
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "booleano") {
		t.Errorf("erro = %v", err)
	}
}
