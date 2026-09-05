package postgres

import (
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// ParaJSON converte um valor do Postgres no que vai virar JSON.
//
// Isto NAO e inferencia: e uma tabela escrita, revisavel, com um teste por
// linha. Cada escolha aqui tem um motivo, e o motivo esta ao lado dela.
//
//	SQL                 Go              JSON
//	NUMERIC/DECIMAL     string          string     float64 perde precisao em dinheiro
//	TIMESTAMPTZ         time.Time       RFC 3339
//	DATE                time.Time       YYYY-MM-DD sem hora falsa
//	BYTEA               []byte          base64     encoding/json ja faz
//	JSON/JSONB          RawMessage      aninhado   nao reserializar
//	UUID                string          string
//	NULL                nil             null
//	array               []any           array
func ParaJSON(v any) any { return converter(v, 0) }

// ParaJSONComOID e a versao que sabe o tipo DECLARADO da coluna.
//
// O OID importa porque o pgx entrega DATE e TIMESTAMPTZ como o mesmo
// time.Time, e so o tipo da coluna distingue os dois. Sem ele, um DATE saia
// como "2026-09-05T00:00:00Z" -- uma hora que ninguem escreveu, e que anda um
// dia na primeira conversao de fuso. Achado pelo teste de integracao contra o
// servidor de verdade, e nao pelos que montam valores em memoria.
func ParaJSONComOID(v any, oid uint32) any { return converter(v, oid) }

// OIDs que mudam a conversao. Os numeros sao do catalogo do Postgres e nao
// mudam: pg_type.oid e parte do protocolo.
const (
	oidDate = 1082
	oidTime = 1083
)

func converter(v any, oid uint32) any {
	switch t := v.(type) {
	case nil:
		return nil

	case pgtype.Numeric:
		// Dinheiro. Virar float64 perde centavos em valores grandes, e o
		// prejuizo aparece meses depois, num relatorio que ninguem confere.
		// String preserva o que veio; quem quiser numero converte no Transform.
		return numericoComoTexto(t)

	case pgtype.Date:
		if !t.Valid {
			return nil
		}
		// Sem hora: uma data com 00:00:00 vira uma hora que ninguem escreveu,
		// e some ou anda um dia quando alguem converte de fuso.
		return t.Time.Format("2006-01-02")

	case time.Time:
		switch oid {
		case oidDate:
			// Sem hora: 00:00:00 e uma hora que ninguem escreveu.
			return t.Format("2006-01-02")
		case oidTime:
			return t.Format("15:04:05.999999999")
		}
		return t.UTC().Format(time.RFC3339Nano)

	case pgtype.Timestamptz:
		if !t.Valid {
			return nil
		}
		return t.Time.UTC().Format(time.RFC3339Nano)

	case pgtype.Timestamp:
		if !t.Valid {
			return nil
		}
		return t.Time.UTC().Format(time.RFC3339Nano)

	case [16]byte:
		// UUID: o pgx entrega os bytes crus, e crus eles virariam um array de
		// numeros no JSON.
		return formatarUUID(t)

	case pgtype.UUID:
		if !t.Valid {
			return nil
		}
		return formatarUUID(t.Bytes)

	case json.RawMessage:
		// JSON/JSONB entram aninhados. Reserializar viraria uma string com
		// JSON dentro, e quem consome teria de decodificar duas vezes.
		return jsonAninhado(t)

	case []byte:
		// BYTEA. encoding/json ja escreve base64.
		return t

	case net.IP:
		return t.String()

	case *net.IPNet:
		if t == nil {
			return nil
		}
		return t.String()

	case map[string]any:
		out := make(map[string]any, len(t))
		for k, v := range t {
			// Sem OID: dentro de um composto nao ha coluna, e o tipo Go ja
			// carrega o que da para saber.
			out[k] = converter(v, 0)
		}
		return out

	case []any:
		out := make([]any, len(t))
		for i, v := range t {
			out[i] = converter(v, oid)
		}
		return out

	default:
		return v
	}
}

// numericoComoTexto usa a representacao decimal exata que o pgtype guarda.
func numericoComoTexto(n pgtype.Numeric) any {
	if !n.Valid {
		return nil
	}
	if n.NaN {
		return "NaN"
	}
	if n.InfinityModifier != pgtype.Finite {
		return strings.TrimSpace(n.InfinityModifier.String())
	}
	b, err := n.MarshalJSON()
	if err != nil {
		return nil
	}
	// MarshalJSON devolve o numero sem aspas; a string e o ponto.
	return strings.Trim(string(b), `"`)
}

// jsonAninhado decodifica para que o valor entre como estrutura, e nao como
// string. Um JSON invalido no banco vira string em vez de derrubar a linha --
// o dado existe, e recusa-lo aqui seria perder o registro inteiro por causa
// de uma coluna.
func jsonAninhado(raw json.RawMessage) any {
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		return string(raw)
	}
	return out
}

func formatarUUID(b [16]byte) string {
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
