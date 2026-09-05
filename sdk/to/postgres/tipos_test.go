package postgres

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestParaColunaLinhaALinha e o lado da ESCRITA da tabela de tipos.
//
// Ele existe por um defeito concreto: o registro do SDK e JSON, entao um
// timestamp nele e uma STRING RFC 3339. O COPY binario do pgx quer um
// time.Time e recusa a string com "cannot find encode plan", que nao diz a
// ninguem que o problema e o formato. So apareceu contra o servidor de
// verdade -- os testes em memoria provam os bytes que montamos.
func TestParaColunaLinhaALinha(t *testing.T) {
	instante := time.Date(2026, 9, 5, 12, 30, 0, 0, time.UTC)

	casos := []struct {
		nome   string
		valor  any
		tipo   string
		quero  any
		porque string
	}{
		{"nil vira NULL em qualquer coluna", nil, "timestamp with time zone", nil, ""},
		{
			"string RFC 3339 vira time.Time",
			"2026-09-05T12:30:00Z", "timestamp with time zone", instante,
			"e a forma que IngestionLoadedAt produz, e a que toda API JSON devolve",
		},
		{
			"RFC 3339 com fracao",
			"2026-09-05T12:30:00.000Z", "timestamp with time zone", instante, "",
		},
		{
			"outro fuso vira o mesmo instante",
			"2026-09-05T09:30:00-03:00", "timestamp with time zone", instante, "",
		},
		{"time.Time passa direto", instante, "timestamp with time zone", instante, ""},
		{
			"epoch em segundos",
			float64(instante.Unix()), "timestamp with time zone", instante,
			"um JSON traz epoch como float64, e recusa-lo perderia a linha",
		},
		{
			"numeric continua string",
			"1234567890123456.78", "numeric", "1234567890123456.78",
			"converter para float aqui desfaria a escolha que o lado da leitura fez",
		},
		{"text passa como veio", "qualquer coisa", "text", "qualquer coisa", ""},
		{"integer passa como veio", int64(42), "integer", int64(42), ""},
	}

	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			got, err := paraColuna(c.valor, c.tipo)
			if err != nil {
				t.Fatalf("paraColuna: %v", err)
			}
			if ts, ok := c.quero.(time.Time); ok {
				gt, ok := got.(time.Time)
				if !ok || !gt.Equal(ts) {
					t.Errorf("= %#v, esperado %v\n  %s", got, ts, c.porque)
				}
				return
			}
			if got != c.quero {
				t.Errorf("= %#v, esperado %#v\n  %s", got, c.quero, c.porque)
			}
		})
	}
}

// TestParaColunaJSONSerializa: um mapa numa coluna jsonb tem de virar
// documento, nao a representacao Go de um mapa.
func TestParaColunaJSONSerializa(t *testing.T) {
	got, err := paraColuna(map[string]any{"a": 1}, "jsonb")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(got.(string)), &doc); err != nil {
		t.Fatalf("nao saiu JSON valido: %q", got)
	}
	if doc["a"] != float64(1) {
		t.Errorf("documento = %v", doc)
	}
}

// TestParaColunaDataTrunca: uma coluna date nao guarda hora, e mandar uma hora
// que o registro nao tinha e inventar dado.
func TestParaColunaDataTrunca(t *testing.T) {
	got, err := paraColuna("2026-09-05T23:59:59Z", "date")
	if err != nil {
		t.Fatal(err)
	}
	ts := got.(time.Time)
	if ts.Hour() != 0 || ts.Minute() != 0 {
		t.Errorf("date = %v; a hora devia ter sido truncada", ts)
	}
}

// TestParaColunaErroDizOFormato: "cannot find encode plan" nao diz a ninguem o
// que fazer. Este erro diz.
func TestParaColunaErroDizOFormato(t *testing.T) {
	_, err := paraColuna("cinco de setembro", "timestamp with time zone")
	if err == nil {
		t.Fatal("texto que nao e data passou")
	}
	for _, exigido := range []string{"RFC 3339", "2026-09-05T12:30:00Z"} {
		if !strings.Contains(err.Error(), exigido) {
			t.Errorf("o erro nao diz %q: %v", exigido, err)
		}
	}
}

// TestParaColunaElideValorLongo: um campo de 4 KB numa mensagem de erro e
// ruido, e pode levar dado que ninguem quer em log.
func TestParaColunaElideValorLongo(t *testing.T) {
	longo := strings.Repeat("x", 4000)
	_, err := paraColuna(longo, "date")
	if err == nil {
		t.Fatal("passou")
	}
	if len(err.Error()) > 300 {
		t.Errorf("a mensagem tem %d bytes; o valor devia ter sido elidido", len(err.Error()))
	}
}
