package run

import (
	"errors"
	"testing"
	"time"
)

// O caminho feliz da secao 7: created -> queued -> running -> success.
func TestCaminhoFeliz(t *testing.T) {
	r := &Run{Status: StatusCreated}
	agora := time.Now()

	for _, s := range []Status{StatusQueued, StatusRunning, StatusSuccess} {
		if err := r.Transition(s, agora); err != nil {
			t.Fatalf("transicao para %s: %v", s, err)
		}
	}
	if r.IniciadoEm == nil || r.TerminadoEm == nil {
		t.Error("running e success devem carimbar os tempos")
	}
}

// O ciclo de retry: failed -> retrying -> queued, e de volta a running.
func TestCicloDeRetry(t *testing.T) {
	r := &Run{Status: StatusRunning}
	agora := time.Now()

	for _, s := range []Status{StatusFailed, StatusRetrying, StatusQueued, StatusRunning} {
		if err := r.Transition(s, agora); err != nil {
			t.Fatalf("transicao para %s: %v", s, err)
		}
	}
}

// Reenfileirar limpa os carimbos: a tentativa nova nao herda os tempos da
// anterior, senao a duracao reportada seria a de outra execucao.
func TestReenfileirarLimpaCarimbos(t *testing.T) {
	agora := time.Now()
	r := &Run{Status: StatusCreated}
	_ = r.Transition(StatusQueued, agora)
	_ = r.Transition(StatusRunning, agora)
	_ = r.Transition(StatusFailed, agora)
	_ = r.Transition(StatusRetrying, agora)
	_ = r.Transition(StatusQueued, agora)

	if r.IniciadoEm != nil || r.TerminadoEm != nil {
		t.Error("reenfileirado deve limpar os carimbos da tentativa anterior")
	}
}

// A secao 7 exige que transicao invalida devolva erro — nao que seja ignorada.
func TestTransicoesInvalidasSaoRecusadas(t *testing.T) {
	casos := []struct{ de, para Status }{
		{StatusSuccess, StatusRunning},  // terminal nao volta
		{StatusCanceled, StatusQueued},  // terminal nao volta
		{StatusCreated, StatusRunning},  // nao pula a fila
		{StatusCreated, StatusSuccess},  // nao pula a execucao
		{StatusQueued, StatusSuccess},   // nao termina sem rodar
		{StatusRunning, StatusRetrying}, // so falha vai para retry
		{StatusFailed, StatusRunning},   // retry passa pela fila
	}
	for _, c := range casos {
		err := Valida(c.de, c.para)
		if err == nil {
			t.Errorf("%s -> %s devia ser recusada", c.de, c.para)
			continue
		}
		var inv ErrTransicaoInvalida
		if !errors.As(err, &inv) {
			t.Errorf("%s -> %s devolveu %T, queria ErrTransicaoInvalida", c.de, c.para, err)
		}
	}
}

// FAILED nao e terminal: quem decide se ha nova tentativa e a politica de retry,
// nao a maquina de estados.
func TestFailedNaoEhTerminal(t *testing.T) {
	if StatusFailed.Terminal() {
		t.Error("failed nao pode ser terminal — ele vai para retrying")
	}
	if !StatusSuccess.Terminal() || !StatusCanceled.Terminal() {
		t.Error("success e canceled sao terminais")
	}
}

// Cancelar deve ser possivel de qualquer estado ativo — e so de estados ativos.
func TestCancelamento(t *testing.T) {
	for _, de := range []Status{StatusCreated, StatusQueued, StatusRunning, StatusRetrying} {
		if err := Valida(de, StatusCanceled); err != nil {
			t.Errorf("devia poder cancelar a partir de %s: %v", de, err)
		}
	}
	if err := Valida(StatusSuccess, StatusCanceled); err == nil {
		t.Error("nao se cancela o que ja teve sucesso")
	}
}

func TestEstadoDesconhecido(t *testing.T) {
	if err := Valida(Status("inventado"), StatusQueued); err == nil {
		t.Error("esperava erro para estado desconhecido")
	}
}
