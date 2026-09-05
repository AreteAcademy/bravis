package core

import "context"

// DestinationChecker e implementado por um destino que sabe conferir a
// declaracao contra a tabela real ANTES de a extracao acontecer.
//
// A conferencia ja existia -- ela roda no Write, com o lote na mao. O problema
// e o momento: num vendor com cota, chegar ate ali significa ter gasto a quota
// da janela inteira para descobrir que uma coluna nao bate. E o invariante I3
// do plan/2026-09-03-sdk-schema-declarado.md.
//
// Opcional de proposito. Um diretorio de arquivos nao tem esquema para
// conferir, e o Redshift precisaria de um cluster de pe -- e um destino que
// nao pode conferir cedo nao deve ser obrigado a fingir que pode.
type DestinationChecker interface {
	// CheckDestination confere a declaracao contra o destino real.
	//
	// Recebe os nomes declarados; devolve erro nomeando a coluna e os dois
	// lados. Um destino que ainda nao existe NAO e erro aqui: criar tabela e
	// decisao do Write, e recusar antes tiraria o CreateTable do caminho.
	CheckDestination(ctx context.Context, columns []string) error
}
