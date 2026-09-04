package execution_test

import (
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/internal/execution"
)

// Os comandos que o conversor gera precisam RENDERIZAR com os padroes reais.
// Um template que so falha na execucao custa uma madrugada.
func TestComandosConvertidosRenderizam(t *testing.T) {
	casos := []struct {
		nome      string
		comando   string
		params    map[string]string
		contem    string
		naoContem string
	}{
		{
			nome:      "lawsuit: dry_run=false NAO passa a flag",
			comando:   `main.py {{ if .limit }} --limit {{ .limit }}{{ end }} {{ if eq .dry_run "true" }} --dry-run{{ end }} --rpm {{ .rpm }}`,
			params:    map[string]string{"limit": "1000", "dry_run": "false", "rpm": "60"},
			contem:    "--limit 1000",
			naoContem: "--dry-run",
		},
		{
			nome:    "lawsuit: dry_run=true passa a flag",
			comando: `main.py {{ if eq .dry_run "true" }} --dry-run{{ end }}`,
			params:  map[string]string{"dry_run": "true"},
			contem:  "--dry-run",
		},
		{
			nome:    "agents: or booleano com os dois falsos",
			comando: `--vars '{"load_full":"{{ if or (eq .full_refresh "true") (eq .load_full "true") }}true{{ else }}false{{ end }}"}'`,
			params:  map[string]string{"full_refresh": "false", "load_full": "false"},
			contem:  `"load_full":"false"`,
		},
		{
			nome:    "agents: or booleano com um verdadeiro",
			comando: `--vars '{"load_full":"{{ if or (eq .full_refresh "true") (eq .load_full "true") }}true{{ else }}false{{ end }}"}'`,
			params:  map[string]string{"full_refresh": "false", "load_full": "true"},
			contem:  `"load_full":"true"`,
		},
	}

	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			got, err := execution.Renderizar(c.comando, c.params)
			if err != nil {
				t.Fatalf("nao renderizou: %v", err)
			}
			if !strings.Contains(got, c.contem) {
				t.Errorf("faltou %q em: %s", c.contem, got)
			}
			if c.naoContem != "" && strings.Contains(got, c.naoContem) {
				t.Errorf("nao deveria conter %q: %s", c.naoContem, got)
			}
		})
	}
}
