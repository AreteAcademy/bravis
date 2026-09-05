package redshift

import (
	"bytes"
	"strconv"

	"github.com/AreteAcademy/brevis/sdk/internal/core"
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

// escreverTexto delega ao core: a mesma regra e precisa aqui e no canonico do
// pycompat, e ter duas copias dela e ter duas chances de divergir do Python
// sem ninguem notar.
func escreverTexto(buf *bytes.Buffer, s string) {
	buf.Write(core.AppendJSONString(buf.AvailableBuffer(), s))
}
