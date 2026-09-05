package core

import (
	"unicode/utf8"
)

// AppendJSONString escreve s como uma string JSON, sem escapar HTML.
//
// Ela existe num lugar so porque a mesma regra e precisa em dois: o NDJSON que
// o Redshift le, e o canonico que reproduz o json.dumps do Python. O
// encoding/json escapa `<`, `>` e `&` por padrao e o Python nao escapa nenhum
// dos tres -- e uma diferenca de escape muda a CHAVE, sem erro.
//
// O conhecimento existia no driver do Redshift e nao era compartilhado; a
// segunda vez que ele foi necessario custou noventa linhas escritas de novo.
//
// A saida e comparada byte a byte com o encoding/json configurado sem escape de
// HTML, em teste -- a afirmacao nao e "esta certo", e "e identico ao stdlib".
func AppendJSONString(dst []byte, s string) []byte {
	if jsonSimples(s) {
		dst = append(dst, '"')
		dst = append(dst, s...)
		return append(dst, '"')
	}

	dst = append(dst, '"')
	inicio := 0
	for i := 0; i < len(s); {
		if b := s[i]; b < utf8.RuneSelf {
			if b >= 0x20 && b != '"' && b != '\\' {
				i++
				continue
			}
			dst = append(dst, s[inicio:i]...)
			switch b {
			case '"':
				dst = append(dst, `\"`...)
			case '\\':
				dst = append(dst, `\\`...)
			case '\n':
				dst = append(dst, `\n`...)
			case '\r':
				dst = append(dst, `\r`...)
			case '\t':
				dst = append(dst, `\t`...)
			default:
				const hex = "0123456789abcdef"
				dst = append(dst, `\u00`...)
				dst = append(dst, hex[b>>4], hex[b&0xF])
			}
			i++
			inicio = i
			continue
		}

		r, tamanho := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && tamanho == 1 {
			// Byte invalido. O encoding/json do Go 1.25 escreve a sequencia
			// escapada e o do 1.27 escreve os bytes de U+FFFD; as duas sao o
			// mesmo code point, e o teste compara o VALOR nesses casos.
			dst = append(dst, s[inicio:i]...)
			dst = append(dst, "\ufffd"...)
			i += tamanho
			inicio = i
			continue
		}
		i += tamanho
	}
	dst = append(dst, s[inicio:]...)
	return append(dst, '"')
}

// jsonSimples diz se a string pode ir entre aspas sem nenhum escape.
func jsonSimples(s string) bool {
	for i := 0; i < len(s); i++ {
		b := s[i]
		if b < 0x20 || b == '"' || b == '\\' || b >= utf8.RuneSelf {
			return false
		}
	}
	return true
}
