package core

import "fmt"

// SourceFailure diz qual origem falhou e por quê.
//
// Ela existe porque num fan-out de milhares de origens, "a execução falhou" não
// é informação: o que resolve é saber QUAIS falharam, para reprocessar essas e
// não as outras. Sem isso, a próxima execução refaz tudo -- e num fan-out de
// 4.803 origens, refazer as 3.000 que já tinham dado certo é o custo real.
type SourceFailure struct {
	// Source é o Describe() da origem, já sem segredo.
	Source string

	// Err é a mensagem. Texto e não error porque isto atravessa o Result e é
	// serializado por quem observa a execução.
	Err string
}

func (f SourceFailure) String() string { return f.Source + ": " + f.Err }

// FailurePolicy diz o que uma fonte composta faz quando uma origem falha.
type FailurePolicy int

const (
	// AbortOnError para na primeira falha. É o padrão, e continua sendo: é o
	// comportamento que o SDK sempre teve, e mudá-lo em silêncio faria uma
	// execução que hoje falha passar a "dar certo" com metade do dado.
	AbortOnError FailurePolicy = iota

	// ContinueOnError registra qual origem falhou e segue para a próxima.
	//
	// Espelha a política que o load já tem: ele tolera uma linha ruim e a
	// reporta em ErrorRows. A assimetria -- o load tolerando e o extract não --
	// é o que este modo corrige.
	//
	// As falhas chegam em Stats.FailedSources e em Result.FailedSources. Uma
	// execução que perdeu 3.000 de 4.803 origens e não diz quais não é
	// tolerância a falha: é perda silenciosa.
	ContinueOnError
)

func (p FailurePolicy) String() string {
	if p == ContinueOnError {
		return "continue"
	}
	return "abort"
}

// ErrTodasAsFontesFalharam é devolvido quando ContinueOnError tolerou TODAS as
// origens.
//
// Zero registro de N origens boas é um resultado; zero registro porque as N
// falharam é uma execução quebrada, e as duas não podem parecer a mesma coisa
// para quem lê o log.
func ErrTodasAsFontesFalharam(n int, primeira SourceFailure) error {
	return fmt.Errorf("as %d origens falharam, e nenhum registro foi lido. A primeira: %s",
		n, primeira)
}
