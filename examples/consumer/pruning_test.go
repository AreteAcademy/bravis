package consumer_test

import (
	"os/exec"
	"strings"
	"testing"
)

// A poda de dependência é o motivo de os drivers viverem em subpacotes, e é
// medível -- então é afirmada, não prometida.
//
// Antes da fase 0 a raiz importava sdk/load, que importa o BigQuery: 458
// pacotes e 21 MB de binário para quem quisesse fazer Postgres -> Postgres.
// Go poda por pacote importado, nunca por campo usado, então a única forma de
// não pagar por um driver é não importar o pacote dele.
func TestQuemNaoUsaBigQueryNaoCompilaBigQuery(t *testing.T) {
	casos := []struct {
		nome     string
		pacotes  []string
		proibido bool
	}{
		{"só a raiz", []string{"github.com/AreteAcademy/brevis/sdk"}, true},
		{"raiz + from", []string{
			"github.com/AreteAcademy/brevis/sdk",
			"github.com/AreteAcademy/brevis/sdk/from",
		}, true},
		// Um pipeline de arquivos inteiro -- from e to -- ainda não traz o
		// BigQuery. Este caso faltava na v0.20.0, e sem ele o defeito passou:
		// to.BigQuery e to.Files viviam no mesmo pacote, então escrever um
		// arquivo compilava o Google.
		{"raiz + from + to (arquivos)", []string{
			"github.com/AreteAcademy/brevis/sdk",
			"github.com/AreteAcademy/brevis/sdk/from",
			"github.com/AreteAcademy/brevis/sdk/to",
		}, true},

		// E o controle: quem pede o BigQuery recebe o BigQuery. Sem isto, o
		// teste passaria com um SDK que não carrega nada.
		{"raiz + to/bigquery", []string{
			"github.com/AreteAcademy/brevis/sdk",
			"github.com/AreteAcademy/brevis/sdk/to/bigquery",
		}, false},
	}

	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			carrega := strings.Contains(deps(t, c.pacotes...), "cloud.google.com/go/bigquery")

			if c.proibido && carrega {
				t.Error("o BigQuery entrou no grafo de quem não o importou")
			}
			if !c.proibido && !carrega {
				t.Error("quem importa to.BigQuery tem de receber o BigQuery; " +
					"sem isto o teste acima não prova nada")
			}
		})
	}
}

// O mesmo raciocínio para os backends de object storage. from.Files serve
// disco, S3 e GCS, mas o backend é um valor -- então ler um CSV local não
// compila a AWS nem o Google. Com os três num pacote só, compilaria.
func TestQuemLeArquivoLocalNaoCompilaNuvem(t *testing.T) {
	casos := []struct {
		nome     string
		pacotes  []string
		procura  string
		esperado bool
	}{
		{"from sozinho não traz a AWS", []string{
			"github.com/AreteAcademy/brevis/sdk/from"}, "aws-sdk-go", false},
		{"from sozinho não traz o Google", []string{
			"github.com/AreteAcademy/brevis/sdk/from"}, "cloud.google.com", false},

		// Os controles: quem pede o backend recebe o backend.
		{"store/s3 traz a AWS", []string{
			"github.com/AreteAcademy/brevis/sdk/store/s3"}, "aws-sdk-go", true},
		{"store/gcs traz o Google", []string{
			"github.com/AreteAcademy/brevis/sdk/store/gcs"}, "cloud.google.com", true},
		{"store/s3 não traz o Google", []string{
			"github.com/AreteAcademy/brevis/sdk/store/s3"}, "cloud.google.com", false},
	}

	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			carrega := strings.Contains(deps(t, c.pacotes...), c.procura)
			if carrega != c.esperado {
				t.Errorf("carrega %q = %v, esperado %v", c.procura, carrega, c.esperado)
			}
		})
	}
}

// deps roda no módulo do sdk, que é quem declara essas dependências. Rodar
// daqui só resolveria o que o módulo examples já importa.
func deps(t *testing.T, pacotes ...string) string {
	t.Helper()
	cmd := exec.Command("go", append([]string{"list", "-deps"}, pacotes...)...)
	cmd.Dir = "../../sdk"
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	return string(out)
}
