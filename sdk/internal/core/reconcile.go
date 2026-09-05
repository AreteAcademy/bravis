package core

import (
	"fmt"
	"sort"
	"strings"
)

// Reconcile decides which columns a load writes, and refuses the cases where
// writing would lose or misplace data.
//
// O destino e a autoridade: e ele que tem de ser satisfeito, e e dele que sai
// a ORDEM -- que importa porque `COPY FROM` e `INSERT ROW` casam valores por
// POSICAO, nao por nome. Foi exatamente isso que custou a v0.12.0 no BigQuery.
//
// A regra e assimetrica de proposito:
//
//   - campo no registro que o destino nao tem -> ERRO nomeando o campo.
//     Seguir descartaria aquele dado sem sinal nenhum, que e o pior modo de
//     falhar: ele some e nada diz.
//   - coluna no destino que o registro nao traz -> tudo bem, fica NULL. Uma
//     tabela de landing legitimamente faz isso.
//
// Vive aqui, e nao no pacote de um destino, porque os quatro destinos com
// esquema tem o mesmo problema. A conferencia de TIPO nao sobe junto: no
// BigQuery ela e feita contra o schema declarado, e nos destinos SQL quem
// recusa o tipo errado e o proprio servidor, na hora do INSERT.
func Reconcile(dest, incoming []string, destino string) ([]string, error) {
	temNoDestino := make(map[string]bool, len(dest))
	for _, c := range dest {
		temNoDestino[c] = true
	}

	var sobrando []string
	trazido := make(map[string]bool, len(incoming))
	for _, c := range incoming {
		trazido[c] = true
		if !temNoDestino[c] {
			sobrando = append(sobrando, c)
		}
	}

	if len(sobrando) > 0 {
		sort.Strings(sobrando)
		return nil, fmt.Errorf("the rows carry column(s) %s, which %s does not have. "+
			"They would be silently dropped, so the load stops here: add the column to the "+
			"table, or remove the field in Transform",
			strings.Join(sobrando, ", "), destino)
	}

	cols := make([]string, 0, len(dest))
	for _, c := range dest {
		if trazido[c] {
			cols = append(cols, c)
		}
	}
	if len(cols) == 0 {
		return nil, fmt.Errorf("no column in common between the rows and the destination")
	}
	return cols, nil
}
