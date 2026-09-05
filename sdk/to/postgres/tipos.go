package postgres

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// paraColuna converte o valor do registro no tipo Go que o COPY binario exige.
//
// Existe porque os dois lados falam linguas diferentes, e isso so apareceu
// contra o servidor de verdade: o registro do SDK e JSON -- um timestamp e uma
// STRING RFC 3339, porque foi assim que ele saiu do transformer ou da API. O
// COPY binario do pgx quer um time.Time, e recusa com "cannot find encode
// plan", que nao diz a ninguem que o problema e o formato.
//
// Como no lado da leitura, isto NAO e inferencia: a conversao sai do tipo
// DECLARADO da coluna, lido de information_schema. Uma coluna text recebe o
// texto como veio; uma timestamptz recebe o texto interpretado.
//
//	coluna              aceita do registro
//	timestamptz/…       string RFC 3339, time.Time, ou numero (epoch segundos)
//	date                string YYYY-MM-DD ou RFC 3339
//	numeric             string, ou numero
//	json/jsonb          qualquer coisa -- vai serializado
//	o resto             passa como veio, e quem recusa e o servidor
func paraColuna(v any, tipo string) (any, error) {
	if v == nil {
		return nil, nil
	}

	switch tipo {
	case "timestamp with time zone", "timestamp without time zone":
		return paraInstante(v, tipo)

	case "date":
		t, err := paraInstante(v, tipo)
		if err != nil {
			return nil, err
		}
		if tt, ok := t.(time.Time); ok {
			return tt.Truncate(24 * time.Hour), nil
		}
		return t, nil

	case "numeric":
		// String preserva a precisao que o lado da leitura preservou de
		// proposito -- converter para float aqui desfaria a escolha inteira.
		//
		// Mas a string nao vai crua: o pgx tenta um plano de encode para
		// string->numeric, ELE FALHA, e o pgx constroi um erro so para cair
		// no plano seguinte. Uma vez por linha. No profile de uma carga de 10
		// mil linhas isso era ~30% das alocacoes, todas em newEncodeError,
		// fmt.Errorf e fmt.Sprintf -- trabalho para produzir um erro que
		// ninguem le.
		return paraNumeric(v)

	case "json", "jsonb":
		// Um mapa ou slice vai serializado; uma string ja e o documento.
		switch t := v.(type) {
		case string:
			return t, nil
		case []byte:
			return string(t), nil
		default:
			b, err := json.Marshal(v)
			if err != nil {
				return nil, fmt.Errorf("encoding as %s: %w", tipo, err)
			}
			return string(b), nil
		}

	default:
		return v, nil
	}
}

// paraNumeric entrega o tipo que o pgx encoda direto.
//
// Um valor que nao seja texto decimal passa como veio: quem recusa e o
// servidor, com a mensagem dele, que e melhor que uma nossa adivinhando.
func paraNumeric(v any) (any, error) {
	texto, ehTexto := v.(string)
	if !ehTexto {
		return v, nil
	}
	var n pgtype.Numeric
	if err := n.Scan(texto); err != nil {
		// O erro do pgtype e NAO embrulhado: ele ecoa o valor recebido
		// inteiro, e um campo de 4 KB numa mensagem de erro vai para log
		// levando dado que ninguem quer la. Foi o teste do tamanho que pegou.
		return nil, fmt.Errorf("%q is not a number this column accepts", elidir(texto))
	}
	return n, nil
}

// paraInstante aceita as tres formas em que um instante chega num registro.
func paraInstante(v any, tipo string) (any, error) {
	switch t := v.(type) {
	case time.Time:
		return t, nil

	case string:
		for _, layout := range []string{
			time.RFC3339Nano,
			time.RFC3339,
			"2006-01-02 15:04:05.999999999Z07:00",
			"2006-01-02 15:04:05",
			"2006-01-02",
		} {
			if ts, err := time.Parse(layout, t); err == nil {
				return ts, nil
			}
		}
		return nil, fmt.Errorf("%q is not a timestamp this column (%s) accepts: use RFC 3339, "+
			"as in 2026-09-05T12:30:00Z", elidir(t), tipo)

	case json.Number:
		n, err := t.Int64()
		if err != nil {
			return nil, fmt.Errorf("%v is not a timestamp: %w", t, err)
		}
		return time.Unix(n, 0).UTC(), nil

	case float64:
		// Epoch em segundos, que e como um JSON costuma trazer.
		return time.Unix(int64(t), 0).UTC(), nil

	case int64:
		return time.Unix(t, 0).UTC(), nil

	case int:
		return time.Unix(int64(t), 0).UTC(), nil

	default:
		return nil, fmt.Errorf("a %T cannot go into a %s column", v, tipo)
	}
}

// elidir encurta um valor para a mensagem de erro. Um campo de 4 KB numa
// mensagem de erro e ruido, e pode carregar dado que ninguem quer em log.
func elidir(s string) string {
	if len(s) <= 40 {
		return s
	}
	return s[:37] + "…"
}
