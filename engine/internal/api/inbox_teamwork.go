package api

import (
	"context"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
)

// Working a conversation together: private notes on a reply, and
// teammates looped in on a reply or a support conversation, who then find
// it under Watching (?following=me). Support conversations keep their
// notes with their messages (support.go).

type inboxNoteView struct {
	ID        string         `json:"id"`
	Author    *supportPerson `json:"author"`
	Body      string         `json:"body"`
	CreatedAt time.Time      `json:"created_at"`
}

// followers lists who is looped in, by name. table and column are
// constants of this package, never input.
func followers(ctx context.Context, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, table, column, ws, id string) ([]supportPerson, error) {
	rows, err := q.Query(ctx, `SELECT u.id::text, COALESCE(NULLIF(u.name, ''), u.email) FROM `+table+` f JOIN users u ON u.id = f.user_id
		WHERE f.`+column+` = $1 AND f.workspace_id = $2 ORDER BY f.created_at, u.id`, id, ws)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[supportPerson])
	if list == nil {
		list = []supportPerson{}
	}
	return list, err
}

// inboundTeamwork is a reply's notes, oldest first, and who is looped in.
func (s *Server) inboundTeamwork(ctx context.Context, ws, id string) ([]inboxNoteView, []supportPerson, error) {
	rows, err := s.pool.Query(ctx, `SELECT n.id::text, n.author_id::text, COALESCE(NULLIF(u.name, ''), u.email), n.body, n.created_at
		FROM inbox_notes n LEFT JOIN users u ON u.id = n.author_id
		WHERE n.inbound_id = $1 AND n.workspace_id = $2 ORDER BY n.created_at, n.id`, id, ws)
	if err != nil {
		return nil, nil, err
	}
	notes, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (inboxNoteView, error) {
		var n inboxNoteView
		var authorID, authorName *string
		err := row.Scan(&n.ID, &authorID, &authorName, &n.Body, &n.CreatedAt)
		if authorID != nil {
			n.Author = &supportPerson{ID: *authorID, Name: deref(authorName)}
		}
		return n, err
	})
	if err != nil {
		return nil, nil, err
	}
	if notes == nil {
		notes = []inboxNoteView{}
	}
	f, err := followers(ctx, s.pool, "inbox_followers", "inbound_id", ws, id)
	return notes, f, err
}

func (s *Server) noteInbound(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	if !s.inboundBrand(w, r, a, true) {
		return
	}
	var req struct {
		Body string `json:"body"`
	}
	if !decode(w, r, &req) || !supportBody(w, r, req.Body) {
		return
	}
	v, ok := s.findInbound(w, r, a.WorkspaceID)
	if !ok {
		return
	}
	ctx := r.Context()
	n := inboxNoteView{Body: req.Body}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var authorID, authorName *string
		if err := tx.QueryRow(ctx, `
			WITH n AS (
				INSERT INTO inbox_notes (workspace_id, brand_id, inbound_id, author_id, body)
				SELECT $1, COALESCE(i.brand_id, outflare_first_brand($1)), i.id, $3, $4 FROM inbound_messages i WHERE i.id = $2
				RETURNING id, author_id, created_at)
			SELECT n.id::text, n.author_id::text, COALESCE(NULLIF(u.name, ''), u.email), n.created_at
			FROM n LEFT JOIN users u ON u.id = n.author_id`,
			a.WorkspaceID, v.ID, nilIfEmpty(a.UserID), req.Body).Scan(&n.ID, &authorID, &authorName, &n.CreatedAt); err != nil {
			return err
		}
		if authorID != nil {
			n.Author = &supportPerson{ID: *authorID, Name: deref(authorName)}
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "inbox.note_added", "inbound_message", v.ID, map[string]any{"note_id": n.ID})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, n)
}

// followerInPath is the user in the path when they belong to the workspace,
// writing the error when not.
func (s *Server) followerInPath(w http.ResponseWriter, r *http.Request, ws string) (string, bool) {
	user := r.PathValue("user")
	var member bool
	if validUUID(user) {
		if err := s.pool.QueryRow(r.Context(), `SELECT EXISTS (SELECT 1 FROM memberships WHERE workspace_id = $1 AND user_id = $2)`, ws, user).Scan(&member); err != nil {
			s.internal(w, r, err)
			return "", false
		}
	}
	if !member {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "that person is not a member of this workspace")
		return "", false
	}
	return user, true
}

func (s *Server) followInbound(w http.ResponseWriter, r *http.Request) {
	s.setInboundFollower(w, r, true)
}
func (s *Server) unfollowInbound(w http.ResponseWriter, r *http.Request) {
	s.setInboundFollower(w, r, false)
}

func (s *Server) setInboundFollower(w http.ResponseWriter, r *http.Request, on bool) {
	a := accessFrom(r.Context())
	if !s.inboundBrand(w, r, a, true) {
		return
	}
	v, ok := s.findInbound(w, r, a.WorkspaceID)
	if !ok {
		return
	}
	user, ok := s.followerInPath(w, r, a.WorkspaceID)
	if !ok {
		return
	}
	ctx := r.Context()
	var list []supportPerson
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var tag interface{ RowsAffected() int64 }
		var err error
		action := "inbox.follower_added"
		if on {
			tag, err = tx.Exec(ctx, `INSERT INTO inbox_followers (workspace_id, brand_id, inbound_id, user_id, added_by, seen_at)
				SELECT $1, COALESCE(i.brand_id, outflare_first_brand($1)), i.id, $3, $4, CASE WHEN $3::uuid = $4::uuid THEN now() END
				FROM inbound_messages i WHERE i.id = $2
				ON CONFLICT DO NOTHING`, a.WorkspaceID, v.ID, user, nilIfEmpty(a.UserID))
			if err == nil && tag.RowsAffected() > 0 {
				err = tellLoopedIn(ctx, tx, a.WorkspaceID, loopInReply, v.ID, user, a.UserID)
			}
		} else {
			action = "inbox.follower_removed"
			tag, err = tx.Exec(ctx, `DELETE FROM inbox_followers WHERE inbound_id = $1 AND workspace_id = $2 AND user_id = $3`, v.ID, a.WorkspaceID, user)
		}
		if err != nil {
			return err
		}
		if tag.RowsAffected() > 0 {
			if err := record(ctx, tx, &a.principal, a.WorkspaceID, action, "inbound_message", v.ID, map[string]any{"user_id": user}); err != nil {
				return err
			}
		}
		list, err = followers(ctx, tx, "inbox_followers", "inbound_id", a.WorkspaceID, v.ID)
		return err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"followers": list})
}

func (s *Server) followSupportConversation(w http.ResponseWriter, r *http.Request) {
	s.setSupportFollower(w, r, true)
}

func (s *Server) unfollowSupportConversation(w http.ResponseWriter, r *http.Request) {
	s.setSupportFollower(w, r, false)
}

func (s *Server) setSupportFollower(w http.ResponseWriter, r *http.Request, on bool) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	c, err := s.supportConversation(ctx, a, r.PathValue("id"))
	if s.supportError(w, r, err) {
		return
	}
	if !s.supportWrites(w, r, a, c) {
		return
	}
	user, ok := s.followerInPath(w, r, a.WorkspaceID)
	if !ok {
		return
	}
	var list []supportPerson
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var tag interface{ RowsAffected() int64 }
		var err error
		action := "support.follower_added"
		if on {
			tag, err = tx.Exec(ctx, `INSERT INTO support_followers (workspace_id, brand_id, conversation_id, user_id, added_by, seen_at)
				SELECT $1, COALESCE(c.brand_id, outflare_first_brand($1)), c.id, $3, $4, CASE WHEN $3::uuid = $4::uuid THEN now() END
				FROM support_conversations c WHERE c.id = $2
				ON CONFLICT DO NOTHING`, a.WorkspaceID, c.ID, user, nilIfEmpty(a.UserID))
			if err == nil && tag.RowsAffected() > 0 {
				err = tellLoopedIn(ctx, tx, a.WorkspaceID, loopInSupport, c.ID, user, a.UserID)
			}
		} else {
			action = "support.follower_removed"
			tag, err = tx.Exec(ctx, `DELETE FROM support_followers WHERE conversation_id = $1 AND workspace_id = $2 AND user_id = $3`, c.ID, a.WorkspaceID, user)
		}
		if err != nil {
			return err
		}
		if tag.RowsAffected() > 0 {
			if err := record(ctx, tx, &a.principal, a.WorkspaceID, action, "support_conversation", c.ID, map[string]any{"user_id": user}); err != nil {
				return err
			}
		}
		list, err = followers(ctx, tx, "support_followers", "conversation_id", a.WorkspaceID, c.ID)
		return err
	})
	if s.supportError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"followers": list})
}
