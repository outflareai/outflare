package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/inbox"
)

// Reply rules: what happens when a reply gets a label, for the workspace
// and, label by label, for a project or a sequence.
func (s *Server) routesReplyRules(mux *http.ServeMux) {
	read, manage := auth.ScopeInboxRead, auth.ScopeInboxManage
	mux.HandleFunc("GET /v1/workspaces/{ws}/reply-rules", s.inWorkspace(read, s.getReplyRules))
	mux.HandleFunc("PUT /v1/workspaces/{ws}/reply-rules", s.inWorkspace(manage, s.setReplyRules))
	mux.HandleFunc("POST /v1/workspaces/{ws}/inbox/{id}/referral", s.inBrand(manage, s.addReferral))
}

type replyRuleSet struct {
	ID         string        `json:"id"`
	ProjectID  *string       `json:"project_id"`
	SequenceID *string       `json:"sequence_id"`
	Rules      inbox.RuleSet `json:"rules"`
	UpdatedAt  time.Time     `json:"updated_at"`
}

func (s *Server) getReplyRules(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	rows, err := s.pool.Query(r.Context(), `
		SELECT id::text, project_id::text, sequence_id::text, rules, updated_at FROM reply_rules
		WHERE workspace_id = $1 ORDER BY (sequence_id IS NOT NULL), (project_id IS NOT NULL), updated_at`, a.WorkspaceID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	sets, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (replyRuleSet, error) {
		var v replyRuleSet
		var raw []byte
		if err := row.Scan(&v.ID, &v.ProjectID, &v.SequenceID, &raw, &v.UpdatedAt); err != nil {
			return v, err
		}
		return v, json.Unmarshal(raw, &v.Rules)
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	labels := map[string][]string{}
	for _, l := range inbox.RuleLabels() {
		labels[l] = inbox.RuleActions[l]
	}
	writeJSON(w, http.StatusOK, map[string]any{"defaults": inbox.DefaultRules(), "actions": labels, "data": sets})
}

func (s *Server) setReplyRules(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		ProjectID  *string       `json:"project_id"`
		SequenceID *string       `json:"sequence_id"`
		Rules      inbox.RuleSet `json:"rules"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.ProjectID != nil && req.SequenceID != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "send project_id or sequence_id, not both")
		return
	}
	if req.Rules == nil {
		req.Rules = inbox.RuleSet{}
	}
	ctx := r.Context()
	var out *replyRuleSet
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		for _, ref := range []struct {
			id    *string
			table string
		}{{req.ProjectID, "projects"}, {req.SequenceID, "sequences"}} {
			if ref.id == nil {
				continue
			}
			var ok bool
			if validUUID(*ref.id) {
				if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM `+ref.table+` WHERE id = $1 AND workspace_id = $2)`,
					*ref.id, a.WorkspaceID).Scan(&ok); err != nil {
					return err
				}
			}
			if !ok {
				return errInvalid(map[string]string{"projects": "project_id", "sequences": "sequence_id"}[ref.table] + " is not in this workspace")
			}
		}
		if err := inbox.CheckRules(ctx, tx, a.WorkspaceID, req.Rules); err != nil {
			var bad inbox.ErrInvalidRule
			if errors.As(err, &bad) {
				return errInvalid(bad.Error())
			}
			return err
		}
		if err := checkDealRules(ctx, tx, a.WorkspaceID, req.Rules); err != nil {
			return err
		}
		labels := make([]string, 0, len(req.Rules))
		for l := range req.Rules {
			labels = append(labels, l)
		}
		data := map[string]any{"project_id": req.ProjectID, "sequence_id": req.SequenceID, "labels": labels}
		match := `workspace_id = $1 AND project_id IS NOT DISTINCT FROM $2::uuid AND sequence_id IS NOT DISTINCT FROM $3::uuid`
		// An empty set clears an override, so it follows the next set up.
		if len(req.Rules) == 0 {
			tag, err := tx.Exec(ctx, `DELETE FROM reply_rules WHERE `+match, a.WorkspaceID, req.ProjectID, req.SequenceID)
			if err != nil || tag.RowsAffected() == 0 {
				return err
			}
			return record(ctx, tx, &a.principal, a.WorkspaceID, "reply_rules.updated", "workspace", a.WorkspaceID, data)
		}
		raw, err := json.Marshal(req.Rules)
		if err != nil {
			return err
		}
		var actor *string
		if a.ActorID != "" {
			actor = &a.ActorID
		}
		v := replyRuleSet{ProjectID: req.ProjectID, SequenceID: req.SequenceID, Rules: req.Rules}
		err = tx.QueryRow(ctx, `UPDATE reply_rules SET rules = $4, updated_by = $5, updated_at = now() WHERE `+match+` RETURNING id::text, updated_at`,
			a.WorkspaceID, req.ProjectID, req.SequenceID, raw, actor).Scan(&v.ID, &v.UpdatedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			err = tx.QueryRow(ctx, `INSERT INTO reply_rules (workspace_id, project_id, sequence_id, rules, updated_by) VALUES ($1, $2, $3, $4, $5)
				RETURNING id::text, updated_at`, a.WorkspaceID, req.ProjectID, req.SequenceID, raw, actor).Scan(&v.ID, &v.UpdatedAt)
		}
		if err != nil {
			return err
		}
		out = &v
		return record(ctx, tx, &a.principal, a.WorkspaceID, "reply_rules.updated", "workspace", a.WorkspaceID, data)
	})
	var inv errInvalid
	switch {
	case errors.As(err, &inv):
		writeError(w, r, http.StatusBadRequest, "invalid_request", string(inv))
	case err != nil:
		s.internal(w, r, err)
	case out == nil:
		writeJSON(w, http.StatusOK, map[string]any{"cleared": true})
	default:
		writeJSON(w, http.StatusOK, out)
	}
}

// addReferral adds the person a reply names as a lead, the way the
// referral rule's "add" does, for the "suggest" setting's button.
func (s *Server) addReferral(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	if !s.inboundBrand(w, r, a, true) {
		return
	}
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "message not found")
		return
	}
	var req struct {
		SequenceID string `json:"sequence_id"`
	}
	if !decode(w, r, &req) {
		return
	}
	ctx := r.Context()
	var ref *inbox.Referral
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM inbound_messages WHERE id = $1 AND workspace_id = $2)`, id, a.WorkspaceID).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return errNotFound
		}
		if req.SequenceID != "" {
			if !validUUID(req.SequenceID) {
				return errInvalid("sequence_id is not a sequence in this workspace")
			}
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM sequences WHERE id = $1 AND workspace_id = $2 AND status <> 'archived')`,
				req.SequenceID, a.WorkspaceID).Scan(&ok); err != nil {
				return err
			}
			if !ok {
				return errInvalid("sequence_id is not a sequence in this workspace")
			}
		}
		var err error
		if ref, err = inbox.AddReferral(ctx, tx, a.WorkspaceID, id, req.SequenceID); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "lead.referral_added", "lead", ref.LeadID, ref.Data())
	})
	var inv errInvalid
	switch {
	case errors.Is(err, errNotFound):
		writeError(w, r, http.StatusNotFound, "not_found", "message not found")
	case errors.Is(err, inbox.ErrNoReferral):
		writeError(w, r, http.StatusUnprocessableEntity, "no_referral", err.Error())
	case errors.As(err, &inv):
		writeError(w, r, http.StatusBadRequest, "invalid_request", string(inv))
	case err != nil:
		s.internal(w, r, err)
	default:
		writeJSON(w, http.StatusOK, ref)
	}
}
