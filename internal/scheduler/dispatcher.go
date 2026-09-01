// Package scheduler contem o dispatcher da secao 27 do plano.
//
// O desenho e o da secao 8: fila PERSISTENTE (Postgres) mais dispatcher EM
// MEMORIA. O dispatcher nao guarda trabalho — ele reivindica da fila, executa e
// devolve o resultado. Se o processo morrer, os itens reivindicados voltam a
// ficar livres pelo `Recuperar`, e nada se perde.
package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	dom "github.com/zarvhq/bravis/internal/domain/run"
	"github.com/zarvhq/bravis/internal/notify"
	"github.com/zarvhq/bravis/internal/queue"
)

// Executar roda um Run. Injetado para que o dispatcher nao conheca executor,
// grafo nem YAML — o que torna a concorrencia testavel sem processo de verdade.
type Executar func(ctx context.Context, runID uuid.UUID) error

// Repo e o que o dispatcher precisa da persistencia. Interface pequena e
// declarada aqui, no consumidor, e nao no pacote que a implementa.
type Repo interface {
	Transicionar(ctx context.Context, id uuid.UUID, para dom.Status) error
	IncrementarTentativa(ctx context.Context, id uuid.UUID) (int, error)
	RegistrarErro(ctx context.Context, id uuid.UUID, msg string) error
	Buscar(ctx context.Context, id uuid.UUID) (dom.Run, error)

	// PassoQueFalhou devolve o node e a saida da ultima tentativa que falhou.
	// Alimenta o alerta: sem isto ele diz que algo falhou, e quem esta de
	// plantao precisa abrir a tela para descobrir o que.
	PassoQueFalhou(ctx context.Context, id uuid.UUID) (passo, log string, err error)
}

// Config parametriza o dispatcher.
type Config struct {
	Worker         string
	MaxConcorrente int
	Intervalo      time.Duration
	MaxTentativas  int
	BackoffBase    time.Duration

	// Visibilidade e quanto tempo um item pode ficar reivindicado sem que o
	// worker termine antes de ser considerado orfao. Precisa ser MAIOR que a
	// execucao mais longa esperada: curto demais, o dispatcher rouba de si
	// mesmo um run que ainda esta rodando.
	Visibilidade time.Duration

	// IntervaloRecuperacao e a frequencia da varredura de orfaos.
	IntervaloRecuperacao time.Duration
}

func (c *Config) padroes() {
	if c.Worker == "" {
		c.Worker = "dispatcher"
	}
	if c.MaxConcorrente <= 0 {
		c.MaxConcorrente = 5
	}
	if c.Intervalo <= 0 {
		c.Intervalo = 200 * time.Millisecond
	}
	if c.MaxTentativas <= 0 {
		c.MaxTentativas = 3
	}
	if c.BackoffBase <= 0 {
		c.BackoffBase = time.Second
	}
	if c.Visibilidade <= 0 {
		c.Visibilidade = 15 * time.Minute
	}
	if c.IntervaloRecuperacao <= 0 {
		c.IntervaloRecuperacao = time.Minute
	}
}

// Dispatcher consome a fila respeitando a concorrencia maxima.
type Dispatcher struct {
	cfg      Config
	fila     *queue.Queue
	repo     Repo
	executar Executar
	log      *slog.Logger

	// Alertas avisa quando um run desiste. Nulo = ninguem e avisado.
	Alertas notify.Notificador

	// URLBase da UI, para o link no alerta.
	URLBase string

	mu    sync.Mutex
	emVoo int
	wg    sync.WaitGroup
}

func New(cfg Config, f *queue.Queue, r Repo, e Executar, log *slog.Logger) *Dispatcher {
	cfg.padroes()
	return &Dispatcher{cfg: cfg, fila: f, repo: r, executar: e, log: log}
}

// Run consome a fila ate o contexto ser cancelado, e entao espera o trabalho em
// voo terminar antes de retornar.
func (d *Dispatcher) Run(ctx context.Context) error {
	tick := time.NewTicker(d.cfg.Intervalo)
	defer tick.Stop()

	// Varredura de orfaos num ticker proprio, muito mais lento que o de claim:
	// e uma rede de seguranca, nao caminho quente.
	recuperacao := time.NewTicker(d.cfg.IntervaloRecuperacao)
	defer recuperacao.Stop()

	for {
		select {
		case <-ctx.Done():
			d.wg.Wait() // shutdown gracioso: nao abandona execucao em voo
			return nil
		case <-tick.C:
			if err := d.cicloDeClaim(ctx); err != nil {
				d.log.Error("ciclo de claim", "erro", err)
			}
		case <-recuperacao.C:
			if n, err := d.RecuperarOrfaos(ctx); err != nil {
				d.log.Error("recuperando orfaos", "erro", err)
			} else if n > 0 {
				d.log.Warn("runs orfas recuperadas", "quantidade", n)
			}
		}
	}
}

// RecuperarOrfaos devolve a fila o que ficou preso num worker que morreu.
//
// Era o modo de falha aberto desde a PHASE 2: `Queue.Recuperar` existia e
// ninguem a chamava. Na pratica, matar o processo no meio de uma execucao
// deixava o item reivindicado para sempre E o Run em "running" para sempre — a
// tela mostrava trabalho em curso que nao existia mais.
//
// O orfao e tratado como FALHA daquela tentativa, e nao como reenfileiramento
// direto, por dois motivos: a maquina de estados nao tem aresta running -> queued
// (secao 7), e um worker que morre no meio consumiu uma tentativa de verdade —
// contabiliza-la e o que impede um run venenoso de derrubar workers em ciclo.
func (d *Dispatcher) RecuperarOrfaos(ctx context.Context) (int, error) {
	itens, err := d.fila.Recuperar(ctx, d.cfg.Visibilidade)
	if err != nil {
		return 0, err
	}
	for _, it := range itens {
		d.falhar(ctx, it, errOrfao{worker: d.cfg.Worker, limite: d.cfg.Visibilidade})
	}
	return len(itens), nil
}

// errOrfao explica na propria mensagem por que o run falhou — e o texto que o
// operador le na tela, e "erro desconhecido" ali custa uma investigacao inteira.
type errOrfao struct {
	worker string
	limite time.Duration
}

func (e errOrfao) Error() string {
	return fmt.Sprintf("execucao orfa: nenhum worker deu sinal em %s "+
		"(o processo que a reivindicou provavelmente caiu)", e.limite)
}

// cicloDeClaim pede a fila APENAS as vagas livres.
//
// E aqui que a concorrencia e imposta, e o motivo de ser confiavel: nao existe
// caminho em que mais itens saiam da fila do que o limite permite, porque quem
// conta as vagas e quem faz o pedido. Um semaforo depois do claim deixaria itens
// reivindicados e parados, invisiveis para outros workers.
func (d *Dispatcher) cicloDeClaim(ctx context.Context) error {
	d.mu.Lock()
	vagas := d.cfg.MaxConcorrente - d.emVoo
	d.mu.Unlock()

	if vagas <= 0 {
		return nil
	}

	itens, err := d.fila.Claim(ctx, d.cfg.Worker, vagas)
	if err != nil {
		return err
	}

	for _, it := range itens {
		d.mu.Lock()
		d.emVoo++
		d.mu.Unlock()

		d.wg.Add(1)
		go func(it queue.Item) {
			defer d.wg.Done()
			defer func() {
				d.mu.Lock()
				d.emVoo--
				d.mu.Unlock()
			}()
			d.processar(ctx, it)
		}(it)
	}
	return nil
}

func (d *Dispatcher) processar(ctx context.Context, it queue.Item) {
	if err := d.repo.Transicionar(ctx, it.RunID, dom.StatusRunning); err != nil {
		// Transicao invalida aqui significa que outro dispatcher pegou o mesmo
		// run, ou que ele foi cancelado. Nao e erro nosso: solta e segue.
		d.log.Warn("nao pude marcar running", "run", it.RunID, "erro", err)
		_ = d.fila.Release(ctx, it.ID, 0)
		return
	}

	err := d.executar(ctx, it.RunID)
	if err == nil {
		if err := d.repo.Transicionar(ctx, it.RunID, dom.StatusSuccess); err != nil {
			d.log.Error("marcando success", "run", it.RunID, "erro", err)
		}
		_ = d.fila.Done(ctx, it.ID)
		return
	}

	d.falhar(ctx, it, err)
}

// falhar decide entre retry e desistencia.
func (d *Dispatcher) falhar(ctx context.Context, it queue.Item, causa error) {
	_ = d.repo.RegistrarErro(ctx, it.RunID, causa.Error())
	if err := d.repo.Transicionar(ctx, it.RunID, dom.StatusFailed); err != nil {
		d.log.Error("marcando failed", "run", it.RunID, "erro", err)
		_ = d.fila.Done(ctx, it.ID)
		return
	}

	tentativa, err := d.repo.IncrementarTentativa(ctx, it.RunID)
	if err != nil {
		d.log.Error("incrementando tentativa", "run", it.RunID, "erro", err)
		_ = d.fila.Done(ctx, it.ID)
		return
	}

	if tentativa >= d.cfg.MaxTentativas {
		// Esgotou: sai da fila e fica em FAILED, que nao e terminal na maquina
		// de estados mas e o fim desta execucao.
		d.log.Warn("tentativas esgotadas", "run", it.RunID, "tentativas", tentativa)
		// O alerta sai AQUI, e nao a cada falha: avisar em toda tentativa
		// transformaria um retry bem-sucedido em dois alertas e um silencio, e
		// canal que grita a toa deixa de ser lido.
		d.avisar(ctx, it.RunID, tentativa, causa)
		_ = d.fila.Done(ctx, it.ID)
		return
	}

	if err := d.repo.Transicionar(ctx, it.RunID, dom.StatusRetrying); err != nil {
		d.log.Error("marcando retrying", "run", it.RunID, "erro", err)
		_ = d.fila.Done(ctx, it.ID)
		return
	}
	if err := d.repo.Transicionar(ctx, it.RunID, dom.StatusQueued); err != nil {
		d.log.Error("reenfileirando", "run", it.RunID, "erro", err)
		_ = d.fila.Done(ctx, it.ID)
		return
	}

	// Backoff exponencial. O item volta a fila com atraso, e nao imediatamente:
	// retry instantaneo contra dependencia fora do ar so gasta a fila.
	atraso := d.cfg.BackoffBase * time.Duration(1<<uint(tentativa-1))
	d.log.Info("reenfileirado", "run", it.RunID, "tentativa", tentativa, "atraso", atraso)
	if err := d.fila.Release(ctx, it.ID, atraso); err != nil {
		d.log.Error("devolvendo a fila", "run", it.RunID, "erro", err)
	}
}

// avisar manda o alerta de falha definitiva.
//
// Nada aqui pode interromper o dispatcher: um webhook fora do ar nao e motivo
// para parar de consumir a fila. Falha ao avisar vira log, e o estado do run no
// banco continua sendo a fonte da verdade.
func (d *Dispatcher) avisar(ctx context.Context, runID uuid.UUID, tentativas int, causa error) {
	if d.Alertas == nil {
		return
	}

	a := notify.Alerta{
		RunID: runID.String(), Status: string(dom.StatusFailed),
		Tentativas: tentativas, Erro: causa.Error(), URLBase: d.URLBase,
	}
	// Os detalhes vem do banco: o dispatcher so conhece o id. Se a leitura
	// falhar, o alerta sai mesmo assim — meia mensagem e melhor que nenhuma
	// quando algo esta quebrado.
	if r, err := d.repo.Buscar(ctx, runID); err == nil {
		a.Workflow, a.Trigger, a.LogicalDate = r.WorkflowSlug, r.TriggerType, r.LogicalDate
		var def struct{ Tags []string }
		if json.Unmarshal(r.Definicao, &def) == nil {
			a.Tags = def.Tags
		}
	} else {
		d.log.Warn("alerta sem detalhes do run", "run", runID, "erro", err)
	}

	// O passo e o log sao um plus: se a consulta falhar, o alerta sai sem eles.
	// Meia mensagem chega; mensagem nenhuma, nao.
	if passo, log, err := d.repo.PassoQueFalhou(ctx, runID); err == nil {
		a.Passo = passo
		a.TrechoDoLog = ultimasLinhas(log, 15)
	} else {
		d.log.Warn("alerta sem o passo que falhou", "run", runID, "erro", err)
	}

	// Contexto proprio: o da execucao pode estar cancelado (foi o cancelamento
	// que trouxe ate aqui), e o alerta e justamente sobre isso.
	ctxAviso, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := d.Alertas.Falhou(ctxAviso, a); err != nil {
		d.log.Error("nao consegui avisar da falha", "run", runID, "erro", err)
	}
}

// ultimasLinhas devolve o FIM do log, que e onde um programa costuma dizer por
// que parou. O comeco fica de fora de proposito: o alerta cabe numa notificacao
// de celular, e o log inteiro esta a um clique de distancia na tela da execucao.
func ultimasLinhas(texto string, n int) string {
	if texto == "" {
		return ""
	}
	linhas := strings.Split(strings.TrimRight(texto, "\n"), "\n")
	if len(linhas) > n {
		linhas = linhas[len(linhas)-n:]
	}
	return strings.Join(linhas, "\n")
}

// EmVoo devolve quantas execucoes estao correndo agora.
func (d *Dispatcher) EmVoo() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.emVoo
}
