// Package run e o modelo de dominio de uma execucao e seus passos.
//
// A secao 7 do plano e explicita: "nao utilizar simples booleans como
// running = true". Estados sao um tipo, e transicoes sao validadas — um `Run`
// nao pode ir de SUCCESS para RUNNING nem por descuido nem por corrida.
package run

import "fmt"

// Status e o estado de uma execucao.
type Status string

const (
	StatusCreated  Status = "created"
	StatusQueued   Status = "queued"
	StatusRunning  Status = "running"
	StatusSuccess  Status = "success"
	StatusFailed   Status = "failed"
	StatusRetrying Status = "retrying"
	StatusCanceled Status = "canceled"
)

// transicoes declara o grafo da secao 7. Manter como dado, e nao como cadeia de
// ifs, torna a maquina inspecionavel e o teste exaustivo trivial.
var transicoes = map[Status][]Status{
	StatusCreated:  {StatusQueued, StatusCanceled},
	StatusQueued:   {StatusRunning, StatusCanceled},
	StatusRunning:  {StatusSuccess, StatusFailed, StatusCanceled},
	StatusFailed:   {StatusRetrying},
	StatusRetrying: {StatusQueued, StatusCanceled},

	// terminais: sem saida. SUCCESS nao volta, e CANCELED tambem nao —
	// re-executar cria um Run novo, preservando o historico do anterior.
	StatusSuccess:  {},
	StatusCanceled: {},
}

// Terminal diz se o estado encerra a vida da execucao.
//
// FAILED nao e terminal: ele pode ir para RETRYING. Quem decide se ainda ha
// tentativa e a politica de retry, nao a maquina de estados.
func (s Status) Terminal() bool {
	return s == StatusSuccess || s == StatusCanceled
}

// PodeIr diz se a transicao e permitida.
func (s Status) PodeIr(destino Status) bool {
	for _, d := range transicoes[s] {
		if d == destino {
			return true
		}
	}
	return false
}

// ErrTransicaoInvalida carrega os dois estados para que o erro diga o que
// aconteceu, e nao apenas que algo foi recusado.
type ErrTransicaoInvalida struct {
	De, Para Status
}

func (e ErrTransicaoInvalida) Error() string {
	return fmt.Sprintf("transicao invalida: %s -> %s", e.De, e.Para)
}

// Valida devolve erro se a transicao nao existir no grafo.
func Valida(de, para Status) error {
	if _, conhecido := transicoes[de]; !conhecido {
		return fmt.Errorf("estado desconhecido: %q", de)
	}
	if !de.PodeIr(para) {
		return ErrTransicaoInvalida{De: de, Para: para}
	}
	return nil
}
