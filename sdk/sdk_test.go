package sdk

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// --- Chave e Campo ---------------------------------------------------------

func TestChaveJuntaNaOrdemDada(t *testing.T) {
	// A ordem e o separador entram no ingestion_id. Este teste existe para
	// travá-los: se ele quebrar, a mesma leitura passa a entrar duas vezes.
	chave := Chave("latitude", "longitude", "time")
	got, err := chave(map[string]any{
		"latitude": -23.55, "longitude": -46.63, "time": "2026-01-01T00:00",
	})
	if err != nil {
		t.Fatalf("Chave: %v", err)
	}
	if got != "-23.55|-46.63|2026-01-01T00:00" {
		t.Errorf("Chave = %q", got)
	}
}

func TestChaveNaoAdicionaCasasEmInteiro(t *testing.T) {
	// JSON entrega todo número como float64. Um id 42 virando "42.0" mudaria
	// o ingestion_id de toda a base.
	got, err := Chave("id")(map[string]any{"id": float64(42)})
	if err != nil {
		t.Fatal(err)
	}
	if got != "42" {
		t.Errorf("Chave = %q, esperado \"42\"", got)
	}
}

func TestChaveErraComCampoAusente(t *testing.T) {
	_, err := Chave("id")(map[string]any{"outro": 1})
	if err == nil {
		t.Fatal("campo ausente tem de ser erro, não chave curta")
	}
	if !strings.Contains(err.Error(), `"id"`) || !strings.Contains(err.Error(), "outro") {
		t.Errorf("o erro precisa nomear o campo e listar os disponíveis: %v", err)
	}
}

func TestCampoLeTimestamp(t *testing.T) {
	got, err := Campo("time")(map[string]any{"time": "2026-01-01T00:00"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "2026-01-01T00:00" {
		t.Errorf("Campo = %q", got)
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

	regs, err := ArraysParalelos("hourly", "time", "temperature_2m")(doc)
	if err != nil {
		t.Fatalf("ArraysParalelos: %v", err)
	}
	if len(regs) != 2 {
		t.Fatalf("esperado 2 registros, veio %d", len(regs))
	}

	r0 := regs[0].(map[string]any)
	if r0["time"] != "h1" || r0["temperature_2m"] != 20.0 {
		t.Errorf("registro 0 = %v", r0)
	}
	// Campos fora do bloco descrevem a série e vão para toda linha.
	if r0["latitude"] != -23.55 {
		t.Errorf("latitude deveria ser copiada para cada registro: %v", r0)
	}
}

func TestArraysParalelosRecusaTamanhosDiferentes(t *testing.T) {
	// Parear por índice com tamanhos diferentes juntaria leituras erradas.
	_, err := ArraysParalelos("h", "a", "b")(map[string]any{
		"h": map[string]any{"a": []any{1, 2, 3}, "b": []any{1}},
	})
	if err == nil {
		t.Fatal("arrays de tamanhos diferentes têm de ser erro")
	}
}

func TestArrayEm(t *testing.T) {
	regs, err := ArrayEm("data", "results")(map[string]any{
		"data": map[string]any{"results": []any{
			map[string]any{"id": 1.0}, map[string]any{"id": 2.0},
		}},
	})
	if err != nil {
		t.Fatalf("ArrayEm: %v", err)
	}
	if len(regs) != 2 {
		t.Errorf("esperado 2, veio %d", len(regs))
	}
}

// --- Guardas ---------------------------------------------------------------

func TestRecusarSe(t *testing.T) {
	guarda := RecusarSe("error")

	err := guarda(200, []byte(`{"error": true, "reason": "parâmetro inválido"}`))
	if err == nil {
		t.Fatal("um 200 marcado com error tem de ser recusado")
	}
	if !strings.Contains(err.Error(), "parâmetro inválido") {
		t.Errorf("o motivo da API precisa aparecer: %v", err)
	}

	if err := guarda(200, []byte(`{"temperature": 20}`)); err != nil {
		t.Errorf("resposta boa foi recusada: %v", err)
	}
	// Corpo não-JSON é problema do decoder reportar, não da guarda.
	if err := guarda(200, []byte(`nada disso`)); err != nil {
		t.Errorf("corpo não-JSON deveria passar: %v", err)
	}
}

func TestExigirCampos(t *testing.T) {
	guarda := ExigirCampos("hourly")
	if err := guarda(200, []byte(`{"daily": {}}`)); err == nil {
		t.Fatal("payload sem o campo exigido tem de ser recusado")
	}
	if err := guarda(200, []byte(`{"hourly": {}}`)); err != nil {
		t.Errorf("payload correto foi recusado: %v", err)
	}
}

// --- Redação de segredo ----------------------------------------------------

func TestRedigirRemoveSegredos(t *testing.T) {
	// Chave de API em query string é o caso comum, e vazá-la em log de pod é
	// incidente.
	casos := map[string]string{
		"https://api.x/v1?api_key=SEGREDO&lat=1": marcador,
		"https://api.x/v1?token=SEGREDO":         marcador,
		"https://api.x/v1?MAP_KEY=SEGREDO":       marcador,
	}
	for bruta, esperado := range casos {
		got := redigir(bruta)
		if strings.Contains(got, "SEGREDO") {
			t.Errorf("segredo vazou: %s -> %s", bruta, got)
		}
		if !strings.Contains(got, esperado) {
			t.Errorf("%s -> %s, esperava conter %s", bruta, got, esperado)
		}
	}

	// O marcador não pode sair percent-encoded, senão o log fica ilegível.
	if got := redigir("https://api.x/v1?api_key=X"); strings.Contains(got, "%2A") || strings.Contains(got, "%25") {
		t.Errorf("marcador escapado no log: %s", got)
	}

	// Parâmetro comum não deve ser mexido.
	if got := redigir("https://api.x/v1?lat=-23.5"); !strings.Contains(got, "-23.5") {
		t.Errorf("parâmetro normal foi redigido: %s", got)
	}
}

// --- Extract + Load ponta a ponta -----------------------------------------

func servidorOpenMeteo(t *testing.T) *httptest.Server {
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
	srv := servidorOpenMeteo(t)
	defer srv.Close()

	dados, err := Extract(context.Background(), Fonte{
		URL:      srv.URL,
		Guarda:   RecusarSe("error"),
		Expandir: ArraysParalelos("hourly", "time", "temperature_2m"),
	})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}

	destino := Destino{
		Provider: "open_meteo",
		Entity:   "hourly_temperature",
		Chave:    Chave("latitude", "longitude", "time"),
		Quando:   Campo("time"),
	}

	envelopes, err := coletar(dados, destino)
	if err != nil {
		t.Fatalf("coletar: %v", err)
	}

	if len(envelopes) != 2 {
		t.Fatalf("esperado 2 leituras, veio %d", len(envelopes))
	}

	e := envelopes[0]
	if e.Provider != "open_meteo" || e.Entity != "hourly_temperature" {
		t.Errorf("procedência não foi carimbada: %+v", e)
	}
	if e.SourceKey != "-23.55|-46.63|2026-01-01T00:00" {
		t.Errorf("SourceKey = %q", e.SourceKey)
	}
	if e.RecordTS != "2026-01-01T00:00" {
		t.Errorf("RecordTS = %q", e.RecordTS)
	}

	// O ingestion_id tem de sair, e ser estável.
	id1, err := e.IngestionID()
	if err != nil {
		t.Fatalf("IngestionID: %v", err)
	}
	id2, _ := envelopes[0].IngestionID()
	if id1 != id2 {
		t.Errorf("ingestion_id instável: %s != %s", id1, id2)
	}
	if envelopes[1].SourceKey == e.SourceKey {
		t.Error("leituras diferentes colidiram na mesma chave")
	}
}

func TestExtractGuardaRecusaAntesDeDecodificar(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"error": true, "reason": "latitude fora do intervalo"}`)
	}))
	defer srv.Close()

	_, err := Extract(context.Background(), Fonte{URL: srv.URL, Guarda: RecusarSe("error")})
	if err == nil {
		t.Fatal("a guarda tinha de recusar um 200 com erro")
	}
	// Sem isso, o documento de erro entraria no warehouse como se fosse dado.
	if !strings.Contains(err.Error(), "latitude fora do intervalo") {
		t.Errorf("o motivo precisa chegar a quem chamou: %v", err)
	}
}

// --- Erros tipados ---------------------------------------------------------

func TestErroDeFonteEmHostInexistente(t *testing.T) {
	_, err := Extract(context.Background(), Fonte{
		URL:         "http://127.0.0.1:1/nada",
		RetryConfig: &RetryConfig{MaxAttempts: 1},
	})
	if err == nil {
		t.Fatal("esperado erro")
	}

	var fonte *ErroDeFonte
	if !errors.As(err, &fonte) {
		t.Fatalf("esperado *ErroDeFonte, veio %T: %v", err, err)
	}
	if !errors.Is(err, ErrFonte) {
		t.Error("errors.Is(err, ErrFonte) tem de funcionar")
	}
}

func TestErroDeFonteCarregaStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	_, err := Extract(context.Background(), Fonte{URL: srv.URL})
	var fonte *ErroDeFonte
	if !errors.As(err, &fonte) {
		t.Fatalf("esperado *ErroDeFonte, veio %T", err)
	}
	if fonte.Status != 404 {
		t.Errorf("Status = %d, esperado 404", fonte.Status)
	}
}

func TestErroDeFormatoEmChaveAusente(t *testing.T) {
	srv := servidorOpenMeteo(t)
	defer srv.Close()

	dados, err := Extract(context.Background(), Fonte{
		URL:      srv.URL,
		Expandir: ArraysParalelos("hourly", "time", "temperature_2m"),
	})
	if err != nil {
		t.Fatal(err)
	}

	// Campo que não existe: é erro de formato, não de fonte — a ação é
	// corrigir o mapeamento, não esperar e tentar de novo.
	_, err = coletar(dados, Destino{
		Provider: "p", Entity: "e", Chave: Chave("campo_inexistente"),
	})
	var formato *ErroDeFormato
	if !errors.As(err, &formato) {
		t.Fatalf("esperado *ErroDeFormato, veio %T: %v", err, err)
	}
	if !errors.Is(err, ErrFormato) {
		t.Error("errors.Is(err, ErrFormato) tem de funcionar")
	}
}

// --- Destino ---------------------------------------------------------------

func TestDestinoExigeIdentidade(t *testing.T) {
	casos := []struct {
		nome     string
		destino  Destino
		esperado string
	}{
		{"sem provider", Destino{Entity: "e", Chave: Chave("id")}, "Provider"},
		{"sem entity", Destino{Provider: "p", Chave: Chave("id")}, "Entity"},
		{"sem chave", Destino{Provider: "p", Entity: "e"}, "Chave"},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			_, _, err := c.destino.resolver()
			if err == nil {
				t.Fatalf("esperado erro citando %s", c.esperado)
			}
			if !strings.Contains(err.Error(), c.esperado) {
				t.Errorf("erro deveria citar %s: %v", c.esperado, err)
			}
		})
	}
}

func TestDestinoPrecedenciaEOrigem(t *testing.T) {
	t.Setenv(EnvProjeto, "do-ambiente")
	t.Setenv(EnvDataset, "dataset-do-ambiente")

	// 1. explícito vence o ambiente
	cfg, origens, err := Destino{
		Provider: "acme", Entity: "tx", Chave: Chave("id"),
		Projeto: "explicito",
	}.resolver()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ProjectID != "explicito" {
		t.Errorf("explícito tem de vencer o ambiente: %s", cfg.ProjectID)
	}
	if origens["projeto"].de != "explícito" {
		t.Errorf("origem do projeto = %q", origens["projeto"].de)
	}

	// 2. ambiente vence o default
	if cfg.Dataset != "dataset-do-ambiente" {
		t.Errorf("ambiente tem de vencer o default: %s", cfg.Dataset)
	}
	if origens["dataset"].de != EnvDataset {
		t.Errorf("origem do dataset = %q", origens["dataset"].de)
	}

	// 3. default quando não há nem um nem outro
	if cfg.Table != "vendors_acme_txs" {
		t.Errorf("nome padrão da tabela = %q", cfg.Table)
	}
	if origens["tabela"].de != "default" {
		t.Errorf("origem da tabela = %q", origens["tabela"].de)
	}
}

func TestDestinoSemProjetoErra(t *testing.T) {
	t.Setenv(EnvProjeto, "")
	_, _, err := Destino{Provider: "p", Entity: "e", Chave: Chave("id")}.resolver()
	if err == nil {
		t.Fatal("sem projeto e sem ambiente tem de ser erro")
	}
	if !strings.Contains(err.Error(), EnvProjeto) {
		t.Errorf("o erro precisa dizer qual variável definir: %v", err)
	}
}

func TestDestinoCriaTabelaPorPadrao(t *testing.T) {
	t.Setenv(EnvProjeto, "p")
	cfg, _, err := Destino{Provider: "a", Entity: "b", Chave: Chave("id")}.resolver()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.CriarTabela || !cfg.WriteEnvelopeColumns {
		t.Errorf("o padrão é o contrato de landing criado pelo SDK: %+v", cfg)
	}

	// PayloadCru desliga os dois: sem o contrato, o SDK não sabe o schema.
	cru, _, err := Destino{Provider: "a", Entity: "b", Chave: Chave("id"), PayloadCru: true}.resolver()
	if err != nil {
		t.Fatal(err)
	}
	if cru.CriarTabela || cru.WriteEnvelopeColumns {
		t.Errorf("PayloadCru não pode criar tabela: %+v", cru)
	}
}

func TestSomenteFiltraCamposVolateis(t *testing.T) {
	// generationtime_ms muda a cada chamada: mantê-lo faria a mesma leitura
	// gravar um payload diferente a cada execução.
	doc := map[string]any{
		"latitude":          -23.55,
		"generationtime_ms": 0.019,
		"hourly":            map[string]any{"time": []any{"h1"}, "temperature_2m": []any{20.0}},
	}

	cru, err := ArraysParalelos("hourly", "time", "temperature_2m")(doc)
	if err != nil {
		t.Fatal(err)
	}
	if _, tem := cru[0].(map[string]any)["generationtime_ms"]; !tem {
		t.Fatal("pré-condição: ArraysParalelos copia todo escalar de topo")
	}

	limpo, err := Somente(
		ArraysParalelos("hourly", "time", "temperature_2m"),
		"time", "temperature_2m", "latitude",
	)(doc)
	if err != nil {
		t.Fatal(err)
	}

	r := limpo[0].(map[string]any)
	if _, tem := r["generationtime_ms"]; tem {
		t.Errorf("campo volátil sobreviveu ao filtro: %v", r)
	}
	if len(r) != 3 || r["latitude"] != -23.55 || r["time"] != "h1" {
		t.Errorf("registro filtrado = %v", r)
	}
}
