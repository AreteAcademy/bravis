package sdk

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	core "github.com/AreteAcademy/bravis/sdk/internal/core"
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

func resp(status int, body string) Response {
	return core.NewResponse(status, nil, "http://exemplo", []byte(body))
}

func TestRecusarSe(t *testing.T) {
	guarda := RejectIf("error")

	err := guarda(resp(200, `{"error": true, "reason": "invalid parameter"}`))
	if err == nil {
		t.Fatal("a 200 flagged with error must be rejected")
	}
	if !strings.Contains(err.Error(), "invalid parameter") {
		t.Errorf("o reason da API precisa aparecer: %v", err)
	}
	if !errors.Is(err, ErrRejected) {
		t.Error("uma recusa tem de ser distinguível de um erro de programação")
	}

	if err := guarda(resp(200, `{"temperature": 20}`)); err != nil {
		t.Errorf("response boa foi recusada: %v", err)
	}
}

// Uma página HTML de erro servida com 200 -- portal em manutenção, WAF, proxy
// -- é exatamente o caso para o qual a guarda existe, e era o único que ela
// deixava passar.
func TestRecusarSeNaoDeixaPassarCorpoQueNaoEJSON(t *testing.T) {
	err := RejectIf("error")(resp(200, `<html><body>Em manutenção</body></html>`))
	if err == nil {
		t.Fatal("um corpo que não é JSON tem de ser recusado, não aceito")
	}
	if !strings.Contains(err.Error(), "not a JSON object") {
		t.Errorf("o erro precisa dizer que a resposta não é JSON: %v", err)
	}
	if !errors.Is(err, ErrRejected) {
		t.Error("é uma recusa da fonte, não um erro de programação")
	}
}

func TestExigirCampos(t *testing.T) {
	guarda := RequireFields("hourly")
	if err := guarda(resp(200, `{"daily": {}}`)); err == nil {
		t.Fatal("a payload missing the required field must be rejected")
	}
	if err := guarda(resp(200, `{"hourly": {}}`)); err != nil {
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
		URL:     srv.URL,
		Records: records(ParallelArrays("hourly", "time", "temperature_2m")),
	})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}

	target := Target{
		Metadata: &Metadata{Provider: "open_meteo", Entity: "hourly_temperature", Key: Key("latitude", "longitude", "time"), When: Field("time")},
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

func TestExtractRecordsRecusaAntesDeDecodificar(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"error": true, "reason": "latitude fora do intervalo"}`)
	}))
	defer srv.Close()

	_, err := Extract(context.Background(), Source{
		URL: srv.URL,
		Records: func(r Response) ([]any, error) {
			return nil, RejectIf("error")(r)
		},
	})
	if err == nil {
		t.Fatal("Records should have rejected a 200 carrying an error")
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
		URL:     srv.URL,
		Records: records(ParallelArrays("hourly", "time", "temperature_2m")),
	})
	if err != nil {
		t.Fatal(err)
	}

	// A field that does not exist is a format error, not a source error: the
	// action is to fix the mapping, not to wait and retry.
	_, err = collect(data, Target{
		Metadata: &Metadata{Provider: "p", Entity: "e", Key: Key("campo_inexistente")},
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

// Provenance is required exactly when the SDK is going to stamp an id, and
// not otherwise.
func TestDestinoExigeIdentidadeSomenteComMetadata(t *testing.T) {
	t.Setenv(EnvProject, "a-project")

	cases := []struct {
		name     string
		target   Target
		expected string
	}{
		{"no provider", Target{Metadata: &Metadata{Entity: "e", Key: Key("id")}}, "Provider"},
		{"no entity", Target{Metadata: &Metadata{Provider: "p", Key: Key("id")}}, "Entity"},
		{"no key", Target{Metadata: &Metadata{Provider: "p", Entity: "e"}}, "Key"},
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

// The other half, and the point of the change: with the flag off none of the
// three is needed, because the SDK has nothing to build out of them.
func TestDestinoSemMetadataNaoExigeProveniencia(t *testing.T) {
	t.Setenv(EnvProject, "a-project")

	cfg, _, err := Target{Table: "minha_tabela"}.resolve()
	if err != nil {
		t.Fatalf("a load that adds no metadata needs no provenance: %v", err)
	}
	if cfg.Table != "minha_tabela" {
		t.Errorf("Table = %q", cfg.Table)
	}
}

// Without Provider and Entity there is no default name to fall back on, and
// "vendors__s" is two missing values pretending to be one.
func TestDestinoSemNomeDeTabelaFalaClaro(t *testing.T) {
	t.Setenv(EnvProject, "a-project")

	_, _, err := Target{}.resolve()
	if err == nil {
		t.Fatal("a target with no table and no provider must not resolve")
	}
	if !strings.Contains(err.Error(), "table not set") {
		t.Errorf("the error should say the table is missing: %v", err)
	}
}

func TestTargetPrecedenceAndOrigin(t *testing.T) {
	t.Setenv(EnvProject, "from-the-environment")
	t.Setenv(EnvDataset, "dataset-from-the-environment")

	// 1. explicit beats the environment
	cfg, origins, err := Target{
		Metadata: &Metadata{Provider: "acme", Entity: "tx", Key: Key("id")},
		Project:  "explicito",
	}.resolve()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ProjectID != "explicito" {
		t.Errorf("explicit must beat the environment: %s", cfg.ProjectID)
	}
	if origins["project"].from != "explicit" {
		t.Errorf("project origin = %q", origins["project"].from)
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
	_, _, err := Target{Metadata: &Metadata{Provider: "p", Entity: "e", Key: Key("id")}}.resolve()
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
	cfg, _, err := Target{Metadata: &Metadata{Provider: "a", Entity: "b", Key: Key("id")}}.resolve()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CreateTable {
		t.Errorf("nil must not create a table: %+v", cfg)
	}

	asked, _, err := Target{
		Metadata: &Metadata{Provider: "a", Entity: "b", Key: Key("id")}, CreateTable: Bool(true),
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
		Metadata:               &Metadata{Provider: "open_meteo", Entity: "hourly", Key: Key("id")},
		CreateTable:            Bool(true),
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
	clean, err := Schema("time", "temperature_2m", "latitude")(raw[0])
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

	cfg, _, err := Target{Metadata: &Metadata{Provider: "a", Entity: "b", Key: Key("id")}}.resolve()
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
		Metadata: &Metadata{Provider: "a", Entity: "b", Key: Key("id")}, Driver: "postgres",
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
		Metadata: &Metadata{Provider: "open_meteo", Entity: "hourly", Key: Key("id")},
		Driver:   DriverBigQuery,
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

	if _, err := collect(data, Target{Metadata: &Metadata{Provider: "p", Entity: "e", Key: Key("id")}}); err != nil {
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
	if _, err := collect(data, Target{Metadata: &Metadata{Provider: "p", Entity: "e", Key: Key("id")}}); err != nil {
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
		URL: srv.URL, Records: records(ParallelArrays("hourly", "time", "temperature_2m")),
	})
	if err != nil {
		t.Fatal(err)
	}

	data = Transform(data, Schema("time", "temperature_2m", "latitude"))
	if _, err := collect(data, Target{Metadata: &Metadata{Provider: "p", Entity: "e", Key: Key("time")}}); err != nil {
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

func TestEveryCoreOptionIsReachable(t *testing.T) {
	// The low-level options live in an internal package: without a re-export
	// here they exist and no consumer can call them. Three had shipped that
	// way -- WithCreateSQL, WithPartitionExpiration and
	// WithRequirePartitionFilter -- which is the same defect as a field that
	// does nothing, only harder to notice because the compiler is happy.
	//
	// Reading types.go is the only way to check: a compile-time reference
	// would be the very thing being tested.
	src, err := os.ReadFile("types.go")
	if err != nil {
		t.Fatal(err)
	}
	core, err := os.ReadFile(filepath.Join("internal", "core", "types.go"))
	if err != nil {
		t.Fatal(err)
	}

	declared := regexp.MustCompile(`(?m)^func (With[A-Za-z]+)\(`).FindAllStringSubmatch(string(core), -1)
	if len(declared) == 0 {
		t.Fatal("found no With* options in core; has the file moved?")
	}

	for _, m := range declared {
		name := m[1]
		if !regexp.MustCompile(`\b` + name + `\s*=\s*core\.` + name + `\b`).Match(src) {
			t.Errorf("core.%s is not re-exported in types.go, so no consumer can call it", name)
		}
	}
}

// --- the payload is the caller's, not the SDK's -------------------------

// With Metadata off the SDK has nothing to build out of the payload, so
// it must not read one field out of it. Proved with selectors that fail if
// they are ever called: a selector that runs is a selector whose failure can
// sink a load the caller never asked the SDK to inspect.
func TestSemMetadataOSDKNaoTocaNoPayload(t *testing.T) {
	srv := openMeteoServer(t)
	defer srv.Close()

	data, err := Extract(context.Background(), Source{
		URL:     srv.URL,
		Records: records(ParallelArrays("hourly", "time", "temperature_2m")),
	})
	if err != nil {
		t.Fatal(err)
	}

	// The old design took Key and When on Target, so a load that added no
	// metadata still ran them over every record. Now they live inside the
	// Metadata block: without it there are no selectors to run, and no way to
	// hand the SDK one. The guarantee moved from a validation to the shape of
	// the API, which is the stronger place for it.
	envelopes, err := collect(data, Target{Table: "qualquer"})
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	for i, e := range envelopes {
		if e.Provider != "" || e.Entity != "" || e.SourceKey != "" || e.RecordTS != "" {
			t.Errorf("record %d was stamped with provenance nobody asked for: %+v", i, e)
		}
	}
	if len(envelopes) == 0 {
		t.Fatal("no records came through")
	}
	for i, e := range envelopes {
		if e.Provider != "" || e.Entity != "" || e.SourceKey != "" || e.RecordTS != "" {
			t.Errorf("record %d was stamped with provenance nobody asked for: %+v", i, e)
		}
	}
}

// And what comes out is what went in, field for field.
func TestSemMetadataOPayloadSaiComoEntrou(t *testing.T) {
	srv := openMeteoServer(t)
	defer srv.Close()

	data, err := Extract(context.Background(), Source{
		URL:     srv.URL,
		Records: records(ParallelArrays("hourly", "time", "temperature_2m")),
	})
	if err != nil {
		t.Fatal(err)
	}

	envelopes, err := collect(data, Target{Table: "qualquer"})
	if err != nil {
		t.Fatalf("collect: %v", err)
	}

	got, ok := envelopes[0].Payload.(map[string]any)
	if !ok {
		t.Fatalf("payload changed shape: %T", envelopes[0].Payload)
	}
	want := []string{"latitude", "longitude", "temperature_2m", "time"}
	if len(got) != len(want) {
		t.Errorf("the SDK added or removed fields: %v", got)
	}
	for _, f := range want {
		if _, present := got[f]; !present {
			t.Errorf("field %q went missing", f)
		}
	}
}

// --- AutoID -------------------------------------------------------------

// AutoID is the whole declaration: nothing about the record goes into the id,
// so nothing about the record has to be described.
func TestAutoIDSozinhoBasta(t *testing.T) {
	t.Setenv(EnvProject, "um-projeto")

	cfg, _, err := Target{
		Table:    "minha_tabela",
		Metadata: &Metadata{AutoID: true},
	}.resolve()
	if err != nil {
		t.Fatalf("AutoID não deveria pedir mais nada: %v", err)
	}
	if !cfg.Metadata || !cfg.AutoID {
		t.Errorf("as flags não chegaram ao load: Metadata=%v AutoID=%v", cfg.Metadata, cfg.AutoID)
	}
}

// A field that is set and never read is the defect this SDK keeps finding in
// itself, so AutoID refuses provenance rather than ignoring it.
func TestAutoIDRecusaProvenienciaQueNaoSeriaLida(t *testing.T) {
	t.Setenv(EnvProject, "um-projeto")

	_, _, err := Target{
		Table:    "t",
		Metadata: &Metadata{AutoID: true, Provider: "acme", Key: Key("id")},
	}.resolve()
	if err == nil {
		t.Fatal("Provider e Key com AutoID não são lidos; aceitar isso é mentir sobre o id")
	}
	for _, want := range []string{"Provider", "Key", "AutoID"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("o erro não menciona %q: %v", want, err)
		}
	}
}

// With AutoID nothing is read out of the record, so collect must not stamp
// provenance either.
func TestAutoIDNaoCarimbaProveniencia(t *testing.T) {
	srv := openMeteoServer(t)
	defer srv.Close()

	data, err := Extract(context.Background(), Source{
		URL: srv.URL, Records: records(ParallelArrays("hourly", "time", "temperature_2m")),
	})
	if err != nil {
		t.Fatal(err)
	}

	envelopes, err := collect(data, Target{Table: "t", Metadata: &Metadata{AutoID: true}})
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	for i, e := range envelopes {
		if e.SourceKey != "" || e.RecordTS != "" {
			t.Errorf("registro %d ganhou proveniência que o id não usa: %+v", i, e)
		}
	}
}

// records adapta um Expander para o campo Records, que é onde a decisão de
// "o que esta resposta carrega" mora agora.
func records(e Expander) func(Response) ([]any, error) {
	return func(r Response) ([]any, error) {
		doc, err := r.Object()
		if err != nil {
			return nil, err
		}
		return e(doc)
	}
}

// --- Records: por resposta, e todo 2xx chega ----------------------------

// Um vendor que responde 204 numa janela vazia não pode ser pipeline
// vermelho. Zero registros é um resultado, não uma falha.
func TestTodoDoisXXChegaAoRecords(t *testing.T) {
	casos := []struct {
		status int
		corpo  string
		linhas int
	}{
		{200, `[{"a":1},{"a":2}]`, 2},
		{201, `[{"a":1}]`, 1},
		{204, ``, 0},
		{206, `[{"a":1}]`, 1},
	}

	for _, c := range casos {
		t.Run(fmt.Sprint(c.status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(c.status)
				_, _ = fmt.Fprint(w, c.corpo)
			}))
			defer srv.Close()

			visto := 0
			data, err := Extract(context.Background(), Source{
				URL: srv.URL,
				Records: func(r Response) ([]any, error) {
					visto = r.Status
					if len(r.Bytes()) == 0 {
						return nil, nil // janela vazia
					}
					var docs []any
					return docs, r.JSON(&docs)
				},
			})
			if err != nil {
				t.Fatalf("http %d derrubou a execução: %v", c.status, err)
			}
			if visto != c.status {
				t.Errorf("Records viu status %d, esperado %d", visto, c.status)
			}

			n := 0
			for _, err := range data.Records {
				if err != nil {
					t.Fatalf("iteração: %v", err)
				}
				n++
			}
			if n != c.linhas {
				t.Errorf("http %d rendeu %d registros, esperado %d", c.status, n, c.linhas)
			}
		})
	}
}

// Não-2xx continua como estava: erro com status e corpo.
func TestNaoDoisXXContinuaFalhando(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(404)
		_, _ = fmt.Fprint(w, `nao existe`)
	}))
	defer srv.Close()

	chamou := false
	_, err := Extract(context.Background(), Source{
		URL:     srv.URL,
		Records: func(Response) ([]any, error) { chamou = true; return nil, nil },
	})
	if err == nil {
		t.Fatal("um 404 tem de falhar")
	}
	if chamou {
		t.Error("um não-2xx não é resposta de sucesso; Records não deve vê-lo")
	}
	var se *SourceError
	if !errors.As(err, &se) || se.Status != 404 {
		t.Errorf("o status precisa chegar a quem chamou: %v", err)
	}
}

// Uma recusa da fonte e um erro de programação pedem coisas diferentes de
// quem está de plantão, então têm de ser distinguíveis.
func TestRecusaSeDistingueDeErroDeProgramacao(t *testing.T) {
	recusa := Reject("open-meteo recusou: %s", "latitude inválida")
	if !errors.Is(recusa, ErrRejected) {
		t.Error("Reject tem de casar com errors.Is(err, ErrRejected)")
	}
	if !strings.Contains(recusa.Error(), "latitude inválida") {
		t.Errorf("a razão precisa sobreviver: %v", recusa)
	}

	if errors.Is(fmt.Errorf("nil map"), ErrRejected) {
		t.Error("um erro comum não pode passar por recusa")
	}

	var r *Rejection
	if !errors.As(recusa, &r) {
		t.Error("Rejection tem de ser alcançável por errors.As")
	}
}

// E a recusa sobrevive à travessia do extract, que é onde ela precisa
// chegar para virar log e alerta.
func TestRecusaSobreviveAoExtract(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"error":true,"reason":"latitude fora do intervalo"}`)
	}))
	defer srv.Close()

	_, err := Extract(context.Background(), Source{
		URL:     srv.URL,
		Records: func(r Response) ([]any, error) { return nil, RejectIf("error")(r) },
	})
	if err == nil {
		t.Fatal("a recusa não chegou")
	}
	if !errors.Is(err, ErrRejected) {
		t.Errorf("a recusa perdeu o tipo na travessia: %T %v", err, err)
	}
	if !strings.Contains(err.Error(), "latitude fora do intervalo") {
		t.Errorf("a razão do vendor se perdeu: %v", err)
	}
}

// Records e DataKey respondem a mesma pergunta, e com Records o DataKey
// nunca seria lido -- um campo que não faz nada é pior que um erro.
func TestRecordsComDataKeyERecusado(t *testing.T) {
	_, err := Extract(context.Background(), Source{
		URL:     "http://exemplo.invalido",
		DataKey: "results",
		Records: func(Response) ([]any, error) { return nil, nil },
	})
	if err == nil {
		t.Fatal("Records junto de DataKey deixaria o DataKey sem efeito")
	}
	for _, want := range []string{"Records", "DataKey", "results"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("o erro não menciona %q: %v", want, err)
		}
	}
}
