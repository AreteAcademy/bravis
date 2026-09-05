package bigquery

import (
	"context"

	"github.com/AreteAcademy/brevis/sdk/internal/core"
	"github.com/AreteAcademy/brevis/sdk/load"
)

// CheckDestination satisfaz core.DestinationChecker.
//
// Ela existe para que a divergencia entre o que o fetcher declara e a tabela
// real apareca ANTES da extracao. A conferencia em si e a mesma que o Write ja
// fazia; o que muda e o momento, e num vendor com cota isso e a diferenca
// entre uma consulta de metadados e a janela inteira de quota.
func (b Table) CheckDestination(ctx context.Context, columns []string) error {
	if len(columns) == 0 {
		return nil
	}

	cfg, _, err := b.config(core.WriteOptions{Columns: columns})
	if err != nil {
		return err
	}

	loader, err := load.New(ctx, cfg)
	if err != nil {
		return err
	}
	return loader.CheckDestination(ctx, columns)
}
