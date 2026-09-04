package kubernetes

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/internal/execution"
)

// opcoesComLiberacao e o que uma instalacao que autorizou este Secret passa.
func opcoesComLiberacao() Opcoes {
	return Opcoes{SecretsPermitidos: []string{"gabriel-session", "cofre"}}.comPadroes()
}

func tarefaComSegredo() execution.TaskExec {
	return execution.TaskExec{
		NodeID:  "fetch_occurrences",
		Image:   "zarv-data-pipeline-go:local",
		Command: "/usr/local/bin/gabriel",
		Env:     map[string]string{"BREVIS_LOG_LEVEL": "info"},
		Secrets: map[string]string{"GABRIEL_SESSION_COOKIE": "gabriel-session/cookie"},
	}
}

// TestSegredoViraSecretKeyRef: o pod referencia a chave, o kubelet resolve. O
// motor nunca ve o valor.
func TestSegredoViraSecretKeyRef(t *testing.T) {
	pod, err := MontarPod(tarefaComSegredo(), opcoesComLiberacao())
	if err != nil {
		t.Fatalf("MontarPod: %v", err)
	}

	var achou *Var
	for i, v := range pod.Spec.Containers[0].Env {
		if v.Name == "GABRIEL_SESSION_COOKIE" {
			achou = &pod.Spec.Containers[0].Env[i]
		}
	}
	if achou == nil {
		t.Fatal("a variavel do segredo nao entrou no container")
	}
	if achou.Value != "" {
		t.Errorf("o valor foi materializado no pod: %q", achou.Value)
	}
	if achou.ValueFrom == nil || achou.ValueFrom.SecretKeyRef == nil {
		t.Fatal("sem valueFrom.secretKeyRef")
	}
	if got := achou.ValueFrom.SecretKeyRef; got.Name != "gabriel-session" || got.Key != "cookie" {
		t.Errorf("coordenada errada: %+v", got)
	}
}

// TestJSONDoPodNaoManda value VAZIO junto de valueFrom: o servidor recusa as
// duas chaves na mesma variavel, e a recusa fala de campo de container, nao de
// linha de YAML.
func TestJSONDoPodNaoMandaValueVazioComValueFrom(t *testing.T) {
	pod, err := MontarPod(tarefaComSegredo(), opcoesComLiberacao())
	if err != nil {
		t.Fatalf("MontarPod: %v", err)
	}
	b, err := json.Marshal(pod)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), `"value":""`) {
		t.Errorf("o JSON manda value vazio junto de valueFrom:\n%s", b)
	}
}

// TestOSegredoNaoVazaNoJSONDoPod: o manifesto vai para o servidor, aparece em
// `kubectl get pod -o yaml` e costuma acabar em log de deploy.
func TestOSegredoNaoVazaNoJSONDoPod(t *testing.T) {
	tarefa := tarefaComSegredo()
	tarefa.Secrets["GABRIEL_SESSION_COOKIE"] = "gabriel-session/cookie"

	pod, err := MontarPod(tarefa, opcoesComLiberacao())
	if err != nil {
		t.Fatalf("MontarPod: %v", err)
	}
	b, _ := json.Marshal(pod)
	// A coordenada pode aparecer; o que nao pode e um valor de segredo, e o
	// motor nao tem nenhum para vazar -- este teste fixa isso.
	if strings.Contains(string(b), "gabriel-session/cookie") {
		t.Errorf("a coordenada crua foi para o manifesto em vez do secretKeyRef:\n%s", b)
	}
}

// TestAmbienteDoPodEDeterministico: dois pods iguais tem de gerar o mesmo
// JSON, senao o diff entre dois deploys vira ruido de ordem de mapa.
func TestAmbienteDoPodEDeterministico(t *testing.T) {
	tarefa := tarefaComSegredo()
	tarefa.Env["A"] = "1"
	tarefa.Env["Z"] = "2"
	tarefa.Secrets["OUTRO"] = "cofre/chave"

	var primeiro string
	for i := 0; i < 20; i++ {
		pod, err := MontarPod(tarefa, opcoesComLiberacao())
		if err != nil {
			t.Fatalf("MontarPod: %v", err)
		}
		b, _ := json.Marshal(pod.Spec.Containers[0].Env)
		if i == 0 {
			primeiro = string(b)
			continue
		}
		if string(b) != primeiro {
			t.Fatalf("ordem instavel:\n%s\n%s", primeiro, b)
		}
	}
}

// TestYAMLNaoEscolheQualSecretMontar: `secrets:` inverte quem escolhe --
// EnvFromSecrets vem do ambiente do scheduler, `secrets:` vem do arquivo, e o
// arquivo e escrito por outra pessoa. Sem a lista, um workflow montaria
// qualquer Secret do namespace, inclusive o do banco do proprio Brevis, e
// rodaria um comando arbitrario com ele em maos.
func TestYAMLNaoEscolheQualSecretMontar(t *testing.T) {
	tarefa := tarefaComSegredo()
	tarefa.Secrets["ROUBADO"] = "brevis-database/url"

	_, err := MontarPod(tarefa, opcoesComLiberacao())
	if err == nil {
		t.Fatal("o YAML montou um Secret que a instalacao nao liberou")
	}
	for _, exigido := range []string{"brevis-database", "BREVIS_POD_ALLOWED_SECRETS"} {
		if !strings.Contains(err.Error(), exigido) {
			t.Errorf("o erro nao diz %q: %v", exigido, err)
		}
	}
}

// TestSemListaNenhumSecretPassa: negar por padrao custa uma variavel na
// instalacao; permitir por padrao custa o inverso, e o inverso e irreversivel.
func TestSemListaNenhumSecretPassa(t *testing.T) {
	_, err := MontarPod(tarefaComSegredo(), Opcoes{}.comPadroes())
	if err == nil {
		t.Fatal("sem lista de liberados, o Secret passou")
	}
	if !strings.Contains(err.Error(), "nenhum esta") {
		t.Errorf("o erro nao explica que a lista esta vazia: %v", err)
	}
}

// TestSemSecretsNoYAMLNadaMuda: um workflow que nao usa `secrets:` nao pode
// passar a exigir configuracao nova.
func TestSemSecretsNoYAMLNadaMuda(t *testing.T) {
	tarefa := tarefaComSegredo()
	tarefa.Secrets = nil

	if _, err := MontarPod(tarefa, Opcoes{}.comPadroes()); err != nil {
		t.Errorf("workflow sem secrets passou a falhar: %v", err)
	}
}
