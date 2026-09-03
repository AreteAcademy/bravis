package sdk

import (
	"errors"
	"fmt"
)

// Three kinds of failure, because they call for three different actions when
// a pipeline pages someone at night:
//
//	ErroDeFonte    the API failed         -> wait and retry, or check the vendor
//	ErroDeFormato  it answered, but the   -> fix the parser or the expansion;
//	               body does not parse       retrying will not help
//	ErroDeDestino  BigQuery refused       -> look at the schema or permissions
//
// A flat error forces reading the message to decide. Use errors.As:
//
//	var e *ErroDeFonte
//	if errors.As(err, &e) { ... }
var (
	// ErrFonte, ErrFormato and ErrDestino allow errors.Is on the category
	// alone, when the details do not matter.
	ErrFonte   = errors.New("erro de fonte")
	ErrFormato = errors.New("erro de formato")
	ErrDestino = errors.New("erro de destino")
)

// ErroDeFonte means the source could not be reached or refused the request:
// network failure, timeout, or an HTTP status the SDK does not retry.
type ErroDeFonte struct {
	URL        string // already redacted
	Status     int    // 0 when the request never got a response
	Tentativas int
	Causa      error
}

func (e *ErroDeFonte) Error() string {
	if e.Status > 0 {
		return fmt.Sprintf("fonte %s respondeu %d após %d tentativa(s): %v",
			e.URL, e.Status, e.Tentativas, e.Causa)
	}
	return fmt.Sprintf("fonte %s falhou após %d tentativa(s): %v", e.URL, e.Tentativas, e.Causa)
}

func (e *ErroDeFonte) Unwrap() error { return e.Causa }
func (e *ErroDeFonte) Is(alvo error) bool {
	return alvo == ErrFonte
}

// ErroDeFormato means the response arrived but could not be understood: a
// decode failure, a guard rejection, or an expansion that did not fit.
type ErroDeFormato struct {
	URL     string // already redacted
	Formato string
	Linha   int // -1 when it is not about a specific record
	Causa   error
}

func (e *ErroDeFormato) Error() string {
	if e.Linha >= 0 {
		return fmt.Sprintf("formato %s de %s, registro %d: %v", e.Formato, e.URL, e.Linha, e.Causa)
	}
	return fmt.Sprintf("formato %s de %s: %v", e.Formato, e.URL, e.Causa)
}

func (e *ErroDeFormato) Unwrap() error { return e.Causa }
func (e *ErroDeFormato) Is(alvo error) bool {
	return alvo == ErrFormato
}

// ErroDeDestino means BigQuery refused the write. Linhas carries the per-row
// diagnostics the job reported, which is usually where the actual answer is.
type ErroDeDestino struct {
	Tabela string
	Linhas []string
	Causa  error
}

func (e *ErroDeDestino) Error() string {
	if len(e.Linhas) > 0 {
		return fmt.Sprintf("destino %s recusou: %v (%d diagnóstico(s) por linha)",
			e.Tabela, e.Causa, len(e.Linhas))
	}
	return fmt.Sprintf("destino %s recusou: %v", e.Tabela, e.Causa)
}

func (e *ErroDeDestino) Unwrap() error { return e.Causa }
func (e *ErroDeDestino) Is(alvo error) bool {
	return alvo == ErrDestino
}
