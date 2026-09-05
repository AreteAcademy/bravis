package kubernetes

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/internal/execution"
)

func tarefaSimples() execution.TaskExec {
	return execution.TaskExec{
		NodeID:  "fetch_occurrences",
		Image:   "zarv-data-pipeline-go:local",
		Command: "/usr/local/bin/gabriel",
	}
}

// TestVolumeDaCredencialMontaEInjetaADiretorio: com o PVC configurado, todo pod
// de passo ganha o volume e a env que o SDK le. E o passo 5 da spec do volume.
func TestVolumeDaCredencialMontaEInjetaADiretorio(t *testing.T) {
	pod, err := MontarPod(tarefaSimples(), Opcoes{
		CredencialPVC: "brevis-credentials",
	}.comPadroes())
	if err != nil {
		t.Fatalf("MontarPod: %v", err)
	}

	if len(pod.Spec.Volumes) != 1 {
		t.Fatalf("volumes = %d, esperado 1", len(pod.Spec.Volumes))
	}
	v := pod.Spec.Volumes[0]
	if v.PVC == nil || v.PVC.ClaimName != "brevis-credentials" {
		t.Errorf("PVC errado: %+v", v)
	}

	c := pod.Spec.Containers[0]
	if len(c.VolumeMounts) != 1 || c.VolumeMounts[0].Name != v.Name {
		t.Fatalf("o mount nao aponta para o volume: %+v", c.VolumeMounts)
	}
	if got := c.VolumeMounts[0].MountPath; got != "/var/brevis/credentials" {
		t.Errorf("mountPath = %q, esperado o padrao", got)
	}

	var dir string
	for _, e := range c.Env {
		if e.Name == "BREVIS_CREDENTIAL_DIR" {
			dir = e.Value
		}
	}
	if dir != c.VolumeMounts[0].MountPath {
		t.Errorf("BREVIS_CREDENTIAL_DIR = %q, e o mount esta em %q", dir, c.VolumeMounts[0].MountPath)
	}
}

// TestCaminhoDoVolumeEConfiguravel.
func TestCaminhoDoVolumeEConfiguravel(t *testing.T) {
	pod, err := MontarPod(tarefaSimples(), Opcoes{
		CredencialPVC:  "meu-pvc",
		CredencialPath: "/mnt/cred",
	}.comPadroes())
	if err != nil {
		t.Fatalf("MontarPod: %v", err)
	}
	if got := pod.Spec.Containers[0].VolumeMounts[0].MountPath; got != "/mnt/cred" {
		t.Errorf("mountPath = %q", got)
	}
}

// TestSemPVCNadaMuda: e assim que a feature continua sendo atalho e nao
// requisito -- uma instalacao que nao a configurou nao pode ver diferenca.
func TestSemPVCNadaMuda(t *testing.T) {
	pod, err := MontarPod(tarefaSimples(), Opcoes{}.comPadroes())
	if err != nil {
		t.Fatalf("MontarPod: %v", err)
	}
	if len(pod.Spec.Volumes) != 0 {
		t.Errorf("montou volume sem PVC: %+v", pod.Spec.Volumes)
	}
	if len(pod.Spec.Containers[0].VolumeMounts) != 0 {
		t.Errorf("montou mount sem PVC")
	}
	for _, e := range pod.Spec.Containers[0].Env {
		if e.Name == "BREVIS_CREDENTIAL_DIR" {
			t.Errorf("injetou a env sem volume nenhum: o SDK tentaria escrever num diretorio que nao existe")
		}
	}
	b, _ := json.Marshal(pod)
	if strings.Contains(string(b), "volumes") {
		t.Errorf("o JSON carrega volumes vazio:\n%s", b)
	}
}

// TestEnvDoPassoVenceODiretorioPadrao: um passo que declara o proprio
// BREVIS_CREDENTIAL_DIR sabe o que esta fazendo, e a injecao nao pode duplicar
// a variavel -- dois valores para o mesmo nome e o servidor escolhendo um.
func TestEnvDoPassoVenceODiretorioPadrao(t *testing.T) {
	tarefa := tarefaSimples()
	tarefa.Env = map[string]string{"BREVIS_CREDENTIAL_DIR": "/outro/lugar"}

	pod, err := MontarPod(tarefa, Opcoes{CredencialPVC: "pvc"}.comPadroes())
	if err != nil {
		t.Fatalf("MontarPod: %v", err)
	}
	var vistos []string
	for _, e := range pod.Spec.Containers[0].Env {
		if e.Name == "BREVIS_CREDENTIAL_DIR" {
			vistos = append(vistos, e.Value)
		}
	}
	if len(vistos) != 1 {
		t.Fatalf("BREVIS_CREDENTIAL_DIR aparece %d vezes: %v", len(vistos), vistos)
	}
	if vistos[0] != "/outro/lugar" {
		t.Errorf("a injecao sobrescreveu o que o passo declarou: %q", vistos[0])
	}
}
