package sdk

import (
	"errors"
	"strings"
	"testing"
)

// The messages are asserted as strings on purpose.
//
// FormatError used to carry a Format field that was interpolated into the
// message and never filled at any of the four sites that build the error, so
// every format error read "formato  from ..." with a hole in it -- and no test
// noticed, because no test looked at the message. These do.
func TestFormatErrorMessage(t *testing.T) {
	casos := []struct {
		nome string
		err  *FormatError
		quer string
	}{
		{
			nome: "sem registro",
			err:  &FormatError{URL: "https://api.example.com/v1", Line: -1, Cause: errors.New("not a JSON object")},
			quer: "format error in https://api.example.com/v1: not a JSON object",
		},
		{
			nome: "com registro",
			err:  &FormatError{URL: "https://api.example.com/v1", Line: 7, Cause: errors.New("building source_key: missing lat")},
			quer: "format error in https://api.example.com/v1, record 7: building source_key: missing lat",
		},
	}

	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			got := c.err.Error()
			if got != c.quer {
				t.Errorf("mensagem\n  got:  %q\n  want: %q", got, c.quer)
			}
			// Um buraco na mensagem e o defeito que este teste existe para
			// pegar: dois espacos seguidos, ou uma virgula solta.
			if strings.Contains(got, "  ") {
				t.Errorf("a mensagem tem espaco duplo, sinal de campo vazio interpolado: %q", got)
			}
			if !errors.Is(c.err, ErrFormat) {
				t.Error("FormatError deve satisfazer errors.Is(err, ErrFormat)")
			}
			if !errors.Is(c.err, c.err.Cause) {
				t.Error("FormatError deve desembrulhar para a causa")
			}
		})
	}
}

func TestSourceErrorMessageHasNoHole(t *testing.T) {
	e := &SourceError{URL: "https://api.example.com/v1", Attempts: 3, Cause: errors.New("connection reset")}
	got := e.Error()
	if strings.Contains(got, "  ") {
		t.Errorf("espaco duplo em SourceError: %q", got)
	}
	if !strings.Contains(got, "3 attempt") {
		t.Errorf("a mensagem deve dizer quantas tentativas houve: %q", got)
	}
}
