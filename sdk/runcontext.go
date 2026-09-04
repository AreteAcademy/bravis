package sdk

import (
	core "github.com/AreteAcademy/brevis/sdk/internal/core"
)

// RunContext is what the Brevis engine knows about this execution. See
// core.RunContext; it lives there because the driver interfaces take it, and
// a driver package cannot import the root.
type RunContext = core.RunContext

// Environment variables the engine sets, re-exported so a fetcher can read
// them without importing an internal package.
const (
	EnvRunID          = core.EnvRunID
	EnvRunFirst       = core.EnvRunFirst
	EnvRunAttempt     = core.EnvRunAttempt
	EnvRunTrigger     = core.EnvRunTrigger
	EnvRunLogicalDate = core.EnvRunLogicalDate
	EnvRunParams      = core.EnvRunParams
)

// ParamCreateTable is the dispatch parameter that asks for the table to be
// created on this run.
const ParamCreateTable = core.ParamCreateTable

func runContextFromEnv() RunContext { return core.RunContextFromEnv() }
