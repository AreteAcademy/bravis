package run

import (
	"time"

	"github.com/google/uuid"
)

// Run e uma execucao de um workflow.
//
// `Definicao` guarda o snapshot do grafo no momento em que o Run nasceu. A
// secao 22 do plano exige isso: editar o workflow depois nao pode mudar o
// significado de uma execucao passada.
type Run struct {
	ID             uuid.UUID
	WorkflowSlug   string
	IdempotencyKey string
	Status         Status
	Attempt        int
	Definicao      []byte

	// TriggerType diz por que o Run existe (secao 12). Sem ele nao da para
	// distinguir um backfill de uma execucao agendada ao investigar incidente.
	TriggerType string

	// Params sao os valores usados NESTA execucao. Guardados junto do run
	// porque a pergunta "com que parametros isso rodou?" e a primeira de
	// qualquer investigacao de backfill.
	Params map[string]string

	// MaxAtivos e o limite de simultaneidade do workflow no instante do
	// disparo. Snapshot: baixar o limite depois nao muda runs ja enfileirados.
	MaxAtivos int

	// LogicalDate e o slot que este Run representa. Nulo em disparo manual, que
	// nao pertence a slot nenhum.
	LogicalDate *time.Time
	CriadoEm    time.Time
	IniciadoEm  *time.Time
	TerminadoEm *time.Time
	Erro        string
}

// TaskRun e a execucao de um no dentro de um Run.
type TaskRun struct {
	ID          uuid.UUID
	RunID       uuid.UUID
	NodeID      string
	Status      Status
	Attempt     int
	ExitCode    *int
	IniciadoEm  *time.Time
	TerminadoEm *time.Time
	Erro        string
}

// Transition move o Run, validando. Carimba os tempos aqui, e nao no chamador,
// para que nao exista caminho que mude o estado sem registrar quando.
func (r *Run) Transition(para Status, agora time.Time) error {
	if err := Valida(r.Status, para); err != nil {
		return err
	}
	r.Status = para

	switch para {
	case StatusRunning:
		if r.IniciadoEm == nil {
			r.IniciadoEm = &agora
		}
	case StatusSuccess, StatusCanceled:
		r.TerminadoEm = &agora
	case StatusQueued:
		// re-enfileirado por retry: a execucao recomeca, entao os carimbos
		// anteriores nao valem mais para a tentativa nova
		r.IniciadoEm, r.TerminadoEm = nil, nil
	}
	return nil
}

// Transition move o TaskRun, com a mesma validacao.
func (t *TaskRun) Transition(para Status, agora time.Time) error {
	if err := Valida(t.Status, para); err != nil {
		return err
	}
	t.Status = para

	switch para {
	case StatusRunning:
		if t.IniciadoEm == nil {
			t.IniciadoEm = &agora
		}
	case StatusSuccess, StatusFailed, StatusCanceled:
		t.TerminadoEm = &agora
	}
	return nil
}
