package kubernetes_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/internal/execution"
	k8s "github.com/AreteAcademy/brevis/internal/execution/kubernetes"
)

func tarefa() execution.TaskExec {
	return execution.TaskExec{
		ExecutionID: "wf:passo",
		NodeID:      "bronze_workspace",
		Workflow:    "platform_workspace",
		RunID:       "1f2e3d4c-0000-0000-0000-000000000000",
		Image:       "us-central1-docker.pkg.dev/zarv/apps/dbt:1.10.3",
		Command:     "dbt build --select bronze_workspace+",
		Shell:       true,
		CPU:         "200m",
		Memoria:     "1Gi",
		MemoriaMax:  "2Gi",
		Env:         map[string]string{"STAGE": "prod", "GOOGLE_PROJECT_ID": "zarv"},
		Timeout:     30 * time.Minute,
	}
}

func TestPodTrazImagemComandoERecursos(t *testing.T) {
	p, err := k8s.MontarPod(tarefa(), k8s.Opcoes{Namespace: "dados", ServiceAccount: "brevis"})
	if err != nil {
		t.Fatal(err)
	}

	c := p.Spec.Containers[0]
	if c.Image != tarefa().Image {
		t.Errorf("imagem = %q", c.Image)
	}
	if len(c.Command) != 3 || c.Command[0] != "/bin/sh" || c.Command[1] != "-c" {
		t.Errorf("comando = %v, quero sh -c", c.Command)
	}
	if !strings.HasPrefix(c.Command[2], "dbt build") {
		t.Errorf("linha de comando = %q", c.Command[2])
	}
	if c.Resources.Requests["cpu"] != "200m" || c.Resources.Requests["memory"] != "1Gi" {
		t.Errorf("requests = %v", c.Resources.Requests)
	}
	if c.Resources.Limits["memory"] != "2Gi" {
		t.Errorf("limits = %v", c.Resources.Limits)
	}
	if _, temCPU := c.Resources.Limits["cpu"]; temCPU {
		t.Error("limite de CPU nao declarado nao deve ser inventado — throttling silencioso e pior que sem limite")
	}

	// Never: quem conta tentativas e aplica backoff e o dispatcher. Deixar o
	// kubelet reiniciar criaria uma segunda politica de retry, invisivel.
	if p.Spec.RestartPolicy != "Never" {
		t.Errorf("restartPolicy = %q", p.Spec.RestartPolicy)
	}
	if p.Spec.ServiceAccountName != "brevis" || p.Metadata.Namespace != "dados" {
		t.Errorf("identidade errada: sa=%q ns=%q", p.Spec.ServiceAccountName, p.Metadata.Namespace)
	}
	if p.Spec.ActiveDeadlineSeconds == nil || *p.Spec.ActiveDeadlineSeconds != 1800 {
		t.Errorf("deadline = %v, quero 1800s", p.Spec.ActiveDeadlineSeconds)
	}
}

// Imagem distroless nao tem shell: `sh -c` falharia com "no such file or
// directory", erro que nao diz nada sobre a causa.
func TestSemShellUsaArgvDireto(t *testing.T) {
	tk := tarefa()
	tk.Shell = false
	tk.Command = "/notify --canal dados"

	p, err := k8s.MontarPod(tk, k8s.Opcoes{})
	if err != nil {
		t.Fatal(err)
	}
	c := p.Spec.Containers[0].Command
	if len(c) != 3 || c[0] != "/notify" || c[2] != "dados" {
		t.Errorf("comando = %v, quero argv direto", c)
	}
}

func TestEnvOrdenadaEEnvFrom(t *testing.T) {
	p, err := k8s.MontarPod(tarefa(), k8s.Opcoes{
		EnvFromSecrets:    []string{"brevis-bigquery"},
		EnvFromConfigMaps: []string{"brevis-config"},
	})
	if err != nil {
		t.Fatal(err)
	}
	c := p.Spec.Containers[0]
	// Ordem estavel: dois pods com o mesmo conteudo tem de gerar o mesmo JSON,
	// senao comparar dois deploys vira ruido.
	if len(c.Env) != 2 || c.Env[0].Name != "GOOGLE_PROJECT_ID" || c.Env[1].Name != "STAGE" {
		t.Errorf("env = %v, quero ordem alfabetica", c.Env)
	}
	if len(c.EnvFrom) != 2 || c.EnvFrom[0].SecretRef.Name != "brevis-bigquery" {
		t.Errorf("envFrom = %+v", c.EnvFrom)
	}
}

// A credencial do BigQuery entra por envFrom, decisao da INSTALACAO. Um YAML de
// pipeline nao deve poder escolher a service account com que roda.
func TestOpcoesNaoVemDoWorkflow(t *testing.T) {
	p, _ := k8s.MontarPod(tarefa(), k8s.Opcoes{
		ServiceAccount: "restrita",
		PullSecrets:    []string{"registry"},
		NodeSelector:   map[string]string{"pool": "dados"},
	})
	if p.Spec.ServiceAccountName != "restrita" ||
		p.Spec.ImagePullSecrets[0].Name != "registry" ||
		p.Spec.NodeSelector["pool"] != "dados" {
		t.Errorf("opcoes da instalacao nao chegaram ao pod: %+v", p.Spec)
	}
}

func TestPodSemImagemEhRecusado(t *testing.T) {
	tk := tarefa()
	tk.Image = ""
	if _, err := k8s.MontarPod(tk, k8s.Opcoes{}); err == nil {
		t.Error("pod sem imagem nao tem o que rodar")
	}
}

// O nome precisa ser estavel para a MESMA tentativa: se o processo morrer entre
// criar o pod e registrar isso, a tentativa seguinte encontra o pod existente em
// vez de subir um segundo rodando o mesmo dbt em paralelo.
func TestNomeDoPodEhEstavelPorTentativa(t *testing.T) {
	a := k8s.NomeDoPod(tarefa())
	if b := k8s.NomeDoPod(tarefa()); a != b {
		t.Errorf("mesma tentativa gerou %q e %q", a, b)
	}

	outra := tarefa()
	outra.Tentativa = 1
	if c := k8s.NomeDoPod(outra); c == a {
		t.Error("tentativas diferentes deveriam gerar pods diferentes")
	}
}

func TestNomeDoPodObedeceOLimiteDoKubernetes(t *testing.T) {
	tk := tarefa()
	tk.Workflow = strings.Repeat("workflow-de-nome-absurdamente-longo-", 3)
	tk.NodeID = strings.Repeat("passo-tambem-enorme-", 3)

	nome := k8s.NomeDoPod(tk)
	if len(nome) > 63 {
		t.Errorf("nome com %d caracteres: %q", len(nome), nome)
	}
	for _, r := range nome {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			t.Fatalf("caractere invalido %q em %q", r, nome)
		}
	}
	if strings.HasPrefix(nome, "-") || strings.HasSuffix(nome, "-") {
		t.Errorf("nome nao pode comecar nem terminar com hifen: %q", nome)
	}

	// Dois nomes longos com o mesmo prefixo nao podem colidir depois do corte.
	outro := tk
	outro.NodeID = strings.Repeat("passo-tambem-enorme-", 3) + "-b"
	if k8s.NomeDoPod(outro) == nome {
		t.Error("o corte em 63 caracteres criou colisao entre dois passos")
	}
}

func TestRotulosPermitemAcharOsPodsDaRun(t *testing.T) {
	p, _ := k8s.MontarPod(tarefa(), k8s.Opcoes{})
	if p.Metadata.Labels["app.kubernetes.io/managed-by"] != "brevis" {
		t.Error("sem o rotulo de gestao nao da para achar os pods do Brevis")
	}
	if p.Metadata.Labels["brevis.dev/workflow"] != "platform-workspace" {
		t.Errorf("rotulo de workflow = %q (sanitizado)", p.Metadata.Labels["brevis.dev/workflow"])
	}
	// O valor original vive na anotacao: rotulo tem limite de 63 e alfabeto
	// restrito, anotacao nao.
	if p.Metadata.Annotations["brevis.dev/workflow"] != "platform_workspace" {
		t.Errorf("anotacao = %q, quero o valor original", p.Metadata.Annotations["brevis.dev/workflow"])
	}
}

// O objeto tem de ser aceito como JSON do Kubernetes: campos vazios omitidos,
// para nao enviar `resources: {}` nem `nodeSelector: null`.
func TestJSONNaoCarregaCamposVazios(t *testing.T) {
	tk := tarefa()
	tk.CPU, tk.Memoria, tk.CPUMax, tk.MemoriaMax = "", "", "", ""
	tk.Timeout = 0

	p, _ := k8s.MontarPod(tk, k8s.Opcoes{})
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, proibido := range []string{`"resources"`, `"nodeSelector"`, `"activeDeadlineSeconds"`, `"imagePullSecrets"`, `"status"`} {
		if strings.Contains(string(b), proibido) {
			t.Errorf("JSON traz %s sem valor: %s", proibido, b)
		}
	}
}
