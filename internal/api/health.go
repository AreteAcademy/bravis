package api

import (
	"context"
	"encoding/json"
	"net/http"
)

// Checker e uma dependencia que o readiness consulta. Interface pequena de
// proposito (regra 5): o postgres.Pool ja a satisfaz sem adaptador.
type Checker interface {
	Check(ctx context.Context) error
}

type respostaSaude struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks,omitempty"`
}

// health responde liveness: o processo esta vivo e servindo.
//
// NAO toca o banco, de proposito. Liveness que depende de dependencia externa
// faz o Kubernetes MATAR o pod quando o banco oscila — trocando uma
// indisponibilidade parcial por um crashloop.
func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	escreverJSON(w, http.StatusOK, respostaSaude{Status: "ok"})
}

// ready responde readiness: o processo consegue atender de fato.
//
// Aqui sim consulta as dependencias. Falha tira o pod do balanceador sem
// mata-lo, que e o comportamento correto quando o banco esta fora.
func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	checks := make(map[string]string, len(s.checkers))
	status := http.StatusOK

	for nome, c := range s.checkers {
		if err := c.Check(r.Context()); err != nil {
			checks[nome] = err.Error()
			status = http.StatusServiceUnavailable
			continue
		}
		checks[nome] = "ok"
	}

	corpo := respostaSaude{Status: "ok", Checks: checks}
	if status != http.StatusOK {
		corpo.Status = "unavailable"
	}
	escreverJSON(w, status, corpo)
}

func escreverJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
