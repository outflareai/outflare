package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/allocation"
)

// Declines: a mailbox the allocator must not give a sequence again, because
// a person turned the suggestion down, undid the allocator's own choice, or
// the sequencer platform refused it. They last until someone clears them.

type declineView struct {
	ID           string    `json:"id"`
	MailboxID    string    `json:"mailbox_id"`
	MailboxEmail string    `json:"mailbox_email"`
	SequenceID   string    `json:"sequence_id"`
	SequenceName string    `json:"sequence_name"`
	Source       string    `json:"source"`
	Reason       string    `json:"reason"`
	ActorName    *string   `json:"actor_name"`
	DeclinedAt   time.Time `json:"declined_at"`
}

// loadDeclines lists declines, newest first; with a brand, those for that
// brand's sequences.
func (s *Server) loadDeclines(ctx context.Context, ws string, seq, mailbox *string, limit, offset int, sel brandSel) ([]declineView, error) {
	args := []any{ws, seq, mailbox, limit, offset}
	brand := sel.sql("q.brand_id", &args)
	rows, err := s.pool.Query(ctx, `
		SELECT d.id, d.mailbox_id, m.email, d.sequence_id, q.name, d.source, d.reason, ac.display_name, d.declined_at
		FROM allocation_declines d JOIN mailboxes m ON m.id = d.mailbox_id JOIN sequences q ON q.id = d.sequence_id
		LEFT JOIN actors ac ON ac.id = d.declined_by
		WHERE d.workspace_id = $1 AND ($2::uuid IS NULL OR d.sequence_id = $2) AND ($3::uuid IS NULL OR d.mailbox_id = $3)`+brand+`
		ORDER BY d.declined_at DESC, d.id DESC LIMIT $4 OFFSET $5`, args...)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[declineView])
	if list == nil {
		list = []declineView{}
	}
	return list, err
}

func (s *Server) listDeclines(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	q := r.URL.Query()
	var seq, mailbox *string
	for key, dst := range map[string]**string{"sequence_id": &seq, "mailbox_id": &mailbox} {
		if v := q.Get(key); v != "" {
			if !validUUID(v) {
				writeError(w, r, http.StatusBadRequest, "invalid_request", key+" must be a UUID")
				return
			}
			*dst = &v
		}
	}
	limit := 100
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 500 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "limit must be 1 to 500")
			return
		}
		limit = n
	}
	offset := 0
	if v := q.Get("offset"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "offset must be 0 or more")
			return
		}
		offset = n
	}
	sel, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	// One more than asked for says whether there is a next page.
	list, err := s.loadDeclines(r.Context(), a.WorkspaceID, seq, mailbox, limit+1, offset, sel)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var next *int
	if len(list) > limit {
		list = list[:limit]
		n := offset + limit
		next = &n
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "next_offset": next})
}

// clearDecline removes one decline, so the allocator may give that mailbox
// to that sequence again, and plans straight away.
func (s *Server) clearDecline(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		s.allocError(w, r, errNotFound)
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var mailbox, seq, source string
		var brand *string
		err := tx.QueryRow(ctx, `
			DELETE FROM allocation_declines d USING sequences q
			WHERE d.id = $1 AND d.workspace_id = $2 AND q.id = d.sequence_id
			RETURNING d.mailbox_id::text, d.sequence_id::text, d.source, q.brand_id::text`, id, a.WorkspaceID).
			Scan(&mailbox, &seq, &source, &brand)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		// Another brand's decline is not there for a brand-limited caller;
		// rolling back keeps it.
		if !a.sees(brand) {
			return errNotFound
		}
		if !a.writes(brand) {
			return errDenied("this decline is for a sequence shared by every brand; a brand-limited caller cannot clear it")
		}
		if err := record(ctx, tx, &a.principal, a.WorkspaceID, "mailbox.allocation.decline_cleared", "mailbox", mailbox,
			map[string]any{"decline_id": id, "sequence_id": seq, "source": source}); err != nil {
			return err
		}
		return allocation.Request(ctx, tx, a.WorkspaceID)
	})
	if s.allocError(w, r, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
