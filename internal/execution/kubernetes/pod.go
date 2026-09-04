package kubernetes

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/AreteAcademy/brevis/internal/execution"
)

// nomeContainer e fixo: o pod tem um container so, e um nome estavel torna o
// `kubectl logs` previsivel sem consultar o spec.
const nomeContainer = "step"

// Pod e o subconjunto do objeto que este motor usa. Escrever as structs a mao,
// em vez de importar as do client-go, mantem a arvore de dependencias pequena e
// deixa visivel exatamente o que se envia ao servidor de API.
type Pod struct {
	APIVersion string   `json:"apiVersion,omitempty"`
	Kind       string   `json:"kind,omitempty"`
	Metadata   Metadata `json:"metadata"`
	Spec       PodSpec  `json:"spec,omitempty"`
	// Ponteiro porque `omitempty` nao omite struct vazia: sem ele, todo pod
	// criado enviaria `"status":{}` ao servidor — inofensivo, mas e ruido num
	// objeto que se le para depurar.
	Status *PodStatus `json:"status,omitempty"`
}

type Metadata struct {
	Name        string            `json:"name,omitempty"`
	Namespace   string            `json:"namespace,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

type PodSpec struct {
	RestartPolicy         string            `json:"restartPolicy,omitempty"`
	ServiceAccountName    string            `json:"serviceAccountName,omitempty"`
	ImagePullSecrets      []RefLocal        `json:"imagePullSecrets,omitempty"`
	NodeSelector          map[string]string `json:"nodeSelector,omitempty"`
	Tolerations           []Toleracao       `json:"tolerations,omitempty"`
	ActiveDeadlineSeconds *int64            `json:"activeDeadlineSeconds,omitempty"`
	Containers            []Container       `json:"containers"`
}

type RefLocal struct {
	Name string `json:"name"`
}

type Toleracao struct {
	Key      string `json:"key,omitempty"`
	Operator string `json:"operator,omitempty"`
	Value    string `json:"value,omitempty"`
	Effect   string `json:"effect,omitempty"`
}

type Container struct {
	Name       string     `json:"name"`
	Image      string     `json:"image"`
	Command    []string   `json:"command,omitempty"`
	Args       []string   `json:"args,omitempty"`
	Env        []Var      `json:"env,omitempty"`
	EnvFrom    []FonteEnv `json:"envFrom,omitempty"`
	Resources  *Recursos  `json:"resources,omitempty"`
	WorkingDir string     `json:"workingDir,omitempty"`
}

type Var struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type FonteEnv struct {
	SecretRef    *RefLocal `json:"secretRef,omitempty"`
	ConfigMapRef *RefLocal `json:"configMapRef,omitempty"`
}

type Recursos struct {
	Requests map[string]string `json:"requests,omitempty"`
	Limits   map[string]string `json:"limits,omitempty"`
}

type PodStatus struct {
	Phase             string            `json:"phase,omitempty"`
	Conditions        []Condicao        `json:"conditions,omitempty"`
	Reason            string            `json:"reason,omitempty"`
	Message           string            `json:"message,omitempty"`
	ContainerStatuses []StatusContainer `json:"containerStatuses,omitempty"`
}

// Condicao carrega o PodScheduled, onde o scheduler explica por que nao coube.
type Condicao struct {
	Type    string `json:"type"`
	Status  string `json:"status"`
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
}

type StatusContainer struct {
	Name  string `json:"name"`
	State struct {
		Waiting *struct {
			Reason  string `json:"reason"`
			Message string `json:"message"`
		} `json:"waiting,omitempty"`
		Running *struct {
			StartedAt string `json:"startedAt"`
		} `json:"running,omitempty"`
		Terminated *struct {
			ExitCode int    `json:"exitCode"`
			Reason   string `json:"reason"`
			Message  string `json:"message"`
		} `json:"terminated,omitempty"`
	} `json:"state"`
}

// Fase devolve a fase atual; vazia enquanto o servidor nao respondeu com status.
func (p Pod) Fase() string {
	if p.Status == nil {
		return ""
	}
	return p.Status.Phase
}

// Terminou diz se o pod chegou a um estado final.
func (p Pod) Terminou() bool {
	f := p.Fase()
	return f == "Succeeded" || f == "Failed"
}

// Saida devolve o codigo de saida do container e se ele ja terminou.
func (p Pod) Saida() (int, bool) {
	if p.Status == nil {
		return 0, false
	}
	for _, c := range p.Status.ContainerStatuses {
		if c.Name == nomeContainer && c.State.Terminated != nil {
			return c.State.Terminated.ExitCode, true
		}
	}
	return 0, false
}

// MotivoDeEspera explica por que o container ainda nao rodou.
//
// E a informacao mais util quando um passo "nao faz nada": ImagePullBackOff e
// CreateContainerConfigError sao problemas de configuracao que, sem isto,
// apareceriam apenas como um pod parado ate o timeout.
func (p Pod) MotivoDeEspera() string {
	if p.Status == nil {
		return ""
	}
	for _, c := range p.Status.ContainerStatuses {
		if c.Name == nomeContainer && c.State.Waiting != nil {
			w := c.State.Waiting
			if w.Message != "" {
				return w.Reason + ": " + w.Message
			}
			return w.Reason
		}
	}
	return ""
}

// Opcoes parametriza como os pods sao criados. Sao decisoes da INSTALACAO —
// credenciais, pool de nos, conta de servico —, nao do autor do workflow: um
// YAML de pipeline nao deve poder escolher a service account com que roda.
type Opcoes struct {
	Namespace         string
	ServiceAccount    string
	PullSecrets       []string
	NodeSelector      map[string]string
	Tolerations       []Toleracao
	EnvFromSecrets    []string
	EnvFromConfigMaps []string
	Labels            map[string]string
	Shell             []string
	// EsperaParaIniciar e quanto um pod pode ficar sem comecar antes de o passo
	// desistir. Existe porque `Pending` nao e erro para o Kubernetes: um pod que
	// nao cabe em no nenhum fica ali para sempre, e sem este limite a etapa
	// espera junto — sem log, sem falha, sem retry. Aconteceu em dev com um
	// request de CPU maior que o livre no pool.
	EsperaParaIniciar time.Duration

	// ManterPodEmFalha deixa o pod para inspecao quando o passo falha. O de
	// sucesso e sempre apagado: milhares de pods Completed poluem o namespace e
	// nao dizem nada que o historico do Brevis nao diga melhor.
	ManterPodEmFalha bool
}

func (o Opcoes) comPadroes() Opcoes {
	if len(o.Shell) == 0 {
		o.Shell = []string{"/bin/sh", "-c"}
	}
	if o.Namespace == "" {
		o.Namespace = "default"
	}
	if o.EsperaParaIniciar <= 0 {
		// Dez minutos cobrem pull de imagem grande (a de dbt tem 620 MB) e um
		// scale-up do autoscaler, sem deixar uma etapa presa a madrugada toda.
		o.EsperaParaIniciar = 10 * time.Minute
	}
	return o
}

// MontarPod traduz uma task no objeto que vai para o servidor de API.
//
// Funcao pura: recebe task e opcoes, devolve o objeto. E o que permite testar o
// spec inteiro — imagem, comando, recursos, rotulos — sem cluster nenhum.
func MontarPod(t execution.TaskExec, o Opcoes) (Pod, error) {
	o = o.comPadroes()
	if t.Image == "" {
		return Pod{}, fmt.Errorf("step %q sem imagem: em Kubernetes cada passo e um pod, e o pod precisa saber o que rodar", t.NodeID)
	}
	if t.Command == "" {
		return Pod{}, fmt.Errorf("step %q sem comando", t.NodeID)
	}

	c := Container{
		Name:       nomeContainer,
		Image:      t.Image,
		WorkingDir: t.WorkDir,
	}
	if t.Shell {
		c.Command = append(append([]string{}, o.Shell...), t.Command)
	} else {
		// Sem shell o comando e um argv. Divisao por espaco e simples de
		// proposito: quem precisa de aspas, pipe ou variavel precisa de shell,
		// e nesse caso `shell: false` e a escolha errada.
		c.Command = strings.Fields(t.Command)
	}

	// Ambiente ordenado: dois pods com o mesmo conteudo tem de gerar o mesmo
	// JSON, senao a comparacao entre dois deploys vira ruido.
	chaves := make([]string, 0, len(t.Env))
	for k := range t.Env {
		chaves = append(chaves, k)
	}
	sort.Strings(chaves)
	for _, k := range chaves {
		c.Env = append(c.Env, Var{Name: k, Value: t.Env[k]})
	}
	for _, s := range o.EnvFromSecrets {
		c.EnvFrom = append(c.EnvFrom, FonteEnv{SecretRef: &RefLocal{Name: s}})
	}
	for _, m := range o.EnvFromConfigMaps {
		c.EnvFrom = append(c.EnvFrom, FonteEnv{ConfigMapRef: &RefLocal{Name: m}})
	}
	if r := recursos(t); r != nil {
		c.Resources = r
	}

	spec := PodSpec{
		// Never: quem decide sobre nova tentativa e o dispatcher, que conta
		// tentativas e aplica backoff. Deixar o kubelet reiniciar por conta
		// criaria uma segunda politica de retry, invisivel para o historico.
		RestartPolicy:      "Never",
		ServiceAccountName: o.ServiceAccount,
		NodeSelector:       o.NodeSelector,
		Tolerations:        o.Tolerations,
		Containers:         []Container{c},
	}
	for _, s := range o.PullSecrets {
		spec.ImagePullSecrets = append(spec.ImagePullSecrets, RefLocal{Name: s})
	}
	if t.Timeout > 0 {
		// Rede de seguranca do lado do cluster: se o processo do Brevis morrer,
		// o pod ainda para sozinho em vez de rodar para sempre.
		segundos := int64(t.Timeout.Seconds())
		spec.ActiveDeadlineSeconds = &segundos
	}

	rotulos := map[string]string{
		"app.kubernetes.io/managed-by": "brevis",
		"brevis.dev/node":              valorDeRotulo(t.NodeID),
	}
	if t.RunID != "" {
		rotulos["brevis.dev/run"] = valorDeRotulo(t.RunID)
	}
	if t.Workflow != "" {
		rotulos["brevis.dev/workflow"] = valorDeRotulo(t.Workflow)
	}
	for k, v := range o.Labels {
		rotulos[k] = v
	}

	return Pod{
		APIVersion: "v1",
		Kind:       "Pod",
		Metadata: Metadata{
			Name:      NomeDoPod(t),
			Namespace: o.Namespace,
			Labels:    rotulos,
			// A anotacao guarda o valor INTEIRO; o rotulo guarda a versao
			// sanitizada. Assim o filtro por rotulo funciona e o valor original
			// nao se perde.
			Annotations: map[string]string{
				"brevis.dev/workflow": t.Workflow,
				"brevis.dev/node":     t.NodeID,
				"brevis.dev/run":      t.RunID,
			},
		},
		Spec: spec,
	}, nil
}

func recursos(t execution.TaskExec) *Recursos {
	r := &Recursos{Requests: map[string]string{}, Limits: map[string]string{}}
	if t.CPU != "" {
		r.Requests["cpu"] = t.CPU
	}
	if t.Memoria != "" {
		r.Requests["memory"] = t.Memoria
	}
	if t.CPUMax != "" {
		r.Limits["cpu"] = t.CPUMax
	}
	if t.MemoriaMax != "" {
		r.Limits["memory"] = t.MemoriaMax
	}
	if len(r.Requests) == 0 {
		r.Requests = nil
	}
	if len(r.Limits) == 0 {
		r.Limits = nil
	}
	if r.Requests == nil && r.Limits == nil {
		return nil
	}
	return r
}

var invalidoEmNome = regexp.MustCompile(`[^a-z0-9-]+`)

// NomeDoPod produz um nome valido e ESTAVEL para a mesma tentativa.
//
// Estavel importa: se o processo morrer entre criar o pod e registrar isso, a
// tentativa seguinte encontra o pod existente (409 AlreadyExists) em vez de
// subir um segundo pod rodando o mesmo dbt em paralelo com o primeiro.
//
// O sufixo de hash resolve a colisao que o corte de 63 caracteres criaria entre
// dois nodes de nome longo e prefixo comum.
func NomeDoPod(t execution.TaskExec) string {
	base := sanitizar(t.Workflow + "-" + t.NodeID)
	soma := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%d|%d",
		t.RunID, t.NodeID, t.TentativaDoRun, t.Tentativa)))
	sufixo := hex.EncodeToString(soma[:4])

	const maxNome = 63
	if len(base)+1+len(sufixo) > maxNome {
		base = base[:maxNome-1-len(sufixo)]
		base = strings.TrimRight(base, "-")
	}
	return base + "-" + sufixo
}

func sanitizar(s string) string {
	s = strings.ToLower(s)
	s = invalidoEmNome.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		s = "step"
	}
	return s
}

// valorDeRotulo obedece o limite de 63 caracteres dos labels; o valor completo
// vai na anotacao, que aceita bem mais.
func valorDeRotulo(s string) string {
	s = sanitizar(s)
	if len(s) > 63 {
		s = strings.TrimRight(s[:63], "-")
	}
	return s
}

// net junta host e porta cuidando de IPv6, onde o host vem sem colchetes.
func net_(host, porta string) string { return net.JoinHostPort(host, porta) }

type tlsConfig struct{ pool *x509.CertPool }

func (t tlsConfig) build() *tls.Config {
	return &tls.Config{RootCAs: t.pool, MinVersion: tls.VersionTLS12}
}

// Motivo e o `reason` do status (DeadlineExceeded, OOMKilled, Evicted) — a
// diferenca entre "o codigo falhou" e "o cluster matou o processo".
func (p Pod) Motivo() string {
	if p.Status == nil {
		return ""
	}
	return p.Status.Reason
}
