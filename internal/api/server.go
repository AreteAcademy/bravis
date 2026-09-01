// Package api expoe a interface HTTP do Bravis.
//
// Usa net/http puro. O roteamento por metodo e path do ServeMux (Go 1.22+)
// cobre o que precisamos, e a regra 6 do plano pede evitar framework quando a
// stdlib resolve. O trabalho dificil deste sistema esta na fila, no scheduler e
// na maquina de estados — nao no HTTP.
package api

import (
	"github.com/zarvhq/bravis/internal/auth"
	"log/slog"
	"net/http"
	"time"
)

// Server carrega o roteador e as dependencias que o readiness consulta.
type Server struct {
	log      *slog.Logger
	checkers map[string]Checker
	mux      *http.ServeMux

	// portao envolve o mux quando ha credencial. Nulo = interface aberta, que
	// so acontece em desenvolvimento (config.Load recusa o contrario).
	portao *auth.Portao
}

// NewServer monta o roteador. Os checkers sao nomeados para que o /ready diga
// QUAL dependencia falhou, e nao apenas que algo falhou.
//
// `ui` pode ser nil: um processo que so serve health check nao precisa das
// paginas, e exigi-las acoplaria o servidor ao banco sem necessidade.
func NewServer(log *slog.Logger, checkers map[string]Checker, ui *UI) *Server {
	return NewServerAutenticado(log, checkers, ui, auth.Credencial{}, false)
}

// NewServerAutenticado e o mesmo, exigindo sessao quando a credencial esta
// configurada. `inseguro` manda o cookie sem a flag Secure — necessario apenas
// para http puro em desenvolvimento, porque um cookie Secure nunca chega de
// volta por http e o login pareceria simplesmente nao funcionar.
func NewServerAutenticado(log *slog.Logger, checkers map[string]Checker, ui *UI,
	cred auth.Credencial, inseguro bool,
) *Server {
	s := &Server{log: log, checkers: checkers, mux: http.NewServeMux()}
	s.mux.HandleFunc("GET /health", s.health)
	s.mux.HandleFunc("GET /ready", s.ready)
	if ui != nil {
		ui.Registrar(s.mux)
	}
	if cred.Ativa() {
		s.portao = &auth.Portao{Cred: cred, Proximo: s.mux, Inseguro: inseguro}
		if ui != nil {
			ui.RegistrarLogin(s.mux, s.portao)
		}
	}
	return s
}

// ServeHTTP faz do Server um http.Handler, com log de acesso.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	inicio := time.Now()
	rec := &gravador{ResponseWriter: w, status: http.StatusOK}
	if s.portao != nil {
		s.portao.ServeHTTP(rec, r)
	} else {
		s.mux.ServeHTTP(rec, r)
	}

	s.log.Info("http",
		"method", r.Method, "path", r.URL.Path,
		"status", rec.status, "duration_ms", time.Since(inicio).Milliseconds())
}

// HTTPServer devolve o servidor configurado. Os timeouts existem porque o
// default do net/http e nenhum: sem eles, uma conexao lenta segura um handler
// indefinidamente.
func (s *Server) HTTPServer(addr string) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           s,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
}

// gravador captura o status para o log de acesso; o http.ResponseWriter nao o
// expoe depois de escrito.
type gravador struct {
	http.ResponseWriter
	status int
}

func (g *gravador) WriteHeader(codigo int) {
	g.status = codigo
	g.ResponseWriter.WriteHeader(codigo)
}
