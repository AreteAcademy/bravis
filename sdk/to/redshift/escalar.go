package redshift

import (
	"bytes"
	"strconv"
	"unicode/utf8"
)

// escreverEscalar escreve os tipos que um registro JSON quase sempre carrega,
// sem passar pelo json.Encoder. Devolve false quando nao sabe escrever.
//
// Existe por uma medicao, e a medicao mudou de sinal entre duas versoes do Go:
// passar `any` ao Encoder custava UMA alocacao por valor no Go 1.27 -- 40 mil
// para 10 mil linhas de quatro colunas -- e quase nenhuma no 1.25, porque a
// analise de escape era outra. O numero de alocacoes nao e propriedade do
// codigo; e do codigo mais o compilador. Escrever o escalar direto no buffer
// nao depende de nenhum dos dois.
//
// A saida e comparada byte a byte com json.Marshal no teste, para todos os
// casos que costumam quebrar quem escreve isto a mao: aspas, contrabarra,
// caracteres de controle, unicode e UTF-8 invalido.
func escreverEscalar(buf *bytes.Buffer, v any) bool {
	switch t := v.(type) {
	case nil:
		buf.WriteString("null")
	case bool:
		if t {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
	case string:
		escreverTexto(buf, t)
	case int:
		buf.Write(strconv.AppendInt(buf.AvailableBuffer(), int64(t), 10))
	case int32:
		buf.Write(strconv.AppendInt(buf.AvailableBuffer(), int64(t), 10))
	case int64:
		buf.Write(strconv.AppendInt(buf.AvailableBuffer(), t, 10))
	case uint64:
		buf.Write(strconv.AppendUint(buf.AvailableBuffer(), t, 10))
	case float64:
		// NaN e Inf nao existem em JSON, e o encoder recusa. Aqui a recusa e
		// devolver false: o encoder produz o erro, com a mensagem dele.
		if t != t || t > 1.7976931348623157e308 || t < -1.7976931348623157e308 {
			return false
		}
		buf.Write(strconv.AppendFloat(buf.AvailableBuffer(), t, 'g', -1, 64))
	default:
		return false
	}
	return true
}

// escreverTexto escreve uma string JSON.
//
// O caminho rapido cobre o texto comum -- sem aspas, contrabarra nem controle
// -- e cai para o lento no resto. O lento segue o RFC 8259 e a mesma escolha
// do encoding/json para os casos de borda, incluindo substituir byte invalido
// por U+FFFD, que e o que o Marshal faz.
func escreverTexto(buf *bytes.Buffer, s string) {
	if simples(s) {
		buf.WriteByte('"')
		buf.WriteString(s)
		buf.WriteByte('"')
		return
	}

	buf.WriteByte('"')
	inicio := 0
	for i := 0; i < len(s); {
		if b := s[i]; b < utf8.RuneSelf {
			if b >= 0x20 && b != '"' && b != '\\' {
				i++
				continue
			}
			buf.WriteString(s[inicio:i])
			switch b {
			case '"':
				buf.WriteString(`\"`)
			case '\\':
				buf.WriteString(`\\`)
			case '\n':
				buf.WriteString(`\n`)
			case '\r':
				buf.WriteString(`\r`)
			case '\t':
				buf.WriteString(`\t`)
			default:
				buf.WriteString(`\u00`)
				const hex = "0123456789abcdef"
				buf.WriteByte(hex[b>>4])
				buf.WriteByte(hex[b&0xF])
			}
			i++
			inicio = i
			continue
		}

		r, tamanho := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && tamanho == 1 {
			// Byte invalido: o encoding/json escreve a SEQUENCIA ESCAPADA
			// \ufffd, e nao os bytes de U+FFFD. Sair diferente dele
			// produziria um arquivo que o COPY le de outro jeito -- e foi o
			// teste byte a byte que pegou.
			buf.WriteString(s[inicio:i])
			buf.WriteString(`\ufffd`)
			i += tamanho
			inicio = i
			continue
		}
		i += tamanho
	}
	buf.WriteString(s[inicio:])
	buf.WriteByte('"')
}

// simples diz se a string pode ir entre aspas sem nenhum escape.
func simples(s string) bool {
	for i := 0; i < len(s); i++ {
		b := s[i]
		if b < 0x20 || b == '"' || b == '\\' || b >= utf8.RuneSelf {
			return false
		}
	}
	return true
}
