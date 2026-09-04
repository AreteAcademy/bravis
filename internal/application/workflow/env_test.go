package workflow

import (
	"strings"
	"testing"
)

const arquivoDoGabriel = `
name: vendors_gabriel_occurrence
type: chain
env:
  BREVIS_LOG_LEVEL: info
steps:
  - id: fetch_occurrences
    image: zarv-data-pipeline-go:local
    shell: false
    run: /usr/local/bin/gabriel
    secrets:
      GABRIEL_SESSION_COOKIE: gabriel-session/cookie
    env:
      BREVIS_LOG_LEVEL: debug
  - id: dbt_build
    image: zarv-data-pipeline-dbt:local
    run: dbt build
`

// TestEnvHerdaDoWorkflowEOPassoSobrescreve: mesma regra de `image` e
// `resources`, e nome a nome -- um passo que muda o log level nao perde as
// outras variaveis do workflow.
func TestEnvHerdaDoWorkflowEOPassoSobrescreve(t *testing.T) {
	w, err := Parse("gabriel.yaml", []byte(arquivoDoGabriel))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	porID := map[string]int{}
	for i, n := range w.Nodes {
		porID[n.ID] = i
	}

	fetch := w.Nodes[porID["fetch_occurrences"]]
	if got := w.EnvDe(fetch)["BREVIS_LOG_LEVEL"]; got != "debug" {
		t.Errorf("o passo declarou debug e ficou %q", got)
	}

	dbt := w.Nodes[porID["dbt_build"]]
	if got := w.EnvDe(dbt)["BREVIS_LOG_LEVEL"]; got != "info" {
		t.Errorf("o passo sem env deveria herdar info, ficou %q", got)
	}
}

// TestSegredoSoVaiParaOPassoQueDeclarou: e a razao de existir a chave por
// passo. Com BREVIS_POD_ENV_FROM_SECRETS o cookie entrava no pod do dbt
// tambem, que nao precisa dele.
func TestSegredoSoVaiParaOPassoQueDeclarou(t *testing.T) {
	w, err := Parse("gabriel.yaml", []byte(arquivoDoGabriel))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	for _, n := range w.Nodes {
		_, tem := w.SecretsDe(n)["GABRIEL_SESSION_COOKIE"]
		if n.ID == "fetch_occurrences" && !tem {
			t.Error("o passo que declarou o segredo nao o recebeu")
		}
		if n.ID == "dbt_build" && tem {
			t.Error("o cookie vazou para o passo de dbt, que nao o declarou")
		}
	}
}

// TestSecretsRecusaOQueNaoECoordenada: o valor e onde encontrar, nunca o
// segredo. Um valor que nao e `secret/chave` quase sempre e alguem colando o
// valor de verdade no arquivo -- e o arquivo esta no git.
func TestSecretsRecusaOQueNaoECoordenada(t *testing.T) {
	casos := map[string]string{
		"sem barra":       "GABRIEL_SESSION_COOKIE: eyJhbGciOiJkaXIi==",
		"secret vazio":    "GABRIEL_SESSION_COOKIE: /cookie",
		"chave vazia":     "GABRIEL_SESSION_COOKIE: gabriel-session/",
		"barra demais":    "GABRIEL_SESSION_COOKIE: ns/gabriel-session/cookie",
		"nome invalido":   "GABRIEL-SESSION-COOKIE: gabriel-session/cookie",
		"nome com espaco": "'GABRIEL COOKIE': gabriel-session/cookie",
	}
	for nome, linha := range casos {
		t.Run(nome, func(t *testing.T) {
			yaml := "name: x\ntype: chain\nsteps:\n  - id: a\n    run: echo\n    secrets:\n      " + linha + "\n"
			if _, err := Parse("x.yaml", []byte(yaml)); err == nil {
				t.Fatalf("aceitou %q", linha)
			}
		})
	}
}

// TestEnvESecretsNaoPodemColidir: a mesma variavel com valor literal e vinda
// de segredo e ambigua, e qualquer desempate seria uma regra que ninguem
// lembra.
func TestEnvESecretsNaoPodemColidir(t *testing.T) {
	yaml := `
name: x
type: chain
env:
  TOKEN: literal
steps:
  - id: a
    run: echo
    secrets:
      TOKEN: cofre/token
`
	_, err := Parse("x.yaml", []byte(yaml))
	if err == nil {
		t.Fatal("aceitou a mesma variavel em env e em secrets")
	}
	if !strings.Contains(err.Error(), "TOKEN") {
		t.Errorf("o erro nao nomeia a variavel: %v", err)
	}
}

// TestEspacoNoNomeDaVariavelNaoPassa: `TOKEN : x` com espaco antes dos dois
// pontos e YAML valido, e o espaco iria junto no nome -- o pod sobe, o binario
// nao acha a variavel, e nada no caminho diz por que.
func TestEspacoNoNomeDaVariavelNaoPassa(t *testing.T) {
	yaml := "name: x\ntype: chain\nsteps:\n  - id: a\n    run: echo\n    env:\n      ' TOKEN ': valor\n"
	w, err := Parse("x.yaml", []byte(yaml))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if _, ok := w.EnvDe(w.Nodes[0])["TOKEN"]; !ok {
		t.Errorf("o nome nao foi aparado: %v", w.EnvDe(w.Nodes[0]))
	}
}

// TestErroDeCoordenadaNaoEcoaOSegredo: o caso mais provavel de coordenada
// invalida e alguem ter colado o segredo de verdade -- e `brevis validate`
// roda na CI, cujo log muita gente le. Foi o que a primeira versao fez.
func TestErroDeCoordenadaNaoEcoaOSegredo(t *testing.T) {
	const colado = "eyJhbGciOiJkaXIiLCJlbmMiOiJBMjU2R0NNIn0..QUJDRA=="

	yaml := "name: x\ntype: chain\nsteps:\n  - id: a\n    run: echo\n    secrets:\n      TOKEN: " + colado + "\n"
	_, err := Parse("x.yaml", []byte(yaml))
	if err == nil {
		t.Fatal("aceitou o segredo colado como coordenada")
	}
	if strings.Contains(err.Error(), colado) {
		t.Errorf("o erro imprimiu o segredo:\n%v", err)
	}
	// E continua ensinando o formato, que e o motivo do erro existir.
	if !strings.Contains(err.Error(), "nome-do-secret/chave") {
		t.Errorf("o erro deixou de ensinar o formato: %v", err)
	}
}
