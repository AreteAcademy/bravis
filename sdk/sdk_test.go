package sdk

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/sdk/from"
	core "github.com/AreteAcademy/brevis/sdk/internal/core"
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
	return core.NewResponse(status, nil, "http://exemplo", []byte(body), false)
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

	data, err := Extract(context.Background(), Source{From: from.HTTP{
		URL:     srv.URL,
		Records: records(ParallelArrays("hourly", "time", "temperature_2m")),
	}})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}

	// A linha inteira é composta na cadeia, ingestion_id incluído. Nada é
	// carimbado depois.
	data = Transform(data,
		Compute("provider", func(map[string]any) (any, error) { return "open_meteo", nil }),
		Compute("entity", func(map[string]any) (any, error) { return "hourly_temperature", nil }),
		Compute("source_key", func(r map[string]any) (any, error) {
			return Key("latitude", "longitude", "time")(r)
		}),
		IngestionID("provider", "entity", "source_key", "time"),
	)

	envelopes, err := collect(data, Target{})
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(envelopes) != 2 {
		t.Fatalf("expected 2 readings, got %d", len(envelopes))
	}

	primeiro := envelopes[0].Payload.(map[string]any)
	if primeiro["source_key"] != "-23.55|-46.63|2026-01-01T00:00" {
		t.Errorf("source_key = %q", primeiro["source_key"])
	}
	if primeiro[ColumnIngestionID] == nil || primeiro[ColumnIngestionID] == "" {
		t.Error("a cadeia não escreveu ingestion_id")
	}

	segundo := envelopes[1].Payload.(map[string]any)
	if primeiro[ColumnIngestionID] == segundo[ColumnIngestionID] {
		t.Error("duas leituras diferentes colidiram no mesmo id")
	}
}

func TestExtractRecordsRecusaAntesDeDecodificar(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"error": true, "reason": "latitude fora do intervalo"}`)
	}))
	defer srv.Close()

	_, err := Extract(context.Background(), Source{From: from.HTTP{
		URL:     srv.URL,
		Records: func(r Response) ([]any, error) { return nil, RejectIf("error")(r) },
	}})
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
		From: from.HTTP{
			URL:         "http://127.0.0.1:1/nada",
			RetryConfig: &RetryConfig{MaxAttempts: 1},
		},
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

	_, err := Extract(context.Background(), Source{From: from.HTTP{URL: srv.URL}})
	var source *SourceError
	if !errors.As(err, &source) {
		t.Fatalf("expected *SourceError, got %T", err)
	}
	if source.Status != 404 {
		t.Errorf("Status = %d, expected 404", source.Status)
	}
}

// Um campo que não existe é erro de formato: a ação é consertar o mapeamento,
// não esperar e tentar de novo. Agora o erro vem da cadeia, que é onde a
// chave passou a ser computada.
func TestErroDeFormatoEmChaveAusente(t *testing.T) {
	srv := openMeteoServer(t)
	defer srv.Close()

	data, err := Extract(context.Background(), Source{From: from.HTTP{
		URL:     srv.URL,
		Records: records(ParallelArrays("hourly", "time", "temperature_2m")),
	}})
	if err != nil {
		t.Fatal(err)
	}
	data = Transform(data, Compute("source_key", func(r map[string]any) (any, error) {
		return Key("campo_inexistente")(r)
	}))

	_, err = collect(data, Target{})
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
	clean, err := Accept("time", "temperature_2m", "latitude")(raw[0])
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
	if _, err := Extract(context.Background(), Source{From: from.HTTP{URL: srv.URL}}); err != nil {
		t.Fatalf("an empty Driver should default to HTTP: %v", err)
	}
	if _, err := Extract(context.Background(), Source{From: from.HTTP{URL: srv.URL}}); err != nil {
		t.Fatalf("DriverHTTP: %v", err)
	}
}

// An unimplemented source used to be a runtime error on Source.Driver. Now
// the driver is a value, so there is no field to write a wrong name into --
// it is a compile error, which is strictly better. What can still go wrong is
// forgetting the driver entirely.
func TestSourceRefusesNoDriver(t *testing.T) {
	_, err := Extract(context.Background(), Source{})
	if err == nil {
		t.Fatal("a Source with no From has nowhere to read from")
	}
	for _, want := range []string{"Source.From", "from.HTTP"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error should say what to pass (%q): %v", want, err)
		}
	}
}

func TestTargetRefusesNoDriver(t *testing.T) {
	err := Target{}.validate()
	if err == nil {
		t.Fatal("a Target with no To has nowhere to write to")
	}
	// "to.BigQuery" saiu da mensagem porque esse tipo não existe desde a
	// v0.19.0, quando o BigQuery virou to/bigquery.Table. Uma sugestão de erro
	// que aponta para uma API removida manda o consumidor procurar o que não
	// há -- e este teste estava fixando exatamente isso.
	for _, want := range []string{"Target.To", "bigquery.Table"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error should say what to pass (%q): %v", want, err)
		}
	}
}

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

	data, err := Extract(context.Background(), Source{From: from.HTTP{URL: srv.URL, FollowLinks: true}})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := collect(data, Target{}); err != nil {
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
		From: from.HTTP{
			URL: srv.URL,
			RetryConfig: &RetryConfig{
				MaxAttempts: 3, InitialBackoff: time.Millisecond,
				MaxBackoff: time.Millisecond, JitterFraction: 0.1,
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := collect(data, Target{}); err != nil {
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

	data, err := Extract(context.Background(), Source{From: from.HTTP{
		URL:     srv.URL,
		Records: records(ParallelArrays("hourly", "time", "temperature_2m")),
	}})
	if err != nil {
		t.Fatal(err)
	}

	data = Transform(data, Accept("time", "temperature_2m", "latitude"))
	if _, err := collect(data, Target{}); err != nil {
		t.Fatal(err)
	}

	if data.stats == nil || data.stats.Pages != 1 || data.stats.Attempts != 1 {
		t.Errorf("Transform lost the counters: %+v", data.stats)
	}
}

// --- Removed surface -------------------------------------------------------

func TestIngestionIDDoNamespacePadraoNaoMuda(t *testing.T) {
	// O nome deste teste era "NamespaceIsNotConfigurable", e virou falso na
	// v0.38.0: sdk.Namespace escolhe outro. O que ele SEMPRE afirmou continua
	// valendo e é o que importa -- o valor do padrão, que é o que quem já
	// gravou tem na tabela.
	//
	// WithMetadataNamespace, antes disso, era aceito, validado, defaultado e
	// então IGNORADO: quem o setava recebia ids idênticos e acreditava no
	// contrário. Foi removido, e é por isso que a configuração de agora entra
	// por um caminho que o teste acima prova que é usado.
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
		From: from.HTTP{
			URL: srv.URL,
			RetryConfig: &RetryConfig{
				MaxAttempts: 3, InitialBackoff: time.Millisecond,
				MaxBackoff: time.Millisecond, JitterFraction: 0.1,
			},
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
	// reflect.DeepEqual e nao ==: o Stats ganhou um slice na v0.39.0, e um
	// struct com slice nao e comparavel.
	if !reflect.DeepEqual(none.Stats(), Stats{}) {
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
			data, err := Extract(context.Background(), Source{From: from.HTTP{
				URL: srv.URL,
				Records: func(r Response) ([]any, error) {
					visto = r.Status
					if len(r.Bytes()) == 0 {
						return nil, nil // janela vazia
					}
					var docs []any
					return docs, r.JSON(&docs)
				},
			}})
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
	_, err := Extract(context.Background(), Source{From: from.HTTP{
		URL:     srv.URL,
		Records: func(Response) ([]any, error) { chamou = true; return nil, nil },
	}})
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

	_, err := Extract(context.Background(), Source{From: from.HTTP{
		URL:     srv.URL,
		Records: func(r Response) ([]any, error) { return nil, RejectIf("error")(r) },
	}})
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
	_, err := Extract(context.Background(), Source{From: from.HTTP{
		URL:     "http://exemplo.invalido",
		DataKey: "results",
		Records: func(Response) ([]any, error) { return nil, nil },
	}})
	if err == nil {
		t.Fatal("Records junto de DataKey deixaria o DataKey sem efeito")
	}
	for _, want := range []string{"Reading", "DataKey", "results"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("o erro não menciona %q: %v", want, err)
		}
	}
}

// Um corpo que não é o esperado é a fonte mandando algo que não é dado, e
// isso vale também quando o fetcher chama os decodificadores direto -- que é
// o que o exemplo da própria spec faz.
func TestDecodificarCorpoErradoEUmaRecusa(t *testing.T) {
	r := resp(200, `<html>Em manutenção</html>`)

	_, err := r.Object()
	if err == nil {
		t.Fatal("HTML não é objeto JSON")
	}
	if !errors.Is(err, ErrRejected) {
		t.Errorf("Object() devolveu erro comum, não recusa: %T %v", err, err)
	}

	var docs []any
	if err := r.JSON(&docs); err == nil {
		t.Fatal("HTML não é o JSON esperado")
	} else if !errors.Is(err, ErrRejected) {
		t.Errorf("JSON() devolveu erro comum, não recusa: %T %v", err, err)
	}
}

// records adapta um Expander para o campo Records, que é onde a decisão de
// "o que esta resposta carrega" mora.
func records(e Expander) func(Response) ([]any, error) {
	return func(r Response) ([]any, error) {
		doc, err := r.Object()
		if err != nil {
			return nil, err
		}
		return e(doc)
	}
}
