package local

import (
	"os"
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/internal/execution"
)

// TestSegredoLocalVemDoAmbienteDoMotor: em Kubernetes a coordenada aponta um
// Secret; no local nao ha Secret nenhum, entao o motor le a variavel de mesmo
// nome do proprio ambiente.
func TestSegredoLocalVemDoAmbienteDoMotor(t *testing.T) {
	t.Setenv("GABRIEL_SESSION_COOKIE", "session=abc==")

	env, err := ambienteDaTask(execution.TaskExec{
		NodeID:  "fetch_occurrences",
		Env:     map[string]string{"BREVIS_LOG_LEVEL": "info"},
		Secrets: map[string]string{"GABRIEL_SESSION_COOKIE": "gabriel-session/cookie"},
	})
	if err != nil {
		t.Fatalf("ambienteDaTask: %v", err)
	}
	if !contem(env, "GABRIEL_SESSION_COOKIE=session=abc==") {
		t.Errorf("o segredo nao chegou ao processo: %v", env)
	}
	if !contem(env, "BREVIS_LOG_LEVEL=info") {
		t.Errorf("o env literal sumiu: %v", env)
	}
}

// TestSegredoAusenteFalhaAntesDeRodar: string vazia viraria um cookie vazio e
// um 401 la na frente, culpando a API por uma variavel que ninguem exportou.
func TestSegredoAusenteFalhaAntesDeRodar(t *testing.T) {
	casos := map[string]func(*testing.T){
		// Setenv registra o cleanup; Unsetenv logo depois deixa a variavel
		// realmente ausente e o valor original volta no fim do teste.
		"nao definida": func(t *testing.T) {
			t.Setenv("GABRIEL_SESSION_COOKIE", "x")
			if err := os.Unsetenv("GABRIEL_SESSION_COOKIE"); err != nil {
				t.Fatal(err)
			}
		},
		"vazia": func(t *testing.T) { t.Setenv("GABRIEL_SESSION_COOKIE", "") },
	}
	for nome, preparar := range casos {
		t.Run(nome, func(t *testing.T) {
			preparar(t)

			_, err := ambienteDaTask(execution.TaskExec{
				NodeID:  "fetch_occurrences",
				Secrets: map[string]string{"GABRIEL_SESSION_COOKIE": "gabriel-session/cookie"},
			})
			if err == nil {
				t.Fatal("segredo ausente passou")
			}
			for _, exigido := range []string{"GABRIEL_SESSION_COOKIE", "fetch_occurrences", "gabriel-session/cookie"} {
				if !strings.Contains(err.Error(), exigido) {
					t.Errorf("o erro nao diz %q: %v", exigido, err)
				}
			}
		})
	}
}

// TestTaskNaoHerdaOAmbienteDoMotorPorAcidente: a regra que ja existia. O
// `secrets:` e o opt-in nominal contra ela, e nao um portao aberto.
func TestTaskNaoHerdaOAmbienteDoMotorPorAcidente(t *testing.T) {
	t.Setenv("SEGREDO_DO_ORQUESTRADOR", "nao deveria vazar")

	env, err := ambienteDaTask(execution.TaskExec{NodeID: "a", Env: map[string]string{"OK": "1"}})
	if err != nil {
		t.Fatalf("ambienteDaTask: %v", err)
	}
	for _, kv := range env {
		if strings.HasPrefix(kv, "SEGREDO_DO_ORQUESTRADOR=") {
			t.Errorf("a task herdou o ambiente do motor: %v", env)
		}
	}
}

// TestSegredoSobrescreveOLiteral: se os dois existirem na mesma task, o
// segredo vence -- mas o dominio ja recusa a colisao, entao isto so fixa que a
// ordem nao e acidental.
func TestSegredoSobrescreveOLiteral(t *testing.T) {
	t.Setenv("TOKEN", "do-ambiente")

	env, err := ambienteDaTask(execution.TaskExec{
		NodeID:  "a",
		Env:     map[string]string{"TOKEN": "literal"},
		Secrets: map[string]string{"TOKEN": "cofre/token"},
	})
	if err != nil {
		t.Fatalf("ambienteDaTask: %v", err)
	}
	if !contem(env, "TOKEN=do-ambiente") {
		t.Errorf("o literal venceu o segredo: %v", env)
	}
}

func contem(env []string, procurado string) bool {
	for _, kv := range env {
		if kv == procurado {
			return true
		}
	}
	return false
}
