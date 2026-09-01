package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

type checkerFalso struct{ err error }

func (c checkerFalso) Check(context.Context) error { return c.err }

func servidorDeTeste(checkers map[string]Checker) *Server {
	return NewServer(slog.New(slog.NewTextHandler(io.Discard, nil)), checkers)
}

// O liveness nao pode depender do banco: se dependesse, uma oscilacao do
// Postgres faria o Kubernetes matar o pod em vez de so tira-lo do balanceador.
func TestHealthIgnoraDependenciaQuebrada(t *testing.T) {
	s := servidorDeTeste(map[string]Checker{
		"postgres": checkerFalso{err: errors.New("conexao recusada")},
	})

	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, queria 200 mesmo com o banco fora", rec.Code)
	}
}

func TestReadyOkQuandoTudoResponde(t *testing.T) {
	s := servidorDeTeste(map[string]Checker{"postgres": checkerFalso{}})

	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ready", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, queria 200", rec.Code)
	}
	var corpo respostaSaude
	if err := json.Unmarshal(rec.Body.Bytes(), &corpo); err != nil {
		t.Fatal(err)
	}
	if corpo.Checks["postgres"] != "ok" {
		t.Errorf("checks[postgres] = %q, queria ok", corpo.Checks["postgres"])
	}
}

func TestReadyFalhaEDizQualDependencia(t *testing.T) {
	s := servidorDeTeste(map[string]Checker{
		"postgres": checkerFalso{err: errors.New("conexao recusada")},
	})

	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ready", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, queria 503", rec.Code)
	}
	var corpo respostaSaude
	if err := json.Unmarshal(rec.Body.Bytes(), &corpo); err != nil {
		t.Fatal(err)
	}
	// Nomear a dependencia e o ponto: "unavailable" sozinho nao diz onde olhar.
	if corpo.Checks["postgres"] != "conexao recusada" {
		t.Errorf("checks[postgres] = %q, queria a causa", corpo.Checks["postgres"])
	}
}

func TestMetodoErradoNaoCasa(t *testing.T) {
	s := servidorDeTeste(nil)

	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/health", nil))

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, queria 405 — o ServeMux do Go 1.22+ casa por metodo", rec.Code)
	}
}
