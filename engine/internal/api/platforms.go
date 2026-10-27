package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/allocation"
	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/platforms"
)

// Outside sequencers in mailbox allocation: the connected platform
// accounts, the campaigns linked to sequences, and syncing on demand.

func (s *Server) routesPlatforms(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/workspaces/{ws}/platforms", s.inWorkspace(auth.ScopeSequencesRead, s.listPlatforms))
	mux.HandleFunc("POST /v1/workspaces/{ws}/platforms/{id}/sync", s.inWorkspace(auth.ScopeSequencesManage, s.syncPlatform))
	mux.HandleFunc("POST /v1/workspaces/{ws}/platforms/{id}/review", s.inWorkspace(auth.ScopeSequencesManage, s.reviewPlatform))
}

// linkView is a platform campaign a sequence runs as.
type linkView struct {
	ID            string     `json:"id"`
	IntegrationID string     `json:"integration_id"`
	Platform      string     `json:"platform"`
	Connector     string     `json:"connector"`
	ExternalID    string     `json:"external_id"`
	Name          string     `json:"name"`
	Status        string     `json:"status"`
	Control       string     `json:"control"`
	LastSyncedAt  *time.Time `json:"last_synced_at"`
}

// loadLinks returns the links of the given sequences by sequence id.
func (s *Server) loadLinks(ctx context.Context, ws string, seqIDs []string) (map[string][]linkView, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT l.sequence_id::text, l.id::text, l.integration_id::text, i.name, i.connector, l.external_id, l.external_name,
		       l.external_status, l.control, l.last_synced_at
		FROM campaign_links l JOIN integrations i ON i.id = l.integration_id
		WHERE l.workspace_id = $1 AND l.sequence_id = ANY($2::uuid[])
		ORDER BY l.created_at, l.id`, ws, seqIDs)
	if err != nil {
		return nil, err
	}
	out := map[string][]linkView{}
	defer rows.Close()
	for rows.Next() {
		var seq string
		var v linkView
		if err := rows.Scan(&seq, &v.ID, &v.IntegrationID, &v.Platform, &v.Connector, &v.ExternalID, &v.Name, &v.Status, &v.Control, &v.LastSyncedAt); err != nil {
			return nil, err
		}
		out[seq] = append(out[seq], v)
	}
	return out, rows.Err()
}

// errLinked refuses changes the platform owns.
func errLinked(platform string) errInvalid {
	return errInvalid("this sequence runs as a campaign in " + platform + "; start, pause and edit its steps there")
}

// linkedPlatform returns the platform a sequence runs in, or "".
func linkedPlatform(ctx context.Context, q pgx.Tx, seqID string) (string, error) {
	var name string
	err := q.QueryRow(ctx, `
		SELECT i.name FROM campaign_links l JOIN integrations i ON i.id = l.integration_id
		WHERE l.sequence_id = $1 ORDER BY l.created_at LIMIT 1`, seqID).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return name, err
}

// route picks where a mailbox would send for a sequence: "" for Outflare
// itself, or a link to a platform campaign. refuse says why it cannot.
func route(ctx context.Context, tx pgx.Tx, mailboxID, seqID string) (linkID, refuse string, err error) {
	rows, err := tx.Query(ctx, `
		SELECT l.id::text, l.control, i.name, p.status
		FROM campaign_links l JOIN integrations i ON i.id = l.integration_id
		LEFT JOIN mailbox_platforms p ON p.integration_id = l.integration_id AND p.mailbox_id = $1
		WHERE l.sequence_id = $2 ORDER BY l.created_at, l.id`, mailboxID, seqID)
	if err != nil {
		return "", "", err
	}
	type cand struct {
		id, control, platform string
		status                *string
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (cand, error) {
		var c cand
		return c, r.Scan(&c.id, &c.control, &c.platform, &c.status)
	})
	if err != nil {
		return "", "", err
	}
	if len(list) == 0 {
		var hasLogin bool
		if err := tx.QueryRow(ctx, `SELECT has_login FROM mailboxes WHERE id = $1`, mailboxID).Scan(&hasLogin); err != nil {
			return "", "", err
		}
		if !hasLogin {
			return "", "Outflare has no login for it, so it can only send in the platform account it came from.", nil
		}
		return "", "", nil
	}
	var names []string
	var unhealthy string
	readOnly := true
	for _, c := range list {
		if c.control == "read_only" {
			names = append(names, c.platform)
			continue
		}
		readOnly = false
		if c.status != nil && *c.status == connectors.MailboxOK {
			return c.id, "", nil
		}
		if c.status != nil {
			unhealthy = c.platform + " reports it as " + *c.status + ". Fix it there first."
			continue
		}
		names = append(names, c.platform)
	}
	if unhealthy != "" {
		return "", unhealthy, nil
	}
	if readOnly {
		return "", names[0] + " picks a mailbox for each contact itself, so Outflare cannot assign mailboxes to this campaign.", nil
	}
	return "", "It is not connected in the " + strings.Join(names, " or ") + " account this campaign sends from. Connect it there first.", nil
}

type platformView struct {
	IntegrationID  string     `json:"integration_id"`
	Name           string     `json:"name"`
	Connector      string     `json:"connector"`
	Control        string     `json:"control"`
	SyncedAt       *time.Time `json:"synced_at"`
	Error          *string    `json:"error"`
	Stale          bool       `json:"stale"`
	AwaitingReview bool       `json:"awaiting_review"`
	Mailboxes      int        `json:"mailboxes"`
	Campaigns      int        `json:"campaigns"`
	Unconfirmed    int        `json:"unconfirmed"`
	Conflicts      int        `json:"conflicts"`
}

func (s *Server) loadPlatforms(ctx context.Context, ws string) ([]platformView, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT i.id::text, i.name, i.connector, ps.control, ps.synced_at, ps.error,
		       ps.synced_at IS NULL OR ps.synced_at < now() - $2::interval OR ps.error IS NOT NULL,
		       ps.reviewed_at IS NULL, ps.mailboxes, ps.campaigns,
		       (SELECT count(*) FROM mailbox_assignments a JOIN campaign_links l ON l.id = a.link_id
		        WHERE l.integration_id = i.id AND a.confirmed_at IS NULL)::int,
		       (SELECT count(*) FROM mailbox_platforms p LEFT JOIN mailbox_assignments a ON a.mailbox_id = p.mailbox_id
		        LEFT JOIN campaign_links l ON l.id = a.link_id
		        WHERE p.integration_id = i.id AND cardinality(p.campaign_ids) > 0
		          AND (l.id IS NULL OR NOT (l.external_id = ANY(p.campaign_ids)) OR cardinality(p.campaign_ids) > 1))::int
		FROM integrations i JOIN platform_syncs ps ON ps.integration_id = i.id
		WHERE i.workspace_id = $1 ORDER BY i.name`, ws, fmt.Sprintf("%d minutes", int(allocation.StaleAfter.Minutes())))
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[platformView])
	if list == nil {
		list = []platformView{}
	}
	return list, err
}

func (s *Server) listPlatforms(w http.ResponseWriter, r *http.Request) {
	list, err := s.loadPlatforms(r.Context(), accessFrom(r.Context()).WorkspaceID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

func (s *Server) syncPlatform(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if s.platforms == nil || !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "platform account not found")
		return
	}
	res, err := s.platforms.Sync(r.Context(), a.WorkspaceID, id)
	switch {
	case errors.Is(err, connectors.ErrNotFound):
		writeError(w, r, http.StatusNotFound, "not_found", "platform account not found")
		return
	case errors.Is(err, platforms.ErrNotPlatform):
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	case err != nil:
		writeError(w, r, http.StatusBadGateway, "platform_error", err.Error())
		return
	}
	err = pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		return record(r.Context(), tx, &a.principal, a.WorkspaceID, "platform.synced", "integration", id, map[string]any{
			"mailboxes": res.Mailboxes, "campaigns": res.Campaigns, "accepted": res.Accepted, "confirmed": res.Confirmed})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// reviewPlatform lets allocation change a newly connected account's
// campaigns: until then its plan is only suggested.
func (s *Server) reviewPlatform(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "platform account not found")
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE platform_syncs SET reviewed_at = COALESCE(reviewed_at, now()) WHERE integration_id = $1 AND workspace_id = $2`, id, a.WorkspaceID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errNotFound
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "platform.reviewed", "integration", id, nil)
	})
	if s.allocError(w, r, err) {
		return
	}
	res, err := s.alloc.Run(ctx, a.WorkspaceID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"applied": viewChanges(res.Applied), "suggested": viewChanges(res.Suggested)})
}
