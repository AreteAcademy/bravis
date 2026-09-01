package auth_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zarvhq/bravis/internal/auth"
)

func credencial(t *testing.T, usuario, senha string) auth.Credencial {
	t.Helper()
	h, err := auth.GerarHash(senha)
	if err != nil {
		t.Fatal(err)
	}
	return auth.Credencial{
		Usuario: usuario, Hash: h,
		Segredo: []byte("um-segredo-de-teste-com-mais-de-32-bytes"),
	}
}

func TestHashConfereASenhaCerta(t *testing.T) {
	h, err := auth.GerarHash("senha-correta-longa")
	if err != nil {
		t.Fatal(err)
	}
	if !auth.ConferirSenha(h, "senha-correta-longa") {
		t.Error("a senha correta foi recusada")
	}
	if auth.ConferirSenha(h, "senha-errada-longa!") {
		t.Error("a senha errada foi aceita")
	}
}

// Dois hashes da MESMA senha precisam diferir: sem sal, hashes iguais no banco
// entregam quais operadores escolheram a mesma senha.
func TestHashUsaSalAleatorio(t *testing.T) {
	a, _ := auth.GerarHash("mesma-senha-aqui")
	b, _ := auth.GerarHash("mesma-senha-aqui")
	if a == b {
		t.Error("dois hashes da mesma senha sairam iguais — nao ha sal")
	}
}

// Hash malformado nunca "passa": o caminho de erro de um verificador de senha e
// exatamente onde um bug vira porta aberta.
func TestHashMalformadoNaoAutentica(t *testing.T) {
	for _, h := range []string{
		"", "abc", "pbkdf2-sha256$", "pbkdf2-sha256$0$c2Fs$Y2hhdmU",
		"pbkdf2-sha256$600000$!!!$!!!", "md5$1$x$y",
		"pbkdf2-sha256$600000$c2Fs$", // chave vazia
	} {
		if auth.ConferirSenha(h, "qualquer-coisa") {
			t.Errorf("hash malformado %q autenticou", h)
		}
		if auth.ConferirSenha(h, "") {
			t.Errorf("hash malformado %q autenticou com senha vazia", h)
		}
	}
}

// Este e o teste que representa o incidente: em dev, um POST anonimo em
// /workflows/<slug>/trigger respondia 303 e disparava um `dbt build` que escreve
// no data warehouse.
func TestDisparoAnonimoEBloqueado(t *testing.T) {
	portao := &auth.Portao{
		Cred: credencial(t, "operador", "senha-de-teste-longa"),
		Proximo: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			t.Error("a requisicao anonima chegou ao handler protegido")
			w.WriteHeader(http.StatusOK)
		}),
	}

	rec := httptest.NewRecorder()
	portao.ServeHTTP(rec, httptest.NewRequest("POST", "/workflows/id_verification/trigger", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d; esperava 401", rec.Code)
	}
}

func TestGetAnonimoVaiParaOLogin(t *testing.T) {
	portao := &auth.Portao{
		Cred:    credencial(t, "operador", "senha-de-teste-longa"),
		Proximo: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
	}

	rec := httptest.NewRecorder()
	portao.ServeHTTP(rec, httptest.NewRequest("GET", "/runs?pagina=2", nil))

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d; esperava 303", rec.Code)
	}
	if destino := rec.Header().Get("Location"); !strings.Contains(destino, "/runs") {
		t.Errorf("Location = %q; deveria trazer o destino original", destino)
	}
}

// As sondas do Kubernetes precisam passar: um /health que pede senha derruba o
// pod em ciclo, e o operador procura o problema no lugar errado.
func TestSondasEAssetsPassamSemSessao(t *testing.T) {
	var chegou []string
	portao := &auth.Portao{
		Cred: credencial(t, "operador", "senha-de-teste-longa"),
		Proximo: http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			chegou = append(chegou, r.URL.Path)
		}),
	}
	for _, caminho := range []string{"/health", "/ready", "/assets/app.css", "/assets/fonts/x.woff2"} {
		portao.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", caminho, nil))
	}
	if len(chegou) != 4 {
		t.Errorf("passaram %v; esperava as quatro rotas livres", chegou)
	}
}

func TestSessaoValidaPassaELevaOUsuario(t *testing.T) {
	cred := credencial(t, "operador", "senha-de-teste-longa")
	var visto string
	portao := &auth.Portao{
		Cred: cred,
		Proximo: http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			visto = auth.De(r.Context())
		}),
	}

	login := httptest.NewRecorder()
	if !portao.Entrar(login, "operador", "senha-de-teste-longa") {
		t.Fatal("a credencial correta foi recusada")
	}
	cookie := login.Result().Cookies()[0]

	req := httptest.NewRequest("GET", "/runs", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	portao.ServeHTTP(rec, req)

	if visto != "operador" {
		t.Errorf("operador no contexto = %q; esperava %q", visto, "operador")
	}
	if rec.Code == http.StatusSeeOther {
		t.Error("a sessao valida foi mandada para o login")
	}
}

// Um cookie forjado nao pode valer. Se a assinatura nao fosse conferida, o
// cliente escreveria o proprio nome de usuario e a propria validade.
func TestCookieForjadoNaoEntra(t *testing.T) {
	cred := credencial(t, "operador", "senha-de-teste-longa")
	portao := &auth.Portao{
		Cred: cred,
		Proximo: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			t.Error("um cookie forjado passou pelo portao")
			w.WriteHeader(http.StatusOK)
		}),
	}
	for _, valor := range []string{
		"operador|99999999999|qualquer-assinatura",
		"operador|99999999999|",
		"operador|99999999999",
		"outro|99999999999|",
		"",
	} {
		req := httptest.NewRequest("GET", "/runs", nil)
		req.AddCookie(&http.Cookie{Name: auth.NomeDoCookie, Value: valor})
		portao.ServeHTTP(httptest.NewRecorder(), req)
	}
}

// Trocar o segredo derruba as sessoes — e a alavanca de emergencia.
func TestTrocarOSegredoInvalidaSessoes(t *testing.T) {
	cred := credencial(t, "operador", "senha-de-teste-longa")
	emissor := &auth.Portao{Cred: cred, Proximo: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})}
	rec := httptest.NewRecorder()
	emissor.Entrar(rec, "operador", "senha-de-teste-longa")
	cookie := rec.Result().Cookies()[0]

	novo := cred
	novo.Segredo = []byte("outro-segredo-completamente-diferente-32")
	portao := &auth.Portao{
		Cred: novo,
		Proximo: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			t.Error("a sessao sobreviveu a troca do segredo")
		}),
	}
	req := httptest.NewRequest("GET", "/runs", nil)
	req.AddCookie(cookie)
	portao.ServeHTTP(httptest.NewRecorder(), req)
}

// `/login?de=https://malicioso` nao pode devolver o operador para fora.
func TestDestinoExternoEDescartado(t *testing.T) {
	for bruto, esperado := range map[string]string{
		"https://malicioso.example": "/",
		"//malicioso.example":       "/",
		"/runs?pagina=2":            "/runs?pagina=2",
		"":                          "/",
	} {
		if d := auth.Destino(bruto); d != esperado {
			t.Errorf("Destino(%q) = %q; esperava %q", bruto, d, esperado)
		}
	}
}

func TestCredencialPelaMetadeERecusada(t *testing.T) {
	if err := (auth.Credencial{Usuario: "operador"}).Validar(); err == nil {
		t.Error("usuario sem hash foi aceito — a porta ficaria aberta achando que fechou")
	}
	if err := (auth.Credencial{Hash: "pbkdf2-sha256$1$a$b"}).Validar(); err == nil {
		t.Error("hash sem usuario foi aceito")
	}
	curto := auth.Credencial{Usuario: "o", Hash: "pbkdf2-sha256$1$a$b", Segredo: []byte("curto")}
	if err := curto.Validar(); err == nil {
		t.Error("segredo curto demais foi aceito")
	}
}
