package execution

import (
	"testing"

	wf "github.com/AreteAcademy/brevis/internal/domain/workflow"
)

// TestPrecedenciaDoAmbiente: do mais fraco ao mais forte -- ambiente global do
// motor, `env:` do workflow, `env:` do passo.
//
// O passo vencer o global e a parte que mudou. Na outra ordem, uma variavel
// declarada no arquivo perderia calada para um BREVIS_TASK_ENV que alguem
// configurou meses atras -- e "perde calada" e o modo de falhar que este
// projeto mais persegue.
func TestPrecedenciaDoAmbiente(t *testing.T) {
	w := wf.Workflow{
		Slug: "x",
		Env:  map[string]string{"NIVEL": "workflow", "SO_DO_WORKFLOW": "1"},
		Nodes: []wf.Node{
			{ID: "com_env", Run: "echo", Env: map[string]string{"NIVEL": "passo"}},
			{ID: "sem_env", Run: "echo"},
		},
	}
	r := Runner{Env: map[string]string{"NIVEL": "global", "SO_DO_GLOBAL": "1"}}

	casos := map[string]string{"com_env": "passo", "sem_env": "workflow"}
	for _, n := range w.Nodes {
		env := mesclarEnv(r.Env, r.contextoDoRun(n.ID, false, 0), w.EnvDe(n))

		if got := env["NIVEL"]; got != casos[n.ID] {
			t.Errorf("%s: NIVEL = %q, esperado %q", n.ID, got, casos[n.ID])
		}
		// Herdar nao pode significar perder o resto.
		if env["SO_DO_WORKFLOW"] != "1" {
			t.Errorf("%s: perdeu a variavel que so o workflow declarou", n.ID)
		}
		if env["SO_DO_GLOBAL"] != "1" {
			t.Errorf("%s: perdeu a variavel que so o global declarou", n.ID)
		}
	}
}

// TestSegredosNaoEntramNoEnvDaTask: se entrassem, o valor teria de ser
// resolvido na montagem -- e passaria pelo dispatcher, pelo log e por qualquer
// dump de TaskExec que alguem escrever depois.
func TestSegredosNaoEntramNoEnvDaTask(t *testing.T) {
	w := wf.Workflow{
		Slug:    "x",
		Secrets: map[string]string{"TOKEN": "cofre/token"},
		Nodes:   []wf.Node{{ID: "a", Run: "echo"}},
	}
	n := w.Nodes[0]

	env := mesclarEnv(nil, nil, w.EnvDe(n))
	if _, tem := env["TOKEN"]; tem {
		t.Error("o segredo entrou no Env da task")
	}
	if w.SecretsDe(n)["TOKEN"] != "cofre/token" {
		t.Error("o segredo nao chegou como coordenada")
	}
}
