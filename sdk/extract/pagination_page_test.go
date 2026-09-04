package extract

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/sdk/internal/core"
)

// paginaNumerada devolve tres paginas de uma linha e depois vazio, gravando a
// sequencia de numeros que recebeu.
func paginaNumerada(t *testing.T, chave string, vistos *[]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bruto := r.URL.Query().Get(chave)
		*vistos = append(*vistos, bruto)

		n, err := strconv.Atoi(bruto)
		if err != nil {
			http.Error(w, "sem numero de pagina: "+r.URL.RawQuery, http.StatusBadRequest)
			return
		}
		if len(*vistos) > 6 { // trava contra loop infinito no teste
			http.Error(w, "paginou demais", http.StatusInternalServerError)
			return
		}
		if n >= 3 {
			_, _ = fmt.Fprint(w, `{"results":[]}`)
			return
		}
		_, _ = fmt.Fprintf(w, `{"results":[{"n":%d}]}`, n)
	}))
}

// TestPageKeyAndaDeUmEmUm: o motivo do campo existir. Antes disso a receita
// era OffsetKey "page" com PageSize 1, que funcionava por acidente.
func TestPageKeyAndaDeUmEmUm(t *testing.T) {
	var vistos []string
	srv := paginaNumerada(t, "page", &vistos)
	defer srv.Close()

	linhas := colher(t, core.Source{URL: srv.URL, PageKey: "page", DataKey: "results"})

	if linhas != 2 {
		t.Errorf("linhas = %d, esperado 2 (paginas 1 e 2)", linhas)
	}
	if got := strings.Join(vistos, ","); got != "1,2,3" {
		t.Errorf("paginas pedidas = %q, esperado \"1,2,3\"", got)
	}
}

// TestPageKeyNumeraAPrimeiraRequisicao: sem isso o servidor escolhe o padrao
// dele e o SDK adivinha o proximo numero -- adivinhar errado pula uma pagina
// inteira em silencio.
func TestPageKeyNumeraAPrimeiraRequisicao(t *testing.T) {
	casos := []struct {
		nome      string
		url       func(string) string
		primeira  int
		sequencia string
	}{
		{"padrao comeca em 1", func(u string) string { return u }, 0, "1,2,3"},
		{"FirstPage escolhe onde comecar", func(u string) string { return u }, 2, "2,3"},
		{"numero na propria url vence", func(u string) string { return u + "?page=2" }, 0, "2,3"},
		{"a url vence ate contra FirstPage", func(u string) string { return u + "?page=2" }, 9, "2,3"},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			var vistos []string
			srv := paginaNumerada(t, "page", &vistos)
			defer srv.Close()

			colher(t, core.Source{
				URL:       c.url(srv.URL),
				PageKey:   "page",
				FirstPage: c.primeira,
				DataKey:   "results",
			})
			if got := strings.Join(vistos, ","); got != c.sequencia {
				t.Errorf("paginas pedidas = %q, esperado %q", got, c.sequencia)
			}
		})
	}
}

// TestApiIndexadaEmZeroDizNaURL: FirstPage nao consegue expressar "comece no
// zero", porque zero e o valor de quem nao setou. A saida documentada e por
// a pagina zero na URL. Este teste existe para que a doc continue verdadeira.
func TestApiIndexadaEmZeroDizNaURL(t *testing.T) {
	var vistos []string
	srv := paginaNumerada(t, "page", &vistos)
	defer srv.Close()

	colher(t, core.Source{URL: srv.URL + "?page=0", PageKey: "page", DataKey: "results"})
	if got := strings.Join(vistos, ","); got != "0,1,2,3" {
		t.Errorf("paginas pedidas = %q, esperado \"0,1,2,3\"", got)
	}
}

// TestPaginacaoRecusaDuasEstrategias: com duas setadas, uma seria lida e a
// outra ignorada em silencio -- que e o defeito que este SDK vive achando.
func TestPaginacaoRecusaDuasEstrategias(t *testing.T) {
	casos := []core.Source{
		{URL: "http://x", PageKey: "page", OffsetKey: "offset"},
		{URL: "http://x", CursorKey: "c", PageKey: "page"},
		{URL: "http://x", FollowLinks: true, OffsetKey: "offset"},
	}
	for _, s := range casos {
		if _, err := JSON(context.Background(), s, nil); err == nil {
			t.Errorf("aceitou duas estrategias: %+v", s)
		}
	}
}

// TestPageSizeSozinhoENegado: PageSize e o passo do OffsetKey. Setado sem ele,
// nao faz nada -- e quem escreveu achou que fazia.
func TestPageSizeSozinhoENegado(t *testing.T) {
	_, err := JSON(context.Background(), core.Source{URL: "http://x", PageSize: 100}, nil)
	if err == nil {
		t.Fatal("PageSize sem OffsetKey passou")
	}
	if !strings.Contains(err.Error(), "PageKey") {
		t.Errorf("o erro nao aponta para PageKey: %v", err)
	}

	_, err = JSON(context.Background(), core.Source{URL: "http://x", FirstPage: 2}, nil)
	if err == nil {
		t.Fatal("FirstPage sem PageKey passou")
	}
}

func colher(t *testing.T, s core.Source) int {
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
