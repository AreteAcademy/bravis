package kubernetes

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/zarvhq/bravis/internal/execution"
)

// API e o que o executor precisa do servidor. Interface no consumidor: e ela
// que permite testar o ciclo de vida inteiro do pod contra um servidor falso.
type API interface {
	CriarPod(ctx context.Context, p Pod) (Pod, error)
	LerPod(ctx context.Context, nome string) (Pod, error)
	Logs(ctx context.Context, nome string, seguir bool) (io.ReadCloser, error)
	ApagarPod(ctx context.Context, nome string) error
}

// Executor roda cada passo como um pod.
//
// O ciclo e sempre o mesmo: cria o pod, espera sair de Pending, acompanha o log
// enquanto roda, le o codigo de saida e apaga. Nenhum estado vive aqui alem dos
// pods em voo — se o processo reiniciar, os pods continuam rodando e o
// dispatcher os reencontra pelo nome deterministico.
type Executor struct {
	api  API
	opts Opcoes

	// Intervalo de sondagem do status. Sondar e nao observar (watch) e
	// deliberado: um watch exige reconexao, resync e tratamento de eventos
	// perdidos para ganhar segundos numa task que dura minutos.
	Intervalo time.Duration

	mu    sync.Mutex
	emVoo map[string]string // execID -> nome do pod
}

func NewExecutor(api API, o Opcoes) *Executor {
	return &Executor{
		api: api, opts: o.comPadroes(),
		Intervalo: time.Second,
		emVoo:     map[string]string{},
	}
}

func (e *Executor) Name() string { return "kubernetes" }

// Execute cria o pod e devolve o canal de eventos. O canal fecha quando o pod
// termina — mesma forma do executor local, entao o runner nao distingue os dois.
func (e *Executor) Execute(ctx context.Context, t execution.TaskExec) (<-chan execution.Event, error) {
	spec, err := MontarPod(t, e.opts)
	if err != nil {
		return nil, err
	}

	criado, err := e.api.CriarPod(ctx, spec)
	if err != nil {
		// AlreadyExists nao e erro: o nome e deterministico por tentativa, entao
		// isto significa que uma execucao anterior criou o pod e morreu antes de
		// registrar. Adotar o pod existente evita rodar o mesmo dbt duas vezes
		// em paralelo.
		if !strings.Contains(err.Error(), "already exists") {
			return nil, err
		}
		criado = spec
	}
	nome := criado.Metadata.Name

	e.mu.Lock()
	e.emVoo[t.ExecutionID] = nome
	e.mu.Unlock()

	eventos := make(chan execution.Event, 64)
	go func() {
		defer close(eventos)
		defer func() {
			e.mu.Lock()
			delete(e.emVoo, t.ExecutionID)
			e.mu.Unlock()
		}()
		e.acompanhar(ctx, nome, t, eventos)
	}()
	return eventos, nil
}

func (e *Executor) acompanhar(ctx context.Context, nome string, t execution.TaskExec, eventos chan<- execution.Event) {
	eventos <- execution.Event{
		Kind: execution.EventStarted, NodeID: t.NodeID,
		Message: fmt.Sprintf("pod %s (%s)", nome, t.Image),
	}

	pod, err := e.esperarSair(ctx, nome, t, eventos)
	if err != nil {
		eventos <- execution.Event{
			Kind: execution.EventFailed, NodeID: t.NodeID,
			Message: err.Error(), Err: err,
		}
		e.limpar(nome, false)
		return
	}

	// O log e drenado ate o fim ANTES de reportar o desfecho: fechar o canal com
	// linhas ainda no buffer perderia justamente as ultimas, que sao as que
	// explicam a falha.
	e.drenarLogs(ctx, nome, t, eventos)

	codigo, terminou := pod.Saida()
	if pod.Fase() == "Succeeded" {
		eventos <- execution.Event{Kind: execution.EventSucceeded, NodeID: t.NodeID, ExitCode: codigo}
		e.limpar(nome, true)
		return
	}

	msg := fmt.Sprintf("pod %s terminou em %s", nome, pod.Fase())
	if terminou {
		msg = fmt.Sprintf("saiu com codigo %d", codigo)
	}
	if pod.Motivo() != "" {
		// DeadlineExceeded, OOMKilled, Evicted: e a diferenca entre "o codigo
		// falhou" e "o cluster matou o processo".
		msg += " (" + pod.Motivo() + ")"
	}
	eventos <- execution.Event{
		Kind: execution.EventFailed, NodeID: t.NodeID,
		ExitCode: codigo, Message: msg, Err: errors.New(msg),
	}
	e.limpar(nome, false)
}

// esperarSair sonda ate o pod terminar, reportando por que ele espera.
func (e *Executor) esperarSair(ctx context.Context, nome string, t execution.TaskExec,
	eventos chan<- execution.Event) (Pod, error) {

	tick := time.NewTicker(e.Intervalo)
	defer tick.Stop()

	var ultimoMotivo string
	seguindo := false
	comecou := time.Now()

	for {
		pod, err := e.api.LerPod(ctx, nome)
		if err != nil {
			return Pod{}, fmt.Errorf("lendo pod %s: %w", nome, err)
		}
		if pod.Terminou() {
			return pod, nil
		}

		// Um pod parado em ImagePullBackOff ou CreateContainerConfigError nao
		// produz log nenhum: sem reportar o motivo, o passo pareceria travado
		// ate o timeout, sem uma linha explicando.
		if motivo := pod.MotivoDeEspera(); motivo != "" && motivo != ultimoMotivo {
			ultimoMotivo = motivo
			eventos <- execution.Event{
				Kind: execution.EventLog, NodeID: t.NodeID, Stream: "stderr",
				Message: "pod aguardando: " + motivo,
			}
		}

		// Assim que o container roda, o log e seguido em paralelo — o operador
		// ve a saida do dbt ao vivo, e nao so no fim.
		if !seguindo && pod.Fase() == "Running" {
			seguindo = true
			go e.seguirLogs(ctx, nome, t, eventos)
		}

		// Pod que nao sai de Pending nao e erro para o Kubernetes: ele espera
		// para sempre por um no que caiba. Sem este corte, a etapa espera junto
		// — sem falha e sem retry —, que foi como um request de CPU maior que o
		// livre no pool travou uma run inteira em dev.
		if !seguindo && time.Since(comecou) > e.opts.EsperaParaIniciar {
			motivo := pod.MotivoDeEspera()
			if motivo == "" {
				motivo = "fase " + pod.Fase()
			}
			if agendamento := e.porQueNaoAgendou(ctx, nome); agendamento != "" {
				motivo = agendamento
			}
			return Pod{}, fmt.Errorf("pod %s nao comecou em %s: %s",
				nome, e.opts.EsperaParaIniciar, motivo)
		}

		select {
		case <-ctx.Done():
			return Pod{}, ctx.Err()
		case <-tick.C:
		}
	}
}

// porQueNaoAgendou le a condicao PodScheduled, que e onde o scheduler explica
// "Insufficient cpu" ou "didn't match node affinity". Sem isso a mensagem diria
// apenas "Pending", que nao ajuda ninguem.
func (e *Executor) porQueNaoAgendou(ctx context.Context, nome string) string {
	pod, err := e.api.LerPod(ctx, nome)
	if err != nil || pod.Status == nil {
		return ""
	}
	for _, c := range pod.Status.Conditions {
		if c.Type == "PodScheduled" && c.Status != "True" && c.Message != "" {
			return c.Reason + ": " + c.Message
		}
	}
	return ""
}

// seguirLogs acompanha a saida enquanto o container vive.
func (e *Executor) seguirLogs(ctx context.Context, nome string, t execution.TaskExec, eventos chan<- execution.Event) {
	corpo, err := e.api.Logs(ctx, nome, true)
	if err != nil {
		return // o log pode nao estar pronto; drenarLogs ainda le no fim
	}
	defer corpo.Close()
	copiar(corpo, t.NodeID, eventos)
}

// drenarLogs le a saida completa depois que o pod termina.
//
// Sem seguir: o container ja acabou, e `follow` numa saida encerrada apenas
// devolve o mesmo conteudo. As linhas repetidas do trecho ja transmitido sao o
// preco de nao perder o final — e perder o final e o que impede entender a
// falha.
func (e *Executor) drenarLogs(ctx context.Context, nome string, t execution.TaskExec, eventos chan<- execution.Event) {
	corpo, err := e.api.Logs(ctx, nome, false)
	if err != nil {
		eventos <- execution.Event{
			Kind: execution.EventLog, NodeID: t.NodeID, Stream: "stderr",
			Message: "nao consegui ler o log do pod: " + err.Error(),
		}
		return
	}
	defer corpo.Close()
	copiar(corpo, t.NodeID, eventos)
}

func copiar(r io.Reader, nodeID string, eventos chan<- execution.Event) {
	s := bufio.NewScanner(r)
	// Linha de dbt com SQL pode passar de 64 KB, o limite padrao do Scanner —
	// e um Scanner que estoura para de ler em silencio.
	s.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for s.Scan() {
		// O log do pod vem por um stream so: o Kubernetes nao separa stdout de
		// stderr. Marcar tudo como stdout seria mentira menor que o contrario,
		// mas a informacao de origem simplesmente nao existe aqui.
		eventos <- execution.Event{
			Kind: execution.EventLog, NodeID: nodeID,
			Stream: "stdout", Message: s.Text(),
		}
	}
}

// limpar apaga o pod, respeitando a opcao de manter os que falharam.
func (e *Executor) limpar(nome string, sucesso bool) {
	if !sucesso && e.opts.ManterPodEmFalha {
		return
	}
	// Contexto proprio: o da execucao ja pode estar cancelado (foi o
	// cancelamento que trouxe ate aqui), e apagar e justamente o que nao pode
	// ser pulado — pod orfao consome quota do namespace para sempre.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = e.api.ApagarPod(ctx, nome)
}

// Cancel apaga o pod da execucao em voo.
func (e *Executor) Cancel(ctx context.Context, execID string) error {
	e.mu.Lock()
	nome, ok := e.emVoo[execID]
	e.mu.Unlock()
	if !ok {
		return fmt.Errorf("execucao %q nao esta rodando", execID)
	}
	return e.api.ApagarPod(ctx, nome)
}
