package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	sch "github.com/zarvhq/bravis/internal/domain/schedule"
	wf "github.com/zarvhq/bravis/internal/domain/workflow"
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
func (r *ScheduleRepo) AvancarSlot(ctx context.Context, slug string, slot time.Time) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE schedules
		SET ultimo_slot = $2, atualizado_em = now()
		WHERE workflow_slug = $1 AND (ultimo_slot IS NULL OR ultimo_slot < $2)`,
		slug, slot)
	return err
}
