package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/verify"
)

func (s *Server) routesVerification(mux *http.ServeMux) {
	read, manage := auth.ScopeLeadsRead, auth.ScopeLeadsManage
	mux.HandleFunc("GET /v1/workspaces/{ws}/verification-settings", s.inWorkspace(read, s.getVerificationSettings))
	mux.HandleFunc("PUT /v1/workspaces/{ws}/verification-settings", s.inWorkspace(manage, s.putVerificationSettings))
	mux.HandleFunc("GET /v1/workspaces/{ws}/verifications", s.inWorkspace(read, s.listVerifications))
	mux.HandleFunc("POST /v1/workspaces/{ws}/verifications", s.inWorkspace(manage, s.startVerification))
	mux.HandleFunc("GET /v1/workspaces/{ws}/verifications/summary", s.inWorkspace(read, s.verificationSummary))
}

type verificationSettingsView struct {
	verify.Settings
	// The connector the integration uses, or "builtin".
	Provider string `json:"provider"`
}

func (s *Server) settingsView(r *http.Request, ws string, set verify.Settings) verificationSettingsView {
	v := verificationSettingsView{Settings: set, Provider: "builtin"}
	if set.IntegrationID != nil && s.integrations != nil {
		if i, err := s.integrations.Get(r.Context(), s.pool, ws, *set.IntegrationID); err == nil {
			v.Provider = i.Connector
		}
	}
	return v
}

func (s *Server) getVerificationSettings(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	set, err := verify.LoadSettings(r.Context(), s.pool, a.WorkspaceID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, s.settingsView(r, a.WorkspaceID, set))
}

func (s *Server) putVerificationSettings(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, err := verify.LoadSettings(r.Context(), s.pool, a.WorkspaceID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	// Every field is optional; missing ones keep their value. An empty
	// integration_id switches back to the built-in checks.
	var req struct {
		IntegrationID    *string `json:"integration_id"`
		VerifyOnImport   *bool   `json:"verify_on_import"`
		VerifyBeforeSend *bool   `json:"verify_before_send"`
		BlockRisky       *bool   `json:"block_risky"`
		BlockCatchAll    *bool   `json:"block_catch_all"`
		BlockUnknown     *bool   `json:"block_unknown"`
		MaxAgeDays       *int    `json:"max_age_days"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.IntegrationID != nil {
		id := strings.TrimSpace(*req.IntegrationID)
		if id == "" || id == "builtin" {
			cur.IntegrationID = nil
		} else {
			if s.integrations == nil || !validUUID(id) {
				writeError(w, r, http.StatusBadRequest, "invalid_request", "integration_id must be one of the workspace's integrations")
				return
			}
			i, err := s.integrations.Get(r.Context(), s.pool, a.WorkspaceID, id)
			if errors.Is(err, connectors.ErrNotFound) {
				writeError(w, r, http.StatusBadRequest, "invalid_request", "no integration with that id in this workspace")
				return
			}
			if err != nil {
				s.internal(w, r, err)
				return
			}
			if d, ok := s.integrations.Registry.Definition(i.Connector); !ok || !d.Has(connectors.CapVerify) {
				writeError(w, r, http.StatusBadRequest, "invalid_request", i.Name+" cannot verify email addresses")
				return
			}
			cur.IntegrationID = &id
		}
	}
	set := func(dst *bool, v *bool) {
		if v != nil {
			*dst = *v
		}
	}
	set(&cur.VerifyOnImport, req.VerifyOnImport)
	set(&cur.VerifyBeforeSend, req.VerifyBeforeSend)
	set(&cur.BlockRisky, req.BlockRisky)
	set(&cur.BlockCatchAll, req.BlockCatchAll)
	set(&cur.BlockUnknown, req.BlockUnknown)
	if req.MaxAgeDays != nil {
		if *req.MaxAgeDays < 1 || *req.MaxAgeDays > 3650 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "max_age_days must be between 1 and 3650")
			return
		}
		cur.MaxAgeDays = *req.MaxAgeDays
	}
	err = pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		if err := verify.SaveSettings(r.Context(), tx, a.WorkspaceID, cur); err != nil {
			return err
		}
		return record(r.Context(), tx, &a.principal, a.WorkspaceID, "verification.settings_updated", "workspace", a.WorkspaceID,
			map[string]any{"integration_id": cur.IntegrationID, "verify_before_send": cur.VerifyBeforeSend,
				"block_risky": cur.BlockRisky, "block_catch_all": cur.BlockCatchAll, "block_unknown": cur.BlockUnknown})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, s.settingsView(r, a.WorkspaceID, cur))
}

func (s *Server) listVerifications(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	q := r.URL.Query()
	status := q.Get("status")
	if status != "" && status != connectors.Deliverable && status != connectors.Undeliverable && status != connectors.Risky && status != connectors.Unknown {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status must be deliverable, undeliverable, risky or unknown")
		return
	}
	limit, offset := 100, 0
	if v, err := strconv.Atoi(q.Get("limit")); err == nil && v > 0 && v <= 1000 {
		limit = v
	}
	if v := q.Get("offset"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "offset must be 0 or more")
			return
		}
		offset = n
	}
	emails := []string{}
	for _, e := range q["email"] {
		for _, p := range strings.Split(e, ",") {
			if p = strings.TrimSpace(p); p != "" {
				emails = append(emails, p)
			}
		}
	}
	// One more than asked for says whether there is a next page.
	list, err := verify.List(r.Context(), s.pool, a.WorkspaceID, status, emails, limit+1, offset)
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

// startVerification queues verification of addresses, a lead list, or
// every lead without a fresh result. It answers at once; results appear
// under GET /verifications as they come in.
func (s *Server) startVerification(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		Emails     []string `json:"emails"`
		ListID     *string  `json:"list_id"`
		Unverified bool     `json:"unverified"` // every lead without a fresh result
		Force      bool     `json:"force"`      // check again even when a fresh result exists
	}
	if !decodeLarge(w, r, &req, 8<<20) {
		return
	}
	emails := req.Emails
	ctx := r.Context()
	if req.ListID != nil {
		if !validUUID(*req.ListID) {
			writeError(w, r, http.StatusNotFound, "not_found", "lead list not found")
			return
		}
		rows, err := s.pool.Query(ctx, `
			SELECT l.email FROM lead_list_members m JOIN leads l ON l.id = m.lead_id JOIN lead_lists ll ON ll.id = m.list_id
			WHERE ll.workspace_id = $1 AND ll.id = $2`, a.WorkspaceID, *req.ListID)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		more, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			s.internal(w, r, err)
			return
		}
		emails = append(emails, more...)
	}
	if req.Unverified {
		set, err := verify.LoadSettings(ctx, s.pool, a.WorkspaceID)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		rows, err := s.pool.Query(ctx, `
			SELECT l.email FROM leads l
			LEFT JOIN email_verifications v ON v.workspace_id = l.workspace_id AND v.email = lower(l.email)
			WHERE l.workspace_id = $1 AND (v.email IS NULL OR v.checked_at < now() - make_interval(days => $2))
			LIMIT 100000`, a.WorkspaceID, set.MaxAgeDays)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		more, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			s.internal(w, r, err)
			return
		}
		emails = append(emails, more...)
	}
	if len(emails) == 0 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "send emails, a list_id, or unverified: true")
		return
	}
	if len(emails) > 100000 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "at most 100,000 addresses per request")
		return
	}
	var ids []int64
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		if ids, err = verify.Enqueue(ctx, tx, a.WorkspaceID, emails, req.Force); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "verification.started", "workspace", a.WorkspaceID,
			map[string]any{"count": len(emails), "force": req.Force})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"queued": len(emails), "jobs": len(ids)})
}

func (s *Server) verificationSummary(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	set, err := verify.LoadSettings(ctx, s.pool, a.WorkspaceID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var out struct {
		Leads         int `json:"leads"`
		Verified      int `json:"verified"`
		Stale         int `json:"stale"`
		Unverified    int `json:"unverified"`
		Deliverable   int `json:"deliverable"`
		Undeliverable int `json:"undeliverable"`
		Risky         int `json:"risky"`
		CatchAll      int `json:"catch_all"`
		Unknown       int `json:"unknown"`
		// Addresses waiting in verification jobs.
		Pending  int                      `json:"pending"`
		Settings verificationSettingsView `json:"settings"`
	}
	err = s.pool.QueryRow(ctx, `
		SELECT count(*)::int,
		       count(v.email) FILTER (WHERE v.checked_at >= now() - make_interval(days => $2))::int,
		       count(v.email) FILTER (WHERE v.checked_at < now() - make_interval(days => $2))::int,
		       count(*) FILTER (WHERE v.email IS NULL)::int,
		       count(*) FILTER (WHERE v.status = 'deliverable')::int,
		       count(*) FILTER (WHERE v.status = 'undeliverable')::int,
		       count(*) FILTER (WHERE v.status = 'risky')::int,
		       count(*) FILTER (WHERE v.catch_all)::int,
		       count(*) FILTER (WHERE v.status = 'unknown')::int
		FROM leads l
		LEFT JOIN email_verifications v ON v.workspace_id = l.workspace_id AND v.email = lower(l.email)
		WHERE l.workspace_id = $1`, a.WorkspaceID, set.MaxAgeDays).
		Scan(&out.Leads, &out.Verified, &out.Stale, &out.Unverified, &out.Deliverable, &out.Undeliverable, &out.Risky, &out.CatchAll, &out.Unknown)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(sum(jsonb_array_length(payload->'emails')), 0)::int FROM jobs
		WHERE workspace_id = $1 AND kind = $2 AND status IN ('queued', 'running')`, a.WorkspaceID, verify.JobVerify).Scan(&out.Pending); err != nil {
		s.internal(w, r, err)
		return
	}
	out.Settings = s.settingsView(r, a.WorkspaceID, set)
	writeJSON(w, http.StatusOK, out)
}
