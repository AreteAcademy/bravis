package components

import (
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

func baldesCom(totais ...int) []postgres.Balde {
	base := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	out := make([]postgres.Balde, len(totais))
	for i, n := range totais {
		out[i] = postgres.Balde{Inicio: base.Add(time.Duration(i) * time.Hour), Sucesso: n}
	}
	return out
}

// O eixo precisa cair em numeros redondos E divisiveis por quatro. A versao
// anterior dividia o maximo cru e produzia rotulos 0/6/12/18/25 — cada intervalo
// diferente do anterior.
func TestTetoDaEscalaEhRedondoEDivisivelPorQuatro(t *testing.T) {
	casos := []struct{ max, esperado int }{
		{0, 4}, {1, 4}, {3, 4}, {4, 4}, {5, 8}, {27, 40}, {40, 40}, {41, 100}, {600, 1000},
	}
	for _, c := range casos {
		obtido := teto(baldesCom(c.max))
		if obtido != c.esperado {
			t.Errorf("teto(%d) = %d, quero %d", c.max, obtido, c.esperado)
		}
		if obtido%4 != 0 {
			t.Errorf("teto(%d) = %d nao e divisivel por 4: os rotulos sairiam quebrados", c.max, obtido)
		}
		if obtido < c.max {
			t.Errorf("teto(%d) = %d corta a coluna mais alta", c.max, obtido)
		}
	}
}

func TestLinhasDeGradeSaoEquidistantes(t *testing.T) {
	linhas := linhasDeGrade(baldesCom(27))
	if len(linhas) != 5 {
		t.Fatalf("obtive %d linhas, quero 5", len(linhas))
	}
	// Tolerancia de 1px: a divisao e inteira, entao uma area de 214px em quatro
	// faixas alterna 53 e 54. Exigir igualdade exata testaria o arredondamento,
	// nao a grade.
	passo := linhas[0].Y - linhas[1].Y
	for i := 1; i < len(linhas)-1; i++ {
		if d := linhas[i].Y - linhas[i+1].Y; d < passo-1 || d > passo+1 {
			t.Fatalf("grade irregular: %v", linhas)
		}
	}
	if linhas[0].Rotulo != "0" || linhas[4].Rotulo != "40" {
		t.Errorf("rotulos = %s .. %s, quero 0 .. 40", linhas[0].Rotulo, linhas[4].Rotulo)
	}
}

// Uma unica falha entre centenas de sucessos ainda precisa ser vista — e o caso
// em que o grafico mais importa.
func TestBarraMinimaSobrevive(t *testing.T) {
	baldes := []postgres.Balde{{Sucesso: 400, Falha: 1}}
	b := barras(baldes)[0]
	if b.HFalha < 2 {
		t.Errorf("altura da falha = %d, sumiria da tela", b.HFalha)
	}
	if b.YFalha+b.HFalha != b.YSucesso {
		t.Errorf("pilha desalinhada: falha termina em %d e sucesso comeca em %d",
			b.YFalha+b.HFalha, b.YSucesso)
	}
}

// A curva de duracao nao pode ligar dois picos por cima de uma hora vazia: isso
// inventaria duracao onde nao houve execucao nenhuma.
func TestLinhaDeDuracaoCortaNoVazio(t *testing.T) {
	base := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	baldes := []postgres.Balde{
		{Inicio: base, Sucesso: 1, DuracaoMedia: time.Second},
		{Inicio: base.Add(time.Hour)},
		{Inicio: base.Add(2 * time.Hour), Sucesso: 1, DuracaoMedia: 2 * time.Second},
	}
	d := linhaDuracao(baldes)
	if contarM(d) != 2 {
		t.Errorf("path %q deveria ter dois inicios (M), um por trecho", d)
	}

	if linhaDuracao(baldesCom(3)) != "" {
		t.Error("sem duracao medida nao deve existir curva")
	}
}

func contarM(s string) int {
	n := 0
	for _, r := range s {
		if r == 'M' {
			n++
		}
	}
	return n
}

func TestArcosDaRoscaFecham(t *testing.T) {
	i := postgres.Indicadores{Total: 10, Sucesso: 7, Falha: 2, EmExecucao: 1}
	arcos := arcos(i)
	if len(arcos) != 3 {
		t.Fatalf("obtive %d arcos, quero 3 (fatia zerada nao vira arco)", len(arcos))
	}
	// Cada arco comeca onde o anterior terminou.
	if arcos[0].Offset != 0 {
		t.Errorf("primeiro arco com deslocamento %d", arcos[0].Offset)
	}
	if arcos[1].Offset >= 0 || arcos[2].Offset >= arcos[1].Offset {
		t.Errorf("deslocamentos nao acumulam: %v", []int{arcos[0].Offset, arcos[1].Offset, arcos[2].Offset})
	}
	if len(arcos2(postgres.Indicadores{})) != 0 {
		t.Error("sem execucoes a rosca nao desenha fatia nenhuma")
	}
}

func arcos2(i postgres.Indicadores) []Arco { return arcos(i) }

func TestDuracaoEscolheUnidade(t *testing.T) {
	casos := []struct {
		d        time.Duration
		esperado string
	}{
		{400 * time.Millisecond, "400ms"},
		{2500 * time.Millisecond, "2.5s"},
		{90 * time.Second, "1m 30s"},
		{3*time.Hour + 4*time.Minute, "3h 04m"},
	}
	for _, c := range casos {
		if obtido := Duracao(&c.d); obtido != c.esperado {
			t.Errorf("Duracao(%s) = %s, quero %s", c.d, obtido, c.esperado)
		}
	}
	if Duracao(nil) != "—" {
		t.Error("duracao ausente deve virar travessao, nao zero")
	}
}
