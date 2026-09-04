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

// jwtDePadding imita um token do NextAuth: base64 com "=" de padding, que e o
// caractere que quebra quem divide nome=valor em todos os "=".
const jwtDePadding = "eyJhbGciOiJkaXIiLCJlbmMiOiJBMjU2R0NNIn0..QUJDRA=="

// TestCookieDoCallerChegaInteiro: o consumidor escreveu 36 linhas para juntar
// Set-Cookie ao header, e a armadilha foi cortar o JWT no segundo "=". Aqui o
// cookie precisa chegar identico ao que o caller passou.
func TestCookieDoCallerChegaInteiro(t *testing.T) {
	var recebido string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie("session-token")
		if err != nil {
			http.Error(w, "sem cookie", http.StatusUnauthorized)
			return
		}
		recebido = c.Value
		_, _ = fmt.Fprint(w, `{"ok":1}`)
	}))
	defer srv.Close()

	drenar(t, core.Source{
		URL:    srv.URL,
		Header: map[string][]string{"Cookie": {"session-token=" + jwtDePadding}},
	})

	if recebido != jwtDePadding {
		t.Errorf("o servidor recebeu %q, o caller mandou %q", recebido, jwtDePadding)
	}
}

// TestCookieRenovadoSobreveveAProximaPagina: era exatamente o que o cookie.go
// do consumidor fazia a mao. Se o jar sumir, a pagina 2 vai com o token velho.
func TestCookieRenovadoSobreveveAProximaPagina(t *testing.T) {
	var vistos []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie("session-token")
		if err != nil {
			http.Error(w, "sem cookie", http.StatusUnauthorized)
			return
		}
		vistos = append(vistos, c.Value)

		if len(vistos) == 1 {
			// a api renova a sessao no meio da caminhada
			http.SetCookie(w, &http.Cookie{Name: "session-token", Value: "renovado==", Path: "/"})
			_, _ = fmt.Fprint(w, `{"results":[{"n":1}]}`)
			return
		}
		_, _ = fmt.Fprint(w, `{"results":[]}`)
	}))
	defer srv.Close()

	drenar(t, core.Source{
		URL:     srv.URL,
		PageKey: "page",
		DataKey: "results",
		Header:  map[string][]string{"Cookie": {"session-token=" + jwtDePadding}},
	})

	if len(vistos) != 2 {
		t.Fatalf("esperava 2 requisicoes, houve %d", len(vistos))
	}
	if vistos[1] != "renovado==" {
		t.Errorf("a pagina 2 foi com %q; o Set-Cookie da pagina 1 nao pegou", vistos[1])
	}
}

// TestCookieNaoVaiDuplicado: o header do caller e o jar sao a mesma coisa. Se
// os dois forem juntos, o servidor recebe dois valores para o mesmo nome e
// escolhe um deles -- silenciosamente o errado.
func TestCookieNaoVaiDuplicado(t *testing.T) {
	var bruto string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bruto = r.Header.Get("Cookie")
		http.SetCookie(w, &http.Cookie{Name: "session-token", Value: "renovado==", Path: "/"})
		if strings.Contains(bruto, "renovado") {
			_, _ = fmt.Fprint(w, `{"results":[]}`)
			return
		}
		_, _ = fmt.Fprint(w, `{"results":[{"n":1}]}`)
	}))
	defer srv.Close()

	drenar(t, core.Source{
		URL:     srv.URL,
		PageKey: "page",
		DataKey: "results",
		Header:  map[string][]string{"Cookie": {"session-token=" + jwtDePadding}},
	})

	if n := strings.Count(bruto, "session-token="); n != 1 {
		t.Errorf("session-token aparece %d vezes no header: %q", n, bruto)
	}
}

// TestCookieMalformadoFalhaCedo: um header de cookie invalido tem que reclamar
// na montagem, nao virar 401 no servidor.
func TestCookieMalformadoFalhaCedo(t *testing.T) {
	_, err := JSON(context.Background(), core.Source{
		URL:    "http://exemplo.invalido",
		Header: map[string][]string{"Cookie": {"isso nao e um cookie"}},
	}, nil)
	if err == nil {
		t.Fatal("cookie invalido passou")
	}
	if !strings.Contains(err.Error(), "Cookie") {
		t.Errorf("o erro nao diz que o problema e o Cookie: %v", err)
	}
}

// TestHeaderDoCallerNaoEMutado: o header e do consumidor, e ele pode reusar o
// mesmo mapa em outra pipeline.
func TestHeaderDoCallerNaoEMutado(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"ok":1}`)
	}))
	defer srv.Close()

	h := map[string][]string{"Cookie": {"session-token=" + jwtDePadding}}
	drenar(t, core.Source{URL: srv.URL, Header: h})

	if got := http.Header(h).Get("Cookie"); got != "session-token="+jwtDePadding {
		t.Errorf("o SDK mexeu no header do caller: %q", got)
	}
}

func drenar(t *testing.T, s core.Source) {
	t.Helper()
	seq, err := JSON(context.Background(), s, nil)
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	for _, err := range seq {
		if err != nil {
			t.Fatalf("iterando: %v", err)
		}
	}
}

// TestCookieSecurePrefixoNaoSomeEmSilencio: o nome real do cookie do NextAuth
// comeca com __Secure-, que numa spec de navegador so vale sobre https. Se o
// jar aplicasse essa regra, o cookie sumiria antes de sair -- e o SDK falharia
// com 401 sem nunca dizer que descartou a credencial.
//
// O jar da stdlib nao aplica a regra do prefixo. Este teste existe para o dia
// em que isso mudar.
func TestCookieSecurePrefixoNaoSomeEmSilencio(t *testing.T) {
	var visto string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		visto = r.Header.Get("Cookie")
		_, _ = fmt.Fprint(w, `{"ok":1}`)
	}))
	defer srv.Close()

	drenar(t, core.Source{URL: srv.URL, Auth: &core.Credential{
		Value: func(context.Context) (string, error) {
			return "__Secure-authjs.session-token=abc==", nil
		},
		Apply: core.AsCookie,
	}})

	if visto == "" {
		t.Fatal("o cookie __Secure- sumiu antes de chegar ao servidor")
	}
}
