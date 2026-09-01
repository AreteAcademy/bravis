// Package schedule decide QUANDO um workflow deve rodar.
//
// A secao 37 do plano separa as responsabilidades sem ambiguidade: o scheduler
// CRIA runs, a fila as EXECUTA. Este pacote nao conhece fila, executor nem
// banco — ele responde a uma pergunta pura: dados o cron, o fuso, o ultimo slot
// materializado e o instante atual, quais slots faltam?
//
// Isolar isso e o que torna a politica de catchup testavel sem relogio falso e
// sem Postgres.
package schedule

import (
	"fmt"
	"time"

	"github.com/robfig/cron/v3"
)

// TriggerType diz por que um run nasceu (secao 12).
type TriggerType string

const (
	TriggerSchedule TriggerType = "schedule"
	TriggerManual   TriggerType = "manual"
	TriggerBackfill TriggerType = "backfill"
	TriggerAPI      TriggerType = "api"
	TriggerRetry    TriggerType = "retry"
)

// Schedule e a agenda de um workflow.
type Schedule struct {
	WorkflowSlug string
	Cron         string
	Timezone     string

	// Catchup=false materializa apenas o slot mais recente perdido. Ver Slots.
	Catchup bool

	Ativo bool

	// UltimoSlot e o ultimo slot ja materializado. Nulo = nunca rodou.
	UltimoSlot *time.Time
}

// Parse valida o cron e o fuso, devolvendo o agendador pronto.
//
// Valida os dois JUNTOS porque um cron valido num fuso invalido nao agenda nada,
// e o erro so apareceria no laco do scheduler, longe de quem escreveu o arquivo.
func (s Schedule) Parse() (cron.Schedule, *time.Location, error) {
	tz := s.Timezone
	if tz == "" {
		tz = "UTC"
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return nil, nil, fmt.Errorf("timezone %q invalido: %w", tz, err)
	}

	// Sem segundos: "0 2 * * *" e cron de 5 campos, como no YAML do plano.
	p := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	sched, err := p.Parse(s.Cron)
	if err != nil {
		return nil, nil, fmt.Errorf("cron %q invalido: %w", s.Cron, err)
	}
	return sched, loc, nil
}

// Slots devolve os instantes que ainda precisam virar Run, ate `agora`.
//
// A politica de catchup e a decisao central desta fase:
//
//   - catchup=true  → TODOS os slots perdidos viram run. Serve para pipeline em
//     que cada dia tem significado proprio e uma lacuna precisa ser preenchida.
//   - catchup=false → apenas o slot mais recente. Serve para o caso em que so o
//     estado atual importa, e reprocessar trinta dias seria desperdicio.
//
// `limite` corta a quantidade: um workflow parado por meses com catchup=true
// criaria milhares de runs de uma vez e afogaria a fila. Devolver o excedente
// como `truncado` deixa isso visivel em vez de silencioso.
func (s Schedule) Slots(agora time.Time, limite int) (slots []time.Time, truncado bool, err error) {
	if !s.Ativo {
		return nil, false, nil
	}
	sched, loc, err := s.Parse()
	if err != nil {
		return nil, false, err
	}

	// Ponto de partida: o ultimo slot materializado, ou o instante atual quando
	// a agenda nunca rodou. Comecar do zero criaria a historia inteira do cron.
	de := agora.In(loc)
	if s.UltimoSlot != nil {
		de = s.UltimoSlot.In(loc)
	}

	// Sem catchup, so o slot MAIS RECENTE interessa — a lacuna e descartada por
	// definicao. Percorre sem acumular, e o limite nao se aplica: nao ha o que
	// truncar quando so um slot sera materializado.
	//
	// O teto de iteracoes protege contra agenda com ultimo_slot muito antigo,
	// que faria o laco varrer anos de cron a cada ciclo.
	if !s.Catchup {
		const maxIter = 500_000
		var ultimo time.Time
		for i := 0; i < maxIter; i++ {
			prox := sched.Next(de)
			if prox.After(agora) {
				break
			}
			ultimo, de = prox, prox
		}
		if ultimo.IsZero() {
			return nil, false, nil
		}
		return []time.Time{ultimo}, false, nil
	}

	for {
		prox := sched.Next(de)
		if prox.After(agora) {
			break
		}
		slots = append(slots, prox)
		de = prox

		// Trunca e SINALIZA. O restante entra nos ciclos seguintes, porque o
		// marcador avanca a cada slot materializado.
		if limite > 0 && len(slots) >= limite {
			return slots, true, nil
		}
	}
	return slots, false, nil
}

// Proximo devolve o proximo disparo depois de `agora`, para exibicao.
func (s Schedule) Proximo(agora time.Time) (time.Time, error) {
	sched, loc, err := s.Parse()
	if err != nil {
		return time.Time{}, err
	}
	return sched.Next(agora.In(loc)), nil
}
