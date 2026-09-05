package mysql

import (
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/sdk/internal/core"
)

// TestInsertSQLNomeiaAsColunas: o SQL afirmado como funcao pura, sem cliente.
// A razao e concreta -- o MERGE do BigQuery saiu com casamento POSICIONAL e
// custou a v0.12.0 porque era montado dentro de um metodo com cliente.
func TestInsertSQLNomeiaAsColunas(t *testing.T) {
	got := InsertSQL("pedidos", []string{"ingestion_id", "valor"}, 2, false)
	esperado := "INSERT INTO `pedidos` (`ingestion_id`, `valor`) VALUES (?,?), (?,?)"
	if got != esperado {
		t.Errorf("SQL:\n  got  %s\n  want %s", got, esperado)
	}
}

// TestInsertSQLIgnoraNaDedup: INSERT IGNORE e a dedup do MySQL.
func TestInsertSQLIgnoraNaDedup(t *testing.T) {
	got := InsertSQL("t", []string{"a"}, 1, true)
	if !strings.HasPrefix(got, "INSERT IGNORE INTO") {
		t.Errorf("dedup nao virou INSERT IGNORE:\n%s", got)
	}
	if semDedup := InsertSQL("t", []string{"a"}, 1, false); strings.Contains(semDedup, "IGNORE") {
		t.Errorf("sem dedup virou IGNORE:\n%s", semDedup)
	}
}

// TestQualificarCitaCadaParte: citar "banco.tabela" inteiro criaria uma tabela
// chamada literalmente "banco.tabela".
func TestQualificarCitaCadaParte(t *testing.T) {
	if got := qualificar("landing.pedidos"); got != "`landing`.`pedidos`" {
		t.Errorf("qualificar = %s", got)
	}
	if got := qualificar("pedidos"); got != "`pedidos`" {
		t.Errorf("qualificar = %s", got)
	}
}

// TestCitarEscapaCrase: uma crase dentro do nome fecharia o identificador e o
// resto viraria SQL.
func TestCitarEscapaCrase(t *testing.T) {
	if got := citar("a`b"); got != "`a``b`" {
		t.Errorf("citar = %s", got)
	}
}

// TestInsertSQLPalavraReservada: uma coluna chamada `order` e legitima.
func TestInsertSQLPalavraReservada(t *testing.T) {
	got := InsertSQL("t", []string{core.MetadataID, "order"}, 1, false)
	if !strings.Contains(got, "`order`") {
		t.Errorf("palavra reservada sem crase:\n%s", got)
	}
}

// TestPartirNome cobre o banco implicito.
func TestPartirNome(t *testing.T) {
	if b, tb := partirNome("landing.pedidos"); b != "landing" || tb != "pedidos" {
		t.Errorf("= (%q, %q)", b, tb)
	}
	if b, tb := partirNome("pedidos"); b != "" || tb != "pedidos" {
		t.Errorf("= (%q, %q); banco vazio significa o do DSN", b, tb)
	}
}

// TestComParseTimeENecessario: sem parseTime=true o driver devolve DATETIME
// como []byte, e todo instante viraria base64 no JSON -- silenciosamente,
// porque []byte e um valor legitimo.
func TestComParseTime(t *testing.T) {
	casos := map[string]string{
		"u:s@tcp(h:3306)/db":                 "u:s@tcp(h:3306)/db?parseTime=true",
		"u:s@tcp(h:3306)/db?charset=utf8":    "u:s@tcp(h:3306)/db?charset=utf8&parseTime=true",
		"u:s@tcp(h:3306)/db?parseTime=false": "u:s@tcp(h:3306)/db?parseTime=false",
	}
	for entrada, quero := range casos {
		if got := comParseTime(entrada); got != quero {
			t.Errorf("comParseTime(%q) = %q, esperado %q", entrada, got, quero)
		}
	}
}
