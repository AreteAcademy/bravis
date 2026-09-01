package execution_test

import (
	"strings"
	"testing"

	"github.com/zarvhq/bravis/internal/execution"
)

func TestRenderizarSubstituiOsParams(t *testing.T) {
	out, err := execution.Renderizar(
		`dbt build --vars '{"load_full":"{{ .load_full }}"}' --select bronze_x+`,
		map[string]string{"load_full": "true"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"load_full":"true"`) {
		t.Errorf("nao substituiu: %s", out)
	}
}

// Param com erro de digitacao no YAML vira string vazia sem `missingkey=error`,
// e o comando sai silenciosamente errado: `--select ` sem alvo, `--date` sem
// data. Falhar aqui, citando o nome, e o que evita isso.
func TestParamInexistenteNoTemplateFalha(t *testing.T) {
	_, err := execution.Renderizar("echo {{ .lod_full }}", map[string]string{"load_full": "true"})
	if err == nil {
		t.Fatal("template com nome errado passou")
	}
	if !strings.Contains(err.Error(), "load_full") {
		t.Errorf("o erro nao diz o que existe: %v", err)
	}
}

func TestCondicionalDoConversor(t *testing.T) {
	cmd := `dbt build{{ if eq .full_refresh "true" }} --full-refresh{{ end }} --select x+`

	com, err := execution.Renderizar(cmd, map[string]string{"full_refresh": "true"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(com, "--full-refresh") {
		t.Errorf("a flag nao entrou: %s", com)
	}

	sem, err := execution.Renderizar(cmd, map[string]string{"full_refresh": "false"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sem, "--full-refresh") {
		t.Errorf("a flag entrou com false: %s", sem)
	}
}

// Comando sem template nao passa pelo motor: e a maioria deles, e evitar o
// parse economiza trabalho em todo passo de todo run.
func TestComandoSemTemplatePassaIntacto(t *testing.T) {
	cmd := `sh -c 'echo {oi} && ls | grep x'`
	out, err := execution.Renderizar(cmd, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out != cmd {
		t.Errorf("mexeu num comando sem template:\n%s\n%s", cmd, out)
	}
}

// Script de varias linhas tem de sair inteiro: colapsar em uma linha faria o
// primeiro `#` comentar o resto.
func TestMultiLinhaSobrevive(t *testing.T) {
	cmd := "set -e\n# comentario\npython3 -m x --date {{ .data }}\necho fim"
	out, err := execution.Renderizar(cmd, map[string]string{"data": "2026-09-01"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(out, "\n") != 3 {
		t.Errorf("perdeu quebras de linha:\n%s", out)
	}
	if !strings.HasSuffix(out, "echo fim") {
		t.Errorf("o fim do script sumiu:\n%s", out)
	}
}
