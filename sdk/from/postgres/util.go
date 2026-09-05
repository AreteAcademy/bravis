package postgres

import (
	"strings"
)

// primeiraLinha resume o SQL para log, sem despejar uma consulta de 40 linhas
// em toda linha de resumo.
func primeiraLinha(sql string) string {
	s := strings.TrimSpace(sql)
	if i := strings.IndexAny(s, "\n\r"); i >= 0 {
		s = strings.TrimSpace(s[:i]) + " …"
	}
	if len(s) > 120 {
		s = s[:117] + "…"
	}
	return s
}

// redigir tira o DSN de dentro de um erro. O pgx as vezes ecoa a string de
// conexao, e ela carrega senha -- que iria para log, para o Result e para
// qualquer lugar que mostre o erro.
func redigir(err error, dsn string) error {
	if err == nil || dsn == "" {
		return err
	}
	if !strings.Contains(err.Error(), dsn) {
		return err
	}
	return errString(strings.ReplaceAll(err.Error(), dsn, "REDACTED"))
}

type errString string

func (e errString) Error() string { return string(e) }
