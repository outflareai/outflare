package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/track"
	"github.com/outflareai/outflare/engine/internal/tracking"
)

func (s *Server) routesTracking(mux *http.ServeMux) {
	read, manage := auth.ScopeMailboxesRead, auth.ScopeMailboxesManage
	mux.HandleFunc("GET /v1/workspaces/{ws}/tracking-domains", s.inBrand(read, s.listTrackingDomains))
	mux.HandleFunc("POST /v1/workspaces/{ws}/tracking-domains", s.inWorkspace(manage, s.addTrackingDomain))
	mux.HandleFunc("POST /v1/workspaces/{ws}/tracking-domains/{id}/check", s.inWorkspace(manage, s.checkTrackingDomain))
	mux.HandleFunc("POST /v1/workspaces/{ws}/tracking-domains/{id}/dns", s.inWorkspace(manage, s.addTrackingDomainDNS))
	mux.HandleFunc("GET /v1/workspaces/{ws}/tracking-domains/{id}/dns/approval", s.inWorkspace(manage, s.trackingDomainDNSApproval))
	mux.HandleFunc("POST /v1/workspaces/{ws}/tracking-domains/{id}/dns/approved", s.inWorkspace(manage, s.trackingDomainDNSApproved))
	mux.HandleFunc("DELETE /v1/workspaces/{ws}/tracking-domains/{id}", s.inWorkspace(manage, s.removeTrackingDomain))
	// Click links in emails. Public: the link id is the authority.
	mux.HandleFunc("GET /c/{id}", s.click)
}

// trackingDomainView is a tracking domain with its one-click DNS: the
// connections that can add its CNAME and the last run.
type trackingDomainView struct {
	tracking.Domain
	DNSInstall     *connectors.DNSInstall `json:"dns_install"`
	DNSConnections []string               `json:"dns_connections"`
}

func (s *Server) trackingDomains(ctx context.Context, ws string, id *string) ([]trackingDomainView, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, domain, status, error, last_checked_at, verified_at, created_at, dns_install, https_status, https_error FROM tracking_domains
		WHERE workspace_id = $1 AND ($2::uuid IS NULL OR id = $2) ORDER BY created_at`, ws, id)
	if err != nil {
		return nil, err
	}
	target := s.trackingHost()
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (trackingDomainView, error) {
		v := trackingDomainView{Domain: tracking.Domain{Target: target}}
		err := row.Scan(&v.ID, &v.Domain.Domain, &v.Status, &v.Error, &v.LastCheckedAt, &v.VerifiedAt, &v.CreatedAt, &v.DNSInstall, &v.HTTPSStatus, &v.HTTPSError)
		return v, err
	})
	if err != nil || len(list) == 0 {
		return list, err
	}
	dns, err := s.dnsConnections(ctx, ws)
	if err != nil {
		return nil, err
	}
	for i := range list {
		list[i].DNSConnections = dns("")
	}
	return list, nil
}

func (s *Server) trackingDomain(ctx context.Context, ws, id string) (trackingDomainView, error) {
	list, err := s.trackingDomains(ctx, ws, &id)
	if err == nil && len(list) == 0 {
		err = pgx.ErrNoRows
	}
	if err != nil {
		return trackingDomainView{}, err
	}
	return list[0], nil
}

func (s *Server) listTrackingDomains(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	list, err := s.trackingDomains(r.Context(), a.WorkspaceID, nil)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "target": s.trackingHost()})
}

// addTrackingDomainDNS adds the domain's CNAME with the workspace's own
// DNS host connection, then looks for it on its own.
func (s *Server) addTrackingDomainDNS(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "tracking domain not found")
		return
	}
	if _, err := s.trackingDomain(r.Context(), a.WorkspaceID, id); errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, http.StatusNotFound, "not_found", "tracking domain not found")
		return
	} else if err != nil {
		s.internal(w, r, err)
		return
	}
	s.queueDNSAdd(w, r, a, "tracking_domain", id, "")
}

func (s *Server) addTrackingDomain(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		Domain string `json:"domain"`
	}
	if !decode(w, r, &req) {
		return
	}
	domain, ok := tracking.ValidDomain(req.Domain)
	if !ok {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "domain must be a host name such as links.acme-com.example")
		return
	}
	if domain == s.trackingHost() {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "use a domain of your own, not this server's address")
		return
	}
	ctx := r.Context()
	var id string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `INSERT INTO tracking_domains (workspace_id, domain) VALUES ($1, $2) ON CONFLICT (domain) DO NOTHING RETURNING id`, a.WorkspaceID, domain).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return errInvalid(domain + " is already in use")
		}
		if err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "tracking_domain.added", "tracking_domain", id, map[string]any{"domain": domain})
	})
	var inv errInvalid
	if errors.As(err, &inv) {
		writeError(w, r, http.StatusConflict, "conflict", string(inv))
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	s.verifyTrackingDomain(w, r, a.WorkspaceID, id, http.StatusCreated)
}

func (s *Server) checkTrackingDomain(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "tracking domain not found")
		return
	}
	s.verifyTrackingDomain(w, r, a.WorkspaceID, id, http.StatusOK)
}

// verifyTrackingDomain checks the CNAME now and saves the outcome. Once
// verified, the workspace's unsubscribe and click links use it.
func (s *Server) verifyTrackingDomain(w http.ResponseWriter, r *http.Request, ws, id string, status int) {
	d, err := s.checkTracking(r.Context(), ws, id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, http.StatusNotFound, "not_found", "tracking domain not found")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, status, d)
}

// checkTracking looks for the domain's CNAME now and saves the outcome.
func (s *Server) checkTracking(ctx context.Context, ws, id string) (trackingDomainView, error) {
	d, err := s.trackingDomain(ctx, ws, id)
	if err != nil {
		return d, err
	}
	state, reason := "verified", ""
	if err := tracking.Verify(ctx, s.cname, d.Domain.Domain, d.Target); err != nil {
		state, reason = "failed", err.Error()
		if d.Status == "pending" {
			state = "pending" // not set up yet, rather than broken
		}
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			UPDATE tracking_domains SET status = $2, error = $3, last_checked_at = now(),
				verified_at = CASE WHEN $2 = 'verified' THEN COALESCE(verified_at, now()) ELSE NULL END
			WHERE id = $1`, id, state, reason); err != nil {
			return err
		}
		// Only a domain that points here gets a certificate, so nobody
		// can ask for them for domains they don't control.
		if state == "verified" && (d.HTTPSStatus == nil || *d.HTTPSStatus == "failed") {
			return s.queueTrackingHTTPS(ctx, tx, ws, id)
		}
		return nil
	})
	if err != nil {
		return d, err
	}
	return s.trackingDomain(ctx, ws, id)
}

func (s *Server) removeTrackingDomain(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "tracking domain not found")
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var domain string
		err := tx.QueryRow(ctx, `DELETE FROM tracking_domains WHERE id = $1 AND workspace_id = $2 RETURNING domain`, id, a.WorkspaceID).Scan(&domain)
		if err != nil {
			return err
		}
		if err := s.queueTrackingHTTPSRemove(ctx, tx, a.WorkspaceID, domain); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "tracking_domain.removed", "tracking_domain", id, map[string]any{"domain": domain})
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, http.StatusNotFound, "not_found", "tracking domain not found")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) click(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !validUUID(id) {
		http.NotFound(w, r)
		return
	}
	now := time.Now()
	c, ok, err := tracking.Click(r.Context(), s.pool, id, r.UserAgent(), now)
	if err != nil {
		s.log.Error("click", "err", err)
	}
	target := c.Target
	if ok && err == nil {
		target = s.withVisitToken(r.Context(), c)
	}
	if ok && s.emails != nil {
		if err := s.emails.Clicked(r.Context(), id, r.UserAgent(), now); err != nil {
			track.Fail(r.Context(), s.log, track.Event{Name: "email.click_failed", Surface: "system", Props: map[string]any{"link_id": id}}, "internal", err)
		}
	}
	if !ok {
		http.Error(w, "This link has expired or was never valid.", http.StatusNotFound)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	http.Redirect(w, r, target, http.StatusFound)
}
