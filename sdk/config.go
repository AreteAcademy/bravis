package sdk

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
)

// Precedence for every setting the SDK resolves, in this order:
//
//  1. what the caller set explicitly on the struct
//  2. the environment variable
//  3. the SDK default
//  4. an error, when there is no sensible default
//
// Names carry the BRAVIS_SDK_ prefix except where the ecosystem already has
// one -- inventing a new name for something that already has a name is
// friction, so the Google variables keep theirs.
const (
	EnvProjeto  = "GOOGLE_PROJECT_ID"
	EnvDataset  = "BRAVIS_SDK_DATASET"
	EnvBucket   = "BRAVIS_SDK_STAGING_BUCKET"
	EnvLogLevel = "BRAVIS_SDK_LOG_LEVEL"
)

// origem records where a resolved value came from, so the startup log can say
// so. Reading the environment silently is how a job works on the machine of
// whoever wrote it and writes to the wrong dataset in the pod.
type origem struct {
	valor string
	de    string // "explícito", the env var name, or "default"
}

func resolver(explicito, envVar, padrao string) origem {
	if explicito != "" {
		return origem{explicito, "explícito"}
	}
	if v := os.Getenv(envVar); v != "" {
		return origem{v, envVar}
	}
	return origem{padrao, "default"}
}

// logResolucao reports every setting and where it came from. Without this,
// "why did it write to the wrong dataset?" costs an hour.
func logResolucao(ctx context.Context, campos map[string]origem) {
	args := make([]any, 0, len(campos)*2)
	for nome, o := range campos {
		valor := o.valor
		if valor == "" {
			valor = "(vazio)"
		}
		args = append(args, nome, fmt.Sprintf("%s (de %s)", valor, o.de))
	}
	slog.InfoContext(ctx, "configuração resolvida", args...)
}

// NivelDeLog reads BRAVIS_SDK_LOG_LEVEL. Unset means info; an unparseable
// value means info and a warning, never a crash -- a bad log level must not
// take down a pipeline.
func NivelDeLog() slog.Level {
	v := os.Getenv(EnvLogLevel)
	if v == "" {
		return slog.LevelInfo
	}

	var nivel slog.Level
	if err := nivel.UnmarshalText([]byte(v)); err != nil {
		slog.Warn("nível de log inválido, usando info", EnvLogLevel, v)
		return slog.LevelInfo
	}
	return nivel
}

// envInt reads an integer environment variable, falling back on anything
// unparseable.
func envInt(key string, padrao int) int {
	v, err := strconv.Atoi(os.Getenv(key))
	if err != nil {
		return padrao
	}
	return v
}
