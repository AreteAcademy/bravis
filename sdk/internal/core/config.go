package core

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
//  2. what the engine injected
//  3. the environment variable
//  4. the SDK default
//  5. an error, when there is no sensible default
//
// Names carry the BRAVIS_SDK_ prefix except where the ecosystem already has
// one -- inventing a new name for something that already has a name is
// friction, so the Google variables keep theirs.
const (
	EnvProject  = "GOOGLE_PROJECT_ID"
	EnvDataset  = "BRAVIS_SDK_DATASET"
	EnvBucket   = "BRAVIS_SDK_STAGING_BUCKET"
	EnvLogLevel = "BRAVIS_SDK_LOG_LEVEL"
)

// Origin records where a resolved value came from, so the startup log can say
// so. Reading the environment silently is how a job works on the machine of
// whoever wrote it and writes to the wrong dataset in the pod.
//
// Exported for the driver packages, which resolve their own settings.
type Origin struct {
	Value string
	Where string // "explicit", the env var name, or "default"
}

// Resolve applies the precedence above to one setting.
func Resolve(explicit, envVar, fallback string) Origin {
	if explicit != "" {
		return Origin{explicit, "explicit"}
	}
	if v := os.Getenv(envVar); v != "" {
		return Origin{v, envVar}
	}
	return Origin{fallback, "default"}
}

// LogResolution reports every setting and where it came from. Without this,
// "why did it write to the wrong dataset?" costs an hour.
func LogResolution(ctx context.Context, fields map[string]Origin) {
	args := make([]any, 0, len(fields)*2)
	for name, o := range fields {
		value := o.Value
		if value == "" {
			value = "(empty)"
		}
		args = append(args, name, fmt.Sprintf("%s (from %s)", value, o.Where))
	}
	slog.InfoContext(ctx, "resolved configuration", args...)
}

// LogLevel reads BRAVIS_SDK_LOG_LEVEL. Unset means info; an unparseable value
// means info and a warning, never a crash -- a bad log level must not take
// down a pipeline.
func LogLevel() slog.Level {
	v := os.Getenv(EnvLogLevel)
	if v == "" {
		return slog.LevelInfo
	}

	var level slog.Level
	if err := level.UnmarshalText([]byte(v)); err != nil {
		slog.Warn("invalid log level, using info", EnvLogLevel, v)
		return slog.LevelInfo
	}
	return level
}

// EnvInt reads an integer environment variable, falling back on anything
// unparseable.
func EnvInt(key string, fallback int) int {
	v, err := strconv.Atoi(os.Getenv(key))
	if err != nil {
		return fallback
	}
	return v
}
