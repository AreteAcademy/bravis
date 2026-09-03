package consumer_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/AreteAcademy/bravis/sdk"
)

// Escrito de fora do módulo do SDK de propósito.
//
// A mesma classe de defeito passou três vezes por testes que viviam dentro do
// pacote e provavam o que o autor enxergava, não o que um consumidor
// consegue: Data.Stats() que não existia, três With* sem re-export, e o
// cmd/bravis que o CI não construía.
//
// Este pacote está no módulo examples, que tem replace para ../sdk. Ele
// compila contra a árvore de trabalho e roda no CI, então uma quebra na
// superfície pública aparece aqui antes de virar release.

func fonte(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"a","v":1}`))
	}))
}

// executa roda o Pipeline como o binário faria e devolve o que foi logado.
func executa(t *testing.T, p sdk.Pipeline) string {
	t.Helper()
	r, w, _ := os.Pipe()
	stderr := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = stderr }()

	err := sdk.Execute(context.Background(), &p, []string{"-dry-run"})
	_ = w.Close()

	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, e := r.Read(buf)
		sb.Write(buf[:n])
		if e != nil {
			break
		}
	}
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	return sb.String()
}

func pipeline(url string) sdk.Pipeline {
	return sdk.Pipeline{
		Name:   "proof/entity",
		Source: sdk.Source{URL: url},
		Target: sdk.Target{
			Provider: "proof", Entity: "entity",
			Key: sdk.Key("id"), When: sdk.Field("id"),
			Project: "p",
			// nil: quem decide é o engine, ou ninguém.
			CreateTable: nil,
		},
	}
}

func TestSemEngineNadaAcontece(t *testing.T) {
	srv := fonte(t)
	defer srv.Close()

	log := executa(t, pipeline(srv.URL))

	if strings.Contains(log, "under Bravis") {
		t.Error("rodando à mão, o fetcher não deve nem saber que isso existe")
	}
}

func TestComEngineOFetcherSabeQueERodadoPorEle(t *testing.T) {
	srv := fonte(t)
	defer srv.Close()

	t.Setenv("BRAVIS_RUN_ID", "run-1")
	t.Setenv("BRAVIS_RUN_FIRST", "true")
	t.Setenv("BRAVIS_RUN_ATTEMPT", "1")

	log := executa(t, pipeline(srv.URL))

	if !strings.Contains(log, "under Bravis") {
		t.Errorf("o contexto do engine não chegou: %s", log)
	}
	if !strings.Contains(log, "first=true") {
		t.Errorf("a primeira execução não foi reportada: %s", log)
	}
}

func TestBeforeEnxergaOsParams(t *testing.T) {
	srv := fonte(t)
	defer srv.Close()

	t.Setenv("BRAVIS_RUN_ID", "run-1")
	t.Setenv("BRAVIS_RUN_PARAMS", `{"load_full":"true"}`)

	var viu string
	p := pipeline(srv.URL)
	p.Before = func(ctx context.Context, p *sdk.Pipeline) error {
		viu = p.Run.Params["load_full"]
		return nil
	}
	executa(t, p)

	if viu != "true" {
		t.Errorf(`p.Run.Params["load_full"] = %q, esperado "true"`, viu)
	}
}

func TestParamsNuncaENulo(t *testing.T) {
	srv := fonte(t)
	defer srv.Close()

	// Sem engine algum: ler uma chave ausente não pode estourar.
	p := pipeline(srv.URL)
	p.Before = func(ctx context.Context, p *sdk.Pipeline) error {
		_ = p.Run.Params["qualquer"]
		return nil
	}
	executa(t, p) // um panic aqui reprova o teste
}

func TestBoolEstaExportado(t *testing.T) {
	// sdk.Bool é a única forma de expressar a recusa explícita, e três opções
	// já ficaram inalcançáveis por não terem sido re-exportadas.
	if v := sdk.Bool(false); v == nil || *v {
		t.Error("sdk.Bool(false) deveria devolver um ponteiro para false")
	}
}

func TestConstantesDoEngineEstaoExportadas(t *testing.T) {
	// O consumidor precisa delas para escrever um teste como este.
	for nome, v := range map[string]string{
		"EnvRunID":         sdk.EnvRunID,
		"EnvRunFirst":      sdk.EnvRunFirst,
		"EnvRunParams":     sdk.EnvRunParams,
		"ParamCreateTable": sdk.ParamCreateTable,
	} {
		if v == "" {
			t.Errorf("%s está vazia", nome)
		}
	}
}

// O preview é feito para o consumidor ver o dado. Se ele não alcança os
// campos, ou se o writer não é dele, o recurso não existe para quem importa.
func TestConsumidorLigaOPreviewEEscolheOnde(t *testing.T) {
	srv := fonte(t)

	var out strings.Builder
	data, err := sdk.Extract(context.Background(), sdk.Source{
		URL:           srv.URL,
		Preview:       2,
		PreviewBytes:  2048,
		PreviewWriter: &out,
	})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	for range data.Records {
	}

	if out.Len() == 0 {
		t.Fatal("o consumidor pediu preview e nada foi escrito no writer dele")
	}
	if !strings.Contains(out.String(), "1 row · 2 columns") {
		t.Errorf("o rodapé não veio junto:\n%s", out.String())
	}
}

// Um número que o consumidor não consegue ler é um número que não existe.
func TestConsumidorLeOTamanhoDoQueFoiExtraido(t *testing.T) {
	srv := fonte(t)

	data, err := sdk.Extract(context.Background(), sdk.Source{URL: srv.URL})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	for range data.Records {
	}

	if data.Stats().Bytes <= 0 {
		t.Errorf("Data.Stats().Bytes = %d depois de drenar o fluxo", data.Stats().Bytes)
	}
}
