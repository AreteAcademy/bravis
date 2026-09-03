package sdk

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// --- Key e Field ---------------------------------------------------------

func TestChaveJuntaNaOrdemDada(t *testing.T) {
	// A ordem e o separador entram no ingestion_id. Este teste exists para
	// freeze them: if it breaks, the same reading starts landing twice.
	key := Key("latitude", "longitude", "time")
	got, err := key(map[string]any{
		"latitude": -23.55, "longitude": -46.63, "time": "2026-01-01T00:00",
	})
	if err != nil {
		t.Fatalf("Key: %v", err)
	}
	if got != "-23.55|-46.63|2026-01-01T00:00" {
		t.Errorf("Key = %q", got)
	}
}

func TestChaveNaoAdicionaCasasEmInteiro(t *testing.T) {
	// JSON delivers every number as float64. An id of 42 becoming "42.0"
	// would change ingestion_id across the whole base.
	got, err := Key("id")(map[string]any{"id": float64(42)})
	if err != nil {
		t.Fatal(err)
	}
	if got != "42" {
		t.Errorf("Key = %q, expected \"42\"", got)
	}
}

func TestChaveErraComCampoAusente(t *testing.T) {
	_, err := Key("id")(map[string]any{"outro": 1})
	if err == nil {
		t.Fatal("a missing field must be an error, not a short key")
	}
	if !strings.Contains(err.Error(), `"id"`) || !strings.Contains(err.Error(), "outro") {
		t.Errorf("the error must name the field and list what is available: %v", err)
	}
}

func TestCampoLeTimestamp(t *testing.T) {
	got, err := Field("time")(map[string]any{"time": "2026-01-01T00:00"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "2026-01-01T00:00" {
		t.Errorf("Field = %q", got)
	}
}

// --- Expansores ------------------------------------------------------------

func TestArraysParalelos(t *testing.T) {
	doc := map[string]any{
		"latitude":  -23.55,
		"longitude": -46.63,
		"hourly": map[string]any{
			"time":           []any{"h1", "h2"},
			"temperature_2m": []any{20.0, 21.0},
		},
	}

	regs, err := ParallelArrays("hourly", "time", "temperature_2m")(doc)
	if err != nil {
		t.Fatalf("ParallelArrays: %v", err)
	}
	if len(regs) != 2 {
		t.Fatalf("expected 2 records, got %d", len(regs))
	}

	r0 := regs[0].(map[string]any)
	if r0["time"] != "h1" || r0["temperature_2m"] != 20.0 {
		t.Errorf("record 0 = %v", r0)
	}
	// Fields outside the block describe the series and go on every row.
	if r0["latitude"] != -23.55 {
		t.Errorf("latitude should be copied onto every record: %v", r0)
	}
}

func TestArraysParalelosRecusaTamanhosDiferentes(t *testing.T) {
	// Pairing by index with different lengths would join the wrong readings.
	_, err := ParallelArrays("h", "a", "b")(map[string]any{
		"h": map[string]any{"a": []any{1, 2, 3}, "b": []any{1}},
	})
	if err == nil {
		t.Fatal("arrays of different lengths must be an error")
	}
}

func TestArrayEm(t *testing.T) {
	regs, err := ArrayAt("data", "results")(map[string]any{
		"data": map[string]any{"results": []any{
			map[string]any{"id": 1.0}, map[string]any{"id": 2.0},
		}},
	})
	if err != nil {
		t.Fatalf("ArrayAt: %v", err)
	}
	if len(regs) != 2 {
		t.Errorf("expected 2, got %d", len(regs))
	}
}

// --- Guardas ---------------------------------------------------------------

func TestRecusarSe(t *testing.T) {
	guarda := RejectIf("error")

	err := guarda(200, []byte(`{"error": true, "reason": "invalid parameter"}`))
	if err == nil {
		t.Fatal("a 200 flagged with error must be rejected")
	}
	if !strings.Contains(err.Error(), "invalid parameter") {
		t.Errorf("o reason da API precisa aparecer: %v", err)
	}

	if err := guarda(200, []byte(`{"temperature": 20}`)); err != nil {
		t.Errorf("response boa foi recusada: %v", err)
	}
	// A non-JSON body is the decoder's problem to report, not the guard's.
	if err := guarda(200, []byte(`nada disso`)); err != nil {
		t.Errorf("a non-JSON body should pass through: %v", err)
	}
}

func TestExigirCampos(t *testing.T) {
	guarda := RequireFields("hourly")
	if err := guarda(200, []byte(`{"daily": {}}`)); err == nil {
		t.Fatal("a payload missing the required field must be rejected")
	}
	if err := guarda(200, []byte(`{"hourly": {}}`)); err != nil {
		t.Errorf("a correct payload was rejected: %v", err)
	}
}

// --- Secret redaction ------------------------------------------------------

func TestRedigirRemoveSegredos(t *testing.T) {
	// An API key in the query string is the common case, and leaking one into
	// pod logs is an incident.
	cases := map[string]string{
		"https://api.x/v1?api_key=SEGREDO&lat=1": marker,
		"https://api.x/v1?token=SEGREDO":         marker,
		"https://api.x/v1?MAP_KEY=SEGREDO":       marker,
	}
	for raw, expected := range cases {
		got := redact(raw)
		if strings.Contains(got, "SEGREDO") {
			t.Errorf("secret vazou: %s -> %s", raw, got)
		}
		if !strings.Contains(got, expected) {
			t.Errorf("%s -> %s, expected it to contain %s", raw, got, expected)
		}
	}

	// The marker must not come out percent-encoded, or the log is unreadable.
	if got := redact("https://api.x/v1?api_key=X"); strings.Contains(got, "%2A") || strings.Contains(got, "%25") {
		t.Errorf("marker escaped in the log: %s", got)
	}

	// An ordinary parameter must be left alone.
	if got := redact("https://api.x/v1?lat=-23.5"); !strings.Contains(got, "-23.5") {
		t.Errorf("an ordinary parameter was redacted: %s", got)
	}
}

// --- Extract + Load ponta a ponta -----------------------------------------

func openMeteoServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{
			"latitude": -23.55, "longitude": -46.63,
			"hourly": {
				"time": ["2026-01-01T00:00", "2026-01-01T01:00"],
				"temperature_2m": [20.5, 21.0]
			}
		}`)
	}))
}

func TestExtractExpandeEMapeia(t *testing.T) {
	srv := openMeteoServer(t)
	defer srv.Close()

	data, err := Extract(context.Background(), Source{
		URL:    srv.URL,
		Guard:  RejectIf("error"),
		Expand: ParallelArrays("hourly", "time", "temperature_2m"),
	})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}

	target := Target{
		Provider: "open_meteo",
		Entity:   "hourly_temperature",
		Key:      Key("latitude", "longitude", "time"),
		When:     Field("time"),
	}

	envelopes, err := collect(data, target)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}

	if len(envelopes) != 2 {
		t.Fatalf("expected 2 readings, got %d", len(envelopes))
	}

	e := envelopes[0]
	if e.Provider != "open_meteo" || e.Entity != "hourly_temperature" {
		t.Errorf("provenance was not stamped: %+v", e)
	}
	if e.SourceKey != "-23.55|-46.63|2026-01-01T00:00" {
		t.Errorf("SourceKey = %q", e.SourceKey)
	}
	if e.RecordTS != "2026-01-01T00:00" {
		t.Errorf("RecordTS = %q", e.RecordTS)
	}

	// ingestion_id must come out, and be stable.
	id1, err := e.IngestionID()
	if err != nil {
		t.Fatalf("IngestionID: %v", err)
	}
	id2, _ := envelopes[0].IngestionID()
	if id1 != id2 {
		t.Errorf("unstable ingestion_id: %s != %s", id1, id2)
	}
	if envelopes[1].SourceKey == e.SourceKey {
		t.Error("different readings collided on the same key")
	}
}

func TestExtractGuardaRecusaAntesDeDecodificar(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"error": true, "reason": "latitude fora do intervalo"}`)
	}))
	defer srv.Close()

	_, err := Extract(context.Background(), Source{URL: srv.URL, Guard: RejectIf("error")})
	if err == nil {
		t.Fatal("the guard should have rejected a 200 carrying an error")
	}
	// Without this, the error document would land in the warehouse as data.
	if !strings.Contains(err.Error(), "latitude fora do intervalo") {
		t.Errorf("o reason precisa chegar a quem chamou: %v", err)
	}
}

// --- Erros tipados ---------------------------------------------------------

func TestErroDeFonteEmHostInexistente(t *testing.T) {
	_, err := Extract(context.Background(), Source{
		URL:         "http://127.0.0.1:1/nada",
		RetryConfig: &RetryConfig{MaxAttempts: 1},
	})
	if err == nil {
		t.Fatal("expected an error")
	}

	var source *SourceError
	if !errors.As(err, &source) {
		t.Fatalf("expected *SourceError, got %T: %v", err, err)
	}
	if !errors.Is(err, ErrSource) {
		t.Error("errors.Is(err, ErrSource) must work")
	}
}

func TestErroDeFonteCarregaStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	_, err := Extract(context.Background(), Source{URL: srv.URL})
	var source *SourceError
	if !errors.As(err, &source) {
		t.Fatalf("expected *SourceError, got %T", err)
	}
	if source.Status != 404 {
		t.Errorf("Status = %d, expected 404", source.Status)
	}
}

func TestErroDeFormatoEmChaveAusente(t *testing.T) {
	srv := openMeteoServer(t)
	defer srv.Close()

	data, err := Extract(context.Background(), Source{
		URL:    srv.URL,
		Expand: ParallelArrays("hourly", "time", "temperature_2m"),
	})
	if err != nil {
		t.Fatal(err)
	}

	// A field that does not exist is a format error, not a source error: the
	// action is to fix the mapping, not to wait and retry.
	_, err = collect(data, Target{
		Provider: "p", Entity: "e", Key: Key("campo_inexistente"),
	})
	var formato *FormatError
	if !errors.As(err, &formato) {
		t.Fatalf("expected *FormatError, got %T: %v", err, err)
	}
	if !errors.Is(err, ErrFormat) {
		t.Error("errors.Is(err, ErrFormat) must work")
	}
}

// --- Target ---------------------------------------------------------------

func TestDestinoExigeIdentidade(t *testing.T) {
	cases := []struct {
		name     string
		target   Target
		expected string
	}{
		{"no provider", Target{Entity: "e", Key: Key("id")}, "Provider"},
		{"no entity", Target{Provider: "p", Key: Key("id")}, "Entity"},
		{"no key", Target{Provider: "p", Entity: "e"}, "Key"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, _, err := c.target.resolve()
			if err == nil {
				t.Fatalf("expected an error naming %s", c.expected)
			}
			if !strings.Contains(err.Error(), c.expected) {
				t.Errorf("the error should name %s: %v", c.expected, err)
			}
		})
	}
}

func TestDestinoPrecedenciaEOrigem(t *testing.T) {
	t.Setenv(EnvProject, "from-the-environment")
	t.Setenv(EnvDataset, "dataset-from-the-environment")

	// 1. explicit beats the environment
	cfg, origins, err := Target{
		Provider: "acme", Entity: "tx", Key: Key("id"),
		Project: "explicito",
	}.resolve()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ProjectID != "explicito" {
		t.Errorf("explicit must beat the environment: %s", cfg.ProjectID)
	}
	if origins["projeto"].from != "explicit" {
		t.Errorf("origin do projeto = %q", origins["projeto"].from)
	}

	// 2. the environment beats the default
	if cfg.Dataset != "dataset-from-the-environment" {
		t.Errorf("the environment must beat the default: %s", cfg.Dataset)
	}
	if origins["dataset"].from != EnvDataset {
		t.Errorf("dataset origin = %q", origins["dataset"].from)
	}

	// 3. the default when there is neither
	if cfg.Table != "vendors_acme_txs" {
		t.Errorf("default table name = %q", cfg.Table)
	}
	if origins["table"].from != "default" {
		t.Errorf("table origin = %q", origins["table"].from)
	}
}

func TestDestinoSemProjetoErra(t *testing.T) {
	t.Setenv(EnvProject, "")
	_, _, err := Target{Provider: "p", Entity: "e", Key: Key("id")}.resolve()
	if err == nil {
		t.Fatal("no project and no environment must be an error")
	}
	if !strings.Contains(err.Error(), EnvProject) {
		t.Errorf("the error must say which variable to set: %v", err)
	}
}

func TestTargetDoesNotCreateTablesUnasked(t *testing.T) {
	t.Setenv(EnvProject, "p")

	// Nothing runs DDL against a warehouse without being asked. The zero
	// value creates nothing; CreateTable is the whole opt-in.
	cfg, _, err := Target{Provider: "a", Entity: "b", Key: Key("id")}.resolve()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CreateTable {
		t.Errorf("the zero value must not create a table: %+v", cfg)
	}

	asked, _, err := Target{
		Provider: "a", Entity: "b", Key: Key("id"), CreateTable: true,
	}.resolve()
	if err != nil {
		t.Fatal(err)
	}
	if !asked.CreateTable {
		t.Errorf("CreateTable did not reach the loader: %+v", asked)
	}
}

func TestTargetCarriesTableOptions(t *testing.T) {
	t.Setenv(EnvProject, "p")

	cfg, _, err := Target{
		Provider: "open_meteo", Entity: "hourly", Key: Key("id"),
		CreateTable:            true,
		PartitionExpiration:    90 * 24 * time.Hour,
		RequirePartitionFilter: true,
		CreateSQL:              "CREATE TABLE x (a INT64)",
	}.resolve()
	if err != nil {
		t.Fatal(err)
	}

	if cfg.PartitionExpiration != 90*24*time.Hour {
		t.Errorf("PartitionExpiration = %v", cfg.PartitionExpiration)
	}
	if !cfg.RequirePartitionFilter {
		t.Error("RequirePartitionFilter did not reach the loader")
	}
	if cfg.CreateSQL == "" {
		t.Error("CreateSQL did not reach the loader")
	}
	// Provider and Entity travel so the created table can be labelled.
	if cfg.Provider != "open_meteo" || cfg.Entity != "hourly" {
		t.Errorf("provider/entity did not reach the loader: %+v", cfg)
	}
}

func TestSomenteFiltraCamposVolateis(t *testing.T) {
	// generationtime_ms changes on every call: keeping it would make the same
	// reading write a different payload on every run.
	doc := map[string]any{
		"latitude":          -23.55,
		"generationtime_ms": 0.019,
		"hourly":            map[string]any{"time": []any{"h1"}, "temperature_2m": []any{20.0}},
	}

	raw, err := ParallelArrays("hourly", "time", "temperature_2m")(doc)
	if err != nil {
		t.Fatal(err)
	}
	if _, present := raw[0].(map[string]any)["generationtime_ms"]; !present {
		t.Fatal("precondition: ParallelArrays copies every top-level scalar")
	}

	// Only is a Transformer now: it projects a record, it does not expand a
	// document, so it belongs between Extract and Load.
	clean, err := Only("time", "temperature_2m", "latitude")(raw[0])
	if err != nil {
		t.Fatal(err)
	}

	r := clean.(map[string]any)
	if _, present := r["generationtime_ms"]; present {
		t.Errorf("a volatile field survived the filter: %v", r)
	}
	if len(r) != 3 || r["latitude"] != -23.55 || r["time"] != "h1" {
		t.Errorf("filtered record = %v", r)
	}
}

// --- Drivers ---------------------------------------------------------------

func TestSourceDriverDefaultsToHTTP(t *testing.T) {
	srv := openMeteoServer(t)
	defer srv.Close()

	// An empty Driver must not be an error: the common case sets nothing.
	if _, err := Extract(context.Background(), Source{URL: srv.URL}); err != nil {
		t.Fatalf("an empty Driver should default to HTTP: %v", err)
	}
	if _, err := Extract(context.Background(), Source{URL: srv.URL, Driver: DriverHTTP}); err != nil {
		t.Fatalf("DriverHTTP: %v", err)
	}
}

func TestSourceRejectsUnimplementedDriver(t *testing.T) {
	// An API that accepts a driver it does not have would silently fetch over
	// HTTP anyway -- the same class of lie as a Format that is ignored.
	_, err := Extract(context.Background(), Source{URL: "http://x", Driver: "s3"})
	if err == nil {
		t.Fatal("an unimplemented driver must be refused")
	}
	if !strings.Contains(err.Error(), "not implemented") || !strings.Contains(err.Error(), "http") {
		t.Errorf("the error must name what is supported: %v", err)
	}
}

func TestTargetDriverDefaultsToBigQuery(t *testing.T) {
	t.Setenv(EnvProject, "p")

	cfg, _, err := Target{Provider: "a", Entity: "b", Key: Key("id")}.resolve()
	if err != nil {
		t.Fatalf("an empty Driver should default to BigQuery: %v", err)
	}
	if cfg.Driver != DriverBigQuery {
		t.Errorf("Driver = %q, expected %q", cfg.Driver, DriverBigQuery)
	}
}

func TestTargetRejectsUnimplementedDriver(t *testing.T) {
	t.Setenv(EnvProject, "p")

	_, _, err := Target{
		Provider: "a", Entity: "b", Key: Key("id"), Driver: "postgres",
	}.resolve()
	if err == nil {
		t.Fatal("an unimplemented driver must be refused")
	}
	if !strings.Contains(err.Error(), "not implemented") {
		t.Errorf("the error must say so: %v", err)
	}
}

func TestDriverIsNotProvider(t *testing.T) {
	t.Setenv(EnvProject, "p")

	// Driver is which system receives the rows; Provider is which vendor the
	// data came from. Only Provider feeds ingestion_id -- confusing the two
	// would silently change every id in the base.
	cfg, _, err := Target{
		Provider: "open_meteo", Entity: "hourly", Key: Key("id"),
		Driver: DriverBigQuery,
	}.resolve()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Driver != DriverBigQuery {
		t.Errorf("Driver = %q", cfg.Driver)
	}

	env := Envelope{Provider: "open_meteo", Entity: "hourly", SourceKey: "k", RecordTS: "t"}
	withDriver, err := env.IngestionID()
	if err != nil {
		t.Fatal(err)
	}

	// The same record, loaded through a different driver, must keep its id.
	withoutDriver := Envelope{Provider: "open_meteo", Entity: "hourly", SourceKey: "k", RecordTS: "t"}
	id2, _ := withoutDriver.IngestionID()
	if withDriver != id2 {
		t.Error("the driver must not take part in ingestion_id")
	}
}

// --- Result counters -------------------------------------------------------

// A number in a result that is always zero is worse than no number, because
// nobody doubts it. These lock Pages and Attempts to reality.

func TestResultCountsPagesWalked(t *testing.T) {
	hits := 0
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if hits < 3 {
			w.Header().Set("Link", fmt.Sprintf(`<%s/?page=%d>; rel="next"`, srv.URL, hits+1))
		}
		_, _ = fmt.Fprintf(w, `{"id": %d}`, hits)
	}))
	defer srv.Close()

	data, err := Extract(context.Background(), Source{URL: srv.URL, FollowLinks: true})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := collect(data, Target{Provider: "p", Entity: "e", Key: Key("id")}); err != nil {
		t.Fatal(err)
	}

	if data.stats.Pages != 3 {
		t.Errorf("Pages = %d, walked %d", data.stats.Pages, hits)
	}
	if data.stats.Attempts != 3 {
		t.Errorf("Attempts = %d, expected one per page with no retries", data.stats.Attempts)
	}
}

func TestResultCountsRetriedAttempts(t *testing.T) {
	// Attempts above Pages is the signal that the source was flaky. It only
	// carries that meaning if retries are actually counted.
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if hits == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = fmt.Fprint(w, `{"id": 1}`)
	}))
	defer srv.Close()

	data, err := Extract(context.Background(), Source{
		URL: srv.URL,
		RetryConfig: &RetryConfig{
			MaxAttempts: 3, InitialBackoff: time.Millisecond,
			MaxBackoff: time.Millisecond, JitterFraction: 0.1,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := collect(data, Target{Provider: "p", Entity: "e", Key: Key("id")}); err != nil {
		t.Fatal(err)
	}

	if data.stats.Pages != 1 {
		t.Errorf("Pages = %d, expected 1", data.stats.Pages)
	}
	if data.stats.Attempts != 2 {
		t.Errorf("Attempts = %d, expected 2 (the 503 and the retry)", data.stats.Attempts)
	}
}

func TestTransformKeepsTheCounters(t *testing.T) {
	// Transform rebuilds Data; dropping the stats there would silently zero
	// the counters for every pipeline that transforms.
	srv := openMeteoServer(t)
	defer srv.Close()

	data, err := Extract(context.Background(), Source{
		URL: srv.URL, Expand: ParallelArrays("hourly", "time", "temperature_2m"),
	})
	if err != nil {
		t.Fatal(err)
	}

	data = Transform(data, Only("time", "temperature_2m", "latitude"))
	if _, err := collect(data, Target{Provider: "p", Entity: "e", Key: Key("time")}); err != nil {
		t.Fatal(err)
	}

	if data.stats == nil || data.stats.Pages != 1 || data.stats.Attempts != 1 {
		t.Errorf("Transform lost the counters: %+v", data.stats)
	}
}

// --- Removed surface -------------------------------------------------------

func TestIngestionIDNamespaceIsNotConfigurable(t *testing.T) {
	// WithMetadataNamespace used to be accepted, validated and defaulted, and
	// then ignored: IngestionID hardcodes the namespace. Anyone setting it got
	// identical ids and believed otherwise. It is gone; this locks the value
	// that the Python implementation was checked against.
	env := Envelope{
		Provider: "gov", Entity: "tx", SourceKey: "k1", RecordTS: "2026-01-01T00:00:00Z",
	}
	id, err := env.IngestionID()
	if err != nil {
		t.Fatal(err)
	}
	// Computed with Python, which is the whole point of the contract:
	//
	//	ns = uuid.UUID("e3a4f8c0-1b9d-4ea0-9c2e-77f6a6c4a4d7")
	//	uuid.uuid5(ns, "gov|tx|k1|2026-01-01T00:00:00Z")
	const fromPython = "93460f64-f56f-5209-a86e-6de9db9fd916"
	if id != fromPython {
		t.Errorf("ingestion_id = %s\nwant        = %s\nchanging this breaks every row already loaded", id, fromPython)
	}
}

func TestDataStatsIsReadableWithoutLoad(t *testing.T) {
	// A dry run, a validation pass, or an extract feeding something other
	// than Load still has to be able to see whether the source was flaky.
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if hits == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = fmt.Fprint(w, `{"id": 1}`)
	}))
	defer srv.Close()

	data, err := Extract(context.Background(), Source{
		URL: srv.URL,
		RetryConfig: &RetryConfig{
			MaxAttempts: 3, InitialBackoff: time.Millisecond,
			MaxBackoff: time.Millisecond, JitterFraction: 0.1,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for range data.Records {
	}

	stats := data.Stats()
	if stats.Pages != 1 || stats.Attempts != 2 {
		t.Errorf("Stats() = %+v, expected 1 page and 2 attempts", stats)
	}

	// Must not panic on a nil Data or one that never ran.
	var none *Data
	if none.Stats() != (Stats{}) {
		t.Error("Stats() on a nil Data should be the zero value")
	}
}
