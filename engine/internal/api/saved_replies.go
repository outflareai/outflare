package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Saved replies: answers a brand's team writes once and drops into a
// reply, in support and in replies to outreach. The web app fills in
// {first_name} and {my_name} when one is used.

func (s *Server) routesSavedReplies(mux *http.ServeMux) {
	read, manage := auth.ScopeInboxRead, auth.ScopeInboxManage
	mux.HandleFunc("GET /v1/brands/{brand}/saved-replies", s.onBrand(read, s.listSavedReplies))
	mux.HandleFunc("POST /v1/brands/{brand}/saved-replies", s.onBrand(manage, s.createSavedReply))
	mux.HandleFunc("PATCH /v1/brands/{brand}/saved-replies/{id}", s.onBrand(manage, s.updateSavedReply))
	mux.HandleFunc("DELETE /v1/brands/{brand}/saved-replies/{id}", s.onBrand(manage, s.deleteSavedReply))
	mux.HandleFunc("POST /v1/brands/{brand}/saved-replies/{id}/used", s.onBrand(manage, s.useSavedReply))
	mux.HandleFunc("POST /v1/brands/{brand}/inbox/{id}/suggest-reply", s.onBrand(manage, s.suggestInboundReply))
}

type savedReply struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Body      string    `json:"body"`
	Uses      int       `json:"uses"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

const savedReplyColumns = `id::text, name, body, uses, created_at, updated_at`

func savedReplyText(name, body *string) error {
	if name != nil {
		*name = strings.TrimSpace(*name)
		if *name == "" || utf8.RuneCountInString(*name) > 80 {
			return errInvalid("name must be 1 to 80 characters")
		}
	}
	if body != nil && (strings.TrimSpace(*body) == "" || len(*body) > 20000) {
		return errInvalid("body must be 1 to 20000 characters")
	}
	return nil
}

func (s *Server) listSavedReplies(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	brand, ok := s.inboxBrand(w, r)
	if !ok {
		return
	}
	rows, err := s.pool.Query(r.Context(), `SELECT `+savedReplyColumns+` FROM saved_replies
		WHERE workspace_id = $1 AND brand_id = $2 ORDER BY uses DESC, lower(name)`, a.WorkspaceID, brand)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[savedReply])
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if list == nil {
		list = []savedReply{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

func (s *Server) createSavedReply(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		Name string `json:"name"`
		Body string `json:"body"`
	}
	if !decode(w, r, &req) {
		return
	}
	if err := savedReplyText(&req.Name, &req.Body); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	brand, ok := s.inboxBrand(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	var v savedReply
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM saved_replies WHERE brand_id = $1`, brand).Scan(&n); err != nil {
			return err
		}
		if n >= 500 {
			return errInvalid("a brand can have at most 500 saved replies")
		}
		rows, err := tx.Query(ctx, `INSERT INTO saved_replies (workspace_id, brand_id, name, body, created_by) VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (brand_id, lower(name)) DO NOTHING RETURNING `+savedReplyColumns, a.WorkspaceID, brand, req.Name, req.Body, nilIfEmpty(a.UserID))
		if err != nil {
			return err
		}
		if v, err = pgx.CollectExactlyOneRow(rows, pgx.RowToStructByPos[savedReply]); errors.Is(err, pgx.ErrNoRows) {
			return errConflict
		} else if err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "saved_reply.created", "saved_reply", v.ID, map[string]any{"brand_id": brand})
	})
	s.savedReplyDone(w, r, err, http.StatusCreated, v)
}

func (s *Server) savedReplyDone(w http.ResponseWriter, r *http.Request, err error, status int, v savedReply) {
	var inv errInvalid
	switch {
	case errors.As(err, &inv):
		writeError(w, r, http.StatusBadRequest, "invalid_request", string(inv))
	case errors.Is(err, errConflict):
		writeError(w, r, http.StatusConflict, "conflict", "a saved reply with that name exists")
	case errors.Is(err, errNotFound):
		writeError(w, r, http.StatusNotFound, "not_found", "saved reply not found")
	case err != nil:
		s.internal(w, r, err)
	default:
		writeJSON(w, status, v)
	}
}

func (s *Server) updateSavedReply(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		Name *string `json:"name"`
		Body *string `json:"body"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Name == nil && req.Body == nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "nothing to change: send name or body")
		return
	}
	if err := savedReplyText(req.Name, req.Body); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	brand, ok := s.inboxBrand(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	ctx := r.Context()
	var v savedReply
	err := errNotFound
	if validUUID(id) {
		err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
			rows, err := tx.Query(ctx, `UPDATE saved_replies SET name = COALESCE($4, name), body = COALESCE($5, body), updated_at = now()
				WHERE id = $1 AND workspace_id = $2 AND brand_id = $3 RETURNING `+savedReplyColumns, id, a.WorkspaceID, brand, req.Name, req.Body)
			if err != nil {
				return err
			}
			v, err = pgx.CollectExactlyOneRow(rows, pgx.RowToStructByPos[savedReply])
			switch {
			case errors.Is(err, pgx.ErrNoRows):
				return errNotFound
			case isUniqueViolation(err):
				return errConflict
			case err != nil:
				return err
			}
			return record(ctx, tx, &a.principal, a.WorkspaceID, "saved_reply.updated", "saved_reply", v.ID, map[string]any{"brand_id": brand})
		})
	}
	s.savedReplyDone(w, r, err, http.StatusOK, v)
}

func (s *Server) deleteSavedReply(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	brand, ok := s.inboxBrand(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	ctx := r.Context()
	err := errNotFound
	if validUUID(id) {
		err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
			tag, err := tx.Exec(ctx, `DELETE FROM saved_replies WHERE id = $1 AND workspace_id = $2 AND brand_id = $3`, id, a.WorkspaceID, brand)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				return errNotFound
			}
			return record(ctx, tx, &a.principal, a.WorkspaceID, "saved_reply.deleted", "saved_reply", id, map[string]any{"brand_id": brand})
		})
	}
	if err != nil {
		s.savedReplyDone(w, r, err, 0, savedReply{})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// useSavedReply counts one use, so the list leads with what the team
// reaches for. Too routine to audit; the request is its event.
func (s *Server) useSavedReply(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	brand, ok := s.inboxBrand(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	err := errNotFound
	var v savedReply
	if validUUID(id) {
		var rows pgx.Rows
		if rows, err = s.pool.Query(r.Context(), `UPDATE saved_replies SET uses = uses + 1 WHERE id = $1 AND workspace_id = $2 AND brand_id = $3
			RETURNING `+savedReplyColumns, id, a.WorkspaceID, brand); err == nil {
			if v, err = pgx.CollectExactlyOneRow(rows, pgx.RowToStructByPos[savedReply]); errors.Is(err, pgx.ErrNoRows) {
				err = errNotFound
			}
		}
	}
	s.savedReplyDone(w, r, err, http.StatusOK, v)
}

// suggestInboundReply drafts an answer to a reply to outreach from what
// customers may be told, with the workspace's own AI. Nothing is sent:
// the person edits and sends it. The same as support's suggest-reply.
func (s *Server) suggestInboundReply(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	if !s.inboundBrand(w, r, a, true) {
		return
	}
	v, ok := s.findInbound(w, r, a.WorkspaceID)
	if !ok {
		return
	}
	question := strings.TrimSpace(v.Body)
	if question == "" {
		writeError(w, r, http.StatusConflict, "nothing_to_answer", "this message has no text to answer")
		return
	}
	if v.Subject != "" {
		question = v.Subject + "\n\n" + question
	}
	// The question is at most 1000 characters: the start of their email.
	if utf8.RuneCountInString(question) > 1000 {
		question = string([]rune(question)[:1000])
	}
	ctx, cancel := context.WithTimeout(r.Context(), knowledgeAskTimeout)
	defer cancel()
	kq := knowledgeQuery{q: question, audience: audienceCustomers, brand: v.BrandID, limit: 8}
	if kq.brand == nil {
		// A reply to a mailbox with no brand counts as the route's brand.
		b, ok := s.inboxBrand(w, r)
		if !ok {
			return
		}
		kq.brand = &b
	}
	start := time.Now()
	ev := track.Event{Name: "inbox.reply_suggested", WorkspaceID: a.WorkspaceID, UserID: a.personID(), ActorID: a.ActorID, ActorType: a.ActorType,
		RequestID: RequestID(ctx), Props: map[string]any{}}
	out, err := s.answerKnowledge(ctx, a, kq, "inbox")
	ev.Props["duration_ms"] = time.Since(start).Milliseconds()
	if err != nil {
		metrics.KnowledgeAsked("error", time.Since(start))
		track.Fail(ctx, s.log, ev, "internal", err)
		s.internal(w, r, err)
		return
	}
	outcome := "answered"
	switch {
	case out.AI.Problem != nil:
		outcome = "no_ai"
		ev.Props["ai_problem"] = out.AI.Problem.Code
	case !out.Answered:
		outcome = "unanswered"
	}
	ev.Props["answered"], ev.Props["cited"] = out.Answered, len(out.Citations)
	metrics.KnowledgeAsked(outcome, time.Since(start))
	track.Emit(ctx, ev)
	writeJSON(w, http.StatusOK, out)
}
