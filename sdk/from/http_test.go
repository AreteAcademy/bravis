package from

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	core "github.com/AreteAcademy/brevis/sdk/internal/core"
)

// O from.HTTP é um adaptador: ele copia os seus campos para a core.Source que
// o extract consome. Um campo esquecido nessa cópia não quebra nada que
// compile -- ele simplesmente deixa de ter efeito, que é o defeito que este
// SDK mais achou em si mesmo.
//
// Este teste confere que cada campo chega ao outro lado.
func TestHTTPPassaTodosOsCamposAdiante(t *testing.T) {
	var (
		metodo   string
		corpo    string
		header   string
		caminho  string
		chamadas int
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chamadas++
		metodo = r.Method
		b := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(b)
		corpo = string(b)
		header = r.Header.Get("X-Cliente")
		caminho = r.URL.Path
		_, _ = w.Write([]byte(`[{"id":1}]`))
	}))
	defer srv.Close()

	stats := &core.Stats{}
	var preview strings.Builder

	seq, err := HTTP{
		URL:          srv.URL + "/v1/eventos",
		Method:       http.MethodPost,
		Body:         strings.NewReader(`{"q":1}`),
		Header:       map[string][]string{"X-Cliente": {"brevis"}},
		Timeout:      5 * time.Second,
		TotalTimeout: time.Minute,
		Format:       core.FormatJSON,
		MaxPages:     3,
		RetryConfig:  &core.RetryConfig{MaxAttempts: 1},
	}.Read(context.Background(), core.ReadOptions{
		Stats: stats, Preview: 5, PreviewWriter: &preview,
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	n := 0
	for _, err := range seq {
		if err != nil {
			t.Fatalf("iterando: %v", err)
		}
		n++
	}

	// Os campos do driver.
	if metodo != http.MethodPost {
		t.Errorf("Method não chegou: %q", metodo)
	}
	if corpo != `{"q":1}` {
		t.Errorf("Body não chegou: %q", corpo)
	}
	if header != "brevis" {
		t.Errorf("Header não chegou: %q", header)
	}
	if caminho != "/v1/eventos" {
		t.Errorf("URL não chegou inteira: %q", caminho)
	}

	// As opções que atravessam todo driver.
	if stats.Bytes == 0 || stats.Pages == 0 {
		t.Errorf("Stats não foi preenchido: %+v", stats)
	}
	if !strings.Contains(preview.String(), "id") {
		t.Errorf("o Preview não saiu:\n%s", preview.String())
	}
	if n != 1 {
		t.Errorf("%d registros, esperado 1", n)
	}
}

// Cada formato tem de chegar ao decodificador certo. Um Format ignorado
// decodificaria JSON onde o fetcher pediu CSV.
func TestHTTPEncaminhaCadaFormato(t *testing.T) {
	casos := []struct {
		formato core.Format
		corpo   string
		campo   string
	}{
		{core.FormatJSON, `[{"a":"1"}]`, "a"},
		{core.FormatNDJSON, `{"a":"1"}` + "\n", "a"},
		{core.FormatCSV, "a\n1\n", "a"},
		{core.FormatXML, `<r><item><a>1</a></item></r>`, "a"},
	}

	for _, c := range casos {
		t.Run(string(c.formato), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(c.corpo))
			}))
			defer srv.Close()

			seq, err := HTTP{URL: srv.URL, Format: c.formato}.
				Read(context.Background(), core.ReadOptions{})
			if err != nil {
				t.Fatalf("Read: %v", err)
			}
			n := 0
			for env, err := range seq {
				if err != nil {
					t.Fatalf("iterando: %v", err)
				}
				if _, err := core.AsObject(env.Payload); err != nil {
					t.Errorf("o registro não é objeto: %v", env.Payload)
				}
				n++
			}
			if n == 0 {
				t.Errorf("nenhum registro saiu de um corpo %s válido", c.formato)
			}
		})
	}
}

func TestHTTPRecusaFormatoDesconhecido(t *testing.T) {
	_, err := HTTP{URL: "http://x", Format: "yaml"}.Read(context.Background(), core.ReadOptions{})
	if err == nil {
		t.Fatal("um formato que o SDK não decodifica tem de ser recusado")
	}
	if !strings.Contains(err.Error(), "yaml") {
		t.Errorf("o erro precisa nomear o formato: %v", err)
	}
}

// Describe é o que aparece no log e na mensagem de erro, então não pode
// carregar o segredo que a URL carrega.
func TestHTTPDescribeNaoVazaSegredo(t *testing.T) {
	got := HTTP{URL: "https://api.exemplo.com/v1?api_key=SEGREDO&lat=-23.5"}.Describe()

	if strings.Contains(got, "SEGREDO") {
		t.Errorf("Describe vazou a chave: %s", got)
	}
	if !strings.Contains(got, "api.exemplo.com") || !strings.Contains(got, "lat=-23.5") {
		t.Errorf("Describe apagou o que serve para depurar: %s", got)
	}
}
