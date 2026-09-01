package execution

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

// Task e uma unidade de trabalho escrita em Go, compilada junto com o binario.
//
// A secao 14 do plano e categorica: "Nao executar codigo arbitrario recebido
// pela API. Tasks locais devem ser compiladas e registradas no runtime". O
// registry existe para tornar isso estrutural — o YAML so pode citar o NOME de
// algo que ja esta no binario, nunca fornecer o codigo.
type Task interface {
	Name() string
	Run(ctx context.Context, in Input) error
}

// Input e o que a task recebe.
type Input struct {
	NodeID string
	With   map[string]any

	// Log emite uma linha para o fluxo de eventos. Existe para que a task
	// reporte progresso sem conhecer canais nem o executor.
	Log func(msg string)
}

// Texto le um parametro obrigatorio de `with`. Conveniencia com erro util: uma
// task que faz a asserção de tipo na mao repete a mesma mensagem ruim.
func (i Input) Texto(chave string) (string, error) {
	v, ok := i.With[chave]
	if !ok {
		return "", fmt.Errorf("parametro %q ausente em `with`", chave)
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("parametro %q deve ser texto, veio %T", chave, v)
	}
	return s, nil
}

// Registry guarda as tasks disponiveis. Seguro para uso concorrente porque o
// dispatcher consulta de varias goroutines.
type Registry struct {
	mu    sync.RWMutex
	tasks map[string]Task
}

func NewRegistry() *Registry {
	return &Registry{tasks: map[string]Task{}}
}

// Register adiciona uma task.
//
// Recusa nome duplicado em vez de sobrescrever: registro silenciosamente
// substituido e um bug que so aparece em producao, quando a task errada roda.
func (r *Registry) Register(t Task) error {
	nome := t.Name()
	if nome == "" {
		return fmt.Errorf("task sem nome")
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, existe := r.tasks[nome]; existe {
		return fmt.Errorf("task %q ja registrada", nome)
	}
	r.tasks[nome] = t
	return nil
}

// MustRegister registra e entra em panico se falhar.
//
// Para uso em `init()` ou no boot: um registro invalido e erro de programacao, e
// falhar no start e melhor que descobrir na primeira execucao agendada.
func (r *Registry) MustRegister(t Task) {
	if err := r.Register(t); err != nil {
		panic(err)
	}
}

// Get busca uma task pelo nome.
func (r *Registry) Get(nome string) (Task, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tasks[nome]
	return t, ok
}

// Nomes lista o que esta registrado, ordenado. Serve ao erro de task
// desconhecida: dizer o que existe economiza uma ida a documentacao.
func (r *Registry) Nomes() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]string, 0, len(r.tasks))
	for n := range r.tasks {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// FuncTask adapta uma funcao a interface Task, para os casos em que um tipo
// proprio seria cerimonia sem ganho.
type FuncTask struct {
	Nome string
	Fn   func(ctx context.Context, in Input) error
}

func (f FuncTask) Name() string { return f.Nome }
func (f FuncTask) Run(ctx context.Context, in Input) error {
	return f.Fn(ctx, in)
}
