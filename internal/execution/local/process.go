// Package local implementa a execucao de processos no host.
//
// Existe por causa da emenda de 2026-08-31 a secao 3 do plano: o texto original
// exigia Kubernetes para qualquer linguagem que nao fosse Go, o que inviabilizava
// desenvolver na propria instancia.
//
// A fronteira e codigo, nao convencao: New recusa-se a construir o executor fora
// do modo local. Um `run:` sem limite declarado e o risco real — nao o comando.
package local

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"

	"github.com/AreteAcademy/brevis/internal/execution"
)

// ProcessExecutor roda comandos arbitrarios como processos do host.
type ProcessExecutor struct {
	shell string

	mu      sync.Mutex
	rodando map[string]context.CancelFunc
}

// ErrForaDoLocal e devolvido quando se tenta construir o executor fora do modo
// local. Tipado para poder ser afirmado em teste.
type ErrForaDoLocal struct{ Env string }

func (e ErrForaDoLocal) Error() string {
	return fmt.Sprintf("ProcessExecutor so opera com BREVIS_ENV=local (recebido %q); "+
		"fora do local, `run:` deve ir para o KubernetesExecutor", e.Env)
}

// New devolve o executor, ou recusa se o ambiente nao for local.
func New(env string) (*ProcessExecutor, error) {
	if env != "local" {
		return nil, ErrForaDoLocal{Env: env}
	}
	return &ProcessExecutor{shell: "/bin/sh", rodando: map[string]context.CancelFunc{}}, nil
}

// ambienteDaTask monta o env do processo: os literais, e os segredos lidos do
// ambiente do proprio motor.
//
// No Kubernetes a coordenada `gabriel-session/cookie` aponta um Secret. Aqui
// nao ha Secret nenhum, entao o motor le a variavel de MESMO NOME do proprio
// ambiente. A assimetria e deliberada e esta documentada no dominio; o que ela
// nao pode fazer e falhar em silencio.
//
// Ausente e ERRO, e nao string vazia. Um GABRIEL_SESSION_COOKIE vazio vira um
// header de cookie vazio e um 401 la na frente, culpando a API por uma
// variavel que ninguem exportou.
func ambienteDaTask(t execution.TaskExec) ([]string, error) {
	env := make(map[string]string, len(t.Env)+len(t.Secrets))
	for k, v := range t.Env {
		env[k] = v
	}

	var faltando []string
	for nome, coord := range t.Secrets {
		v, existe := os.LookupEnv(nome)
		if !existe || v == "" {
			faltando = append(faltando, fmt.Sprintf("%s (secrets: %s)", nome, coord))
			continue
		}
		env[nome] = v
	}
	if len(faltando) > 0 {
		sort.Strings(faltando)
		return nil, fmt.Errorf("task %q: no modo local os segredos vem do ambiente do "+
			"proprio motor, e estes nao estao definidos: %s",
			t.NodeID, strings.Join(faltando, ", "))
	}

	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	// Ordenado para que dois processos iguais tenham o mesmo env: sem isso, um
	// diff entre duas execucoes vira ruido de ordem de mapa.
	sort.Strings(out)
	return out, nil
}

func (p *ProcessExecutor) Name() string { return "process" }

// Execute dispara o comando e devolve o canal de eventos. O canal fecha quando o
// processo termina — quem consome pode usar `range` sem coordenacao extra.
func (p *ProcessExecutor) Execute(ctx context.Context, t execution.TaskExec) (<-chan execution.Event, error) {
	if t.Command == "" {
		return nil, fmt.Errorf("task %q sem comando", t.NodeID)
	}

	ctx, cancel := context.WithCancel(ctx)
	if t.Timeout > 0 {
		// CommandContext mata o processo quando o contexto expira, entao o
		// timeout vale para o comando inteiro, nao so para a espera.
		ctx, cancel = context.WithTimeout(ctx, t.Timeout)
	}

	p.mu.Lock()
	p.rodando[t.ExecutionID] = cancel
	p.mu.Unlock()

	// `sh -c` porque o YAML declara uma linha de shell ("python fetch.py"), nao
	// um argv. Aceitar a linha e o ponto do `run:`.
	cmd := exec.CommandContext(ctx, p.shell, "-c", t.Command)
	cmd.Dir = t.WorkDir

	// Ambiente explicito, sem herdar o do processo pai: o orquestrador carrega
	// credenciais que uma task nao deve enxergar por acidente.
	//
	// `secrets:` e justamente o opt-in nominal contra essa regra -- "esta, de
	// proposito" -- e por isso ele resolve DEPOIS, podendo sobrescrever.
	ambiente, err := ambienteDaTask(t)
	if err != nil {
		cancel()
		return nil, err
	}
	cmd.Env = ambiente

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("iniciando %q: %w", t.NodeID, err)
	}

	eventos := make(chan execution.Event, 64)
	go func() {
		defer close(eventos)
		defer func() {
			p.mu.Lock()
			delete(p.rodando, t.ExecutionID)
			p.mu.Unlock()
			cancel()
		}()

		eventos <- execution.Event{Kind: execution.EventStarted, NodeID: t.NodeID}

		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); repassar(stdout, "stdout", t.NodeID, eventos) }()
		go func() { defer wg.Done(); repassar(stderr, "stderr", t.NodeID, eventos) }()
		wg.Wait() // drenar ANTES do Wait: fechar os pipes cedo perderia as ultimas linhas

		err := cmd.Wait()
		code := cmd.ProcessState.ExitCode()
		if err != nil {
			eventos <- execution.Event{
				Kind: execution.EventFailed, NodeID: t.NodeID,
				ExitCode: code, Err: err,
				Message: fmt.Sprintf("saiu com codigo %d", code),
			}
			return
		}
		eventos <- execution.Event{Kind: execution.EventSucceeded, NodeID: t.NodeID, ExitCode: code}
	}()

	return eventos, nil
}

// Cancel interrompe uma execucao em voo.
func (p *ProcessExecutor) Cancel(_ context.Context, execID string) error {
	p.mu.Lock()
	cancel, ok := p.rodando[execID]
	p.mu.Unlock()
	if !ok {
		return fmt.Errorf("execucao %q nao esta rodando", execID)
	}
	cancel()
	return nil
}

func repassar(r io.Reader, stream, nodeID string, out chan<- execution.Event) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024) // linhas longas de log nao podem truncar a saida
	for sc.Scan() {
		out <- execution.Event{
			Kind: execution.EventLog, NodeID: nodeID,
			Stream: stream, Message: sc.Text(),
		}
	}
}
