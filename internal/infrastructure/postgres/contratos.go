package postgres

import (
	app "github.com/AreteAcademy/brevis/internal/application/execution"
)

// O que o runner espera deste repositorio, checado em tempo de compilacao.
//
// Sem isto, uma assinatura que muda de um lado so aparece quando alguem
// monta os dois — que hoje nao acontece em lugar nenhum do codigo, porque a
// ligacao dispatcher -> Runner ainda nao existe.
var (
	_ app.Historico   = (*RunRepo)(nil)
	_ app.Persistidor = (*RunRepo)(nil)
)
