// Package observability concentra logging, metricas e tracing.
//
// Na Phase 0 so existe logging. Metricas e tracing entram na Phase 0 do plano
// apenas como health check; a secao 32 os detalha para fases posteriores, e a
// regra 2 proibe antecipar.
package observability

import (
	"log/slog"
	"os"
	"strings"
)

// NewLogger devolve um logger estruturado. JSON fora do ambiente local porque e
// o que os coletores esperam; texto no local porque a saida e lida por humanos.
func NewLogger(env, nivel string) *slog.Logger {
	opts := &slog.HandlerOptions{Level: parseNivel(nivel)}

	var h slog.Handler
	if env == "local" {
		h = slog.NewTextHandler(os.Stdout, opts)
	} else {
		h = slog.NewJSONHandler(os.Stdout, opts)
	}
	return slog.New(h).With("service", "bravis", "env", env)
}

func parseNivel(n string) slog.Level {
	switch strings.ToLower(n) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
