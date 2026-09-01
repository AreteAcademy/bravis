package workflow

import "testing"

func no(id, run string) Node { return Node{ID: id, Run: run} }

func TestValidateAceitaCadeiaSimples(t *testing.T) {
	w := Workflow{
		Slug:  "daily-report",
		Kind:  KindChain,
		Nodes: []Node{no("a", "echo a"), no("b", "echo b")},
		Edges: []Edge{{From: "a", To: "b"}},
	}
	if err := w.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestValidateRecusaIDDuplicado(t *testing.T) {
	w := Workflow{Slug: "x", Nodes: []Node{no("a", "echo"), no("a", "echo")}}
	if err := w.Validate(); err == nil {
		t.Fatal("esperava erro de id duplicado")
	}
}

func TestValidateRecusaDependenciaInexistente(t *testing.T) {
	w := Workflow{
		Slug:  "x",
		Nodes: []Node{no("a", "echo")},
		Edges: []Edge{{From: "fantasma", To: "a"}},
	}
	if err := w.Validate(); err == nil {
		t.Fatal("esperava erro de dependencia inexistente")
	}
}

// O ciclo e a invariante que mais importa: um grafo ciclico trava o executor em
// vez de falhar, e o erro precisa dizer QUAIS steps o formam.
func TestValidateEncontraCicloEMostraOCaminho(t *testing.T) {
	w := Workflow{
		Slug:  "x",
		Nodes: []Node{no("a", "e"), no("b", "e"), no("c", "e")},
		Edges: []Edge{{From: "a", To: "b"}, {From: "b", To: "c"}, {From: "c", To: "a"}},
	}
	err := w.Validate()
	if err == nil {
		t.Fatal("esperava erro de ciclo")
	}
	if got := err.Error(); !contem(got, "a -> b -> c -> a") {
		t.Errorf("erro = %q; queria o caminho do ciclo", got)
	}
}

func TestValidateRecusaAutoDependencia(t *testing.T) {
	w := Workflow{Slug: "x", Nodes: []Node{no("a", "e")}, Edges: []Edge{{From: "a", To: "a"}}}
	if err := w.Validate(); err == nil {
		t.Fatal("esperava erro de auto-dependencia")
	}
}

func TestValidateExigeExatamenteUmaFormaDeExecucao(t *testing.T) {
	casos := map[string]Node{
		"nem run nem action": {ID: "a"},
		"run e action":       {ID: "a", Run: "echo", Action: "docker.run"},
		"with sem action":    {ID: "a", Run: "echo", With: map[string]any{"image": "x"}},
	}
	for nome, n := range casos {
		t.Run(nome, func(t *testing.T) {
			w := Workflow{Slug: "x", Nodes: []Node{n}}
			if err := w.Validate(); err == nil {
				t.Fatalf("esperava erro para %q", nome)
			}
		})
	}
}

func TestValidateRecusaWorkflowVazio(t *testing.T) {
	if err := (Workflow{Slug: "x"}).Validate(); err == nil {
		t.Fatal("esperava erro de workflow sem steps")
	}
}

func contem(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
