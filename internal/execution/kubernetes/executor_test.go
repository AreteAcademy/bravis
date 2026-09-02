package kubernetes_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AreteAcademy/bravis/internal/execution"
	k8s "github.com/AreteAcademy/bravis/internal/execution/kubernetes"
)

// apiFalsa simula o servidor de API: uma sequencia de fases, o log e o registro
// do que foi pedido. Testar o ciclo inteiro sem cluster e o que torna este
// caminho verificavel na CI.
type apiFalsa struct {
	mu sync.Mutex

	fases     []k8s.Pod // devolvidas em ordem, a ultima repete
	lidas     int
	log       string
	erroLog   error
	erroCriar error

	criados  []k8s.Pod
	apagados []string
}

func (a *apiFalsa) CriarPod(_ context.Context, p k8s.Pod) (k8s.Pod, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.erroCriar != nil {
		return k8s.Pod{}, a.erroCriar
	}
	a.criados = append(a.criados, p)
	return p, nil
}

func (a *apiFalsa) LerPod(_ context.Context, _ string) (k8s.Pod, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	i := a.lidas
	if i >= len(a.fases) {
		i = len(a.fases) - 1
	}
	a.lidas++
	return a.fases[i], nil
}

func (a *apiFalsa) Logs(_ context.Context, _ string, _ bool) (io.ReadCloser, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.erroLog != nil {
		return nil, a.erroLog
	}
	return io.NopCloser(strings.NewReader(a.log)), nil
}

func (a *apiFalsa) ApagarPod(_ context.Context, nome string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.apagados = append(a.apagados, nome)
	return nil
}

func fase(f string) k8s.Pod {
	return k8s.Pod{
		Metadata: k8s.Metadata{Name: "p"},
		Status:   &k8s.PodStatus{Phase: f},
	}
}

func comSaida(f string, codigo int) k8s.Pod {
	p := fase(f)
	var st k8s.StatusContainer
	st.Name = "step"
	st.State.Terminated = &struct {
		ExitCode int    `json:"exitCode"`
		Reason   string `json:"reason"`
		Message  string `json:"message"`
	}{ExitCode: codigo}
	p.Status.ContainerStatuses = []k8s.StatusContainer{st}
	return p
}

func rodar(t *testing.T, api *apiFalsa, tk execution.TaskExec) []execution.Event {
	t.Helper()
	e := k8s.NewExecutor(api, k8s.Opcoes{Namespace: "dados"})
	e.Intervalo = time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ch, err := e.Execute(ctx, tk)
	if err != nil {
		t.Fatal(err)
	}
	var out []execution.Event
	for ev := range ch {
		out = append(out, ev)
	}
	return out
}

func TestPodBemSucedidoReportaLogEApaga(t *testing.T) {
	api := &apiFalsa{
		fases: []k8s.Pod{fase("Pending"), fase("Running"), comSaida("Succeeded", 0)},
		log:   "Running with dbt=1.10.3\nCompleted successfully\n",
	}
	eventos := rodar(t, api, tarefa())

	var sucesso bool
	var linhas []string
	for _, e := range eventos {
		switch e.Kind {
		case execution.EventSucceeded:
			sucesso = true
		case execution.EventLog:
			linhas = append(linhas, e.Message)
		}
	}
	if !sucesso {
		t.Fatalf("sem evento de sucesso: %+v", eventos)
	}
	if len(linhas) == 0 || !strings.Contains(strings.Join(linhas, "\n"), "Completed successfully") {
		t.Errorf("log do pod nao chegou: %v", linhas)
	}
	if len(api.apagados) != 1 {
		t.Errorf("pod de sucesso tem de ser apagado (apagados=%v)", api.apagados)
	}
	if len(api.criados) != 1 || api.criados[0].Spec.Containers[0].Image != tarefa().Image {
		t.Errorf("pod criado errado: %+v", api.criados)
	}
}

func TestPodQueFalhaCarregaOCodigoDeSaida(t *testing.T) {
	api := &apiFalsa{
		fases: []k8s.Pod{fase("Running"), comSaida("Failed", 2)},
		log:   "Database Error in model x\n",
	}
	eventos := rodar(t, api, tarefa())

	var falha *execution.Event
	for i := range eventos {
		if eventos[i].Kind == execution.EventFailed {
			falha = &eventos[i]
		}
	}
	if falha == nil {
		t.Fatalf("sem evento de falha: %+v", eventos)
	}
	if falha.ExitCode != 2 {
		t.Errorf("exit code = %d, quero 2", falha.ExitCode)
	}
	if !strings.Contains(falha.Message, "codigo 2") {
		t.Errorf("mensagem = %q", falha.Message)
	}
}

// O motivo do Kubernetes distingue "o codigo falhou" de "o cluster matou o
// processo" — OOMKilled e DeadlineExceeded exigem acoes opostas.
func TestMotivoDoClusterApareceNaFalha(t *testing.T) {
	morto := comSaida("Failed", 137)
	morto.Status.Reason = "DeadlineExceeded"
	api := &apiFalsa{fases: []k8s.Pod{fase("Running"), morto}}

	for _, e := range rodar(t, api, tarefa()) {
		if e.Kind == execution.EventFailed {
			if !strings.Contains(e.Message, "DeadlineExceeded") {
				t.Errorf("mensagem sem o motivo do cluster: %q", e.Message)
			}
			return
		}
	}
	t.Fatal("sem evento de falha")
}

// Pod parado em ImagePullBackOff nao produz log nenhum: sem reportar o motivo, o
// passo pareceria travado ate o timeout, sem uma linha explicando.
func TestMotivoDeEsperaEhReportado(t *testing.T) {
	preso := fase("Pending")
	var st k8s.StatusContainer
	st.Name = "step"
	st.State.Waiting = &struct {
		Reason  string `json:"reason"`
		Message string `json:"message"`
	}{Reason: "ImagePullBackOff", Message: "Back-off pulling image"}
	preso.Status.ContainerStatuses = []k8s.StatusContainer{st}

	api := &apiFalsa{fases: []k8s.Pod{preso, preso, comSaida("Failed", 1)}}

	var achou bool
	for _, e := range rodar(t, api, tarefa()) {
		if e.Kind == execution.EventLog && strings.Contains(e.Message, "ImagePullBackOff") {
			achou = true
		}
	}
	if !achou {
		t.Error("o motivo da espera nao foi reportado — o passo pareceria travado sem explicacao")
	}
}

// Nome deterministico: um pod que ja existe foi criado por uma execucao que
// morreu antes de registrar. Adotar evita subir um segundo rodando o mesmo dbt.
func TestPodJaExistenteEhAdotado(t *testing.T) {
	api := &apiFalsa{
		erroCriar: errors.New(`pods "x" already exists`),
		fases:     []k8s.Pod{comSaida("Succeeded", 0)},
	}
	eventos := rodar(t, api, tarefa())
	for _, e := range eventos {
		if e.Kind == execution.EventSucceeded {
			return
		}
	}
	t.Fatalf("pod existente deveria ser acompanhado, nao recusado: %+v", eventos)
}

// Falha ao criar (RBAC, quota, imagem invalida) tem de subir como erro do passo,
// nao virar um pod fantasma que ninguem acompanha.
func TestErroAoCriarSobeParaOChamador(t *testing.T) {
	api := &apiFalsa{erroCriar: errors.New(`pods is forbidden: cannot create resource "pods"`)}
	e := k8s.NewExecutor(api, k8s.Opcoes{})
	if _, err := e.Execute(context.Background(), tarefa()); err == nil {
		t.Fatal("esperava erro")
	} else if !strings.Contains(err.Error(), "forbidden") {
		t.Errorf("erro perdeu a causa: %v", err)
	}
}

func TestPodEmFalhaPodeSerMantidoParaInspecao(t *testing.T) {
	api := &apiFalsa{fases: []k8s.Pod{comSaida("Failed", 1)}}
	e := k8s.NewExecutor(api, k8s.Opcoes{ManterPodEmFalha: true})
	e.Intervalo = time.Millisecond

	ch, err := e.Execute(context.Background(), tarefa())
	if err != nil {
		t.Fatal(err)
	}
	for range ch {
	}
	if len(api.apagados) != 0 {
		t.Errorf("pod em falha foi apagado apesar da opcao: %v", api.apagados)
	}
}

// `Pending` nao e erro para o Kubernetes: um pod que nao cabe em no nenhum fica
// ali para sempre. Sem um corte, a etapa espera junto — sem falha e sem retry —,
// que foi como um request de CPU maior que o livre no pool travou uma run
// inteira em dev.
func TestPodQueNaoComecaFalhaComOMotivoDoScheduler(t *testing.T) {
	preso := fase("Pending")
	preso.Status.Conditions = []k8s.Condicao{{
		Type: "PodScheduled", Status: "False", Reason: "Unschedulable",
		Message: "0/8 nodes are available: 6 Insufficient cpu.",
	}}
	api := &apiFalsa{fases: []k8s.Pod{preso}}

	e := k8s.NewExecutor(api, k8s.Opcoes{EsperaParaIniciar: 30 * time.Millisecond})
	e.Intervalo = time.Millisecond

	ch, err := e.Execute(context.Background(), tarefa())
	if err != nil {
		t.Fatal(err)
	}
	var falha *execution.Event
	for ev := range ch {
		if ev.Kind == execution.EventFailed {
			e := ev
			falha = &e
		}
	}
	if falha == nil {
		t.Fatal("o passo nunca falhou — ficaria preso para sempre")
	}
	if !strings.Contains(falha.Message, "Insufficient cpu") {
		t.Errorf("a mensagem nao diz por que nao agendou: %q", falha.Message)
	}
}

// O nome do pod distingue a tentativa DO RUN: sem isso o retry do dispatcher
// (que recomeca o run do zero) reencontra o pod da tentativa anterior.
func TestNomeMudaComATentativaDoRun(t *testing.T) {
	a := tarefa()
	b := tarefa()
	b.TentativaDoRun = 1
	if k8s.NomeDoPod(a) == k8s.NomeDoPod(b) {
		t.Error("tentativas diferentes do run geraram o mesmo pod")
	}
}
