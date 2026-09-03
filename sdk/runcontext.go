package sdk

import (
	"encoding/json"
	"log/slog"
	"os"
	"strconv"
	"time"
)

// Environment the Bravis engine injects into a step it runs. A fetcher never
// reads these itself: sdk.Run picks them up, and Pipeline.Run exposes what is
// useful.
//
// The prefix is deliberately not BRAVIS_SDK_. Those say what the SDK does;
// these say what this particular execution is. Two categories, two prefixes.
//
// This is not a private channel. The process can read its own environment,
// and someone will. What is promised is that a fetcher does not *have* to --
// not that it cannot.
const (
	EnvRunID          = "BRAVIS_RUN_ID"
	EnvRunFirst       = "BRAVIS_RUN_FIRST"
	EnvRunAttempt     = "BRAVIS_RUN_ATTEMPT"
	EnvRunTrigger     = "BRAVIS_RUN_TRIGGER"
	EnvRunLogicalDate = "BRAVIS_RUN_LOGICAL_DATE"
	EnvRunParams      = "BRAVIS_RUN_PARAMS"
)

// ParamCreateTable, when the engine passes it as "true" in the run params,
// turns table creation on for this execution.
//
// It exists for the case that is not a first run and is not worth a code
// change: the table was dropped by mistake, recreate it. Without it the only
// way to get a table back would be to fake a first run.
const ParamCreateTable = "create_table"

// RunContext is what the engine knows about this execution and the fetcher
// does not.
//
// Every field is zero when the SDK runs outside the engine, which is the
// normal case: a fetcher run by hand should not notice this exists.
type RunContext struct {
	// ID of the run, for tying a log line to a row in the engine's history.
	ID string

	// First is true when no earlier attempt of this step has succeeded. The
	// engine decides it, because only the engine has the history -- inferring
	// it from "the table is missing" would confuse a first run with a table
	// someone dropped by mistake.
	First bool

	// Attempt counts from 1. A retry of the same run does not make First true
	// again.
	Attempt int

	// Trigger is schedule, manual or backfill.
	Trigger string

	// LogicalDate is the slot this run represents. Zero on a manual trigger,
	// which belongs to no slot.
	LogicalDate time.Time

	// Params are the values this execution was dispatched with. Never nil, so
	// Params["x"] on a fetcher running by hand is an empty string rather than
	// a panic.
	Params map[string]string
}

// runContextFromEnv reads what the engine injected.
//
// Nothing here is required, and nothing is an error: a fetcher run by hand
// sees an empty RunContext and behaves exactly as it did before this existed.
// A malformed value is logged and dropped rather than failing the run -- a bad
// environment variable is not worth losing a load over.
func runContextFromEnv() RunContext {
	rc := RunContext{
		ID:      os.Getenv(EnvRunID),
		First:   os.Getenv(EnvRunFirst) == "true",
		Trigger: os.Getenv(EnvRunTrigger),
		Params:  map[string]string{},
	}

	if v := os.Getenv(EnvRunAttempt); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			slog.Warn("ignoring malformed run attempt", EnvRunAttempt, v)
		} else {
			rc.Attempt = n
		}
	}

	if v := os.Getenv(EnvRunLogicalDate); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			slog.Warn("ignoring malformed logical date", EnvRunLogicalDate, v)
		} else {
			rc.LogicalDate = t
		}
	}

	if v := os.Getenv(EnvRunParams); v != "" {
		var params map[string]string
		if err := json.Unmarshal([]byte(v), &params); err != nil {
			slog.Warn("ignoring malformed run params", EnvRunParams, v, "error", err)
		} else {
			rc.Params = params
		}
	}

	return rc
}

// fromEngine reports whether anything at all arrived, which is how the log
// tells "run by hand" apart from "run by Bravis".
func (r RunContext) fromEngine() bool {
	return r.ID != ""
}

// Args renders the context as slog key-value pairs. "Which run was this?" is
// the first question of any investigation, and the answer cannot depend on
// someone having thought to log it.
func (r RunContext) Args() []any {
	args := []any{"run_id", r.ID, "first", r.First, "attempt", r.Attempt}
	if r.Trigger != "" {
		args = append(args, "trigger", r.Trigger)
	}
	if !r.LogicalDate.IsZero() {
		args = append(args, "logical_date", r.LogicalDate.Format(time.RFC3339))
	}
	if len(r.Params) > 0 {
		args = append(args, "params", r.Params)
	}
	return args
}
