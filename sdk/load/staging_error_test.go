package load

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cloud.google.com/go/storage"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"

	"github.com/AreteAcademy/brevis/sdk/internal/core"
)

// TestStagingErrorNamesTheBucketAndTheWayOut: o erro que o consumidor viu era
// "close gcs writer: googleapi: Error 404: The specified bucket does not
// exist". Ele nao dizia qual bucket, nem que o padrao mudou na v0.25.0, nem
// as duas saidas. Este teste fixa as quatro coisas.
func TestStagingErrorNamesTheBucketAndTheWayOut(t *testing.T) {
	l := &Loader{cfg: &core.LoadConfig{
		StagingBucket:   "projeto-brevis-staging",
		StagingPrefix:   "brevis/",
		ThresholdForGCS: 5000,
	}}

	for _, causa := range []struct {
		nome string
		err  error
	}{
		{"sentinela do storage", storage.ErrBucketNotExist},
		{"404 da api json", &googleapi.Error{Code: 404, Message: "The specified bucket does not exist"}},
	} {
		t.Run(causa.nome, func(t *testing.T) {
			msg := l.stagingError(causa.err, 12000).Error()

			for _, exigido := range []string{
				"projeto-brevis-staging", // qual bucket
				"12000",                  // por que estagiou
				"InlineLimit",            // saida 1
				"create the bucket",      // saida 2
				"v0.25.0",                // por que o nome mudou
				"StagingBucket",          // como escolher outro
			} {
				if !strings.Contains(msg, exigido) {
					t.Errorf("mensagem nao diz %q:\n%s", exigido, msg)
				}
			}
		})
	}
}

// TestStagingErrorNaoInventaDiagnostico: uma falha que nao e bucket ausente
// -- rede, permissao -- nao pode virar "crie o bucket". Envolver o erro certo
// importa mais do que ter conselho para dar.
func TestStagingErrorNaoInventaDiagnostico(t *testing.T) {
	l := &Loader{cfg: &core.LoadConfig{StagingBucket: "b", StagingPrefix: "p/"}}
	causa := errors.New("connection reset by peer")

	err := l.stagingError(causa, 12000)
	if !errors.Is(err, causa) {
		t.Fatal("o erro original precisa continuar acessivel por errors.Is")
	}
	if strings.Contains(err.Error(), "create the bucket") {
		t.Errorf("aconselhou criar bucket numa falha de rede:\n%s", err)
	}
	if !strings.Contains(err.Error(), "gs://b/p/") {
		t.Errorf("nem no caminho generico diz onde estava escrevendo:\n%s", err)
	}
}

// TestLoadViaGCSUsaStagingError e o teste que importa: os dois acima provam a
// funcao, este prova o ponto de uso. Sem ele, trocar a chamada de volta por
// fmt.Errorf("close gcs writer: %w", err) passaria verde -- foi o que
// aconteceu quando escrevi so os de cima.
//
// O GCS falso responde 404 em tudo, que e o que um bucket ausente parece.
func TestLoadViaGCSUsaStagingError(t *testing.T) {
	gcs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":404,"message":"The specified bucket does not exist"}}`))
	}))
	defer gcs.Close()

	ctx := context.Background()
	cliente, err := storage.NewClient(ctx,
		option.WithEndpoint(gcs.URL),
		option.WithoutAuthentication(),
	)
	if err != nil {
		t.Fatalf("cliente falso: %v", err)
	}
	defer func() { _ = cliente.Close() }()

	l := &Loader{
		cfg: &core.LoadConfig{
			Format:          "ndjson",
			StagingBucket:   "projeto-brevis-staging",
			StagingPrefix:   "brevis/",
			ThresholdForGCS: 5000,
		},
		gcs: cliente,
	}

	_, _, err = l.loadViaGCS(ctx, nil, []byte(`{"a":1}`+"\n"), 12000)
	if err == nil {
		t.Fatal("bucket ausente precisa falhar")
	}
	msg := err.Error()
	if strings.Contains(msg, "close gcs writer") {
		t.Errorf("voltou a mensagem crua do writer:\n%s", msg)
	}
	for _, exigido := range []string{"projeto-brevis-staging", "InlineLimit", "12000"} {
		if !strings.Contains(msg, exigido) {
			t.Errorf("o caminho real nao diz %q:\n%s", exigido, msg)
		}
	}
}
