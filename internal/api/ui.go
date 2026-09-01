package api

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/a-h/templ"

	"github.com/zarvhq/bravis/internal/infrastructure/postgres"
	"github.com/zarvhq/bravis/web/pages"
)

// Leitura e o que a UI precisa do banco. Interface declarada aqui, no consumidor.
type Leitura interface {
	ContagemPorStatus(ctx context.Context) (map[string]int, error)
	UltimasRuns(ctx context.Context, limite int) ([]postgres.ResumoRun, error)
	Workflows(ctx context.Context) ([]postgres.ResumoWorkflow, error)
	Projetos(ctx context.Context) ([]postgres.ResumoProjeto, error)
	ProfundidadeDaFila(ctx context.Context) (int, int, error)
}

// UI registra as paginas server-rendered.
type UI struct {
	leitura Leitura
	log     *slog.Logger
}

func NewUI(l Leitura, log *slog.Logger) *UI { return &UI{leitura: l, log: log} }

// Registrar liga as rotas ao mux.
func (u *UI) Registrar(mux *http.ServeMux) {
	mux.HandleFunc("GET /{$}", u.dashboard) // {$} casa a raiz EXATA, nao o prefixo
	mux.HandleFunc("GET /runs", u.runs)
	mux.HandleFunc("GET /workflows", u.workflows)
	mux.HandleFunc("GET /projects", u.projetos)
	mux.Handle("GET /assets/", http.StripPrefix("/assets/", http.FileServer(http.Dir("web/assets"))))
}

func (u *UI) dashboard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	status, err := u.leitura.ContagemPorStatus(ctx)
	if err != nil {
		u.erro(w, r, err)
		return
	}
	pendentes, reivindicados, err := u.leitura.ProfundidadeDaFila(ctx)
	if err != nil {
		u.erro(w, r, err)
		return
	}
	ultimas, err := u.leitura.UltimasRuns(ctx, 15)
	if err != nil {
		u.erro(w, r, err)
		return
	}
	u.render(w, r, pages.Dashboard(status, pendentes, reivindicados, ultimas))
}

func (u *UI) runs(w http.ResponseWriter, r *http.Request) {
	runs, err := u.leitura.UltimasRuns(r.Context(), 100)
	if err != nil {
		u.erro(w, r, err)
		return
	}
	u.render(w, r, pages.Runs(runs))
}

func (u *UI) workflows(w http.ResponseWriter, r *http.Request) {
	ws, err := u.leitura.Workflows(r.Context())
	if err != nil {
		u.erro(w, r, err)
		return
	}
	u.render(w, r, pages.Workflows(ws))
}

func (u *UI) projetos(w http.ResponseWriter, r *http.Request) {
	ps, err := u.leitura.Projetos(r.Context())
	if err != nil {
		u.erro(w, r, err)
		return
	}
	u.render(w, r, pages.Projetos(ps))
}

func (u *UI) render(w http.ResponseWriter, r *http.Request, c templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := c.Render(r.Context(), w); err != nil {
		// O header ja foi enviado: nao da para trocar o status agora. Registrar
		// e o que resta, e e melhor que engolir em silencio.
		u.log.Error("renderizando pagina", "path", r.URL.Path, "erro", err)
	}
}

// erro devolve 500 sem vazar detalhe interno para a pagina.
func (u *UI) erro(w http.ResponseWriter, r *http.Request, err error) {
	u.log.Error("consultando dados da ui", "path", r.URL.Path, "erro", err)
	http.Error(w, "erro interno", http.StatusInternalServerError)
}
