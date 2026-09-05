package postgres

import (
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/sdk/internal/core"
)

// TestInsertSQLNomeiaAsColunas: o §5.4 do plano pede o SQL afirmado como
// funcao pura, sem cliente -- e a razao e concreta. O MERGE do BigQuery saiu
// com casamento POSICIONAL e custou a v0.12.0 justamente porque o SQL era
// montado dentro de um metodo com cliente e nunca tinha sido visto por um
// teste.
func TestInsertSQLNomeiaAsColunas(t *testing.T) {
	got := InsertSQL("landing.pedidos", "brevis_stage",
		[]string{"ingestion_id", "provider", "valor"})

	esperado := `INSERT INTO landing.pedidos ("ingestion_id", "provider", "valor") ` +
		`SELECT "ingestion_id", "provider", "valor" FROM brevis_stage ` +
		`ON CONFLICT ("ingestion_id") DO NOTHING`
	if got != esperado {
		t.Errorf("SQL:\n  got  %s\n  want %s", got, esperado)
	}
}

// TestInsertSQLCitaIdentificadores: uma coluna chamada "order" ou "select" e
// legitima, e sem aspas ela vira erro de sintaxe no meio de uma carga -- num
// lote que ja rodou o extract inteiro.
func TestInsertSQLCitaIdentificadores(t *testing.T) {
	got := InsertSQL("t", "s", []string{core.MetadataID, "order", "group"})
	for _, palavra := range []string{`"order"`, `"group"`} {
		if !strings.Contains(got, palavra) {
			t.Errorf("%s nao esta citado:\n%s", palavra, got)
		}
	}
}

// TestInsertSQLEscapaAspas: uma aspa dentro do nome fecharia o identificador e
// o resto da coluna viraria SQL.
func TestInsertSQLEscapaAspas(t *testing.T) {
	got := InsertSQL("t", "s", []string{`a"b`})
	if !strings.Contains(got, `"a""b"`) {
		t.Errorf("aspa nao escapada:\n%s", got)
	}
}

// TestInsertSQLConflitaNoIngestionID: a dedup do SDK e por ingestion_id, e
// trocar essa coluna por outra faria a dedup casar pela coisa errada em
// silencio.
func TestInsertSQLConflitaNoIngestionID(t *testing.T) {
	got := InsertSQL("t", "s", []string{"a"})
	if !strings.Contains(got, `ON CONFLICT ("`+core.MetadataID+`")`) {
		t.Errorf("o ON CONFLICT nao e por %s:\n%s", core.MetadataID, got)
	}
}

// TestPartirNome cobre o esquema implicito e o nome com partes demais.
func TestPartirNome(t *testing.T) {
	casos := []struct {
		nome, esquema, tabela string
		erro                  bool
	}{
		{"pedidos", "public", "pedidos", false},
		{"landing.pedidos", "landing", "pedidos", false},
		{"a.b.c", "", "", true},
	}
	for _, c := range casos {
		e, tb, err := partirNome(c.nome)
		if (err != nil) != c.erro {
			t.Errorf("%q: erro = %v, esperado erro=%v", c.nome, err, c.erro)
			continue
		}
		if !c.erro && (e != c.esquema || tb != c.tabela) {
			t.Errorf("%q = (%q, %q), esperado (%q, %q)", c.nome, e, tb, c.esquema, c.tabela)
		}
	}
}

// TestLinhasSegueAOrdemDaTabela: COPY FROM casa por POSICAO. Se as linhas
// saissem na ordem do registro, cada valor pousaria na coluna errada -- e o
// banco aceitaria calado sempre que os tipos coincidissem.
func TestLinhasSegueAOrdemDaTabela(t *testing.T) {
	envelopes := []core.Envelope{
		{Payload: map[string]any{"c": 3, "a": 1, "b": 2}},
		{Payload: map[string]any{"b": 20, "a": 10}}, // sem "c"
	}
	l := &linhas{colunas: []string{"a", "b", "c"}, envelopes: envelopes}

	if !l.Next() {
		t.Fatal("sem primeira linha")
	}
	v, err := l.Values()
	if err != nil {
		t.Fatal(err)
	}
	if v[0] != 1 || v[1] != 2 || v[2] != 3 {
		t.Errorf("linha 1 = %v, esperado [1 2 3] na ordem da TABELA", v)
	}

	if !l.Next() {
		t.Fatal("sem segunda linha")
	}
	v, err = l.Values()
	if err != nil {
		t.Fatal(err)
	}
	// Coluna que o registro nao traz vira NULL, que e legitimo numa landing.
	if v[0] != 10 || v[1] != 20 || v[2] != nil {
		t.Errorf("linha 2 = %v, esperado [10 20 <nil>]", v)
	}

	if l.Next() {
		t.Error("Next continuou depois do fim")
	}
}

// TestLinhasNaoAlocaPorLinha: um lote de 500 mil registros nao pode alocar um
// slice novo por linha. O pgx consome cada linha antes de pedir a proxima,
// entao o buffer e reusavel -- e este teste fixa isso.
func TestLinhasNaoAlocaPorLinha(t *testing.T) {
	envelopes := make([]core.Envelope, 1000)
	for i := range envelopes {
		envelopes[i] = core.Envelope{Payload: map[string]any{"a": i}}
	}
	l := &linhas{colunas: []string{"a"}, envelopes: envelopes}

	alocacoes := testing.AllocsPerRun(100, func() {
		l.i = 0
		for l.Next() {
			if _, err := l.Values(); err != nil {
				t.Fatal(err)
			}
		}
	})
	// Zero e o alvo: o buffer e alocado uma vez, fora do laco medido.
	if alocacoes > 0 {
		t.Errorf("%.0f alocacoes para 1000 linhas; o buffer deveria ser reusado", alocacoes)
	}
}
