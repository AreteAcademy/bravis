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
		{"só a raiz", []string{"github.com/AreteAcademy/bravis/sdk"}, true},
		{"raiz + from", []string{
			"github.com/AreteAcademy/bravis/sdk",
			"github.com/AreteAcademy/bravis/sdk/from",
		}, true},
		// E o controle: quem pede o BigQuery recebe o BigQuery. Sem isto, o
		// teste passaria com um SDK que não carrega nada.
		{"raiz + to", []string{
			"github.com/AreteAcademy/bravis/sdk",
			"github.com/AreteAcademy/bravis/sdk/to",
		}, false},
	}

	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			out, err := exec.Command("go", append([]string{"list", "-deps"}, c.pacotes...)...).Output()
			if err != nil {
				t.Fatalf("go list: %v", err)
			}
			carrega := strings.Contains(string(out), "cloud.google.com/go/bigquery")

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
