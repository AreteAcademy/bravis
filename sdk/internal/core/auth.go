package core

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
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

	// Refresh optionally calls an endpoint before the first page to renew a
	// session. Nil skips it.
	Refresh *Refresh

	mu       sync.Mutex
	cached   string
	cachedAt time.Time
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
	if c.Value == nil {
		return "", fmt.Errorf("Credential.Value is nil: it is what produces the secret")
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

	v, err := c.Value(ctx)
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
	if c.Value == nil {
		return fmt.Errorf("Auth.Value is nil: it is what produces the secret. " +
			"For an environment variable use from.FromEnv(\"NAME\")")
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
