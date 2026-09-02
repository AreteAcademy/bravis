package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	sch "github.com/AreteAcademy/bravis/internal/domain/schedule"
	wf "github.com/AreteAcademy/bravis/internal/domain/workflow"
)

// WorkflowRepo persiste a definicao publicada de um workflow.
type WorkflowRepo struct{ pool *Pool }

func NewWorkflowRepo(p *Pool) *WorkflowRepo { return &WorkflowRepo{pool: p} }

// Publicar grava o workflow e sua agenda numa transacao.
//
// As duas coisas juntas, e nao em chamadas separadas: publicar o grafo sem a
// agenda deixaria um workflow que nunca dispara, e a agenda sem o grafo faria o
// scheduler criar runs de algo que nao existe.
func (r *WorkflowRepo) Publicar(ctx context.Context, w wf.Workflow, projeto uuid.UUID) error {
	def, err := json.Marshal(w)
	if err != nil {
		return err
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op depois do commit

	_, err = tx.Exec(ctx, `
		INSERT INTO workflows (id, project_id, slug, name, definicao)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (project_id, slug) DO UPDATE
		SET name = EXCLUDED.name, definicao = EXCLUDED.definicao, updated_at = now()`,
		uuid.New(), projeto, w.Slug, w.Name, def)
	if err != nil {
		return fmt.Errorf("publicando workflow %q: %w", w.Slug, err)
	}

	if w.Schedule == "" {
		// Sem cron: remove a agenda se existia. Tirar o `schedule` do YAML deve
		// desagendar, nao deixar a agenda antiga viva.
		if _, err := tx.Exec(ctx, `DELETE FROM schedules WHERE workflow_slug = $1`, w.Slug); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}

	// `ultimo_slot` NAO e sobrescrito no update: republicar um workflow nao pode
	// fazer o scheduler recriar slots ja materializados.
	_, err = tx.Exec(ctx, `
		INSERT INTO schedules (id, workflow_slug, cron, timezone, catchup, ativo)
		VALUES ($1, $2, $3, $4, $5, true)
		ON CONFLICT (workflow_slug) DO UPDATE
		SET cron = EXCLUDED.cron, timezone = EXCLUDED.timezone,
		    catchup = EXCLUDED.catchup, atualizado_em = now()`,
		uuid.New(), w.Slug, w.Schedule, "UTC", false)
	if err != nil {
		return fmt.Errorf("publicando agenda de %q: %w", w.Slug, err)
	}
	return tx.Commit(ctx)
}

// Definicao le o grafo publicado.
func (r *WorkflowRepo) Definicao(ctx context.Context, slug string) (wf.Workflow, error) {
	var bruto []byte
	if err := r.pool.QueryRow(ctx,
		`SELECT definicao FROM workflows WHERE slug = $1`, slug).Scan(&bruto); err != nil {
		return wf.Workflow{}, fmt.Errorf("workflow %q: %w", slug, err)
	}
	var w wf.Workflow
	return w, json.Unmarshal(bruto, &w)
}

// Podar remove do projeto os workflows que NAO estao na lista, junto com suas
// agendas. Devolve os slugs removidos.
//
// Existe porque publicar so adicionava: tirar um arquivo da pasta nao tirava
// nada do banco, e o scheduler continuava materializando runs de um workflow que
// ninguem enxergava mais. Com agendas de 15 minutos, isso e trabalho invisivel
// rodando para sempre.
//
// O historico (`runs`) NAO e apagado: ele referencia o slug como texto, nao por
// chave estrangeira, justamente para sobreviver a remocao da definicao. Apagar
// a execucao junto seria apagar a evidencia do que aconteceu.
func (r *WorkflowRepo) Podar(ctx context.Context, projeto uuid.UUID, manter []string) ([]string, error) {
	linhas, err := r.pool.Query(ctx, `
		DELETE FROM workflows
		WHERE project_id = $1 AND NOT (slug = ANY($2))
		RETURNING slug`, projeto, manter)
	if err != nil {
		return nil, err
	}
	defer linhas.Close()

	var removidos []string
	for linhas.Next() {
		var slug string
		if err := linhas.Scan(&slug); err != nil {
			return nil, err
		}
		removidos = append(removidos, slug)
	}
	if err := linhas.Err(); err != nil {
		return nil, err
	}
	if len(removidos) == 0 {
		return nil, nil
	}

	// A agenda vive numa tabela separada, ligada por slug em texto — o CASCADE
	// nao a alcanca, e uma agenda orfa continuaria criando runs.
	if _, err := r.pool.Exec(ctx,
		`DELETE FROM schedules WHERE workflow_slug = ANY($1)`, removidos); err != nil {
		return removidos, err
	}
	return removidos, nil
}

// ScheduleRepo le e atualiza agendas.
type ScheduleRepo struct{ pool *Pool }

func NewScheduleRepo(p *Pool) *ScheduleRepo { return &ScheduleRepo{pool: p} }

// Ativas lista as agendas que o scheduler deve avaliar.
func (r *ScheduleRepo) Ativas(ctx context.Context) ([]sch.Schedule, error) {
	linhas, err := r.pool.Query(ctx, `
		SELECT workflow_slug, cron, timezone, catchup, ativo, ultimo_slot
		FROM schedules WHERE ativo`)
	if err != nil {
		return nil, err
	}
	defer linhas.Close()

	var out []sch.Schedule
	for linhas.Next() {
		var s sch.Schedule
		if err := linhas.Scan(&s.WorkflowSlug, &s.Cron, &s.Timezone,
			&s.Catchup, &s.Ativo, &s.UltimoSlot); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, linhas.Err()
}

// AvancarSlot marca ate onde a agenda ja foi materializada.
//
// A condicao `ultimo_slot IS NULL OR ultimo_slot < $2` torna a operacao
// idempotente e segura sob concorrencia: dois schedulers avaliando a mesma
// agenda nunca fazem o marcador retroceder.
// DefinirAtivo pausa ou retoma uma agenda e devolve o estado resultante.
//
// Devolve em vez de so gravar porque a UI alterna sem saber o valor atual: sem o
// retorno, a tela precisaria de uma segunda consulta e ficaria sujeita a corrida
// entre dois operadores clicando ao mesmo tempo.
//
// Pausar NAO cancela o que ja esta na fila: os runs materializados sao trabalho
// aceito, e descarta-los ao pausar surpreenderia quem so queria parar de criar
// novos.
func (r *ScheduleRepo) DefinirAtivo(ctx context.Context, slug string, ativo bool) (bool, error) {
	var resultado bool
	err := r.pool.QueryRow(ctx, `
		UPDATE schedules SET ativo = $2, atualizado_em = now()
		WHERE workflow_slug = $1
		RETURNING ativo`, slug, ativo).Scan(&resultado)
	return resultado, err
}

// Alternar inverte o estado atual numa unica ida ao banco.
func (r *ScheduleRepo) Alternar(ctx context.Context, slug string) (bool, error) {
	var resultado bool
	err := r.pool.QueryRow(ctx, `
		UPDATE schedules SET ativo = NOT ativo, atualizado_em = now()
		WHERE workflow_slug = $1
		RETURNING ativo`, slug).Scan(&resultado)
	return resultado, err
}

func (r *ScheduleRepo) AvancarSlot(ctx context.Context, slug string, slot time.Time) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE schedules
		SET ultimo_slot = $2, atualizado_em = now()
		WHERE workflow_slug = $1 AND (ultimo_slot IS NULL OR ultimo_slot < $2)`,
		slug, slot)
	return err
}
