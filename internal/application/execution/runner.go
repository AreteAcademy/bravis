// Package execution (application) percorre o grafo e executa os nos.
//
// Esta e a versao LOCAL: sem fila, sem persistencia, sem scheduler — essas
// pecas tem fase propria no plano (§37, fases 2 e 4). O que existe aqui e o
// suficiente para `bravis run arquivo.yaml` rodar na propria instancia, que foi
// o pedido.
package execution

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/zarvhq/bravis/internal/domain/run"
	wf "github.com/zarvhq/bravis/internal/domain/workflow"
	"github.com/zarvhq/bravis/internal/execution"
	"github.com/zarvhq/bravis/internal/graph"
	"sync"
	"time"
)

// Reporter recebe os eventos da execucao. Interface pequena para que a CLI, os
// testes e o persistidor possam observar o mesmo fluxo.
type Reporter interface {
	Evento(execution.Event)
}

// Persistidor grava o estado de cada passo. Opcional: o `bravis run` local nao
// tem banco, e exigi-lo tornaria a execucao ad-hoc dependente de infraestrutura.
type Persistidor interface {
	IniciarTask(ctx context.Context, runID uuid.UUID, nodeID string, tentativa int) error
	TerminarTask(ctx context.Context, runID uuid.UUID, nodeID string, tentativa int,
		status run.Status, exit *int, erro string, log string) error
}

// Runner executa um workflow inteiro.
//
// Guarda DOIS executores e escolhe por no: `run:` vai para o de processo,
// `action:` resolve no registry Go. A escolha e do runner, e nao do executor,
// para que cada executor continue ignorando a existencia do outro.
type Runner struct {
	Processo execution.Executor // atende `run:`; pode ser nil se so houver tasks Go
	Go       execution.Executor // atende `action:`; pode ser nil

	WorkDir string
	Env     map[string]string
	Report  Reporter

	// Timeout por no. Zero = sem limite.
	Timeout time.Duration

	// MaxTentativas por no. Zero ou 1 = tentativa unica.
	MaxTentativas int
	BackoffBase   time.Duration

	// Persist e RunID sao usados juntos: sem os dois, o estado por passo nao e
	// gravado e a DAG na UI aparece sem estado de execucao.
	Persist Persistidor
	RunID   uuid.UUID

	// Params sao os valores desta execucao. Entram no comando do passo por
	// template; ver execution.Renderizar.
	Params map[string]string

	// Vagas limita quantos PASSOS correm ao mesmo tempo — em Kubernetes, quantos
	// pods existem simultaneamente. Nulo = sem limite.
	//
	// Precisa ser compartilhado entre todos os Runners do processo, e por isso e
	// injetado em vez de criado aqui: o teto e do CLUSTER, nao de um workflow.
	// Sem ele, o limite de concorrencia do dispatcher contava RUNS — cinco runs
	// com tres passos paralelos cada davam quinze pods, nao cinco.
	Vagas chan struct{}

	// TentativaDoRun e a tentativa deste RUN, contada pelo dispatcher. Entra no
	// nome do pod para que um retry nao reencontre o pod da tentativa anterior.
	TentativaDoRun int

	// Pods executa passos como pod no Kubernetes. Quando presente, ele atende
	// todo passo que declara `image:` — e a mesma DAG roda em pod no cluster e
	// em processo na maquina, sem alterar o YAML.
	Pods execution.Executor
}

// Run percorre o grafo por niveis: tudo dentro de um nivel roda em paralelo, e o
// nivel seguinte so comeca quando o anterior fecha inteiro.
//
// Para na PRIMEIRA falha do nivel, sem iniciar o proximo. Continuar depois de um
// erro produziria resultado parcial que parece completo — foi assim que uma
// pipeline ficou 28 dias atrasada sem ninguem ver, no sistema que este substitui.
func (r Runner) Run(ctx context.Context, w wf.Workflow) error {
	niveis, err := graph.Niveis(w)
	if err != nil {
		return err
	}
	porID := make(map[string]wf.Node, len(w.Nodes))
	for _, n := range w.Nodes {
		porID[n.ID] = n
	}

	for i, nivel := range niveis {
		if err := r.rodarNivel(ctx, w, nivel, porID); err != nil {
			return fmt.Errorf("nivel %d: %w", i+1, err)
		}
	}
	return nil
}

func (r Runner) rodarNivel(ctx context.Context, w wf.Workflow, nivel []string, porID map[string]wf.Node) error {
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		erros []error
	)

	for _, id := range nivel {
		n := porID[id]

		wg.Add(1)
		go func(n wf.Node) {
			defer wg.Done()
			if err := r.rodarNo(ctx, w, n); err != nil {
				mu.Lock()
				erros = append(erros, err)
				mu.Unlock()
			}
		}(n)
	}
	wg.Wait()

	if len(erros) > 0 {
		return erros[0]
	}
	return nil
}

// rodarNo executa um no, com retry.
//
// O retry e POR NO, e nao apenas por Run como no dispatcher: refazer o workflow
// inteiro porque um `notify.sh` falhou desperdicaria o trabalho ja concluido.
func (r Runner) rodarNo(ctx context.Context, w wf.Workflow, n wf.Node) error {
	tentativas := r.MaxTentativas
	if tentativas < 1 {
		tentativas = 1
	}

	var ultima error
	for t := 1; t <= tentativas; t++ {
		// A vaga e tomada por TENTATIVA, nao pelo passo inteiro: segurar o lugar
		// durante o backoff deixaria uma vaga do cluster ociosa esperando um
		// relogio.
		libera, err := r.ocupar(ctx)
		if err != nil {
			return err
		}
		r.marcarInicio(ctx, n.ID, t-1)
		var saida string
		saida, ultima = r.tentar(ctx, w, n, t-1)
		r.marcarFim(ctx, n.ID, t-1, ultima, saida)
		libera()
		if ultima == nil {
			return nil
		}
		if t == tentativas {
			break
		}
		// Nao insiste se o contexto morreu: seria retry contra um cancelamento.
		if ctx.Err() != nil {
			break
		}

		espera := r.BackoffBase * time.Duration(1<<uint(t-1))
		if r.Report != nil {
			r.Report.Evento(execution.Event{
				Kind: execution.EventLog, NodeID: n.ID, Stream: "stderr",
				Message: fmt.Sprintf("tentativa %d/%d falhou, repetindo em %s", t, tentativas, espera),
			})
		}
		select {
		case <-time.After(espera):
		case <-ctx.Done():
			return ultima
		}
	}
	return ultima
}

// ocupar toma uma vaga e devolve a funcao que a libera.
//
// Bloqueia ate haver lugar — e esse o comportamento pedido: com dez passos
// prontos e cinco vagas, cinco correm e os outros esperam, entrando conforme as
// vagas se abrem. Recusar em vez de esperar transformaria excesso de trabalho em
// falha, quando ele e apenas fila.
func (r Runner) ocupar(ctx context.Context) (func(), error) {
	if r.Vagas == nil {
		return func() {}, nil
	}
	select {
	case r.Vagas <- struct{}{}:
		var uma sync.Once
		return func() { uma.Do(func() { <-r.Vagas }) }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// marcarInicio e marcarFim so gravam quando ha persistidor E RunID. Falha ao
// gravar nao interrompe a execucao: perder o registro de um passo e ruim, mas
// abortar o workflow por causa disso e pior.
func (r Runner) marcarInicio(ctx context.Context, nodeID string, tentativa int) {
	if r.Persist == nil || r.RunID == uuid.Nil {
		return
	}
	if err := r.Persist.IniciarTask(ctx, r.RunID, nodeID, tentativa); err != nil && r.Report != nil {
		r.Report.Evento(execution.Event{
			Kind: execution.EventLog, NodeID: nodeID, Stream: "stderr",
			Message: "nao consegui registrar o inicio do passo: " + err.Error(),
		})
	}
}

func (r Runner) marcarFim(ctx context.Context, nodeID string, tentativa int, causa error, log string) {
	if r.Persist == nil || r.RunID == uuid.Nil {
		return
	}
	status, msg := run.StatusSuccess, ""
	var exit *int
	if causa != nil {
		status, msg = run.StatusFailed, causa.Error()
		var passo *ErroDePasso
		// Exit 0 nao e gravado: uma task Go que falha nao tem processo, e um
		// zero na coluna leria como "terminou bem" ao lado de status failed.
		if errors.As(causa, &passo) && passo.ExitCode != 0 {
			exit = &passo.ExitCode
		}
	}
	if err := r.Persist.TerminarTask(ctx, r.RunID, nodeID, tentativa, status, exit, msg, log); err != nil && r.Report != nil {
		r.Report.Evento(execution.Event{
			Kind: execution.EventLog, NodeID: nodeID, Stream: "stderr",
			Message: "nao consegui registrar o fim do passo: " + err.Error(),
		})
	}
}

// ErroDePasso e a falha de um passo, com o contexto necessario para entende-la
// sem abrir log nenhum: o codigo de saida, o que ele significa, e as ultimas
// linhas que o processo escreveu em stderr.
//
// Antes so sobrava "saiu com codigo 127" — tecnicamente correto e inutil. A
// causa (`/bin/sh: python: not found`) passava pelos eventos como log e era
// descartada ali mesmo, entao a tela mostrava o sintoma sem a explicacao.
type ErroDePasso struct {
	NodeID   string
	ExitCode int
	Mensagem string

	// Saida sao as ultimas linhas de stderr. Guardar so as ultimas, e nao tudo,
	// porque um processo verboso encheria a coluna de erro do banco — e a causa
	// quase sempre esta no fim.
	Saida []string
}

func (e *ErroDePasso) Error() string {
	cabecalho := fmt.Sprintf("step %q: %s", e.NodeID, e.Mensagem)
	if dica := dicaDoCodigo(e.ExitCode); dica != "" {
		cabecalho += " (" + dica + ")"
	}
	if len(e.Saida) == 0 {
		return cabecalho
	}
	return cabecalho + "\n" + strings.Join(e.Saida, "\n")
}

// dicaDoCodigo traduz os codigos de saida que o shell reserva. Sao os que mais
// confundem: 127 nao e erro da aplicacao, e sim comando inexistente — a
// diferenca entre procurar defeito no codigo e procurar na imagem.
func dicaDoCodigo(c int) string {
	switch c {
	case 126:
		return "comando sem permissao de execucao"
	case 127:
		return "comando nao encontrado — verifique se ele existe na imagem do worker"
	case 130:
		return "interrompido por SIGINT"
	case 137:
		return "morto por SIGKILL — normalmente falta de memoria"
	case 143:
		return "encerrado por SIGTERM"
	case -1:
		return "encerrado por sinal, sem codigo de saida"
	}
	return ""
}

// linhasDeContexto e quantas linhas de stderr acompanham a falha. Cinco cobrem
// uma stack trace curta ou a mensagem final de um comando sem afogar a tela.
const linhasDeContexto = 5

// tentar roda o passo uma vez e devolve a saida completa (com teto) junto com o
// desfecho. A saida sobe mesmo em caso de sucesso: um passo que terminou bem
// mas produziu pouca coisa e um sinal, e so se percebe olhando o log.
func (r Runner) tentar(ctx context.Context, w wf.Workflow, n wf.Node, tentativa int) (string, error) {
	exec, tarefa, err := r.montar(w, n, tentativa)
	if err != nil {
		return "", err
	}

	eventos, err := exec.Execute(ctx, tarefa)
	if err != nil {
		return "", fmt.Errorf("step %q: %w", n.ID, err)
	}

	var falha *ErroDePasso
	var stderr, stdout []string

	// A saida inteira (com teto) vai para o banco. As janelas de 5 linhas
	// abaixo continuam existindo para a MENSAGEM de erro, que precisa caber num
	// alerta do Slack; esta guarda o que o operador vai querer ler depois,
	// quando o pod que a produziu ja nao existe.
	var completa janela

	for e := range eventos {
		if r.Report != nil {
			r.Report.Evento(e)
		}
		// Mantem uma janela deslizante das ultimas linhas. E preciso coletar
		// SEMPRE, e nao so depois de falhar: quando o evento de falha chega, as
		// linhas que o explicam ja passaram.
		//
		// Os dois fluxos, separados: nem todo programa escreve erro em stderr.
		// O dbt imprime "Parsing Error / Env var required but not provided" em
		// STDOUT, e capturar so stderr deixava a falha como "saiu com codigo 2",
		// sem a causa que estava na tela o tempo todo.
		if e.Kind == execution.EventLog {
			if linha := strings.TrimSpace(e.Message); linha != "" {
				completa.Escrever(linha)
				alvo := &stdout
				if e.Stream == "stderr" {
					alvo = &stderr
				}
				*alvo = append(*alvo, linha)
				if len(*alvo) > linhasDeContexto {
					*alvo = (*alvo)[1:]
				}
			}
		}
		if e.Kind == execution.EventFailed {
			falha = &ErroDePasso{NodeID: n.ID, ExitCode: e.ExitCode, Mensagem: e.Message}
		}
	}
	if falha == nil {
		return completa.String(), nil
	}
	// stderr primeiro: quando existe, e onde o programa quis reportar erro.
	// stdout so entra na ausencia dele, para nao encher a mensagem com a saida
	// normal de um comando que apenas terminou mal.
	falha.Saida = stderr
	if len(falha.Saida) == 0 {
		falha.Saida = stdout
	}
	return completa.String(), falha
}

// montar escolhe o executor e monta a task.
func (r Runner) montar(w wf.Workflow, n wf.Node, tentativa int) (execution.Executor, execution.TaskExec, error) {
	imagem := w.ImagemDe(n)
	recursos := w.RecursosDe(n)

	t := execution.TaskExec{
		ExecutionID: w.Slug + ":" + n.ID,
		NodeID:      n.ID,
		Workflow:    w.Slug,
		RunID:       r.RunID.String(),
		// A tentativa entra no NOME do pod. Sem ela, um retry reencontra o pod
		// da tentativa anterior — e como o executor adota pod existente (para
		// nao subir dois iguais quando o processo morre no meio), ele fica
		// preso ao pod quebrado para sempre. Foi assim em dev: um pod Pending
		// por CPU insuficiente foi readotado a cada retry.
		Tentativa:  tentativa,
		Image:      imagem,
		Shell:      n.UsaShell(),
		CPU:        recursos.CPU,
		Memoria:    recursos.Memory,
		CPUMax:     recursos.CPULimit,
		MemoriaMax: recursos.MemoryLimit,
		WorkDir:    r.WorkDir,
		Env:        r.Env,
		Timeout:    r.Timeout,
	}

	if n.Action != "" {
		if r.Go == nil {
			return nil, t, fmt.Errorf("step %q usa `action: %s`, mas nenhum executor Go foi configurado", n.ID, n.Action)
		}
		t.Action, t.With = n.Action, n.With
		return r.Go, t, nil
	}

	// O comando e renderizado AQUI, na montagem da task, e nao na publicacao:
	// o mesmo workflow roda com params diferentes a cada disparo, e um comando
	// congelado no banco perderia isso.
	comando, err := execution.Renderizar(n.Run, r.Params)
	if err != nil {
		return nil, t, fmt.Errorf("step %q: %w", n.ID, err)
	}
	t.Command = comando

	// Um passo com `image:` roda em POD quando ha executor de pods. E a
	// diferenca entre o modo local e o cluster, e ela mora AQUI, num lugar so —
	// o YAML e identico nos dois, e o executor nao sabe qual e o outro.
	if imagem != "" && r.Pods != nil {
		return r.Pods, t, nil
	}
	if r.Processo == nil {
		if imagem != "" {
			return nil, t, fmt.Errorf("step %q declara `image: %s`, mas este processo nao tem executor de pods nem de processo", n.ID, imagem)
		}
		return nil, t, fmt.Errorf("step %q usa `run:`, mas nenhum executor de processo foi configurado", n.ID)
	}
	// Local com `image:` declarada: roda na propria instancia e AVISA. Silenciar
	// faria parecer que o passo rodou na imagem declarada, que e o tipo de
	// engano que so aparece quando o resultado ja esta errado.
	if imagem != "" && r.Report != nil {
		r.Report.Evento(execution.Event{
			Kind: execution.EventLog, NodeID: n.ID, Stream: "stderr",
			Message: fmt.Sprintf("modo local: rodando na instancia, ignorando `image: %s`", imagem),
		})
	}
	return r.Processo, t, nil
}
