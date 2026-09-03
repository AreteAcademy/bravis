package sdk

import (
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// segredosNaQuery are the query parameters whose values must never reach a
// log. An API key in the query string is the common case for several public
// sources, and a key leaked into pod logs is an incident that nobody notices
// until the logs leave the cluster.
var segredosNaQuery = []string{
	"key", "api_key", "apikey", "token", "access_token", "auth",
	"password", "secret", "signature", "sig",
}

// marcador replaces a secret's value. It is alphanumeric on purpose:
// url.Values.Encode percent-encodes anything else, and "%2A%2A%2A" in a log
// line is noise where "REDACTED" is an answer.
const marcador = "REDACTED"

// redigir removes credentials from a URL so it can be logged or put in an
// error message. Anything unparseable is replaced entirely rather than
// guessed at -- a URL we cannot parse is a URL we cannot promise is clean.
func redigir(bruta string) string {
	u, err := url.Parse(bruta)
	if err != nil {
		return "[url ilegível]"
	}

	if u.User != nil {
		if _, temSenha := u.User.Password(); temSenha {
			u.User = url.UserPassword(u.User.Username(), marcador)
		}
	}

	q := u.Query()
	for chave := range q {
		baixa := strings.ToLower(chave)
		for _, segredo := range segredosNaQuery {
			if baixa == segredo || strings.HasSuffix(baixa, "_"+segredo) {
				q.Set(chave, marcador)
				break
			}
		}
	}
	u.RawQuery = q.Encode()

	return u.String()
}

// statusDe pulls the HTTP status out of the "http NNN: ..." errors extract
// produces, so the typed error can carry it.
var padraoStatus = regexp.MustCompile(`\bhttp (\d{3})\b`)

func statusDe(err error) (int, bool) {
	m := padraoStatus.FindStringSubmatch(err.Error())
	if m == nil {
		return 0, false
	}
	n, convErr := strconv.Atoi(m[1])
	if convErr != nil {
		return 0, false
	}
	return n, true
}

// ehDeTransporte tells a failure to reach the source from a failure to
// understand what it sent. They call for different actions: wait, or fix the
// parser.
func ehDeTransporte(err error) bool {
	texto := strings.ToLower(err.Error())
	for _, marca := range []string{
		"fetch failed", "connection", "timeout", "deadline exceeded",
		"no such host", "context cancel", "rate limiter", "eof",
	} {
		if strings.Contains(texto, marca) {
			return true
		}
	}
	return false
}
