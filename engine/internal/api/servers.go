package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/netip"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/mailbox"
)

func (s *Server) routesServers(mux *http.ServeMux) {
	read, manage := auth.ScopeMailboxesRead, auth.ScopeMailboxesManage
	mux.HandleFunc("GET /v1/workspaces/{ws}/sending-servers", s.inWorkspace(read, s.listServers))
	mux.HandleFunc("POST /v1/workspaces/{ws}/sending-servers/check", s.inWorkspace(manage, s.checkAllServers))
	mux.HandleFunc("GET /v1/workspaces/{ws}/sending-servers/{id}", s.inWorkspace(read, s.getServer))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/sending-servers/{id}", s.inWorkspace(manage, s.updateServer))
	mux.HandleFunc("DELETE /v1/workspaces/{ws}/sending-servers/{id}", s.inWorkspace(manage, s.deleteServer))
	mux.HandleFunc("POST /v1/workspaces/{ws}/sending-servers/{id}/check", s.inWorkspace(manage, s.checkServer))
}

type serverView struct {
	ID             string              `json:"id"`
	Host           string              `json:"host"`
	Name           string              `json:"name"`
	IPs            []string            `json:"ips"`
	IPsSource      string              `json:"ips_source"`
	Status         string              `json:"status"`
	PausedBy       *string             `json:"paused_by"`
	StatusReason   *string             `json:"status_reason"`
	PauseOnListing bool                `json:"pause_on_listing"`
	DailyLimit     *int                `json:"daily_limit"`
	Notes          string              `json:"notes"`
	Health         string              `json:"health"`
	Issues         []mailbox.Issue     `json:"issues"`
	Blocklists     []mailbox.IPListing `json:"blocklists"`
	CheckedAt      *time.Time          `json:"checked_at"`
	Mailboxes      int                 `json:"mailboxes"`
	MailboxCounts  mailboxCounts       `json:"mailbox_counts"`
	Domains        []string            `json:"domains"`
	DailyCapacity  int                 `json:"daily_capacity"`
	Sent24h        int                 `json:"sent_24h"`
	Sent7d         int                 `json:"sent_7d"`
	Replies7d      int                 `json:"replies_7d"`
	// Leads emailed through this server in 7 days, and those of them who
	// replied: the reply rate's two halves.
	ContactedLeads7d int       `json:"contacted_leads_7d"`
	RepliedLeads7d   int       `json:"replied_leads_7d"`
	Bounces7d        int       `json:"bounces_7d"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// serverSelect reads servers with their mailbox and sending counts. $1 is
// the workspace; $2 is one server id, or null for all of them.
const serverSelect = `
	WITH mb AS (
		SELECT server_id, count(*)::int AS total,
		       count(*) FILTER (WHERE status = 'active')::int AS active,
		       count(*) FILTER (WHERE status = 'error')::int AS error,
		       count(*) FILTER (WHERE status = 'paused')::int AS paused,
		       count(*) FILTER (WHERE status = 'verifying')::int AS verifying,
		       COALESCE(sum(daily_limit) FILTER (WHERE status = 'active'), 0)::int AS capacity,
		       array_agg(DISTINCT domain ORDER BY domain) AS domains
		FROM mailboxes WHERE workspace_id = $1 AND server_id IS NOT NULL AND ($2::uuid IS NULL OR server_id = $2)
		GROUP BY server_id
	), sent AS (
		SELECT m.server_id,
		       count(*) FILTER (WHERE g.created_at > now() - interval '24 hours')::int AS d1,
		       count(*)::int AS d7,
		       count(DISTINCT g.lead_id)::int AS leads
		FROM messages g JOIN mailboxes m ON m.id = g.mailbox_id
		WHERE g.workspace_id = $1 AND g.status = 'sent' AND g.created_at > now() - interval '7 days'
		  AND m.server_id IS NOT NULL AND ($2::uuid IS NULL OR m.server_id = $2)
		GROUP BY m.server_id
	), inb AS (
		SELECT m.server_id,
		       count(*) FILTER (WHERE i.kind = 'reply')::int AS replies,
		       count(*) FILTER (WHERE i.kind = 'bounce')::int AS bounces,
		       count(DISTINCT i.lead_id) FILTER (WHERE i.kind = 'reply')::int AS replied
		FROM inbound_messages i JOIN mailboxes m ON m.id = i.mailbox_id
		WHERE i.workspace_id = $1 AND i.received_at > now() - interval '7 days'
		  AND m.server_id IS NOT NULL AND ($2::uuid IS NULL OR m.server_id = $2)
		GROUP BY m.server_id
	)
	SELECT sv.id, sv.host, sv.name, sv.ips, sv.ips_source, sv.status, sv.paused_by, sv.status_reason,
	       sv.pause_on_listing, sv.daily_limit, sv.notes, sv.blocklists, sv.checked_at, sv.created_at, sv.updated_at,
	       COALESCE(mb.total, 0), COALESCE(mb.active, 0), COALESCE(mb.error, 0), COALESCE(mb.paused, 0),
	       COALESCE(mb.verifying, 0), COALESCE(mb.capacity, 0), COALESCE(mb.domains, '{}'),
	       COALESCE(sent.d1, 0), COALESCE(sent.d7, 0), COALESCE(inb.replies, 0), COALESCE(sent.leads, 0), COALESCE(inb.replied, 0), COALESCE(inb.bounces, 0)
	FROM sending_servers sv
	LEFT JOIN mb ON mb.server_id = sv.id
	LEFT JOIN sent ON sent.server_id = sv.id
	LEFT JOIN inb ON inb.server_id = sv.id
	WHERE sv.workspace_id = $1 AND ($2::uuid IS NULL OR sv.id = $2)
	ORDER BY sv.host`

func (s *Server) queryServers(ctx context.Context, ws string, id *string) ([]serverView, error) {
	rows, err := s.pool.Query(ctx, serverSelect, ws, id)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (serverView, error) {
		var v serverView
		var lists []byte
		c := &v.MailboxCounts
		err := r.Scan(&v.ID, &v.Host, &v.Name, &v.IPs, &v.IPsSource, &v.Status, &v.PausedBy, &v.StatusReason,
			&v.PauseOnListing, &v.DailyLimit, &v.Notes, &lists, &v.CheckedAt, &v.CreatedAt, &v.UpdatedAt,
			&v.Mailboxes, &c.Active, &c.Error, &c.Paused, &c.Verifying, &v.DailyCapacity, &v.Domains,
			&v.Sent24h, &v.Sent7d, &v.Replies7d, &v.ContactedLeads7d, &v.RepliedLeads7d, &v.Bounces7d)
		if err != nil {
			return v, err
		}
		if err := json.Unmarshal(lists, &v.Blocklists); err != nil {
			return v, err
		}
		capacity := v.DailyCapacity
		switch {
		case v.Status == "paused":
			v.DailyCapacity = 0
		case v.DailyLimit != nil && *v.DailyLimit < v.DailyCapacity:
			v.DailyCapacity = *v.DailyLimit
		}
		facts := mailbox.ServerFacts{
			Host: v.Host, Checked: v.CheckedAt != nil, Paused: v.Status == "paused", IPs: v.IPs, Listings: v.Blocklists,
			Mailboxes: v.Mailboxes, Failing: c.Error, Sent7d: v.Sent7d, Bounces7d: v.Bounces7d,
			DailyLimit: v.DailyLimit, Capacity: capacity,
		}
		if v.PausedBy != nil {
			facts.PausedBy = *v.PausedBy
		}
		v.Issues = mailbox.ServerIssues(facts)
		v.Health = mailbox.Health(facts.Checked, v.Issues)
		return v, nil
	})
}

func (s *Server) listServers(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	list, err := s.queryServers(r.Context(), a.WorkspaceID, nil)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

func (s *Server) findServer(w http.ResponseWriter, r *http.Request, ws, id string) (*serverView, bool) {
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "sending server not found")
		return nil, false
	}
	list, err := s.queryServers(r.Context(), ws, &id)
	if err != nil {
		s.internal(w, r, err)
		return nil, false
	}
	if len(list) == 0 {
		writeError(w, r, http.StatusNotFound, "not_found", "sending server not found")
		return nil, false
	}
	return &list[0], true
}

func (s *Server) getServer(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	if v, ok := s.findServer(w, r, a.WorkspaceID, r.PathValue("id")); ok {
		writeJSON(w, http.StatusOK, v)
	}
}

// parseIPs validates and normalises a list of sending IPs.
func parseIPs(in []string) ([]string, bool) {
	seen := map[string]bool{}
	out := []string{}
	for _, raw := range in {
		ip, err := netip.ParseAddr(strings.TrimSpace(raw))
		if err != nil {
			return nil, false
		}
		if v := ip.String(); !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out, true
}

func (s *Server) updateServer(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "sending server not found")
		return
	}
	var req struct {
		Name           *string       `json:"name"`
		Status         *string       `json:"status"`
		DailyLimit     optional[int] `json:"daily_limit"`
		Notes          *string       `json:"notes"`
		PauseOnListing *bool         `json:"pause_on_listing"`
		// A list sets the sending IPs by hand; null goes back to the
		// host's DNS records.
		IPs optional[[]string] `json:"ips"`
	}
	if !decode(w, r, &req) {
		return
	}
	var ips []string
	switch {
	case req.Status != nil && *req.Status != "active" && *req.Status != "paused":
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status must be active or paused")
		return
	case req.DailyLimit.Value != nil && (*req.DailyLimit.Value < 1 || *req.DailyLimit.Value > 1000000):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "daily_limit must be 1 to 1000000, or null for no server cap")
		return
	case req.Name != nil && len(*req.Name) > 200:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "name must be at most 200 characters")
		return
	case req.Notes != nil && len(*req.Notes) > 2000:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "notes must be at most 2000 characters")
		return
	case req.IPs.Value != nil:
		var ok bool
		if ips, ok = parseIPs(*req.IPs.Value); !ok || len(ips) == 0 || len(ips) > 256 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "ips must be 1 to 256 IPv4 or IPv6 addresses, or null to use the host's DNS records")
			return
		}
	}

	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var oldStatus string
		err := tx.QueryRow(ctx, `SELECT status FROM sending_servers WHERE workspace_id = $1 AND id = $2 FOR UPDATE`,
			a.WorkspaceID, id).Scan(&oldStatus)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		statusChanged := req.Status != nil && *req.Status != oldStatus
		var source *string
		if req.IPs.Set {
			v := "dns"
			if req.IPs.Value != nil {
				v = "manual"
			}
			source = &v
		}
		_, err = tx.Exec(ctx, `
			UPDATE sending_servers SET
				name = COALESCE($3, name),
				status = COALESCE($4, status),
				paused_by = CASE WHEN $5 THEN (CASE WHEN $4 = 'paused' THEN 'person' END) ELSE paused_by END,
				status_reason = CASE WHEN $5 THEN NULL ELSE status_reason END,
				daily_limit = CASE WHEN $6 THEN $7 ELSE daily_limit END,
				notes = COALESCE($8, notes),
				pause_on_listing = COALESCE($9, pause_on_listing),
				ips_source = COALESCE($10, ips_source),
				ips = CASE WHEN $10 = 'manual' THEN $11 ELSE ips END,
				updated_at = now()
			WHERE workspace_id = $1 AND id = $2`,
			a.WorkspaceID, id, req.Name, req.Status, statusChanged, req.DailyLimit.Set, req.DailyLimit.Value,
			req.Notes, req.PauseOnListing, source, ips)
		if err != nil {
			return err
		}
		data := map[string]any{}
		if req.Name != nil {
			data["name"] = *req.Name
		}
		if req.DailyLimit.Set {
			data["daily_limit"] = req.DailyLimit.Value
		}
		if req.Notes != nil {
			data["notes"] = *req.Notes
		}
		if req.PauseOnListing != nil {
			data["pause_on_listing"] = *req.PauseOnListing
		}
		if req.IPs.Set {
			data["ips"] = ips
			// New addresses need a fresh blocklist check.
			if err := s.mail.QueueServerCheck(ctx, tx, a.WorkspaceID, id); err != nil {
				return err
			}
		}
		action := "sending_server.updated"
		if statusChanged {
			action = map[string]string{"paused": "sending_server.paused", "active": "sending_server.resumed"}[*req.Status]
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, action, "sending_server", id, data)
	})
	switch {
	case errors.Is(err, errNotFound):
		writeError(w, r, http.StatusNotFound, "not_found", "sending server not found")
		return
	case err != nil:
		s.internal(w, r, err)
		return
	}
	if v, ok := s.findServer(w, r, a.WorkspaceID, id); ok {
		writeJSON(w, http.StatusOK, v)
	}
}

func (s *Server) deleteServer(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "sending server not found")
		return
	}
	ctx := r.Context()
	var n int
	var host string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM mailboxes WHERE workspace_id = $1 AND server_id = $2`, a.WorkspaceID, id).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return errConflict
		}
		err := tx.QueryRow(ctx, `DELETE FROM sending_servers WHERE workspace_id = $1 AND id = $2 RETURNING host`, a.WorkspaceID, id).Scan(&host)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "sending_server.removed", "sending_server", id, map[string]any{"host": host})
	})
	switch {
	case errors.Is(err, errNotFound):
		writeError(w, r, http.StatusNotFound, "not_found", "sending server not found")
	case errors.Is(err, errConflict):
		writeError(w, r, http.StatusConflict, "conflict", "remove or move the server's mailboxes first; it still has "+mailbox.Plural(n, "mailbox", "mailboxes"))
	case err != nil:
		s.internal(w, r, err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) checkServer(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "sending server not found")
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM sending_servers WHERE workspace_id = $1 AND id = $2)`, a.WorkspaceID, id).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return errNotFound
		}
		return s.mail.QueueServerCheck(ctx, tx, a.WorkspaceID, id)
	})
	switch {
	case errors.Is(err, errNotFound):
		writeError(w, r, http.StatusNotFound, "not_found", "sending server not found")
	case err != nil:
		s.internal(w, r, err)
	default:
		writeJSON(w, http.StatusAccepted, map[string]bool{"queued": true})
	}
}

func (s *Server) checkAllServers(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	var n int
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text FROM sending_servers WHERE workspace_id = $1`, a.WorkspaceID)
		if err != nil {
			return err
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		for _, id := range ids {
			if err := s.mail.QueueServerCheck(ctx, tx, a.WorkspaceID, id); err != nil {
				return err
			}
		}
		n = len(ids)
		return nil
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]int{"queued": n})
}
