// Package graph resolve ordem de execucao a partir do grafo do workflow.
package graph

import (
	"fmt"

	wf "github.com/AreteAcademy/brevis/internal/domain/workflow"
)

// Niveis devolve os nos agrupados por nivel topologico: tudo no nivel N pode
// rodar em paralelo, e o nivel N+1 so comeca quando o N termina.
//
// Agrupar por nivel, em vez de devolver uma lista linear, e o que preserva o
// paralelismo declarado. Uma ordenacao topologica simples serializaria
// gold_metrics e gold_users, que sao independentes.
func Niveis(w wf.Workflow) ([][]string, error) {
	entrada := make(map[string]int, len(w.Nodes))
	saida := make(map[string][]string, len(w.Nodes))
	for _, n := range w.Nodes {
		entrada[n.ID] = 0
	}
	for _, e := range w.Edges {
		entrada[e.To]++
		saida[e.From] = append(saida[e.From], e.To)
	}

	// primeiro nivel: tudo sem dependencia, na ordem do arquivo para a saida ser
	// deterministica
	var atual []string
	for _, n := range w.Nodes {
		if entrada[n.ID] == 0 {
			atual = append(atual, n.ID)
		}
	}

	var niveis [][]string
	vistos := 0
	for len(atual) > 0 {
		niveis = append(niveis, atual)
		vistos += len(atual)

		var proximo []string
		for _, n := range w.Nodes { // itera pelos nos, nao pelo mapa: determinismo
			if !contem(atual, n.ID) {
				continue
			}
			for _, dest := range saida[n.ID] {
				entrada[dest]--
				if entrada[dest] == 0 {
					proximo = append(proximo, dest)
				}
			}
		}
		atual = proximo
	}

	// Workflow.Validate ja recusa ciclos; esta guarda protege contra um grafo
	// montado em codigo sem passar pela validacao.
	if vistos != len(w.Nodes) {
		return nil, fmt.Errorf("grafo tem ciclo ou no inalcancavel (%d de %d nos ordenados)", vistos, len(w.Nodes))
	}
	return niveis, nil
}

func contem(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
