package sdk

import (
	"strings"
	"testing"
	"time"
)

// --- reading what the engine injected --------------------------------------

func TestRunContextIsEmptyWithoutTheEngine(t *testing.T) {
	// The normal case: a fetcher run by hand must not notice this exists.
	rc := runContextFromEnv()

	if rc.FromEngine() {
		t.Errorf("nothing was injected, so nothing should be reported: %+v", rc)
	}
	if rc.First || rc.Attempt != 0 || rc.ID != "" {
		t.Errorf("expected a zero context, got %+v", rc)
	}
	// Never nil: Params["x"] on a fetcher run by hand must not panic.
	if rc.Params == nil {
		t.Fatal("Params must be an empty map, not nil")
	}
	if rc.Params["anything"] != "" {
		t.Error("an absent key should read as empty")
	}
}

func TestRunContextReadsWhatTheEngineInjected(t *testing.T) {
	t.Setenv(EnvRunID, "1f0c8e2a-0000-4000-8000-000000000000")
	t.Setenv(EnvRunFirst, "true")
	t.Setenv(EnvRunAttempt, "2")
	t.Setenv(EnvRunTrigger, "backfill")
	t.Setenv(EnvRunLogicalDate, "2026-09-03T00:00:00Z")
	t.Setenv(EnvRunParams, `{"load_full":"true","region":"br"}`)

	rc := runContextFromEnv()

	if !rc.FromEngine() {
		t.Error("an injected run id means the engine ran this")
	}
	if !rc.First || rc.Attempt != 2 || rc.Trigger != "backfill" {
		t.Errorf("rc = %+v", rc)
	}
	if rc.LogicalDate.Format(time.RFC3339) != "2026-09-03T00:00:00Z" {
		t.Errorf("LogicalDate = %v", rc.LogicalDate)
	}
	if rc.Params["load_full"] != "true" || rc.Params["region"] != "br" {
		t.Errorf("Params = %v", rc.Params)
	}
}

func TestRunContextSurvivesMalformedValues(t *testing.T) {
	// A bad environment variable is not worth losing a load over: drop the
	// value, keep going. The engine is upstream; the fetcher cannot fix it.
	t.Setenv(EnvRunID, "some-id")
	t.Setenv(EnvRunAttempt, "not a number")
	t.Setenv(EnvRunLogicalDate, "yesterday")
	t.Setenv(EnvRunParams, "{not json")

	rc := runContextFromEnv()

	if rc.Attempt != 0 || !rc.LogicalDate.IsZero() {
		t.Errorf("malformed values should be dropped: %+v", rc)
	}
	if rc.Params == nil {
		t.Error("Params must stay an empty map after a parse failure")
	}
	if rc.ID != "some-id" {
		t.Error("a good value must survive a bad neighbour")
	}
}

func TestRunContextArgsCarryTheIdentity(t *testing.T) {
	// "Which run was this?" is the first question of any investigation, and
	// the answer cannot depend on someone having thought to log it.
	rc := RunContext{
		ID: "abc", First: true, Attempt: 1, Trigger: "schedule",
		LogicalDate: time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC),
		Params:      map[string]string{"load_full": "true"},
	}

	args := rc.Args()
	joined := strings.Join(keysOf(args), ",")
	for _, want := range []string{"run_id", "first", "attempt", "trigger", "logical_date", "params"} {
		if !strings.Contains(joined, want) {
			t.Errorf("%s missing from the log line: %v", want, args)
		}
	}
}

func keysOf(args []any) []string {
	out := make([]string, 0, len(args)/2)
	for i := 0; i < len(args); i += 2 {
		if k, ok := args[i].(string); ok {
			out = append(out, k)
		}
	}
	return out
}

// --- the tri-state ---------------------------------------------------------
