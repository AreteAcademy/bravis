package execution

import (
	"fmt"
	"strings"
)

// TetoDoLog e quanto da saida de um passo vai para o banco.
//
// 128 KB cobre com folga uma run de dbt com 60 nos (~25 KB de texto) e ainda
// segura um backfill barulhento. O teto existe porque um `while true; do echo`
// num workflow qualquer nao pode encher o disco do Postgres.
const TetoDoLog = 128 << 10

// fatiaDoInicio e quanto do teto fica reservado para o COMECO da saida quando
// ela nao cabe inteira. So o fim seria mais simples, mas perderia o comando que
// rodou e a configuracao que ele imprimiu na largada — metade do diagnostico.
const fatiaDoInicio = TetoDoLog / 4

// janela acumula a saida de um passo respeitando o teto, guardando o comeco e o
// fim e dizendo quanto descartou no meio.
//
// A alternativa — cortar quando estoura e ficar so com o comeco — perde
// exatamente o trecho onde um programa relata por que falhou.
type janela struct {
	inicio  strings.Builder
	fim     []string // buffer circular das linhas recentes
	fimLen  int      // bytes vivos em `fim`
	cortado int      // bytes descartados no meio
}

// Escrever acrescenta uma linha.
func (j *janela) Escrever(linha string) {
	n := len(linha) + 1 // +1 pela quebra

	if j.inicio.Len()+n <= fatiaDoInicio {
		j.inicio.WriteString(linha)
		j.inicio.WriteByte('\n')
		return
	}

	j.fim = append(j.fim, linha)
	j.fimLen += n

	// Descarta pela frente ate voltar ao teto. O que sai daqui ja passou pelo
	// comeco, entao e mesmo o miolo — a parte menos util das duas pontas.
	for j.fimLen > TetoDoLog-fatiaDoInicio && len(j.fim) > 1 {
		j.cortado += len(j.fim[0]) + 1
		j.fimLen -= len(j.fim[0]) + 1
		j.fim = j.fim[1:]
	}
}

// String monta o texto final, com a marca do que ficou de fora.
//
// Toda linha sai terminada em quebra, inclusive a ultima: um log em que as
// primeiras linhas terminam em \n e as ultimas nao e desconfortavel de ler e
// arma armadilha para quem for concatenar depois.
func (j *janela) String() string {
	if j.cortado == 0 {
		return j.inicio.String() + linhas(j.fim)
	}
	// A marca e explicita: um log truncado em silencio faz o leitor concluir
	// que o programa parou ali.
	var b strings.Builder
	b.WriteString(j.inicio.String())
	fmt.Fprintf(&b, "\n[... %s omitidos pelo limite de %s por passo ...]\n\n",
		emKB(j.cortado), emKB(TetoDoLog))
	b.WriteString(linhas(j.fim))
	return b.String()
}

// linhas junta terminando cada uma com quebra. Vazio continua vazio — nao gera
// uma quebra solta.
func linhas(ls []string) string {
	if len(ls) == 0 {
		return ""
	}
	return strings.Join(ls, "\n") + "\n"
}

func emKB(n int) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	return fmt.Sprintf("%d KB", n/1024)
}
