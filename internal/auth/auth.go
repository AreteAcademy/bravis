// Package auth fecha a interface do Brevis com uma credencial de operador.
//
// Existe por um motivo concreto: em dev, um `POST /workflows/<slug>/trigger`
// anonimo respondia 303 e disparava a pipeline. Qualquer pessoa na internet
// podia rodar um `dbt build` que faz MERGE no data warehouse. Uma interface de
// orquestracao e um controle remoto do warehouse — deixa-la aberta e o mesmo
// que publicar o terminal.
//
// O escopo e deliberadamente pequeno: UMA credencial de operador, vinda da
// configuracao. Nao ha cadastro, papeis nem multi-usuario, porque nada disso
// existe no produto ainda e inventa-los aqui seria construir o andar antes da
// parede. O que existe precisa estar certo: hash com derivacao lenta, sessao
// assinada, comparacao em tempo constante.
//
// Tudo em stdlib. `crypto/pbkdf2` entrou na biblioteca padrao no Go 1.24, o que
// dispensa `x/crypto` para o unico pedaco que faltava.
package auth

import (
	"context"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// iteracoes do PBKDF2. O numero e alto de proposito: o custo e pago uma vez por
// login humano, e e exatamente ele que torna caro um ataque de dicionario sobre
// um hash vazado.
const iteracoes = 600_000

// tamanhoChave e o do SHA-256 — nao ha ganho em derivar mais bytes que o hash.
const tamanhoChave = 32

// ValidadeDaSessao e quanto tempo um login vale. Um turno de trabalho: curto o
// bastante para uma aba esquecida num notebook nao virar acesso permanente,
// longo o bastante para nao pedir senha no meio de uma investigacao.
const ValidadeDaSessao = 12 * time.Hour

// NomeDoCookie e o do cookie de sessao.
const NomeDoCookie = "brevis_sessao"

// ---------------------------------------------------------------------------
// Hash de senha
// ---------------------------------------------------------------------------

// GerarHash produz o texto que vai na configuracao, no formato
// `pbkdf2-sha256$<iteracoes>$<sal>$<chave>`.
//
// O formato carrega o numero de iteracoes junto porque ele vai mudar: quando
// dobrarmos o custo daqui a alguns anos, hashes antigos precisam continuar
// conferindo. Um formato que so guarda o digest obriga a invalidar todo mundo.
func GerarHash(senha string) (string, error) {
	sal := make([]byte, 16)
	if _, err := rand.Read(sal); err != nil {
		return "", err
	}
	chave, err := pbkdf2.Key(sha256.New, senha, sal, iteracoes, tamanhoChave)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("pbkdf2-sha256$%d$%s$%s", iteracoes,
		base64.RawStdEncoding.EncodeToString(sal),
		base64.RawStdEncoding.EncodeToString(chave)), nil
}

// ConferirSenha compara a senha com o hash em tempo constante.
//
// Devolve false — e nao erro — para hash malformado: quem chama esta num
// caminho de login, e a unica resposta segura ali e "nao entrou". O erro de
// configuracao e pego no boot, por Credencial.Validar.
func ConferirSenha(hash, senha string) bool {
	partes := strings.Split(hash, "$")
	if len(partes) != 4 || partes[0] != "pbkdf2-sha256" {
		return false
	}
	iter, err := strconv.Atoi(partes[1])
	if err != nil || iter <= 0 {
		return false
	}
	sal, err := base64.RawStdEncoding.DecodeString(partes[2])
	if err != nil {
		return false
	}
	esperado, err := base64.RawStdEncoding.DecodeString(partes[3])
	if err != nil {
		return false
	}
	obtido, err := pbkdf2.Key(sha256.New, senha, sal, iter, len(esperado))
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(obtido, esperado) == 1
}

// ---------------------------------------------------------------------------
// Credencial
// ---------------------------------------------------------------------------

// Credencial e o operador unico da instalacao, vindo da configuracao.
type Credencial struct {
	Usuario string
	Hash    string

	// Segredo assina o cookie de sessao. Trocá-lo derruba todas as sessoes,
	// que e a alavanca de emergencia quando se suspeita de vazamento.
	Segredo []byte
}

// Ativa diz se ha credencial configurada.
func (c Credencial) Ativa() bool {
	return c.Usuario != "" && c.Hash != ""
}

// Validar recusa configuracao pela metade.
//
// Metade configurada e pior que nada: quem preencheu o usuario acredita que
// fechou a porta. Falhar no boot e a unica forma de essa crenca nao durar ate o
// incidente.
func (c Credencial) Validar() error {
	if !c.Ativa() {
		if c.Usuario != "" || c.Hash != "" {
			return errors.New("credencial pela metade: BREVIS_AUTH_USUARIO e " +
				"BREVIS_AUTH_SENHA_HASH precisam vir juntos")
		}
		return nil
	}
	if !strings.HasPrefix(c.Hash, "pbkdf2-sha256$") {
		return errors.New("BREVIS_AUTH_SENHA_HASH nao esta no formato esperado; " +
			"gere com `brevis hash`")
	}
	if len(c.Segredo) < 32 {
		return errors.New("BREVIS_AUTH_SEGREDO precisa de ao menos 32 bytes " +
			"(gere com `openssl rand -base64 48`)")
	}
	return nil
}

// ---------------------------------------------------------------------------
// Sessao
// ---------------------------------------------------------------------------

// emitir monta o valor assinado do cookie: `<usuario>|<expira>|<hmac>`.
//
// A assinatura cobre usuario E expiracao. Cobrir so o usuario deixaria o
// cliente escolher a propria validade; cobrir so a expiracao deixaria trocar de
// usuario. E HMAC, e nao um hash do segredo concatenado, porque a construcao
// ingenua e vulneravel a extensao de comprimento.
func (c Credencial) emitir(agora time.Time) string {
	corpo := c.Usuario + "|" + strconv.FormatInt(agora.Add(ValidadeDaSessao).Unix(), 10)
	return corpo + "|" + base64.RawURLEncoding.EncodeToString(c.assinar(corpo))
}

func (c Credencial) assinar(corpo string) []byte {
	m := hmac.New(sha256.New, c.Segredo)
	m.Write([]byte(corpo))
	return m.Sum(nil)
}

// conferirSessao valida assinatura e prazo do cookie.
func (c Credencial) conferirSessao(valor string, agora time.Time) bool {
	i := strings.LastIndex(valor, "|")
	if i < 0 {
		return false
	}
	corpo, assinatura := valor[:i], valor[i+1:]

	bruta, err := base64.RawURLEncoding.DecodeString(assinatura)
	if err != nil {
		return false
	}
	// A assinatura e conferida ANTES do prazo, e em tempo constante: ler um
	// campo de um cookie nao assinado ja e confiar nele.
	if !hmac.Equal(bruta, c.assinar(corpo)) {
		return false
	}

	usuario, prazo, ok := strings.Cut(corpo, "|")
	if !ok || usuario != c.Usuario {
		// Usuario diferente do configurado: a credencial mudou desde o login.
		return false
	}
	expira, err := strconv.ParseInt(prazo, 10, 64)
	if err != nil {
		return false
	}
	return agora.Unix() < expira
}

// ---------------------------------------------------------------------------
// Middleware
// ---------------------------------------------------------------------------

// Portao envolve um handler exigindo sessao valida.
//
// `Rotas` que dispensam sessao sao poucas e explicitas. Sondas do Kubernetes
// entram nessa lista por necessidade — um /health que pede senha derruba o pod.
type Portao struct {
	Cred     Credencial
	Proximo  http.Handler
	Login    http.Handler // renderiza a tela de login
	Inseguro bool         // http puro: manda o cookie sem a flag Secure
}

// livre lista o que responde sem sessao.
func livre(caminho string) bool {
	switch caminho {
	case "/health", "/ready", "/login", "/logout":
		return true
	}
	// Os assets sao publicos por natureza: CSS, fontes e JS da propria tela de
	// login. Protege-los quebraria a pagina que pede a senha.
	return strings.HasPrefix(caminho, "/assets/")
}

func (p *Portao) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case !p.Cred.Ativa(), livre(r.URL.Path):
		p.Proximo.ServeHTTP(w, r)
		return
	}

	cookie, err := r.Cookie(NomeDoCookie)
	if err == nil && p.Cred.conferirSessao(cookie.Value, time.Now()) {
		p.Proximo.ServeHTTP(w, r.WithContext(EmContexto(r.Context(), p.Cred.Usuario)))
		return
	}

	// Um POST sem sessao nao vira redirecionamento para o login: o navegador
	// perderia o corpo e o operador reenviaria as cegas depois de entrar.
	// 401 diz a verdade sobre o que aconteceu.
	if r.Method != http.MethodGet {
		http.Error(w, "sessao expirada; entre novamente", http.StatusUnauthorized)
		return
	}
	destino := "/login"
	if alvo := r.URL.RequestURI(); alvo != "/" {
		destino += "?de=" + escaparDestino(alvo)
	}
	http.Redirect(w, r, destino, http.StatusSeeOther)
}

// Entrar confere a credencial e grava o cookie. Devolve false se nao bateu.
func (p *Portao) Entrar(w http.ResponseWriter, usuario, senha string) bool {
	// As duas comparacoes correm SEMPRE, mesmo com usuario errado: sair cedo
	// faz um usuario invalido responder mais rapido que um valido, e a
	// diferenca de tempo entrega quais nomes existem.
	usuarioOK := subtle.ConstantTimeCompare([]byte(usuario), []byte(p.Cred.Usuario)) == 1
	senhaOK := ConferirSenha(p.Cred.Hash, senha)
	if !usuarioOK || !senhaOK {
		return false
	}
	http.SetCookie(w, &http.Cookie{
		Name:  NomeDoCookie,
		Value: p.Cred.emitir(time.Now()),
		Path:  "/",
		// HttpOnly: um XSS na interface nao consegue ler a sessao.
		HttpOnly: true,
		// Lax, e nao Strict: o redirecionamento pos-login e uma navegacao de
		// origem externa, e Strict esconderia o cookie justamente nela.
		SameSite: http.SameSiteLaxMode,
		Secure:   !p.Inseguro,
		Expires:  time.Now().Add(ValidadeDaSessao),
	})
	return true
}

// Sair apaga o cookie.
func (p *Portao) Sair(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: NomeDoCookie, Value: "", Path: "/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: !p.Inseguro,
		MaxAge: -1,
	})
}

// escaparDestino permite apenas caminho interno no `?de=`.
//
// Sem isto, `/login?de=https://malicioso` faria a nossa propria tela de login
// devolver o operador autenticado para fora — o classico open redirect.
func escaparDestino(alvo string) string {
	if !strings.HasPrefix(alvo, "/") || strings.HasPrefix(alvo, "//") {
		return "/"
	}
	return alvo
}

// Destino saneia o `?de=` na hora de redirecionar pos-login.
func Destino(bruto string) string {
	if bruto == "" {
		return "/"
	}
	return escaparDestino(bruto)
}

// ---------------------------------------------------------------------------
// Sessao no contexto
// ---------------------------------------------------------------------------

type chave struct{}

// EmContexto guarda o operador da requisicao. O layout usa isso para decidir se
// mostra o botao de sair — uma instalacao sem credencial nao deve exibir um
// botao que nao faz nada.
func EmContexto(ctx context.Context, usuario string) context.Context {
	return context.WithValue(ctx, chave{}, usuario)
}

// De devolve o operador da requisicao, ou vazio quando nao ha sessao.
func De(ctx context.Context) string {
	u, _ := ctx.Value(chave{}).(string)
	return u
}
