package api

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"

	wf "github.com/AreteAcademy/bravis/internal/domain/workflow"
	"github.com/AreteAcademy/bravis/internal/graph"
	"github.com/AreteAcademy/bravis/internal/infrastructure/postgres"
)

// Este arquivo implementa o fluxo da secao 20 do plano:
//
//	definicao no banco -> API -> JSON do React Flow -> UI
//
// O React Flow e camada de VISUALIZACAO, nunca fonte da verdade. O layout sai
// daqui, do servidor, reaproveitando o mesmo `graph.Niveis` que o executor usa
// para decidir o que roda em paralelo. Assim o desenho corresponde a execucao
// real: nos na mesma coluna sao os que rodam juntos de fato, e nao um palpite
// de um algoritmo de layout no navegador.

type noFlow struct {
	ID       string         `json:"id"`
	Type     string         `json:"type"`
	Position posicao        `json:"position"`
	Data     map[string]any `json:"data"`
}

type posicao struct {
	X int `json:"x"`
	Y int `json:"y"`
}

type arestaFlow struct {
	ID       string `json:"id"`
	Source   string `json:"source"`
	Target   string `json:"target"`
	Animated bool   `json:"animated"`
}

type respostaGrafo struct {
	Slug     string       `json:"slug"`
	RunID    string       `json:"run_id,omitempty"`
	Status   string       `json:"status,omitempty"`
	Terminal bool         `json:"terminal"`
	Nodes    []noFlow     `json:"nodes"`
	Edges    []arestaFlow `json:"edges"`
}

// Espacamento do layout. Constantes e nao configuracao: o tamanho do no e fixo
// no CSS, e deixar isso ajustavel so criaria duas fontes da verdade.
const (
	larguraNivel = 260
	alturaNo     = 110
)

// grafoDoWorkflow desenha a definicao PUBLICADA, sem estado de execucao. E a
// tela de "como este workflow e", que precisa funcionar para um workflow que
// nunca rodou.
func (u *UI) grafoDoWorkflow(w http.ResponseWriter, r *http.Request) {
	def, err := u.defs.Definicao(r.Context(), r.PathValue("slug"))
	if err != nil {
		http.Error(w, "workflow nao encontrado", http.StatusNotFound)
		return
	}
	u.responderGrafo(w, def, nil, "", "")
}

// grafoDaRun desenha o SNAPSHOT gravado no Run, nao a definicao atual: se o
// workflow foi editado depois, a tela de uma execucao passada tem que continuar
// mostrando o grafo que de fato rodou (secao 22).
func (u *UI) grafoDaRun(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.Error(w, "id invalido", http.StatusBadRequest)
		return
	}
	ctx := r.Context()

	execucao, err := u.execs.Buscar(ctx, id)
	if err != nil {
		http.Error(w, "run nao encontrada", http.StatusNotFound)
		return
	}
	var def wf.Workflow
	if err := json.Unmarshal(execucao.Definicao, &def); err != nil {
		u.erro(w, r, err)
		return
	}

	// Estado ausente nao e erro: uma run recem-enfileirada ainda nao tem nenhum
	// passo iniciado, e a tela deve mostrar o grafo todo em cinza.
	estados, err := u.execs.EstadoDosNos(ctx, id)
	if err != nil {
		u.log.Warn("estado dos nos indisponivel", "run", id, "erro", err)
		estados = nil
	}
	u.responderGrafo(w, def, estados, id.String(), string(execucao.Status))
}

func (u *UI) responderGrafo(w http.ResponseWriter, def wf.Workflow,
	estados map[string]postgres.EstadoNo, runID, status string) {

	niveis, err := graph.Niveis(def)
	if err != nil {
		// Chegar aqui significa grafo ciclico gravado no banco. Nao e 500: e um
		// dado invalido, e a mensagem tem que dizer isso na tela.
		http.Error(w, "grafo invalido: "+err.Error(), http.StatusUnprocessableEntity)
		return
	}

	resp := respostaGrafo{
		Slug: def.Slug, RunID: runID, Status: status,
		// `failed` NAO entra: a maquina de estados da secao 7 permite
		// failed -> retrying, entao o cliente ainda precisa consultar (mais
		// devagar). Marcar failed como terminal congelaria a tela no meio de um
		// retry em andamento.
		Terminal: status == "success" || status == "canceled",
		Nodes:    []noFlow{}, Edges: []arestaFlow{},
	}

	for nivel, ids := range niveis {
		// Centraliza cada coluna: um nivel de um no so fica alinhado ao centro
		// dos demais, em vez de encostado no topo.
		desloc := -(len(ids) - 1) * alturaNo / 2
		for i, id := range ids {
			no := acharNo(def.Nodes, id)
			dados := map[string]any{
				"label":  id,
				"acao":   rotuloDaAcao(no),
				"status": "pending",
			}
			if e, ok := estados[id]; ok {
				dados["status"] = e.Status
				dados["duracao_ms"] = e.DuracaoMs
				dados["tentativa"] = e.Tentativa
				if e.Erro != "" {
					dados["erro"] = e.Erro
				}
				if e.ExitCode != nil {
					dados["exit_code"] = *e.ExitCode
				}
			}
			resp.Nodes = append(resp.Nodes, noFlow{
				ID: id, Type: "bravis",
				Position: posicao{X: nivel * larguraNivel, Y: desloc + i*alturaNo},
				Data:     dados,
			})
		}
	}

	for _, e := range def.Edges {
		resp.Edges = append(resp.Edges, arestaFlow{
			ID: e.From + "->" + e.To, Source: e.From, Target: e.To,
			// So anima a aresta que chega no que esta correndo agora: animar
			// tudo vira ruido e some com a informacao.
			Animated: estados[e.To].Status == "running",
		})
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		u.log.Error("serializando grafo", "slug", def.Slug, "erro", err)
	}
}

func acharNo(nodes []wf.Node, id string) wf.Node {
	for _, n := range nodes {
		if n.ID == id {
			return n
		}
	}
	return wf.Node{}
}

// rotuloDaAcao e a segunda linha do card: o que o no faz, nao como se chama.
func rotuloDaAcao(n wf.Node) string {
	if n.Action != "" {
		return n.Action
	}
	if len(n.Run) > 42 {
		return n.Run[:39] + "..."
	}
	return n.Run
}
