package schedule

import (
	"testing"
	"time"
)

func emUTC(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func ptr(t time.Time) *time.Time { return &t }

// catchup=true preenche a lacuna inteira: cada slot perdido vira um run, porque
// cada dia tem significado proprio.
func TestCatchupTruePreencheALacuna(t *testing.T) {
	s := Schedule{
		Cron: "0 2 * * *", Timezone: "UTC", Catchup: true, Ativo: true,
		UltimoSlot: ptr(emUTC("2026-01-01T02:00:00Z")),
	}
	slots, truncado, err := s.Slots(emUTC("2026-01-05T03:00:00Z"), 0)
	if err != nil {
		t.Fatal(err)
	}
	if truncado {
		t.Error("nao devia truncar")
	}
	if len(slots) != 4 { // 02, 03, 04, 05 de janeiro
		t.Fatalf("slots = %d (%v), queria 4", len(slots), slots)
	}
	if !slots[0].Equal(emUTC("2026-01-02T02:00:00Z")) {
		t.Errorf("primeiro slot = %v", slots[0])
	}
}

// catchup=false materializa so o mais recente: reprocessar quatro dias seria
// desperdicio quando apenas o estado atual importa.
func TestCatchupFalseSoOMaisRecente(t *testing.T) {
	s := Schedule{
		Cron: "0 2 * * *", Timezone: "UTC", Catchup: false, Ativo: true,
		UltimoSlot: ptr(emUTC("2026-01-01T02:00:00Z")),
	}
	slots, _, err := s.Slots(emUTC("2026-01-05T03:00:00Z"), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(slots) != 1 {
		t.Fatalf("slots = %d (%v), queria 1", len(slots), slots)
	}
	if !slots[0].Equal(emUTC("2026-01-05T02:00:00Z")) {
		t.Errorf("slot = %v, queria o mais recente (05/01)", slots[0])
	}
}

// Um workflow parado por muito tempo com catchup=true afogaria a fila. O limite
// corta e SINALIZA, em vez de truncar em silencio.
func TestLimiteTruncaESinaliza(t *testing.T) {
	s := Schedule{
		Cron: "0 * * * *", Timezone: "UTC", Catchup: true, Ativo: true,
		UltimoSlot: ptr(emUTC("2026-01-01T00:00:00Z")),
	}
	slots, truncado, err := s.Slots(emUTC("2026-02-01T00:00:00Z"), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(slots) != 10 {
		t.Errorf("slots = %d, queria 10", len(slots))
	}
	if !truncado {
		t.Error("truncado devia ser true — havia centenas de slots")
	}
}

// Agenda nova nao materializa a historia inteira do cron: comeca de agora.
func TestSemUltimoSlotNaoCriaHistoria(t *testing.T) {
	s := Schedule{Cron: "0 2 * * *", Timezone: "UTC", Catchup: true, Ativo: true}
	slots, _, err := s.Slots(emUTC("2026-06-15T03:00:00Z"), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(slots) != 0 {
		t.Errorf("slots = %v; agenda nova nao deve criar retroativo", slots)
	}
}

// O fuso muda o instante em UTC do disparo — e o caso do horario brasileiro,
// onde "02:00" nao e 02:00Z.
func TestTimezoneMudaOInstante(t *testing.T) {
	base := Schedule{Cron: "0 2 * * *", Ativo: true, UltimoSlot: ptr(emUTC("2026-06-10T00:00:00Z"))}

	utc := base
	utc.Timezone = "UTC"
	sUTC, _, err := utc.Slots(emUTC("2026-06-10T12:00:00Z"), 0)
	if err != nil {
		t.Fatal(err)
	}

	sp := base
	sp.Timezone = "America/Sao_Paulo"
	sSP, _, err := sp.Slots(emUTC("2026-06-10T12:00:00Z"), 0)
	if err != nil {
		t.Fatal(err)
	}

	if len(sUTC) == 0 || len(sSP) == 0 {
		t.Fatal("esperava um slot em cada")
	}
	if sUTC[0].Equal(sSP[0]) {
		t.Error("02:00 em Sao Paulo nao pode ser o mesmo instante que 02:00 UTC")
	}
	// Sao Paulo e UTC-3: 02:00 local = 05:00Z
	if h := sSP[0].UTC().Hour(); h != 5 {
		t.Errorf("hora UTC = %d, queria 5", h)
	}
}

func TestAgendaInativaNaoProduzSlot(t *testing.T) {
	s := Schedule{
		Cron: "0 2 * * *", Timezone: "UTC", Catchup: true, Ativo: false,
		UltimoSlot: ptr(emUTC("2026-01-01T02:00:00Z")),
	}
	slots, _, err := s.Slots(emUTC("2026-01-05T03:00:00Z"), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(slots) != 0 {
		t.Errorf("agenda inativa produziu %d slots", len(slots))
	}
}

// Cron e fuso sao validados juntos: um cron valido num fuso invalido nao agenda
// nada, e o erro apareceria longe de quem escreveu o arquivo.
func TestValidacao(t *testing.T) {
	if _, _, err := (Schedule{Cron: "invalido", Timezone: "UTC"}).Parse(); err == nil {
		t.Error("esperava erro de cron invalido")
	}
	if _, _, err := (Schedule{Cron: "0 2 * * *", Timezone: "Marte/Olimpo"}).Parse(); err == nil {
		t.Error("esperava erro de timezone invalido")
	}
	if _, _, err := (Schedule{Cron: "0 2 * * *"}).Parse(); err != nil {
		t.Errorf("timezone vazio deve assumir UTC: %v", err)
	}
}

// O YAML do autor: "0 2 * * *" todo dia as 02:00.
func TestCronDoExemploDoAutor(t *testing.T) {
	s := Schedule{Cron: "0 2 * * *", Timezone: "UTC", Ativo: true}
	prox, err := s.Proximo(emUTC("2026-03-10T23:30:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	if !prox.Equal(emUTC("2026-03-11T02:00:00Z")) {
		t.Errorf("proximo = %v, queria 11/03 02:00Z", prox)
	}
}

// REGRESSAO: com catchup=false e uma lacuna maior que o limite por ciclo, o
// retorno antecipado da truncagem pulava o filtro e devolvia `limite` slots —
// fazendo catchup=false se comportar como true. Descoberto num teste ponta a
// ponta que criou 1.100 runs onde deveria criar 1.
func TestCatchupFalseNaoEhContornadoPelaTruncagem(t *testing.T) {
	s := Schedule{
		Cron: "0 * * * *", Timezone: "UTC", Catchup: false, Ativo: true,
		UltimoSlot: ptr(emUTC("2026-01-01T00:00:00Z")),
	}
	// dois meses de lacuna horaria contra um limite de 100 por ciclo
	slots, truncado, err := s.Slots(emUTC("2026-03-01T00:00:00Z"), 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(slots) != 1 {
		t.Fatalf("slots = %d, queria 1 — catchup=false descarta a lacuna", len(slots))
	}
	if truncado {
		t.Error("truncado = true; sem catchup nao ha o que truncar")
	}
	if !slots[0].Equal(emUTC("2026-03-01T00:00:00Z")) {
		t.Errorf("slot = %v, queria o mais recente", slots[0])
	}
}
