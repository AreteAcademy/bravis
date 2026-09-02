package execution_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	app "github.com/AreteAcademy/bravis/internal/application/execution"
	wf "github.com/AreteAcademy/bravis/internal/domain/workflow"
	"github.com/AreteAcademy/bravis/internal/execution"
	"github.com/AreteAcademy/bravis/internal/execution/local"
)

type coletor struct {
	mu      sync.Mutex
	eventos []execution.Event
}

func (c *coletor) Evento(e execution.Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.eventos = append(c.eventos, e)
}

// CRITERIO DE ACEITE DA PHASE 3 (secao 37):
//
//	Task A -> (Task B + Task C) -> Task D, com paralelismo.
func TestCriterioDeAceite_DAGGoComParalelismo(t *testing.T) {
	reg := execution.NewRegistry()

	var (
		mu     sync.Mutex
		ordem  []string
		emVoo  atomic.Int32
		picoBC atomic.Int32
	)

	registrar := func(nome string, dura time.Duration) {
		reg.MustRegister(execution.FuncTask{Nome: nome, Fn: func(ctx context.Context, _ execution.Input) error {
			n := emVoo.Add(1)
			for {
				p := picoBC.Load()
				if n <= p || picoBC.CompareAndSwap(p, n) {
					break
				}
			}
			defer emVoo.Add(-1)

			mu.Lock()
			ordem = append(ordem, nome)
			mu.Unlock()

			select {
			case <-time.After(dura):
			case <-ctx.Done():
				return ctx.Err()
			}
			return nil
		}})
	}
	registrar("task_a", 10*time.Millisecond)
	registrar("task_b", 150*time.Millisecond)
	registrar("task_c", 150*time.Millisecond)
	registrar("task_d", 10*time.Millisecond)

	w := wf.Workflow{
		Slug: "dag_go", Kind: wf.KindDAG,
		Nodes: []wf.Node{
			{ID: "a", Action: "task_a"},
			{ID: "b", Action: "task_b"},
			{ID: "c", Action: "task_c"},
			{ID: "d", Action: "task_d"},
		},
		Edges: []wf.Edge{
			{From: "a", To: "b"}, {From: "a", To: "c"},
			{From: "b", To: "d"}, {From: "c", To: "d"},
		},
	}
	if err := w.Validate(); err != nil {
		t.Fatal(err)
	}

	c := &coletor{}
	r := app.Runner{Go: local.NewGoExecutor(reg), Report: c}

	inicio := time.Now()
	if err := r.Run(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	duracao := time.Since(inicio)

	// b e c rodaram JUNTAS: em serie o total passaria de 300ms
	if picoBC.Load() < 2 {
		t.Errorf("pico de concorrencia = %d; b e c deviam rodar em paralelo", picoBC.Load())
	}
	if duracao > 280*time.Millisecond {
		t.Errorf("levou %s; em paralelo deveria ficar perto de 170ms, nao de 320ms", duracao)
	}

	// a primeiro, d por ultimo — a ordem topologica foi respeitada
	if ordem[0] != "task_a" {
		t.Errorf("primeira = %q, queria task_a", ordem[0])
	}
	if ordem[len(ordem)-1] != "task_d" {
		t.Errorf("ultima = %q, queria task_d", ordem[len(ordem)-1])
	}
	t.Logf("ordem: %v | duracao: %s | pico: %d", ordem, duracao.Round(time.Millisecond), picoBC.Load())
}

// O retry e POR NO: refazer o workflow inteiro porque o ultimo step falhou
// desperdicaria o trabalho ja concluido.
func TestRetryPorNo(t *testing.T) {
	reg := execution.NewRegistry()
	var tentativas atomic.Int32

	reg.MustRegister(execution.FuncTask{Nome: "instavel", Fn: func(context.Context, execution.Input) error {
		if tentativas.Add(1) < 3 {
			return fmt.Errorf("falha transitoria")
		}
		return nil
	}})

	w := wf.Workflow{Slug: "w", Nodes: []wf.Node{{ID: "n", Action: "instavel"}}}
	r := app.Runner{
		Go: local.NewGoExecutor(reg), Report: &coletor{},
		MaxTentativas: 3, BackoffBase: time.Millisecond,
	}
	if err := r.Run(context.Background(), w); err != nil {
		t.Fatalf("devia ter sucesso na 3a tentativa: %v", err)
	}
	if n := tentativas.Load(); n != 3 {
		t.Errorf("tentativas = %d, queria 3", n)
	}
}

func TestRetryDesisteAposOLimite(t *testing.T) {
	reg := execution.NewRegistry()
	var tentativas atomic.Int32
	reg.MustRegister(execution.FuncTask{Nome: "sempre_falha", Fn: func(context.Context, execution.Input) error {
		tentativas.Add(1)
		return fmt.Errorf("falha permanente")
	}})

	w := wf.Workflow{Slug: "w", Nodes: []wf.Node{{ID: "n", Action: "sempre_falha"}}}
	r := app.Runner{
		Go: local.NewGoExecutor(reg), Report: &coletor{},
		MaxTentativas: 2, BackoffBase: time.Millisecond,
	}
	if err := r.Run(context.Background(), w); err == nil {
		t.Fatal("esperava falha")
	}
	if n := tentativas.Load(); n != 2 {
		t.Errorf("tentativas = %d, queria exatamente 2", n)
	}
}

// Cancelar o contexto interrompe a DAG e nao dispara retry — repetir contra um
// cancelamento e desperdicio.
func TestCancelamentoNaoDisparaRetry(t *testing.T) {
	reg := execution.NewRegistry()
	var tentativas atomic.Int32
	reg.MustRegister(execution.FuncTask{Nome: "lenta", Fn: func(ctx context.Context, _ execution.Input) error {
		tentativas.Add(1)
		<-ctx.Done()
		return ctx.Err()
	}})

	w := wf.Workflow{Slug: "w", Nodes: []wf.Node{{ID: "n", Action: "lenta"}}}
	r := app.Runner{
		Go: local.NewGoExecutor(reg), Report: &coletor{},
		MaxTentativas: 5, BackoffBase: time.Millisecond,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	_ = r.Run(ctx, w)

	if n := tentativas.Load(); n != 1 {
		t.Errorf("tentativas = %d; cancelamento nao deve repetir", n)
	}
}

// Um step sem executor configurado precisa falhar com mensagem clara, e nao
// ser pulado em silencio.
func TestStepSemExecutorFalhaExplicitamente(t *testing.T) {
	w := wf.Workflow{Slug: "w", Nodes: []wf.Node{{ID: "n", Action: "qualquer"}}}
	err := app.Runner{Report: &coletor{}}.Run(context.Background(), w)
	if err == nil {
		t.Fatal("esperava erro, nao silencio")
	}
}

// O exit code tem que sobreviver ao caminho todo: evento do executor -> erro
// tipado -> persistencia. Sem isto, distinguir 127 (comando inexistente) de 2
// (erro da aplicacao) so olhando log.
func TestErroDePassoCarregaExitCode(t *testing.T) {
	exec, err := local.New("local")
	if err != nil {
		t.Fatal(err)
	}
	w := wf.Workflow{Slug: "sai", Nodes: []wf.Node{{ID: "falha", Run: "sh -c 'exit 3'"}}}

	erro := app.Runner{
		Processo: exec, Report: &coletor{},
		Env: map[string]string{"PATH": os.Getenv("PATH")},
	}.Run(context.Background(), w)
	if erro == nil {
		t.Fatal("esperava falha")
	}

	var passo *app.ErroDePasso
	if !errors.As(erro, &passo) {
		t.Fatalf("erro %T nao carrega o exit code", erro)
	}
	if passo.ExitCode != 3 || passo.NodeID != "falha" {
		t.Errorf("node=%q exit=%d, quero falha e 3", passo.NodeID, passo.ExitCode)
	}
}

// O erro precisa dizer POR QUE o passo falhou, nao so que falhou. "saiu com
// codigo 127" e tecnicamente correto e inutil: a causa (`sh: xpto: not found`)
// passava pelos eventos como log e era descartada ali mesmo.
func TestErroCarregaSaidaDeErroEDicaDoCodigo(t *testing.T) {
	exec, err := local.New("local")
	if err != nil {
		t.Fatal(err)
	}
	w := wf.Workflow{Slug: "ausente", Nodes: []wf.Node{
		{ID: "fetch_data", Run: "sh -c 'comando_que_nao_existe_abc'"},
	}}

	erro := app.Runner{
		Processo: exec, Report: &coletor{},
		Env: map[string]string{"PATH": os.Getenv("PATH")},
	}.Run(context.Background(), w)
	if erro == nil {
		t.Fatal("esperava falha")
	}

	msg := erro.Error()
	if !strings.Contains(msg, "127") {
		t.Errorf("mensagem sem o codigo de saida: %q", msg)
	}
	if !strings.Contains(msg, "comando nao encontrado") {
		t.Errorf("mensagem sem a traducao do codigo: %q", msg)
	}
	if !strings.Contains(msg, "comando_que_nao_existe_abc") {
		t.Errorf("mensagem sem a linha de stderr que explica a falha: %q", msg)
	}

	var passo *app.ErroDePasso
	if !errors.As(erro, &passo) || len(passo.Saida) == 0 {
		t.Fatalf("o erro nao carrega a saida: %+v", passo)
	}
}

// Processo verboso nao pode encher a coluna de erro do banco: so as ultimas
// linhas acompanham a falha, que e onde a causa quase sempre esta.
func TestSaidaDeErroFicaNasUltimasLinhas(t *testing.T) {
	exec, err := local.New("local")
	if err != nil {
		t.Fatal(err)
	}
	w := wf.Workflow{Slug: "verboso", Nodes: []wf.Node{
		{ID: "ruido", Run: "sh -c 'for i in 1 2 3 4 5 6 7 8 9 10; do echo linha $i >&2; done; exit 9'"},
	}}

	erro := app.Runner{
		Processo: exec, Report: &coletor{},
		Env: map[string]string{"PATH": os.Getenv("PATH")},
	}.Run(context.Background(), w)

	var passo *app.ErroDePasso
	if !errors.As(erro, &passo) {
		t.Fatalf("erro %T", erro)
	}
	if len(passo.Saida) != 5 {
		t.Errorf("guardou %d linhas, quero 5", len(passo.Saida))
	}
	if len(passo.Saida) > 0 && passo.Saida[len(passo.Saida)-1] != "linha 10" {
		t.Errorf("ultima linha = %q, quero a mais recente", passo.Saida[len(passo.Saida)-1])
	}
	// Codigo sem significado especial nao ganha traducao inventada.
	if strings.Contains(erro.Error(), "(") {
		t.Errorf("codigo 9 nao deveria receber dica: %q", erro.Error())
	}
}

// Sucesso continua sem erro mesmo com o processo escrevendo em stderr — muito
// comando legitimo usa stderr para progresso.
func TestStderrEmPassoQueDaCertoNaoViraFalha(t *testing.T) {
	exec, err := local.New("local")
	if err != nil {
		t.Fatal(err)
	}
	w := wf.Workflow{Slug: "avisos", Nodes: []wf.Node{
		{ID: "ok", Run: "sh -c 'echo aviso >&2; exit 0'"},
	}}
	if erro := (app.Runner{
		Processo: exec, Report: &coletor{},
		Env: map[string]string{"PATH": os.Getenv("PATH")},
	}).Run(context.Background(), w); erro != nil {
		t.Errorf("passo com stderr e exit 0 virou falha: %v", erro)
	}
}

// O dbt imprime "Parsing Error" em STDOUT. Capturar so stderr deixava a falha
// como "saiu com codigo 2", sem a causa que estava na tela o tempo todo.
func TestFalhaSemStderrUsaOStdout(t *testing.T) {
	exec, err := local.New("local")
	if err != nil {
		t.Fatal(err)
	}
	w := wf.Workflow{Slug: "dbt", Nodes: []wf.Node{{
		ID:  "run",
		Run: `sh -c 'echo "Running with dbt=1.10.3"; echo "Env var required but not provided: GOOGLE_PROJECT_ID"; exit 2'`,
	}}}

	erro := (app.Runner{
		Processo: exec, Report: &coletor{},
		Env: map[string]string{"PATH": os.Getenv("PATH")},
	}).Run(context.Background(), w)
	if erro == nil {
		t.Fatal("esperava falha")
	}
	if !strings.Contains(erro.Error(), "Env var required") {
		t.Errorf("a causa, impressa em stdout, nao chegou a mensagem: %q", erro.Error())
	}
}

// Quando ha stderr, ele manda: e onde o programa quis reportar erro, e a saida
// normal nao deve encher a mensagem.
func TestStderrTemPrecedenciaSobreStdout(t *testing.T) {
	exec, _ := local.New("local")
	w := wf.Workflow{Slug: "misto", Nodes: []wf.Node{{
		ID:  "run",
		Run: `sh -c 'echo "linha normal de progresso"; echo "causa real" >&2; exit 1'`,
	}}}

	erro := (app.Runner{
		Processo: exec, Report: &coletor{},
		Env: map[string]string{"PATH": os.Getenv("PATH")},
	}).Run(context.Background(), w)

	msg := erro.Error()
	if !strings.Contains(msg, "causa real") {
		t.Errorf("stderr nao chegou: %q", msg)
	}
	if strings.Contains(msg, "linha normal de progresso") {
		t.Errorf("stdout entrou junto com stderr: %q", msg)
	}
}

// O pedido, literal: com dez passos prontos e cinco vagas, cinco correm e os
// outros entram conforme as vagas se abrem — nunca seis ao mesmo tempo.
//
// Antes o limite do dispatcher contava RUNS: cinco runs com tres passos
// paralelos cada davam quinze pods no cluster, nao cinco.
func TestVagasLimitamPassosSimultaneos(t *testing.T) {
	const passos, teto = 10, 5

	var emVoo, pico int64
	reg := execution.NewRegistry()
	reg.MustRegister(execution.FuncTask{Nome: "ocupa", Fn: func(ctx context.Context, in execution.Input) error {
		atual := atomic.AddInt64(&emVoo, 1)
		for {
			anterior := atomic.LoadInt64(&pico)
			if atual <= anterior || atomic.CompareAndSwapInt64(&pico, anterior, atual) {
				break
			}
		}
		time.Sleep(40 * time.Millisecond)
		atomic.AddInt64(&emVoo, -1)
		return nil
	}})

	// Todos no MESMO nivel: sem dependencia entre eles, o runner dispara os dez
	// de uma vez se nada o segurar.
	w := wf.Workflow{Slug: "paralelo"}
	for i := 0; i < passos; i++ {
		w.Nodes = append(w.Nodes, wf.Node{ID: fmt.Sprintf("p%d", i), Action: "ocupa"})
	}

	r := app.Runner{
		Go:     local.NewGoExecutor(reg),
		Report: &coletor{},
		Vagas:  make(chan struct{}, teto),
	}
	if err := r.Run(context.Background(), w); err != nil {
		t.Fatal(err)
	}

	if p := atomic.LoadInt64(&pico); p > teto {
		t.Errorf("pico de %d passos simultaneos, o teto e %d", p, teto)
	} else if p < teto {
		t.Errorf("pico de %d: as vagas nao foram usadas, o limite virou serializacao", p)
	}
}

// O semaforo e do PROCESSO, nao do workflow: dois runs concorrentes dividem o
// mesmo teto, senao cada run teria cinco pods para si.
func TestVagasSaoCompartilhadasEntreRuns(t *testing.T) {
	const teto = 3

	var emVoo, pico int64
	reg := execution.NewRegistry()
	reg.MustRegister(execution.FuncTask{Nome: "ocupa", Fn: func(ctx context.Context, in execution.Input) error {
		atual := atomic.AddInt64(&emVoo, 1)
		for {
			anterior := atomic.LoadInt64(&pico)
			if atual <= anterior || atomic.CompareAndSwapInt64(&pico, anterior, atual) {
				break
			}
		}
		time.Sleep(30 * time.Millisecond)
		atomic.AddInt64(&emVoo, -1)
		return nil
	}})

	vagas := make(chan struct{}, teto)
	monta := func(slug string) wf.Workflow {
		w := wf.Workflow{Slug: slug}
		for i := 0; i < 5; i++ {
			w.Nodes = append(w.Nodes, wf.Node{ID: fmt.Sprintf("p%d", i), Action: "ocupa"})
		}
		return w
	}

	var wg sync.WaitGroup
	for _, slug := range []string{"a", "b"} {
		wg.Add(1)
		go func(slug string) {
			defer wg.Done()
			r := app.Runner{Go: local.NewGoExecutor(reg), Report: &coletor{}, Vagas: vagas}
			if err := r.Run(context.Background(), monta(slug)); err != nil {
				t.Error(err)
			}
		}(slug)
	}
	wg.Wait()

	if p := atomic.LoadInt64(&pico); p > teto {
		t.Errorf("pico de %d com dois runs; o teto do processo e %d", p, teto)
	}
}

// Sem vagas configuradas, nada muda — o modo local de `bravis run` nao deve
// ganhar um limite que ninguem pediu.
func TestSemVagasNaoHaLimite(t *testing.T) {
	reg := execution.NewRegistry()
	reg.MustRegister(execution.FuncTask{Nome: "nada", Fn: func(context.Context, execution.Input) error { return nil }})

	w := wf.Workflow{Slug: "livre", Nodes: []wf.Node{
		{ID: "a", Action: "nada"}, {ID: "b", Action: "nada"},
	}}
	if err := (app.Runner{Go: local.NewGoExecutor(reg), Report: &coletor{}}).Run(context.Background(), w); err != nil {
		t.Fatal(err)
	}
}

// O nome do pod inclui a tentativa. Sem isso o retry reencontra o pod da
// tentativa anterior, e como o executor ADOTA pod existente (para nao subir
// dois iguais quando o processo morre no meio) ele fica preso ao pod quebrado.
// Aconteceu em dev: um pod Pending por CPU insuficiente foi readotado a cada
// retry, e a run nunca saiu do lugar.
func TestTentativaChegaNaTask(t *testing.T) {
	var vistas []int
	espiao := &executorEspiao{aoExecutar: func(tk execution.TaskExec) {
		vistas = append(vistas, tk.Tentativa)
	}}

	w := wf.Workflow{Slug: "w", Image: "img", Nodes: []wf.Node{{ID: "a", Run: "x"}}}
	_ = app.Runner{
		Pods: espiao, Report: &coletor{},
		MaxTentativas: 3, BackoffBase: time.Millisecond,
	}.Run(context.Background(), w)

	if len(vistas) != 3 {
		t.Fatalf("tentativas observadas: %v", vistas)
	}
	for i, n := range vistas {
		if n != i {
			t.Errorf("tentativa %d chegou como %d; o nome do pod repetiria", i, n)
		}
	}
}

// executorEspiao registra o que recebeu e sempre falha, para exercitar o retry.
type executorEspiao struct{ aoExecutar func(execution.TaskExec) }

func (e *executorEspiao) Name() string { return "espiao" }
func (e *executorEspiao) Execute(_ context.Context, t execution.TaskExec) (<-chan execution.Event, error) {
	e.aoExecutar(t)
	ch := make(chan execution.Event, 1)
	ch <- execution.Event{Kind: execution.EventFailed, NodeID: t.NodeID, Message: "falhou"}
	close(ch)
	return ch, nil
}
func (e *executorEspiao) Cancel(context.Context, string) error { return nil }
