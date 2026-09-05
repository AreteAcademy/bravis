package mysql

import (
	"encoding/json"
	"strconv"
	"time"
)

// ParaJSON converte um valor do MySQL no que vai virar JSON.
//
// O `database/sql` devolve []byte para quase tudo quando se le em `any`, entao
// a conversao sai do TIPO DECLARADO da coluna -- que e o que
// Rows.ColumnTypes() da. Sem ele, todo DECIMAL viraria string de bytes e todo
// INT tambem, e ninguem entenderia por que o JSON esta cheio de base64.
//
//	MySQL               JSON        por que
//	DECIMAL/NUMERIC     string      float64 perde centavos em dinheiro
//	DATE                YYYY-MM-DD  sem hora falsa
//	DATETIME/TIMESTAMP  RFC 3339
//	JSON                aninhado    nao reserializar
//	BLOB/BINARY         base64      encoding/json ja faz
//	TINYINT(1)          numero      o driver nao distingue de BOOL sem o DSN
//	NULL                null
func ParaJSON(v any, declarado string) any {
	if v == nil {
		return nil
	}

	// time.Time chega quando o DSN tem parseTime=true, que o driver garante.
	if t, ok := v.(time.Time); ok {
		if declarado == "DATE" {
			// Sem hora: 00:00:00 e uma hora que ninguem escreveu.
			return t.Format("2006-01-02")
		}
		return t.UTC().Format(time.RFC3339Nano)
	}

	b, ehBytes := v.([]byte)
	if !ehBytes {
		return v
	}

	switch declarado {
	case "DECIMAL", "NUMERIC":
		// String preserva a precisao. Dinheiro em float64 perde centavos em
		// valores grandes, e o prejuizo aparece meses depois.
		return string(b)

	case "JSON":
		var doc any
		if err := json.Unmarshal(b, &doc); err != nil {
			// O dado existe; recusar aqui perderia o registro inteiro por
			// causa de uma coluna.
			return string(b)
		}
		return doc

	case "BLOB", "TINYBLOB", "MEDIUMBLOB", "LONGBLOB", "BINARY", "VARBINARY", "GEOMETRY":
		// encoding/json ja escreve base64.
		return b

	case "TINYINT", "SMALLINT", "MEDIUMINT", "INT", "INTEGER", "BIGINT", "YEAR":
		if n, err := strconv.ParseInt(string(b), 10, 64); err == nil {
			return n
		}
		return string(b)

	case "UNSIGNED TINYINT", "UNSIGNED SMALLINT", "UNSIGNED INT", "UNSIGNED BIGINT":
		if n, err := strconv.ParseUint(string(b), 10, 64); err == nil {
			return n
		}
		return string(b)

	case "FLOAT", "DOUBLE":
		if f, err := strconv.ParseFloat(string(b), 64); err == nil {
			return f
		}
		return string(b)

	case "DATE":
		return string(b)

	case "DATETIME", "TIMESTAMP":
		// Sem parseTime o driver entrega texto; normaliza para RFC 3339.
		if t, err := time.Parse("2006-01-02 15:04:05.999999", string(b)); err == nil {
			return t.UTC().Format(time.RFC3339Nano)
		}
		return string(b)

	default:
		// CHAR, VARCHAR, TEXT, ENUM, SET e o resto sao texto.
		return string(b)
	}
}
