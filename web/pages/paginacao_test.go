package pages

import "testing"

func TestPaginacaoContas(t *testing.T) {
	p := Paginacao{Pagina: 3, PorPagina: 25, Total: 63}
	if p.Paginas() != 3 {
		t.Errorf("paginas = %d, quero 3", p.Paginas())
	}
	if p.Primeiro() != 51 || p.Ultimo() != 63 {
		t.Errorf("intervalo = %d–%d, quero 51–63", p.Primeiro(), p.Ultimo())
	}

	// Lista vazia nao pode dizer "1–0 de 0".
	vazia := Paginacao{Pagina: 1, PorPagina: 25, Total: 0}
	if vazia.Primeiro() != 0 || vazia.Paginas() != 1 {
		t.Errorf("vazia: primeiro=%d paginas=%d", vazia.Primeiro(), vazia.Paginas())
	}
}

// A janela nao lista cem paginas: mostra cinco, centradas na atual, e encosta
// nas bordas sem encolher.
func TestJanelaDePaginas(t *testing.T) {
	casos := []struct {
		pagina, total int
		esperado      []int
	}{
		{1, 250, []int{1, 2, 3, 4, 5}},
		{7, 250, []int{5, 6, 7, 8, 9}},
		{10, 250, []int{6, 7, 8, 9, 10}},
		{1, 50, []int{1, 2}},
	}
	for _, c := range casos {
		p := Paginacao{Pagina: c.pagina, PorPagina: 25, Total: c.total}
		j := p.Janela()
		if len(j) != len(c.esperado) {
			t.Fatalf("pagina %d de %d: %v, quero %v", c.pagina, p.Paginas(), j, c.esperado)
		}
		for i := range j {
			if j[i] != c.esperado[i] {
				t.Fatalf("pagina %d: %v, quero %v", c.pagina, j, c.esperado)
			}
		}
	}
}

// Trocar de filtro volta para a pagina 1: continuar na pagina 7 de um resultado
// que agora tem duas seria uma tela vazia sem explicacao.
func TestTrocarFiltroReiniciaPagina(t *testing.T) {
	f := Filtro{Tag: "zarv", Pagina: 7, PorPagina: PorPaginaPadrao}
	if u := f.Com("estado", "failed"); contemTexto(u, "pagina=") {
		t.Errorf("URL %q manteve a pagina ao trocar o filtro", u)
	}
	// Navegar entre paginas preserva o resto do filtro.
	u := f.ComPagina(3)
	if !contemTexto(u, "tag=zarv") || !contemTexto(u, "pagina=3") {
		t.Errorf("URL de pagina = %q", u)
	}
}

// Terceiro clique no mesmo cabecalho remove a ordenacao.
func TestComOrdemAlternaEDepoisLimpa(t *testing.T) {
	f := Filtro{PorPagina: PorPaginaPadrao}
	primeiro := f.ComOrdem("ultima")
	if !contemTexto(primeiro, "ordem=ultima") || contemTexto(primeiro, "dir=desc") {
		t.Errorf("primeiro clique = %q, queria ascendente", primeiro)
	}

	f.Ordem = "ultima"
	if segundo := f.ComOrdem("ultima"); !contemTexto(segundo, "dir=desc") {
		t.Errorf("segundo clique = %q, queria descendente", segundo)
	}

	f.Desc = true
	if terceiro := f.ComOrdem("ultima"); contemTexto(terceiro, "ordem=") {
		t.Errorf("terceiro clique = %q, queria sem ordenacao", terceiro)
	}
	if f.Seta("ultima") != "↓" || f.Seta("workflow") != "" {
		t.Error("a seta so aparece na coluna ordenada")
	}
}

func TestFiltroDeRunsRemovePeriodo(t *testing.T) {
	f := FiltroRuns{Estado: "failed", De: "2026-09-01T02:00:00Z", Ate: "2026-09-01T03:00:00Z"}
	if !f.Ativo() {
		t.Error("filtro com periodo deveria contar como ativo")
	}
	if u := f.Com("estado", ""); contemTexto(u, "estado=") {
		t.Errorf("remover o estado deixou %q", u)
	}
	if !contemTexto(f.Com("estado", ""), "de=") {
		t.Error("remover o estado nao pode levar o periodo junto")
	}
	if (FiltroRuns{}).Ativo() {
		t.Error("filtro vazio nao esta ativo")
	}
}

func contemTexto(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
