package api

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/a-h/templ"
	"github.com/google/uuid"

	"github.com/zarvhq/bravis/internal/branding"
	"github.com/zarvhq/bravis/internal/domain/run"
	sch "github.com/zarvhq/bravis/internal/domain/schedule"
	wf "github.com/zarvhq/bravis/internal/domain/workflow"
	"github.com/zarvhq/bravis/internal/infrastructure/postgres"
	"github.com/zarvhq/bravis/web/assets"
	"github.com/zarvhq/bravis/web/pages"
)

// janelaOverview e o horizonte do dashboard. Vinte e quatro horas cobrem o ciclo
// diario completo — a maioria das agendas e diaria, e uma janela menor mostraria
// so um pedaco do dia e faria a taxa de sucesso oscilar por recorte, nao por
// mudanca real.
const janelaOverview = 24 * time.Hour

// Leitura e o que a UI precisa do banco. Interface declarada aqui, no consumidor.
type Leitura interface {
	Indicadores(ctx context.Context, janela time.Duration) (postgres.Indicadores, error)
	ExecucoesPorHora(ctx context.Context, horas int) ([]postgres.Balde, error)
	EmAndamento(ctx context.Context, limite int) ([]postgres.ResumoRun, error)
	UltimasRuns(ctx context.Context, limite int) ([]postgres.ResumoRun, error)
	Runs(ctx context.Context, f postgres.FiltroRuns) ([]postgres.ResumoRun, error)
	ContarRuns(ctx context.Context, f postgres.FiltroRuns) (int, error)
	RunsDoWorkflow(ctx context.Context, slug string, limite int) ([]postgres.ResumoRun, error)
	Workflows(ctx context.Context) ([]postgres.ResumoWorkflow, error)
	Agendas(ctx context.Context) ([]postgres.AgendaResumo, error)
	Projetos(ctx context.Context) ([]postgres.ResumoProjeto, error)
	ProfundidadeDaFila(ctx context.Context) (int, int, error)
}

// Definicoes le a definicao publicada de um workflow. Separada de `Leitura`
// porque devolve o dominio, nao uma projecao de tela.
type Definicoes interface {
	Definicao(ctx context.Context, slug string) (wf.Workflow, error)
}

// Execucoes le uma Run e o estado de seus passos.
type Execucoes interface {
	Buscar(ctx context.Context, id uuid.UUID) (run.Run, error)
	EstadoDosNos(ctx context.Context, id uuid.UUID) (map[string]postgres.EstadoNo, error)
}

// Acoes sao os dois efeitos que a tela dispara. Interface pequena de proposito:
// a UI nao deve poder fazer mais nada no sistema do que pausar uma agenda e
// mandar rodar agora.
type Acoes interface {
	Alternar(ctx context.Context, slug string) (bool, error)
	Disparar(ctx context.Context, slug string, agora time.Time, params map[string]string) (uuid.UUID, error)
}

// UI registra as paginas server-rendered e o JSON que a ilha React consome.
type UI struct {
	leitura Leitura
	defs    Definicoes
	execs   Execucoes
	acoes   Acoes
	marca   branding.Marca
	log     *slog.Logger
}

func NewUI(l Leitura, d Definicoes, e Execucoes, a Acoes, m branding.Marca, log *slog.Logger) *UI {
	return &UI{leitura: l, defs: d, execs: e, acoes: a, marca: m, log: log}
}

// Registrar liga as rotas ao mux.
func (u *UI) Registrar(mux *http.ServeMux) {
	mux.HandleFunc("GET /{$}", u.overview) // {$} casa a raiz EXATA, nao o prefixo
	mux.HandleFunc("GET /runs", u.runs)
	mux.HandleFunc("GET /workflows", u.workflows)
	mux.HandleFunc("GET /projects", u.projetos)
	mux.HandleFunc("GET /workflows/{slug}", u.workflow)
	mux.HandleFunc("GET /runs/{id}", u.run)

	// Efeitos por POST, nao GET: um link que pausa a agenda seria disparado por
	// qualquer prefetch de navegador ou varredura de link.
	mux.HandleFunc("POST /workflows/{slug}/toggle", u.alternar)
	mux.HandleFunc("POST /workflows/{slug}/trigger", u.disparar)

	// O JSON que a ilha React busca. Fica sob /api para deixar claro, na URL,
	// o que e pagina e o que e dado — o mesmo caminho serve os dois.
	mux.HandleFunc("GET /api/workflows/{slug}/graph", u.grafoDoWorkflow)
	mux.HandleFunc("GET /api/runs/{id}/graph", u.grafoDaRun)

	// Servidos do embed, nao do disco: o container e distroless e nao tem
	// web/assets, e o binario precisa funcionar de qualquer diretorio.
	mux.Handle("GET /assets/", http.StripPrefix("/assets/", http.FileServerFS(assets.FS)))
}

func (u *UI) overview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	ind, err := u.leitura.Indicadores(ctx, janelaOverview)
	if err != nil {
		u.erro(w, r, err)
		return
	}
	baldes, err := u.leitura.ExecucoesPorHora(ctx, int(janelaOverview.Hours()))
	if err != nil {
		u.erro(w, r, err)
		return
	}
	emCurso, err := u.leitura.EmAndamento(ctx, 8)
	if err != nil {
		u.erro(w, r, err)
		return
	}
	recentes, err := u.leitura.UltimasRuns(ctx, 10)
	if err != nil {
		u.erro(w, r, err)
		return
	}
	pendentes, _, err := u.leitura.ProfundidadeDaFila(ctx)
	if err != nil {
		u.erro(w, r, err)
		return
	}
	agendas, err := u.leitura.Agendas(ctx)
	if err != nil {
		u.erro(w, r, err)
		return
	}

	u.render(w, r, pages.Overview(pages.DadosOverview{
		Janela:    janelaOverview,
		Ind:       ind,
		Baldes:    baldes,
		EmCurso:   emCurso,
		Proximas:  proximasExecucoes(agendas, time.Now(), 8, u.log),
		Recentes:  recentes,
		Pendentes: pendentes,
	}))
}

// proximasExecucoes calcula o proximo disparo de cada agenda ativa e devolve os
// mais proximos primeiro.
//
// O calculo fica aqui, e nao no banco: o cron e regra de dominio (`schedule`),
// e reimplementa-lo em SQL criaria uma segunda interpretacao do mesmo campo —
// que um dia divergiria da que o scheduler usa de verdade.
func proximasExecucoes(agendas []postgres.AgendaResumo, agora time.Time, limite int,
	log *slog.Logger) []pages.ProximaExecucao {

	var out []pages.ProximaExecucao
	for _, a := range agendas {
		if !a.Ativo {
			continue
		}
		s := sch.Schedule{WorkflowSlug: a.WorkflowSlug, Cron: a.Cron, Timezone: a.Timezone, Ativo: true}
		prox, err := s.Proximo(agora)
		if err != nil {
			// Cron invalido no banco nao pode derrubar o dashboard inteiro; a
			// agenda apenas nao aparece na lista.
			log.Warn("cron invalido ao calcular proximo disparo",
				"workflow", a.WorkflowSlug, "cron", a.Cron, "erro", err)
			continue
		}
		out = append(out, pages.ProximaExecucao{
			Workflow: a.WorkflowSlug, Cron: a.Cron, Timezone: a.Timezone, Quando: prox,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Quando.Before(out[j].Quando) })
	if len(out) > limite {
		out = out[:limite]
	}
	return out
}

func (u *UI) runs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := pages.FiltroRuns{
		Estado:    estadoValido(q.Get("estado")),
		Workflow:  q.Get("workflow"),
		De:        q.Get("de"),
		Ate:       q.Get("ate"),
		Pagina:    pagina(q.Get("pagina")),
		PorPagina: pages.PorPaginaPadrao,
	}

	de, ate := instante(f.De), instante(f.Ate)
	if de == nil {
		f.De = ""
	}
	if ate == nil {
		f.Ate = ""
	}
	f.Rotulo = rotuloDoPeriodo(de, ate)

	consulta := postgres.FiltroRuns{
		Estado: f.Estado, Workflow: f.Workflow, De: de, Ate: ate,
		Limite: f.PorPagina, Offset: (f.Pagina - 1) * f.PorPagina,
	}
	total, err := u.leitura.ContarRuns(r.Context(), consulta)
	if err != nil {
		u.erro(w, r, err)
		return
	}
	runs, err := u.leitura.Runs(r.Context(), consulta)
	if err != nil {
		u.erro(w, r, err)
		return
	}
	u.render(w, r, pages.Runs(runs, f, total))
}

// estadoValido recusa qualquer estado fora da maquina da secao 7.
//
// Nao e sobre SQL — o valor ja vai parametrizado. E sobre a tela: `?estado=xpto`
// devolveria uma lista vazia com os chips todos apagados, e o operador leria isso
// como "nao ha execucoes" em vez de "o filtro nao existe".
func estadoValido(s string) string {
	switch s {
	case "queued", "running", "success", "failed", "retrying", "canceled":
		return s
	}
	return ""
}

func pagina(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 {
		return 1
	}
	return n
}

// instante aceita o RFC3339 que os links do grafico emitem. Valor invalido vira
// ausencia de filtro, nao erro: um link colado pela metade nao deve dar 500.
func instante(s string) *time.Time {
	if s == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return nil
	}
	return &t
}

// rotuloDoPeriodo descreve a janela em portugues, no fuso de quem le. Sem isso o
// chip de filtro mostraria "2026-09-01T02:00:00Z", que nao e o horario que a
// pessoa viu no grafico.
func rotuloDoPeriodo(de, ate *time.Time) string {
	switch {
	case de != nil && ate != nil:
		l := de.Local()
		if ate.Sub(*de) == time.Hour {
			return l.Format("02/01 15h") + "–" + ate.Local().Format("15h")
		}
		return l.Format("02/01 15:04") + " → " + ate.Local().Format("02/01 15:04")
	case de != nil:
		return "a partir de " + de.Local().Format("02/01 15:04")
	case ate != nil:
		return "até " + ate.Local().Format("02/01 15:04")
	}
	return ""
}

func (u *UI) workflows(w http.ResponseWriter, r *http.Request) {
	todos, err := u.leitura.Workflows(r.Context())
	if err != nil {
		u.erro(w, r, err)
		return
	}

	agora := time.Now()
	for i := range todos {
		todos[i].ProximaRun = proximaDoWorkflow(todos[i], agora)
	}

	q := r.URL.Query()
	f := pages.Filtro{
		Busca:     strings.TrimSpace(q.Get("q")),
		Estado:    estadoValido(q.Get("estado")),
		Ativo:     q.Get("ativo"),
		Tag:       q.Get("tag"),
		Ordem:     ordemValida(q.Get("ordem")),
		Desc:      q.Get("dir") == "desc",
		Pagina:    pagina(q.Get("pagina")),
		PorPagina: pages.PorPaginaPadrao,
	}

	filtrados := filtrar(todos, f)
	ordenar(filtrados, f)
	u.render(w, r, pages.Workflows(recortar(filtrados, f), tagsDe(todos), f,
		len(todos), len(filtrados)))
}

// ordemValida limita a ordenacao as colunas que existem — sem isso, `?ordem=;`
// so produziria uma lista com ordem inexplicada.
func ordemValida(s string) string {
	switch s {
	case "workflow", "agenda", "proxima", "ultima":
		return s
	}
	return ""
}

// ordenar aplica a coluna escolhida.
//
// Duas regras que a inversao ingenua (comparar com os argumentos trocados)
// quebrava: o valor AUSENTE fica por ultimo nas duas direcoes — ordenar por
// "ultima execucao" nao pode comecar por quem nunca rodou — e o desempate pelo
// slug e sempre crescente, senao duas linhas equivalentes trocam de lugar a cada
// carregamento.
func ordenar(ws []postgres.ResumoWorkflow, f pages.Filtro) {
	if f.Ordem == "" {
		return
	}
	sort.SliceStable(ws, func(i, j int) bool {
		a, b := ws[i], ws[j]
		temA, temB := temValor(a, f.Ordem), temValor(b, f.Ordem)
		if temA != temB {
			return temA
		}
		if temA {
			if c := comparaCampo(a, b, f.Ordem); c != 0 {
				if f.Desc {
					return c > 0
				}
				return c < 0
			}
		}
		return a.Slug < b.Slug
	})
}

func temValor(w postgres.ResumoWorkflow, campo string) bool {
	switch campo {
	case "agenda":
		return w.Cron != ""
	case "proxima":
		return w.ProximaRun != nil
	case "ultima":
		return w.UltimaRunEm != nil
	}
	return true
}

func comparaCampo(a, b postgres.ResumoWorkflow, campo string) int {
	switch campo {
	case "agenda":
		return strings.Compare(a.Cron, b.Cron)
	case "proxima":
		return comparaTempo(a.ProximaRun, b.ProximaRun)
	case "ultima":
		return comparaTempo(a.UltimaRunEm, b.UltimaRunEm)
	}
	return strings.Compare(a.Slug, b.Slug)
}

// comparaTempo so recebe valores presentes: a ausencia e decidida antes, em
// `temValor`, justamente para nao depender da direcao.
func comparaTempo(a, b *time.Time) int {
	switch {
	case a.Before(*b):
		return -1
	case a.After(*b):
		return 1
	}
	return 0
}

// recortar devolve a pagina pedida. Pagina alem do fim volta vazia em vez de
// estourar o slice — acontece ao filtrar estando numa pagina alta.
func recortar(ws []postgres.ResumoWorkflow, f pages.Filtro) []postgres.ResumoWorkflow {
	de := (f.Pagina - 1) * f.PorPagina
	if de >= len(ws) {
		return nil
	}
	ate := de + f.PorPagina
	if ate > len(ws) {
		ate = len(ws)
	}
	return ws[de:ate]
}

func proximaDoWorkflow(w postgres.ResumoWorkflow, agora time.Time) *time.Time {
	if !w.Ativo || w.Cron == "" {
		return nil
	}
	s := sch.Schedule{WorkflowSlug: w.Slug, Cron: w.Cron, Timezone: w.Timezone, Ativo: true}
	prox, err := s.Proximo(agora)
	if err != nil {
		return nil
	}
	return &prox
}

// filtrar aplica a barra de busca em memoria.
//
// Em memoria e nao em SQL porque a lista de workflows e da ordem de dezenas, e o
// filtro por ULTIMO estado exigiria repetir o LATERAL da consulta dentro de um
// WHERE. Se um dia forem milhares, isto vira predicado no banco.
func filtrar(ws []postgres.ResumoWorkflow, f pages.Filtro) []postgres.ResumoWorkflow {
	busca := strings.ToLower(f.Busca)
	out := make([]postgres.ResumoWorkflow, 0, len(ws))
	for _, w := range ws {
		if busca != "" && !strings.Contains(strings.ToLower(w.Slug), busca) &&
			!strings.Contains(strings.ToLower(w.Nome), busca) {
			continue
		}
		if f.Estado != "" && w.UltimoStatus != f.Estado {
			continue
		}
		switch f.Ativo {
		case "active":
			if !w.Ativo {
				continue
			}
		case "paused":
			// Sem agenda nao e "pausado": e um workflow que nunca teve cron, e
			// misturar os dois esconderia justamente a agenda desligada.
			if w.Ativo || !w.TemAgenda {
				continue
			}
		}
		if f.Tag != "" && !contem(w.Tags, f.Tag) {
			continue
		}
		out = append(out, w)
	}
	return out
}

func contem(lista []string, alvo string) bool {
	for _, s := range lista {
		if s == alvo {
			return true
		}
	}
	return false
}

// tagsDe junta as tags de TODOS os workflows, nao das linhas filtradas: a barra
// de filtros nao pode encolher conforme se filtra, ou fica impossivel voltar.
func tagsDe(ws []postgres.ResumoWorkflow) []string {
	vistas := map[string]struct{}{}
	var out []string
	for _, w := range ws {
		for _, t := range w.Tags {
			if _, ja := vistas[t]; ja {
				continue
			}
			vistas[t] = struct{}{}
			out = append(out, t)
		}
	}
	sort.Strings(out)
	return out
}

func (u *UI) projetos(w http.ResponseWriter, r *http.Request) {
	ps, err := u.leitura.Projetos(r.Context())
	if err != nil {
		u.erro(w, r, err)
		return
	}
	u.render(w, r, pages.Projetos(ps))
}

// workflow e a pagina de um workflow: cabecalho SSR + a DAG como ilha.
func (u *UI) workflow(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	def, err := u.defs.Definicao(r.Context(), slug)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	// Historico ausente nao impede a tela: a definicao e o conteudo principal.
	ultimas, err := u.leitura.RunsDoWorkflow(r.Context(), slug, 10)
	if err != nil {
		u.log.Warn("historico do workflow indisponivel", "workflow", slug, "erro", err)
	}
	u.render(w, r, pages.Workflow(def, ultimas))
}

// run e a pagina de uma execucao. O cabecalho vem do banco no server; a DAG com
// o estado de cada passo vem por fetch, e so ela precisa de JavaScript.
func (u *UI) run(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	execucao, err := u.execs.Buscar(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	u.render(w, r, pages.Run(execucao))
}

func (u *UI) alternar(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	ativo, err := u.acoes.Alternar(r.Context(), slug)
	if err != nil {
		u.erro(w, r, err)
		return
	}
	u.log.Info("agenda alternada", "workflow", slug, "ativo", ativo)
	u.voltar(w, r)
}

func (u *UI) disparar(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")

	// Os params vem do formulario, prefixados com `param.` para nao colidirem
	// com campos futuros do proprio form.
	if err := r.ParseForm(); err != nil {
		http.Error(w, "formulario invalido", http.StatusBadRequest)
		return
	}
	params := map[string]string{}
	for chave, valores := range r.PostForm {
		if nome, ok := strings.CutPrefix(chave, "param."); ok && len(valores) > 0 {
			params[nome] = valores[0]
		}
	}

	id, err := u.acoes.Disparar(r.Context(), slug, time.Now(), params)
	if err != nil {
		// Param invalido e erro de ENTRADA, nao do servidor: 500 aqui mandaria
		// o operador procurar defeito na plataforma em vez de no valor digitado.
		u.log.Warn("disparo recusado", "workflow", slug, "erro", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if id == uuid.Nil {
		// Chave de idempotencia colidiu: dois cliques no mesmo segundo viram um
		// run so. Voltar para a lista e o comportamento certo — nao ha run novo
		// para onde ir.
		u.log.Info("disparo ignorado por idempotencia", "workflow", slug)
		u.voltar(w, r)
		return
	}
	u.log.Info("run manual criado", "workflow", slug, "run", id)
	http.Redirect(w, r, "/runs/"+id.String(), http.StatusSeeOther)
}

// voltar devolve o operador para a tela de onde ele clicou, preservando filtros.
//
// 303 e nao 302: apos um POST, o 303 obriga o navegador a refazer a navegacao
// como GET, e e o que impede o "reenviar formulario?" ao atualizar a pagina.
func (u *UI) voltar(w http.ResponseWriter, r *http.Request) {
	destino := r.Referer()
	// So aceita destino do proprio site: um Referer externo transformaria o
	// redirect num vetor de redirecionamento aberto.
	if destino == "" || !strings.HasPrefix(destino, "/") {
		if u := parseMesmoHost(r); u != "" {
			destino = u
		} else {
			destino = "/workflows"
		}
	}
	http.Redirect(w, r, destino, http.StatusSeeOther)
}

// parseMesmoHost aceita o Referer apenas quando ele aponta para este mesmo host.
func parseMesmoHost(r *http.Request) string {
	ref := r.Referer()
	if ref == "" {
		return ""
	}
	u, err := url.Parse(ref)
	if err != nil || u.Host != r.Host {
		return ""
	}
	caminho := u.EscapedPath()
	if u.RawQuery != "" {
		caminho += "?" + u.RawQuery
	}
	return caminho
}

func (u *UI) render(w http.ResponseWriter, r *http.Request, c templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// A marca viaja no contexto: todo template a alcanca sem que ela precise
	// entrar na assinatura de cada pagina.
	if err := c.Render(branding.EmContexto(r.Context(), u.marca), w); err != nil {
		u.log.Error("renderizando pagina", "path", r.URL.Path, "erro", err)
	}
}

func (u *UI) erro(w http.ResponseWriter, r *http.Request, err error) {
	u.log.Error("consultando dados da ui", "path", r.URL.Path, "erro", err)
	http.Error(w, "erro interno", http.StatusInternalServerError)
}
