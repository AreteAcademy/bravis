package execution_test

import (
	"context"
	"testing"

	"github.com/AreteAcademy/bravis/internal/execution"
)

func tarefa(nome string) execution.Task {
	return execution.FuncTask{Nome: nome, Fn: func(context.Context, execution.Input) error { return nil }}
}

// Sobrescrever um registro em silencio e um bug que so aparece em producao,
// quando a task errada roda.
func TestRegisterRecusaDuplicado(t *testing.T) {
	r := execution.NewRegistry()
	if err := r.Register(tarefa("sync")); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(tarefa("sync")); err == nil {
		t.Fatal("esperava recusa de nome duplicado")
	}
}

func TestRegisterRecusaNomeVazio(t *testing.T) {
	if err := execution.NewRegistry().Register(tarefa("")); err == nil {
		t.Fatal("esperava recusa de nome vazio")
	}
}

func TestNomesVemOrdenados(t *testing.T) {
	r := execution.NewRegistry()
	for _, n := range []string{"zeta", "alfa", "meio"} {
		r.MustRegister(tarefa(n))
	}
	got := r.Nomes()
	if len(got) != 3 || got[0] != "alfa" || got[2] != "zeta" {
		t.Errorf("Nomes() = %v, queria ordenado", got)
	}
}

func TestInputTextoValidaParametro(t *testing.T) {
	in := execution.Input{With: map[string]any{"image": "acme:1.0", "porta": 8080}}

	if v, err := in.Texto("image"); err != nil || v != "acme:1.0" {
		t.Errorf("Texto(image) = %q, %v", v, err)
	}
	if _, err := in.Texto("ausente"); err == nil {
		t.Error("esperava erro de parametro ausente")
	}
	if _, err := in.Texto("porta"); err == nil {
		t.Error("esperava erro de tipo: porta e int, nao texto")
	}
}
