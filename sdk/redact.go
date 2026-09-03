package sdk

import (
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// querySecrets are the query parameters whose values must never reach a
// log. An API key in the query string is the common case for several public
// sources, and a key leaked into pod logs is an incident that nobody notices
// until the logs leave the cluster.
var querySecrets = []string{
	"key", "api_key", "apikey", "token", "access_token", "auth",
	"password", "secret", "signature", "sig",
}

// marker replaces a secret's value. It is alphanumeric on purpose:
// url.Values.Encode percent-encodes anything else, and "%2A%2A%2A" in a log
// line is noise where "REDACTED" is an answer.
const marker = "REDACTED"

// redact removes credentials from a URL so it can be logged or put in an
// error message. Anything unparseable is replaced entirely rather than
// guessed at -- a URL we cannot parse is a URL we cannot promise is clean.
func redact(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "[unreadable url]"
	}

	if u.User != nil {
		if _, temSenha := u.User.Password(); temSenha {
			u.User = url.UserPassword(u.User.Username(), marker)
		}
	}

	q := u.Query()
	for key := range q {
		lower := strings.ToLower(key)
		for _, secret := range querySecrets {
			if lower == secret || strings.HasSuffix(lower, "_"+secret) {
				q.Set(key, marker)
				break
			}
		}
	}
	u.RawQuery = q.Encode()

	return u.String()
}

// statusOf pulls the HTTP status out of the "http NNN: ..." errors extract
// produces, so the typed error can carry it.
var statusPattern = regexp.MustCompile(`\bhttp (\d{3})\b`)

func statusOf(err error) (int, bool) {
	m := statusPattern.FindStringSubmatch(err.Error())
	if m == nil {
		return 0, false
	}
	n, convErr := strconv.Atoi(m[1])
	if convErr != nil {
		return 0, false
	}
	return n, true
}

// isTransport tells a failure to reach the source from a failure to
// understand what it sent. They call for different actions: wait, or fix the
// parser.
func isTransport(err error) bool {
	text := strings.ToLower(err.Error())
	for _, mark := range []string{
		"fetch failed", "connection", "timeout", "deadline exceeded",
		"no such host", "context cancel", "rate limiter", "eof",
	} {
		if strings.Contains(text, mark) {
			return true
		}
	}
	return false
}
