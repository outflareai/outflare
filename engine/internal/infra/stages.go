package infra

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/audit"
	"github.com/outflareai/outflare/engine/internal/platforms"
)

// lifecycleSelect reads the mailboxes whose stage may change on its own:
// warming and resting ones, and ready ones that rest on their own.
const lifecycleSelect = `
	SELECT m.id::text, m.workspace_id::text, m.lifecycle_stage, m.stage_changed_at, m.stage_until, m.stage_reason,
	       m.auto_rest,
	       -- The sequencer that runs the warm-up gives its score, read as
	       -- the share of mail reaching the inbox.
	       m.warmup_enabled AND m.warmup_integration_id IS NOT NULL AND COALESCE(wp.warmup_enabled, false),
	       CASE WHEN wp.warmup_score IS NULL THEN 0 ELSE 100 END,
	       COALESCE(wp.warmup_score, 0),
	       COALESCE(d.checked_at IS NOT NULL AND (d.spf <> 'pass' OR d.dkim <> 'pass'
	                OR d.blocklists @> '[{"status": "listed"}]'), false),
	       p.rest_below, p.retire_after_days
	FROM mailboxes m
	LEFT JOIN sending_domains d ON d.workspace_id = m.workspace_id AND d.domain = m.domain
	LEFT JOIN infra_plans p ON p.id = m.infra_plan_id
	LEFT JOIN mailbox_platforms wp ON wp.mailbox_id = m.id AND wp.integration_id = m.warmup_integration_id
	WHERE m.lifecycle_stage IN ('warming', 'resting') OR (m.lifecycle_stage = 'ready' AND m.auto_rest)`

// Lifecycle moves every mailbox that is due to its next stage, and
// returns how many moved.
func (s *Service) Lifecycle(ctx context.Context) (int, error) {
	now := s.now()
	rows, err := s.Pool.Query(ctx, lifecycleSelect)
	if err != nil {
		return 0, err
	}
	type row struct {
		id, ws, reason string
		facts          MailboxFacts
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (row, error) {
		var x row
		var restBelow, retireAfter *int
		err := r.Scan(&x.id, &x.ws, &x.facts.Stage, &x.facts.StageChangedAt, &x.facts.StageUntil, &x.reason,
			&x.facts.AutoRest, &x.facts.WarmupEnabled, &x.facts.Placed, &x.facts.Inbox, &x.facts.DomainCritical,
			&restBelow, &retireAfter)
		if restBelow != nil {
			x.facts.RestBelow = float64(*restBelow) / 100
		}
		if retireAfter != nil {
			x.facts.RetireAfter = time.Duration(*retireAfter) * 24 * time.Hour
		}
		return x, err
	})
	if err != nil {
		return 0, err
	}
	moved := 0
	for _, x := range list {
		c := NextStage(x.facts, now)
		sameUntil := (c.Until == nil) == (x.facts.StageUntil == nil) && (c.Until == nil || c.Until.Equal(*x.facts.StageUntil))
		if c.Same && (c.Reason == "" || c.Reason == x.reason) && sameUntil {
			continue
		}
		err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
			if c.Same {
				_, err := tx.Exec(ctx, `
					UPDATE mailboxes SET stage_reason = CASE WHEN $3 = '' THEN stage_reason ELSE $3 END, stage_until = $4
					WHERE id = $1 AND lifecycle_stage = $2`, x.id, x.facts.Stage, c.Reason, c.Until)
				return err
			}
			ok, err := moveStage(ctx, tx, x.id, x.facts.Stage, c.Stage, c.Reason, c.Until)
			if err != nil || !ok {
				return err
			}
			moved++
			return audit.Record(ctx, tx, audit.Event{WorkspaceID: x.ws, ActorType: "system", Action: "mailbox.stage_changed",
				TargetType: "mailbox", TargetID: x.id, Data: map[string]any{"from": x.facts.Stage, "to": c.Stage, "reason": c.Reason}})
		})
		if err != nil {
			return moved, err
		}
	}
	return moved, nil
}

// moveStage changes a mailbox's stage if it is still in from. Retiring
// also pauses the mailbox and turns warm-up off, at the sequencer that
// runs it too.
func moveStage(ctx context.Context, tx pgx.Tx, id, from, to, reason string, until *time.Time) (bool, error) {
	var ws, sequencer *string
	if to == StageRetired {
		err := tx.QueryRow(ctx, `SELECT workspace_id::text, warmup_integration_id::text FROM mailboxes WHERE id = $1 AND warmup_enabled`,
			id).Scan(&ws, &sequencer)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return false, err
		}
	}
	tag, err := tx.Exec(ctx, `
		UPDATE mailboxes SET
			lifecycle_stage = $3, stage_reason = $4, stage_until = $5, stage_changed_at = now(),
			status = CASE WHEN $3 = 'retired' AND status <> 'error' THEN 'paused' ELSE status END,
			status_reason = CASE WHEN $3 = 'retired' THEN 'Retired: ' || $4 ELSE status_reason END,
			warmup_enabled = CASE WHEN $3 = 'retired' THEN false ELSE warmup_enabled END,
			updated_at = now()
		WHERE id = $1 AND lifecycle_stage = $2`, id, from, to, reason, until)
	if err != nil || tag.RowsAffected() != 1 {
		return false, err
	}
	if sequencer != nil {
		if err := platforms.RequestWarmup(ctx, tx, *ws, id, *sequencer); err != nil {
			return false, err
		}
	}
	return true, nil
}

// Move is a change of stage asked for by a person or agent.
type Move struct {
	To     string
	Reason string
	// When a rest ends, or a warm-up may finish at the earliest.
	Until *time.Time
	// For warming and resting: rest on its own when placement drops.
	AutoRest *bool
}

// MoveResult is what happened to one mailbox.
type MoveResult struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	From  string `json:"from"`
	To    string `json:"to"`
	// Why it did not move; empty when it did.
	Skipped string `json:"skipped,omitempty"`
	// The plan whose supplier the mailbox can be cancelled at.
	PlanID *string `json:"-"`
}

// ErrInvalidMove is returned for a stage that does not exist.
var ErrInvalidMove = errors.New("invalid stage")

// MoveMailboxes moves the workspace's given mailboxes by hand. A mailbox
// that cannot make the move is skipped with the reason.
func MoveMailboxes(ctx context.Context, tx pgx.Tx, ws string, ids []string, m Move) ([]MoveResult, error) {
	switch m.To {
	case StageWarming, StageReady, StageResting, StageRetired:
	default:
		return nil, ErrInvalidMove
	}
	rows, err := tx.Query(ctx, `
		SELECT id::text, email, lifecycle_stage, infra_plan_id::text FROM mailboxes
		WHERE workspace_id = $1 AND id = ANY($2::uuid[]) ORDER BY lower(email) FOR UPDATE`, ws, ids)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (MoveResult, error) {
		var x MoveResult
		return x, r.Scan(&x.ID, &x.Email, &x.From, &x.PlanID)
	})
	if err != nil {
		return nil, err
	}
	for i := range list {
		x := &list[i]
		x.To = m.To
		if !CanMove(x.From, m.To) {
			x.To = x.From
			x.Skipped = skipReason(x.From, m.To)
			continue
		}
		if _, err := moveStage(ctx, tx, x.ID, x.From, m.To, m.Reason, m.Until); err != nil {
			return nil, err
		}
		if m.AutoRest != nil {
			if _, err := tx.Exec(ctx, `UPDATE mailboxes SET auto_rest = $2 WHERE id = $1`, x.ID, *m.AutoRest); err != nil {
				return nil, err
			}
		}
	}
	return list, nil
}

func skipReason(from, to string) string {
	switch {
	case from == to:
		return "already " + from
	case from == StageRetired:
		return "retired mailboxes stay retired; connect it again to reuse it"
	}
	return "cannot go from " + from + " to " + to
}
