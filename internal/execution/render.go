package execution

import (
	"fmt"
	"sort"
	"strings"
	"text/template"
)

// Renderizar substitui os params na linha de comando de um passo.
//
// `text/template` da stdlib, com `missingkey=error`: um param com erro de
// digitacao no YAML falha AQUI, com o nome do que faltou, em vez de virar string
// vazia e produzir um comando silenciosamente errado — `--select ` sem alvo, ou
// `--date` sem data.
//
// So o comando e renderizado. `image:` NAO e templatavel de proposito: quem
// dispara um run escolheria a imagem que o pod roda, o que e escolher o codigo
// que executa.
func Renderizar(comando string, params map[string]string) (string, error) {
	if !strings.Contains(comando, "{{") {
		return comando, nil
	}
	t, err := template.New("passo").Option("missingkey=error").Parse(comando)
	if err != nil {
		return "", fmt.Errorf("comando com template invalido: %w", err)
	}

	var saida strings.Builder
	if err := t.Execute(&saida, params); err != nil {
		return "", fmt.Errorf("%w (params disponiveis: %s)", err, chaves(params))
	}
	return saida.String(), nil
}

func chaves(m map[string]string) string {
	if len(m) == 0 {
		return "nenhum"
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}
