package sdk

import (
	"log/slog"

	core "github.com/AreteAcademy/bravis/sdk/internal/core"
)

// Environment variables the SDK reads. See core for the precedence rules.
const (
	EnvProject  = core.EnvProject
	EnvDataset  = core.EnvDataset
	EnvBucket   = core.EnvBucket
	EnvLogLevel = core.EnvLogLevel
)

// LogLevel reads BRAVIS_SDK_LOG_LEVEL. Unset means info; an unparseable value
// means info and a warning, never a crash.
func LogLevel() slog.Level { return core.LogLevel() }
