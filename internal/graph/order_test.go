package graph

import (
	"testing"

	wf "github.com/AreteAcademy/bravis/internal/domain/workflow"
)

func TestNiveisEmCadeia(t *testing.T) {
	w := wf.Workflow{
		Nodes: []wf.Node{{ID: "a"}, {ID: "b"}, {ID: "c"}},
		Edges: []wf.Edge{{From: "a", To: "b"}, {From: "b", To: "c"}},
	}
	n, err := Niveis(w)
	if err != nil {
		t.Fatal(err)
	}
	if len(n) != 3 {
		t.Fatalf("niveis = %d, queria 3 (cadeia nao paraleliza)", len(n))
	}
}

// O ponto do agrupamento por nivel: gold_metrics e gold_users sao independentes
// e devem sair juntas. Uma ordenacao topologica linear as serializaria.
func TestNiveisPreservamParalelismo(t *testing.T) {
	w := wf.Workflow{
		Nodes: []wf.Node{{ID: "silver"}, {ID: "metrics"}, {ID: "users"}, {ID: "publish"}},
		Edges: []wf.Edge{
			{From: "silver", To: "metrics"},
			{From: "silver", To: "users"},
			{From: "metrics", To: "publish"},
			{From: "users", To: "publish"},
		},
	}
	n, err := Niveis(w)
	if err != nil {
		t.Fatal(err)
	}
	if len(n) != 3 {
		t.Fatalf("niveis = %v, queria 3", n)
	}
	if len(n[1]) != 2 {
		t.Errorf("nivel 1 = %v, queria metrics e users juntos", n[1])
	}
	if len(n[2]) != 1 || n[2][0] != "publish" {
		t.Errorf("nivel 2 = %v, queria publish sozinho no fim", n[2])
	}
}

func TestNiveisSemArestasRodamTodosJuntos(t *testing.T) {
	w := wf.Workflow{Nodes: []wf.Node{{ID: "a"}, {ID: "b"}, {ID: "c"}}}
	n, err := Niveis(w)
	if err != nil {
		t.Fatal(err)
	}
	if len(n) != 1 || len(n[0]) != 3 {
		t.Fatalf("niveis = %v; sem dependencia tudo e um nivel so", n)
	}
}

func TestNiveisDetectaCicloMontadoEmCodigo(t *testing.T) {
	w := wf.Workflow{
		Nodes: []wf.Node{{ID: "a"}, {ID: "b"}},
		Edges: []wf.Edge{{From: "a", To: "b"}, {From: "b", To: "a"}},
	}
	if _, err := Niveis(w); err == nil {
		t.Fatal("esperava erro de ciclo")
	}
}
