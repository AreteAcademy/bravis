package mysql

import (
	"encoding/json"
	"testing"
	"time"
)

// TestParaJSONLinhaALinha cobre a tabela de tipos, um caso por linha.
//
// O database/sql devolve []byte para quase tudo quando se le em `any`, entao a
// conversao sai do TIPO DECLARADO da coluna. Sem ele todo DECIMAL viraria
// base64 no JSON, e um INT tambem.
func TestParaJSONLinhaALinha(t *testing.T) {
	casos := []struct {
		nome      string
		valor     any
		declarado string
		quero     string
		porque    string
	}{
		{"NULL", nil, "VARCHAR", `null`, ""},
		{
			"DECIMAL vira string", []byte("123456789012345678.99"), "DECIMAL",
			`"123456789012345678.99"`,
			"float64 perde centavos em valores grandes",
		},
		{"BIGINT vira numero", []byte("42"), "BIGINT", `42`, "sem isto viraria base64"},
		{"INT vira numero", []byte("7"), "INT", `7`, ""},
		{"DOUBLE vira numero", []byte("1.5"), "DOUBLE", `1.5`, ""},
		{"VARCHAR vira texto", []byte("ola"), "VARCHAR", `"ola"`, ""},
		{
			"JSON aninhado", []byte(`{"a":[1,2]}`), "JSON", `{"a":[1,2]}`,
			"reserializar viraria string com JSON dentro",
		},
		{
			"JSON invalido vira string", []byte(`{quebrado`), "JSON", `"{quebrado"`,
			"o dado existe; recusar perderia o registro inteiro por uma coluna",
		},
		{"BLOB em base64", []byte{0xde, 0xad}, "BLOB", `"3q0="`, ""},
		{"VARBINARY em base64", []byte{0xde, 0xad}, "VARBINARY", `"3q0="`, ""},
		{"DATE como texto", []byte("2026-09-05"), "DATE", `"2026-09-05"`, ""},
		{
			"DATETIME sem parseTime", []byte("2026-09-05 12:30:00"), "DATETIME",
			`"2026-09-05T12:30:00Z"`,
			"este e o caminho que existe quando o DSN nao tem parseTime",
		},
		{"ENUM vira texto", []byte("ativo"), "ENUM", `"ativo"`, ""},
	}

	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			b, err := json.Marshal(ParaJSON(c.valor, c.declarado))
			if err != nil {
				t.Fatal(err)
			}
			if string(b) != c.quero {
				t.Errorf("= %s, esperado %s\n  %s", b, c.quero, c.porque)
			}
		})
	}
}

// TestOsDoisCaminhosDeInstanteConcordam e a razao de o fallback de texto
// existir, e a prova de que ele nao e codigo morto: com ou sem parseTime, o
// mesmo instante sai igual.
//
// Este teste tambem impede o comentario mentiroso que eu tinha escrito -- que
// sem parseTime o instante viraria base64. Nao vira; o que muda e o custo.
func TestOsDoisCaminhosDeInstanteConcordam(t *testing.T) {
	instante := time.Date(2026, 9, 5, 12, 30, 0, 0, time.UTC)

	comParse := ParaJSON(instante, "DATETIME")
	semParse := ParaJSON([]byte("2026-09-05 12:30:00"), "DATETIME")

	if comParse != semParse {
		t.Errorf("os caminhos divergem: com parseTime %v, sem %v", comParse, semParse)
	}
	if comParse != "2026-09-05T12:30:00Z" {
		t.Errorf("= %v", comParse)
	}
}

// TestDateNaoGanhaHora: 00:00:00 e uma hora que ninguem escreveu, e ela anda um
// dia na primeira conversao de fuso.
func TestDateNaoGanhaHora(t *testing.T) {
	d := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	if got := ParaJSON(d, "DATE"); got != "2026-09-05" {
		t.Errorf("DATE = %v, esperado sem hora", got)
	}
}
