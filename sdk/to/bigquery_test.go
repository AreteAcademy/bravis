package to

import (
	"strings"
	"testing"

	core "github.com/AreteAcademy/bravis/sdk/internal/core"
)

// boolp is sdk.Bool. Duplicated because to cannot import the root.
func boolp(b bool) *bool { return &b }

func run(first bool, params map[string]string) core.WriteOptions {
	if params == nil {
		params = map[string]string{}
	}
	return core.WriteOptions{Run: core.RunContext{First: first, Params: params}}
}

// The tri-state exists because two states cannot carry "I did not say".
func TestCreateTableUnsetLetsTheEngineDecide(t *testing.T) {
	t.Setenv(core.EnvProject, "p")
	dest := BigQuery{Table: "t"}

	// Outside the engine: nothing is created.
	cfg, origins, err := dest.config(run(false, nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CreateTable {
		t.Error("with nobody asking, nothing should be created")
	}
	if origins["create_table"].Where != "default" {
		t.Errorf("origin = %q", origins["create_table"].Where)
	}

	// First run of this step: created.
	cfg, origins, err = dest.config(run(true, nil))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.CreateTable {
		t.Error("a first run should create the table")
	}
	if !strings.Contains(origins["create_table"].Where, "first run") {
		t.Errorf("the log has to say why: %q", origins["create_table"].Where)
	}
}

func TestCreateTableParamAsksWithoutFakingAFirstRun(t *testing.T) {
	t.Setenv(core.EnvProject, "p")

	cfg, origins, err := BigQuery{Table: "t"}.config(
		run(false, map[string]string{core.ParamCreateTable: "true"}))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.CreateTable {
		t.Error("create_table=true should create the table")
	}
	if !strings.Contains(origins["create_table"].Where, core.ParamCreateTable) {
		t.Errorf("the log has to name the parameter: %q", origins["create_table"].Where)
	}
}

// An explicit refusal has to win, or the same code behaves differently inside
// and outside the engine with nothing to warn you.
func TestExplicitRefusalBeatsTheEngine(t *testing.T) {
	t.Setenv(core.EnvProject, "p")

	cfg, origins, err := BigQuery{Table: "t", CreateTable: boolp(false)}.config(
		run(true, map[string]string{core.ParamCreateTable: "true"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CreateTable {
		t.Error("sdk.Bool(false) must refuse even on a first run")
	}
	if origins["create_table"].Where != "explicit" {
		t.Errorf("origin = %q", origins["create_table"].Where)
	}
}

func TestExplicitTrueWorksWithoutTheEngine(t *testing.T) {
	t.Setenv(core.EnvProject, "p")

	cfg, _, err := BigQuery{Table: "t", CreateTable: boolp(true)}.config(run(false, nil))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.CreateTable {
		t.Error("sdk.Bool(true) creates without the engine")
	}
}

// The precedence, and the log that says which rung answered.
func TestConfigPrecedenceAndOrigin(t *testing.T) {
	t.Setenv(core.EnvProject, "from-the-environment")
	t.Setenv(core.EnvDataset, "dataset-from-the-environment")

	cfg, origins, err := BigQuery{Table: "t", Dataset: "explicit"}.config(run(false, nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Dataset != "explicit" {
		t.Errorf("explicit should beat the environment: %q", cfg.Dataset)
	}
	if origins["dataset"].Where != "explicit" {
		t.Errorf("origin = %q", origins["dataset"].Where)
	}
	if cfg.ProjectID != "from-the-environment" {
		t.Errorf("the environment should fill the project: %q", cfg.ProjectID)
	}
	if origins["project"].Where != core.EnvProject {
		t.Errorf("the log must name the variable: %q", origins["project"].Where)
	}

	// And the default, when neither said anything.
	t.Setenv(core.EnvDataset, "")
	cfg, origins, _ = BigQuery{Table: "t"}.config(run(false, nil))
	if cfg.Dataset != "landing" {
		t.Errorf("dataset default = %q", cfg.Dataset)
	}
	if origins["dataset"].Where != "default" {
		t.Errorf("origin = %q", origins["dataset"].Where)
	}
}

// Without a project there is nowhere to write, and the error says which
// variable would have answered.
func TestConfigRefusesWithoutAProject(t *testing.T) {
	t.Setenv(core.EnvProject, "")

	_, _, err := BigQuery{Table: "t"}.config(run(false, nil))
	if err == nil {
		t.Fatal("a destination with no project must not resolve")
	}
	if !strings.Contains(err.Error(), core.EnvProject) {
		t.Errorf("the error should name the variable: %v", err)
	}
}

func TestConfigRefusesWithoutATable(t *testing.T) {
	t.Setenv(core.EnvProject, "p")

	_, _, err := BigQuery{}.config(run(false, nil))
	if err == nil {
		t.Fatal("a destination with no table must not resolve")
	}
	if !strings.Contains(err.Error(), "table") {
		t.Errorf("the error should say the table is missing: %v", err)
	}
}

// Describe is what the log and the error message show, so it has to be the
// table anyone would grep for.
func TestDescribeNamesTheTable(t *testing.T) {
	t.Setenv(core.EnvDataset, "")
	if got := (BigQuery{Dataset: "bronze", Table: "pedidos"}).Describe(); got != "bronze.pedidos" {
		t.Errorf("Describe() = %q", got)
	}
	if got := (BigQuery{Table: "pedidos"}).Describe(); got != "landing.pedidos" {
		t.Errorf("Describe() with the default dataset = %q", got)
	}
}
