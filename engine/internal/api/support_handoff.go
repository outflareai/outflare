package api

import (
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/signals"
)

// Handing a support conversation to another team in one step ("Sales
// opportunity" in the Inbox): the conversation moves to the team with
// nobody assigned, a note says why, whoever handed it over keeps
// watching, and the customer's owner gets a signal so the opportunity is
// in Needs attention too.

func (s *Server) handOffSupportConversation(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		TeamID string `json:"team_id"`
		Note   string `json:"note"`
		// Signal is the signal type to raise about the customer; "" raises
		// none.
		Signal string `json:"signal"`
	}
	if !decode(w, r, &req) {
		return
	}
	req.Note = strings.TrimSpace(req.Note)
	switch {
	case !validUUID(req.TeamID):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "team_id is the team to hand the conversation to")
		return
	case len(req.Note) > 2000:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "note is at most 2,000 characters")
		return
	case req.Signal != "" && !signals.ValidKey(req.Signal):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "signal is one of the brand's signal types, such as upsell")
		return
	case req.Signal != "" && !a.has(auth.ScopeCustomersManage):
		writeError(w, r, http.StatusForbidden, "forbidden", s.missingScope(a, auth.ScopeCustomersManage))
		return
	}
	ctx := r.Context()
	c, err := s.supportConversation(ctx, a, r.PathValue("id"))
	if s.supportError(w, r, err) {
		return
	}
	if !s.supportWrites(w, r, a, c) {
		return
	}
	// What became of the signal: raised, duplicate, capped, no_record, or
	// none when none was asked for or nobody is known yet.
	outcome := "none"
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var team string
		err := tx.QueryRow(ctx, `SELECT name FROM teams WHERE workspace_id = $1 AND id = $2`, a.WorkspaceID, req.TeamID).Scan(&team)
		if err == pgx.ErrNoRows {
			return errInvalid("team not found")
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE support_conversations SET team_id = $3, assignee_id = NULL, unread = true,
			status = CASE WHEN status = 'closed' THEN status ELSE 'open' END, updated_at = now()
			WHERE id = $1 AND workspace_id = $2`, c.ID, a.WorkspaceID, req.TeamID); err != nil {
			return err
		}
		body := "Handed to " + team + "."
		if req.Note != "" {
			body += " " + req.Note
		}
		if _, err := tx.Exec(ctx, `INSERT INTO support_messages (workspace_id, conversation_id, kind, record_id, author_id, body, status)
			VALUES ($1, $2, 'note', $3, $4, $5, 'saved')`, a.WorkspaceID, c.ID, c.RecordID, nilIfEmpty(a.UserID), body); err != nil {
			return err
		}
		if a.UserID != "" {
			if _, err := tx.Exec(ctx, `INSERT INTO support_followers (workspace_id, brand_id, conversation_id, user_id, added_by, seen_at)
				SELECT $1, COALESCE(c.brand_id, outflare_first_brand($1)), c.id, $3, $3, now() FROM support_conversations c WHERE c.id = $2
				ON CONFLICT DO NOTHING`, a.WorkspaceID, c.ID, a.UserID); err != nil {
				return err
			}
		}
		if req.Signal != "" && c.RecordID != nil && c.BrandID != nil {
			if err := signals.EnsureTypes(ctx, tx, a.WorkspaceID, *c.BrandID); err != nil {
				return err
			}
			var known bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM signal_types WHERE brand_id = $1 AND key = $2 AND archived_at IS NULL)`, *c.BrandID, req.Signal).Scan(&known); err != nil {
				return err
			}
			if !known {
				return errInvalid("signal is one of the brand's signal types, such as upsell (GET signal-types lists them)")
			}
			source := "manual"
			if a.ActorType == "api_token" {
				source = "api"
			}
			title := "Sales opportunity from support"
			if c.Subject != "" {
				title = truncate("From support: "+c.Subject, 300)
			}
			if _, outcome, err = signals.Raise(ctx, tx, signals.Signal{WorkspaceID: a.WorkspaceID, BrandID: *c.BrandID, RecordID: *c.RecordID, Type: req.Signal,
				Title: title, Detail: req.Note, Source: source, CreatedBy: a.personID(), Data: map[string]any{"channel": "support", "ref": c.ID},
				ActorID: a.ActorID, ActorType: a.ActorType, UserID: a.personID(), RequestID: RequestID(ctx)}); err != nil {
				return err
			}
			metrics.SignalRaised(source, outcome)
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "support.conversation_handed_off", "support_conversation", c.ID,
			map[string]any{"team_id": req.TeamID, "signal": outcome, "had_note": req.Note != ""})
	})
	if s.supportError(w, r, err) {
		return
	}
	v, err := s.supportConversation(ctx, a, c.ID)
	if s.supportError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"conversation": v, "signal": outcome})
}
