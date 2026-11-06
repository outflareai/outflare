package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/support"
)

// Held Auto replies: by email, an answer Auto would send waits the brand's
// hold (email_hold_minutes) as a draft with send_at set. The conversation
// shows when it goes; a teammate can stop it (it stays a draft), edit and
// send it, or discard it. When the hold is over, it goes only if nothing
// changed: Auto still on, no reply from the team, no newer message, and
// the conversation still open.

const jobAssistantSend = "support.assistant_send"

// assistantEmailHold is how many minutes an Auto email reply waits, for the
// conversation's brand (its own settings, else the workspace's).
func assistantEmailHold(ctx context.Context, q jobs.Querier, ws string, brand *string) (int, error) {
	var hold int
	err := q.QueryRow(ctx, `SELECT email_hold_minutes FROM support_assistant_settings
		WHERE workspace_id = $1 AND brand_id IS NOT DISTINCT FROM $2::uuid LIMIT 1`, ws, brand).Scan(&hold)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	return hold, err
}

// holdAssistantReply sets when a drafted run goes and queues its send.
func holdAssistantReply(ctx context.Context, tx pgx.Tx, ws, run string, hold time.Duration) error {
	at := time.Now().Add(hold)
	if _, err := tx.Exec(ctx, `UPDATE support_assistant_runs SET send_at = $2 WHERE id = $1`, run, at); err != nil {
		return err
	}
	_, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: jobAssistantSend, WorkspaceID: &ws, MaxAttempts: 3, RunAt: at,
		Payload: map[string]any{"run_id": run}, IdempotencyKey: jobAssistantSend + ":" + run})
	return err
}

// assistantSendJob sends a held reply once its hold is over, if it still
// should go.
func (s *Server) assistantSendJob(ctx context.Context, job jobs.Job) error {
	var p struct {
		Run string `json:"run_id"`
	}
	if err := json.Unmarshal(job.Payload, &p); err != nil {
		return err
	}
	if job.WorkspaceID == nil {
		return errors.New("no workspace")
	}
	ws := *job.WorkspaceID
	sent, result := false, ""
	var channel string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var conv, action, reply, status string
		var brand *string
		var created, sendAt time.Time
		err := tx.QueryRow(ctx, `SELECT r.conversation_id::text, r.brand_id::text, r.channel, r.action, r.reply, r.created_at, r.send_at, c.status
			FROM support_assistant_runs r JOIN support_conversations c ON c.id = r.conversation_id
			WHERE r.id = $1 AND r.workspace_id = $2 AND r.outcome = 'drafted' AND r.send_at IS NOT NULL FOR UPDATE OF r`, p.Run, ws).
			Scan(&conv, &brand, &channel, &action, &reply, &created, &sendAt, &status)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // stopped, used, discarded or gone
		}
		if err != nil {
			return err
		}
		if time.Until(sendAt) > time.Second {
			return errors.New("held reply isn't due yet")
		}
		mode, err := support.AssistantMode(ctx, tx, ws, brand, channel)
		if err != nil {
			return err
		}
		if mode != "auto" || !s.autoAvailable(ctx, ws, brand) {
			// Auto was turned off while it waited: a draft for the team.
			result = "auto_off"
			_, err := tx.Exec(ctx, `UPDATE support_assistant_runs SET send_at = NULL WHERE id = $1`, p.Run)
			return err
		}
		var moved bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM support_messages WHERE conversation_id = $1 AND kind = 'out' AND created_at > $2)
			OR EXISTS (SELECT 1 FROM support_assistant_runs WHERE conversation_id = $1 AND created_at > $2 AND id <> $3)`,
			conv, created, p.Run).Scan(&moved); err != nil {
			return err
		}
		if moved || status == support.StatusClosed {
			// The team replied, the customer wrote again, or it was closed:
			// this reply is out of date.
			result = "cancelled"
			_, err := tx.Exec(ctx, `UPDATE support_assistant_runs SET send_at = NULL, outcome = 'discarded', decided_at = now(),
				checks = checks || '{"hold_cancelled": true}' WHERE id = $1`, p.Run)
			return err
		}
		ok, err := s.assistantSend(ctx, tx, ws, brand, conv, channel, p.Run, assistantResult{Action: action, Reply: reply})
		if err != nil {
			return err
		}
		sent, result = ok, "sent"
		if !ok {
			result = "refused"
		}
		// A refused send stays a draft for the team.
		_, err = tx.Exec(ctx, `UPDATE support_assistant_runs SET send_at = NULL WHERE id = $1`, p.Run)
		return err
	})
	if err == nil && sent {
		metrics.SupportAssistant(channel, "auto_sent", 0)
	}
	if err == nil && result != "" {
		metrics.SupportAssistantHeld(result)
	}
	return err
}

// stopAssistantRun stops a held Auto reply: it stays a draft for the team.
func (s *Server) stopAssistantRun(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	id := r.PathValue("id")
	c, ok := s.assistantRunConversation(w, r, a, id)
	if !ok {
		return
	}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE support_assistant_runs SET send_at = NULL WHERE id = $1 AND outcome = 'drafted' AND send_at IS NOT NULL`, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errAlreadyDecided
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "support.assistant_stopped", "support_conversation", c.ID, map[string]any{"run_id": id})
	})
	if errors.Is(err, errAlreadyDecided) {
		writeError(w, r, http.StatusConflict, "already_decided", "this reply isn't waiting to be sent: it was already sent, stopped or discarded")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	metrics.SupportAssistantHeld("stopped")
	v, err := s.assistantRun(ctx, a.WorkspaceID, id)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// assistantRunConversation finds a run's conversation for a write by the
// caller, or writes the error.
func (s *Server) assistantRunConversation(w http.ResponseWriter, r *http.Request, a *access, id string) (*supportConversationView, bool) {
	ctx := r.Context()
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "run not found")
		return nil, false
	}
	var conv string
	err := s.pool.QueryRow(ctx, `SELECT conversation_id::text FROM support_assistant_runs WHERE id = $1 AND workspace_id = $2`, id, a.WorkspaceID).Scan(&conv)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, http.StatusNotFound, "not_found", "run not found")
		return nil, false
	}
	if err != nil {
		s.internal(w, r, err)
		return nil, false
	}
	c, err := s.supportConversation(ctx, a, conv)
	if errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "run not found")
		return c, false
	}
	if s.supportError(w, r, err) || !s.supportWrites(w, r, a, c) {
		return c, false
	}
	return c, true
}
