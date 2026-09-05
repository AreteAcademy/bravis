package extract

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/AreteAcademy/brevis/sdk/internal/core"
)

// defaultWarnAfter is when an unconfigured WarnAfter starts warning. A week is
// long enough for somebody to notice on a Monday.
const defaultWarnAfter = 7 * 24 * time.Hour

// authenticate resolves the credential and writes it onto the header the
// requests will carry.
//
// It runs before the client is built, so that a secret applied as a cookie is
// seeded into the jar like any other -- and from there the jar is the single
// place a cookie lives, including one the refresh reissues.
func authenticate(ctx context.Context, source *core.Source) error {
	if source.Auth == nil {
		return nil
	}
	if err := source.Auth.Check(); err != nil {
		return err
	}

	secret, err := source.Auth.Get(ctx)
	if err != nil {
		return err
	}

	// The caller's header is theirs; they may reuse the map on another
	// pipeline, and it must not come back carrying a secret.
	h := http.Header(source.Header).Clone()
	if h == nil {
		h = http.Header{}
	}
	source.Auth.Apply(h, secret)
	source.Header = h

	return nil
}

// aplicarRotacao reescreve o cabecalho Cookie com os valores reemitidos.
//
// Reescreve por NOME, preservando os cookies que a renovacao nao tocou: um
// cabecalho com dois cookies, dos quais a API reemitiu um, tem de continuar
// com os dois.
func aplicarRotacao(source *core.Source, rotacoes map[string]string) {
	if len(rotacoes) == 0 {
		return
	}

	h := http.Header(source.Header).Clone()
	if h == nil {
		h = http.Header{}
	}
	atuais, err := http.ParseCookie(h.Get("Cookie"))
	if err != nil {
		// O cabecalho foi montado pelo Applier e ja passou por ParseCookie na
		// montagem do cliente; chegar aqui invalido nao deveria acontecer, e
		// perder a rotacao e melhor que perder a credencial inteira.
		return
	}

	var partes []string
	for _, c := range atuais {
		if novo, tem := rotacoes[c.Name]; tem {
			c.Value = novo
		}
		partes = append(partes, c.Name+"="+c.Value)
	}
	h.Set("Cookie", strings.Join(partes, "; "))
	source.Header = h
}

// renewRequest makes the refresh call, with the same retries the pages get.
//
// Without them the walk is lopsided: a blip on the data endpoint costs a
// retry and a blip on the renewal costs the whole run. Same RetryConfig, same
// backoff, same reading of Retry-After.
func renewRequest(ctx context.Context, client *http.Client, source core.Source, method, rawURL string) ([]byte, error) {
	fail := func(format string, a ...any) ([]byte, error) {
		return nil, fmt.Errorf("refresh "+redactURL(rawURL)+": "+format, a...)
	}

	attempts := 1
	if source.RetryConfig != nil && source.RetryConfig.MaxAttempts > 0 {
		attempts = source.RetryConfig.MaxAttempts
	}

	for attempt := 0; attempt < attempts; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, rawURL, nil)
		if err != nil {
			return fail("%w", err)
		}
		// O cabecalho vai INTEIRO, com a credencial. Era exatamente isto que
		// faltava no §9: a renovacao dependia do jar, o jar casava por path, e
		// /api/auth/session nao casava com a fonte em /api/proxy.
		req.Header = http.Header(source.Header).Clone()

		resp, err := client.Do(req)
		if err != nil {
			if shouldRetry(err) && attempt < attempts-1 {
				time.Sleep(calculateBackoff(attempt, source.RetryConfig))
				continue
			}
			return fail("after %d attempt(s): %w", attempt+1, err)
		}

		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()

		if resp.StatusCode >= 200 && resp.StatusCode <= 299 {
			if readErr != nil {
				return fail("read response: %w", readErr)
			}
			return body, nil
		}

		if shouldRetryStatus(resp.StatusCode) && attempt < attempts-1 {
			time.Sleep(retryAfter(resp, attempt, source.RetryConfig))
			continue
		}

		// A refresh that fails is not a warning to move past: every page
		// after it would go out with a credential the API just refused, and
		// the run would fail anyway -- later, and blaming the data endpoint.
		return fail("http %d: %s", resp.StatusCode, string(body))
	}

	return fail("out of attempts")
}

// renew calls the refresh endpoint before the first page.
//
// It shares the walk's client, so a Set-Cookie in the response lands in the
// jar and applies to every page that follows -- which is the whole mechanism.
// Nothing is written anywhere: the reissued value lives for this run only.
func renew(ctx context.Context, client *http.Client, source *core.Source, jar *credentialJar, stats *core.Stats) error {
	r := source.Auth.Refresh

	method := r.Method
	if method == "" {
		method = "GET"
	}

	body, err := renewRequest(ctx, client, *source, method, r.URL)
	if err != nil {
		return err
	}

	// O que a renovacao reemitiu passa a valer para as paginas. Sem isto a
	// renovacao renova para ninguem: o valor novo ficaria so no jar, preso ao
	// diretorio da URL de renovacao, e as paginas seguiriam com o antigo.
	aplicarRotacao(source, jar.Rotacoes())

	if r.ExpiresAt == nil {
		return nil
	}

	expires, err := r.ExpiresAt(body)
	if err != nil {
		return fmt.Errorf("refresh %s: %w", redactURL(r.URL), err)
	}
	if stats != nil {
		stats.CredentialExpiry = expires
	}

	warnAfter := r.WarnAfter
	if warnAfter == 0 {
		warnAfter = defaultWarnAfter
	}
	left := time.Until(expires)

	switch {
	case left <= 0:
		slog.WarnContext(ctx, "credential has expired",
			"expires", expires.Format(time.RFC3339),
			"url", redactURL(r.URL))
	case left < warnAfter:
		slog.WarnContext(ctx, "credential expires soon",
			"expires", expires.Format(time.RFC3339),
			"left", core.RoundDuration(left),
			"url", redactURL(r.URL))
	default:
		slog.DebugContext(ctx, "credential renewed",
			"expires", expires.Format(time.RFC3339),
			"left", core.RoundDuration(left))
	}

	return nil
}
