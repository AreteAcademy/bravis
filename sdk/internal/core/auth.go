package core

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Secret produces the value an HTTP source authenticates with.
//
// It is a function rather than a string so that a credential which has to be
// fetched -- a login call, a secret manager -- is expressible without the SDK
// knowing how. FromEnv covers the common case.
type Secret func(ctx context.Context) (string, error)

// Applier puts the secret on the outgoing request.
type Applier func(h http.Header, secret string)

// Credential is how an HTTP source authenticates, and what the SDK does to
// keep the credential alive.
//
// Nothing here is required: a source with a static key in Header needs none
// of it. What it buys is the two things every consumer was writing by hand --
// caching a login so the API is not asked for a token on every page, and
// renewing a session that would otherwise expire in silence.
//
// It holds a cache, so it is used through a pointer.
type Credential struct {
	// Value produces the secret. Required.
	Value Secret

	// Apply puts it on the request. Required. See AsBearer, AsCookie,
	// AsCookieNamed and AsHeader.
	Apply Applier

	// TTL caches the value in memory for this long, so a Value that logs in
	// is not called once per pipeline run when several run in one process.
	// Zero calls Value once per run, which is what a plain env var wants.
	//
	// The cache never reaches disk and never outlives the process. Some APIs
	// rate-limit authentication attempts rather than requests, which is the
	// case this exists for.
	TTL time.Duration

	// Login troca segredos por um token vindo do CORPO da resposta, que e a
	// forma que a maioria das APIs usa. Nil pula.
	//
	// Ele existe porque o contorno -- por o login dentro de Value, que e uma
	// func -- funciona e tem um custo escondido: a requisicao de login passa a
	// ser a UNICA do fetcher sem retry, sem rate limit, sem timeout por
	// tentativa e sem redacao de segredo no log. E ela e a que carrega as
	// credenciais.
	//
	// Value continua existindo e continua valendo para o que nao cabe aqui --
	// um secret manager, um arquivo, uma env. Login e Value juntos e erro:
	// duas fontes para o mesmo segredo, e a que perde perde em silencio.
	Login *Login

	// Refresh optionally calls an endpoint before the first page to renew a
	// session. Nil skips it.
	Refresh *Refresh

	mu       sync.Mutex
	cached   string
	cachedAt time.Time

	// login e a Secret que o extract monta a partir de Login, com o cliente
	// dele. Ela vive aqui, e nao em Login, porque e o Get que a chama -- e o
	// Get e quem tem a trava e o TTL.
	login Secret
}

// PrepararLogin instala a Secret que faz o login. Chamada pelo extract, que e
// quem tem o cliente HTTP.
func (c *Credential) PrepararLogin(s Secret) { c.login = s }

// Login troca segredos por um token, com o cliente do SDK.
//
//	Auth: &from.Credential{
//	    Login: &from.Login{
//	        URL:    "https://api.example.com/oauth/token",
//	        Method: "POST",
//	        Body:   from.JSONBody(map[string]any{"client_id": id, "client_secret": segredo}),
//	        Token:  from.CampoJSON("data.accessToken"),
//	    },
//	    Apply: from.AsBearer,
//	    TTL:   50 * time.Minute,
//	}
//
// O que ele compra nao e conveniencia: e a requisicao mais sensivel do fetcher
// deixar de ser a unica sem retry, sem rate limit, sem timeout e sem redacao no
// log. Escrita a mao, ela costuma sair com http.DefaultClient -- que nao tem
// timeout nenhum.
//
// Combine com TTL: sem ele o login acontece uma vez por execucao, e algumas
// APIs limitam a FREQUENCIA de autenticacao em vez da de requisicoes.
type Login struct {
	// URL do endpoint de login. Obrigatoria.
	URL string

	// Method e o verbo. Vazio usa POST -- que e o que um login e.
	Method string

	// Body monta o corpo. Nil manda sem corpo.
	//
	// E uma func e nao bytes porque o corpo carrega segredo: ele e montado na
	// hora da requisicao e nao fica vivo num campo de struct que qualquer
	// dump de configuracao imprimiria.
	Body func(ctx context.Context) (contentType string, corpo []byte, err error)

	// Header sao cabecalhos proprios do login -- uma chave de API que
	// autoriza a troca, por exemplo.
	Header map[string][]string

	// Token le o token do CORPO da resposta. Obrigatorio: se o token viesse
	// num cookie, o caminho seria Refresh.
	Token func(corpo []byte) (string, error)
}

// Refresh renews a credential that expires, by asking the API to reissue it.
//
// It exists for a session token a human pasted in: the vendor has no
// programmatic login, the token has a sliding expiry, and only the renewal
// endpoint pushes the window forward. Without the call the pipeline dies on
// the day the window closes, with a 401 that says nothing about why.
//
// The reissued cookie is picked up by the same cookie jar the pages use, so
// it applies to this run. It is never written anywhere: a rotated token does
// not invalidate the previous one, so the cost of not storing it is that
// somebody re-pastes the credential once per expiry window -- and ExpiresAt
// plus WarnAfter is how they find out before it is too late.
type Refresh struct {
	// URL of the renewal endpoint. Required.
	URL string

	// Method defaults to GET.
	Method string

	// ExpiresAt reads the new expiry out of the response body. Optional; see
	// JSONField. Without it the SDK renews but cannot say for how long, and
	// WarnAfter has nothing to compare against.
	ExpiresAt func(body []byte) (time.Time, error)

	// Store keeps the rotated credential between runs. Nil keeps today's
	// behaviour: the renewed value lives for this run only.
	//
	// It exists because the alternative is a person re-pasting the credential
	// once per expiry window, forever. With it, the environment variable stops
	// holding the ROTATING value and starts holding a STATIC key -- pasted
	// once, never again. That asymmetry is the whole point; it is not "an
	// environment variable versus a file".
	//
	//	Store: from.FileStore{Name: "gabriel-session"}
	//
	// The read order is store, then Value as the seed, then renew, then save.
	//
	// Last writer wins. Two processes renewing at once write two values, and
	// both work only because rotating does not invalidate the previous token
	// at the vendor this was built for. For a vendor that DOES invalidate the
	// previous one, do not use this without a lock of your own.
	Store CredentialStore

	// WarnAfter warns once the credential has less than this left. Requires
	// ExpiresAt. Zero warns at 7 days.
	//
	// The warning is both a log line and Stats.CredentialExpiry, because a
	// warning nobody reads is how the silent death happens in the first
	// place.
	WarnAfter time.Duration
}

// Get returns the secret, from the cache when TTL still covers it.
//
// The lock serializes concurrent callers so an API that rate-limits logins
// sees one attempt, not one per goroutine.
func (c *Credential) Get(ctx context.Context) (string, error) {
	if c.Value == nil && c.Login == nil {
		return "", fmt.Errorf("Credential precisa de Value ou de Login")
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.TTL > 0 && c.cached != "" && time.Since(c.cachedAt) < c.TTL {
		return c.cached, nil
	}

	// O store vem antes da semente. Um valor guardado e o resultado da ultima
	// rotacao; a semente e o que alguem colou uma vez, e pode ja ter vencido.
	if c.Refresh != nil && c.Refresh.Store != nil {
		guardado, err := c.Refresh.Store.Load()
		if err != nil {
			// Ler falhou de verdade -- permissao, disco. Nao e motivo para
			// parar: a semente ainda pode servir, e parar aqui trocaria uma
			// credencial talvez velha por nenhuma.
			slog.WarnContext(ctx, "credential store: could not be read",
				"store", c.Refresh.Store.Describe(),
				"falling_back_to", "Credential.Value",
				"error", err)
		} else if guardado != "" {
			c.cached, c.cachedAt = guardado, time.Now()
			return guardado, nil
		}
	}

	produzir := c.Value
	if produzir == nil {
		// O Login e feito por quem tem o cliente HTTP -- o extract --, e
		// chega aqui como uma Secret ja fechada sobre ele. Ver
		// extract.PrepararLogin.
		produzir = c.login
	}
	if produzir == nil {
		return "", fmt.Errorf("credential: Login declarado mas não preparado; isto é um " +
			"defeito do SDK, não da sua configuração")
	}

	v, err := produzir(ctx)
	if err != nil {
		return "", fmt.Errorf("credential: %w", err)
	}
	if v == "" {
		return "", fmt.Errorf("credential: Value returned an empty secret")
	}

	c.cached, c.cachedAt = v, time.Now()
	return v, nil
}

// Check reports a Credential that cannot work, at setup rather than as a 401.
func (c *Credential) Check() error {
	if c == nil {
		return nil
	}
	if c.Value == nil && c.Login == nil {
		return fmt.Errorf("Auth.Value e Auth.Login estão os dois nil, e um dos dois precisa " +
			"existir: Value produz o segredo, Login o troca por um token. Para uma variável " +
			"de ambiente, from.FromEnv(\"NOME\")")
	}
	if c.Value != nil && c.Login != nil {
		return fmt.Errorf("Auth.Value e Auth.Login estão os dois preenchidos, e os dois " +
			"produzem o mesmo segredo -- a que perdesse perderia em silêncio. Login faz a " +
			"requisição com o cliente do SDK; Value é para o que não é uma requisição HTTP")
	}
	if c.Login != nil {
		if c.Login.URL == "" {
			return fmt.Errorf("Auth.Login.URL está vazia: é o endpoint que troca os segredos " +
				"pelo token")
		}
		if c.Login.Token == nil {
			return fmt.Errorf("Auth.Login.Token é nil: é o que diz ONDE o token está no corpo " +
				"da resposta. Use from.CampoJSON(\"data.accessToken\")")
		}
	}
	if c.Apply == nil {
		return fmt.Errorf("Auth.Apply is nil: it is what puts the secret on the " +
			"request. Use from.AsBearer, from.AsCookie or from.AsHeader(name)")
	}
	if c.Refresh == nil {
		return nil
	}
	if c.Refresh.URL == "" {
		return fmt.Errorf("Auth.Refresh.URL is empty: it is the endpoint that reissues " +
			"the credential")
	}
	// A recusa do store acontece aqui, na montagem: descobrir que a
	// credencial nao seria guardada depois da carga inteira e tarde demais.
	if v, ok := c.Refresh.Store.(CredentialStoreChecker); ok {
		if err := v.CheckStore(); err != nil {
			return err
		}
	}
	if c.Refresh.Store != nil && c.Refresh.ExpiresAt == nil {
		// Aviso e nao recusa: ha fontes cuja renovacao nao devolve validade
		// nenhuma, e para elas o store ainda vale. Mas quem escolhe isso tem
		// de escolher sabendo.
		slog.Warn("credential store without ExpiresAt: a refresh that did not authenticate will be saved",
			"store", c.Refresh.Store.Describe(),
			"why", "the status is 200 either way, so the body is the only place the difference shows",
			"risk", "a dead credential is read before Value on the next run, and swapping the "+
				"environment variable stops fixing it",
			"fix", "set Refresh.ExpiresAt, for example from.JSONField(\"expires\")")
	}
	if c.Refresh.WarnAfter != 0 && c.Refresh.ExpiresAt == nil {
		return fmt.Errorf("Auth.Refresh.WarnAfter says when to warn and Auth.Refresh." +
			"ExpiresAt says what to compare it against, and ExpiresAt is not set -- " +
			"so the warning could never fire. Use from.JSONField(\"expires\")")
	}
	return nil
}

// FromEnv reads the secret from an environment variable.
//
// An unset or empty variable is an error, named, at setup: the alternative is
// an empty Authorization header and a 401 that blames the API.
func FromEnv(name string) Secret {
	return func(context.Context) (string, error) {
		v := os.Getenv(name)
		if v == "" {
			return "", fmt.Errorf("environment variable %s is unset or empty", name)
		}
		return v, nil
	}
}

// AsBearer sends the secret as "Authorization: Bearer <secret>".
func AsBearer(h http.Header, secret string) {
	h.Set("Authorization", "Bearer "+secret)
}

// AsCookie sends the secret as the whole Cookie header.
//
// The secret is the full "name=value", which is what someone copying a
// session cookie out of a browser has in hand. Use AsCookieNamed when only
// the value is stored.
func AsCookie(h http.Header, secret string) {
	h.Set("Cookie", secret)
}

// AsCookieNamed sends the secret as the value of one named cookie.
func AsCookieNamed(name string) Applier {
	return func(h http.Header, secret string) {
		h.Set("Cookie", name+"="+secret)
	}
}

// AsHeader sends the secret as the whole value of a header of your choosing,
// for the APIs that want X-API-Key or similar.
func AsHeader(name string) Applier {
	return func(h http.Header, secret string) {
		h.Set(name, secret)
	}
}

// JSONField reads an RFC 3339 timestamp from a top-level field of a JSON
// response body -- {"expires": "2026-10-04T22:15:07.197Z"} is JSONField("expires").
func JSONField(name string) func([]byte) (time.Time, error) {
	return func(body []byte) (time.Time, error) {
		var doc map[string]any
		if err := json.Unmarshal(body, &doc); err != nil {
			return time.Time{}, fmt.Errorf("refresh response is not a JSON object: %w", err)
		}
		raw, ok := doc[name]
		if !ok {
			return time.Time{}, fmt.Errorf("refresh response has no field %q", name)
		}
		s, ok := raw.(string)
		if !ok {
			return time.Time{}, fmt.Errorf("field %q is %T, want an RFC 3339 string", name, raw)
		}
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return time.Time{}, fmt.Errorf("field %q = %q is not RFC 3339: %w", name, s, err)
		}
		return t, nil
	}
}

// CampoJSON le um campo do corpo da resposta, por caminho separado por pontos.
//
//	Token: from.CampoJSON("data.accessToken")
//
// O caminho aceita pontos porque a convencao larga poe o token dentro de um
// envelope, e nao na raiz.
//
// Um campo ausente e ERRO nomeando o caminho -- e nao string vazia. Um token
// vazio vira um cabecalho de autorizacao vazio e um 401 mais adiante, culpando
// a API por um caminho que este lado escreveu errado.
func CampoJSON(caminho string) func([]byte) (string, error) {
	return func(corpo []byte) (string, error) {
		var atual any
		if err := json.Unmarshal(corpo, &atual); err != nil {
			return "", fmt.Errorf("a resposta do login não é JSON: %w", err)
		}

		partes := strings.Split(caminho, ".")
		for i, parte := range partes {
			obj, ok := atual.(map[string]any)
			if !ok {
				return "", fmt.Errorf("%q: %q não é um objeto",
					caminho, strings.Join(partes[:i], "."))
			}
			v, existe := obj[parte]
			if !existe {
				return "", fmt.Errorf("%q: a resposta não tem %q. Confira o caminho -- um "+
					"token ausente viraria um cabeçalho vazio e um 401 mais adiante, "+
					"culpando a API", caminho, parte)
			}
			atual = v
		}

		switch t := atual.(type) {
		case string:
			return t, nil
		case json.Number:
			return t.String(), nil
		case float64:
			return strconv.FormatFloat(t, 'f', -1, 64), nil
		default:
			return "", fmt.Errorf("%q levou a um %T, e um token precisa ser texto", caminho, atual)
		}
	}
}

// JSONBody monta um corpo JSON para o Login.
//
//	Body: from.JSONBody(map[string]any{"client_id": id, "client_secret": segredo})
//
// A serializacao acontece na hora da requisicao, e nao aqui: o corpo carrega
// segredo, e um []byte guardado num campo de struct aparece em qualquer dump
// de configuracao.
func JSONBody(v any) func(context.Context) (string, []byte, error) {
	return func(context.Context) (string, []byte, error) {
		b, err := json.Marshal(v)
		if err != nil {
			return "", nil, fmt.Errorf("montando o corpo do login: %w", err)
		}
		return "application/json", b, nil
	}
}

// FormBody monta um corpo application/x-www-form-urlencoded, que e o formato
// que o OAuth2 usa.
func FormBody(campos map[string]string) func(context.Context) (string, []byte, error) {
	return func(context.Context) (string, []byte, error) {
		v := url.Values{}
		for k, valor := range campos {
			v.Set(k, valor)
		}
		return "application/x-www-form-urlencoded", []byte(v.Encode()), nil
	}
}
