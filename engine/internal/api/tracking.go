package api

import (
	"context"
	"errors"
	"github.com/outflareai/outflare/engine/internal/quota"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/track"
	"github.com/outflareai/outflare/engine/internal/tracking"
)

func (s *Server) routesTracking(mux *http.ServeMux) {
	read, manage := auth.ScopeMailboxesRead, auth.ScopeMailboxesManage
	for _, t := range s.twins("tracking-domains") {
		mux.HandleFunc("GET "+t.path, t.on(s.inBrand)(read, s.listTrackingDomains))
		mux.HandleFunc("POST "+t.path, t.on(s.inWorkspace)(manage, s.addTrackingDomain))
		mux.HandleFunc("POST "+t.path+"/{id}/check", t.on(s.inWorkspace)(manage, s.owned("tracking_domains", "id", s.checkTrackingDomain)))
		mux.HandleFunc("POST "+t.path+"/{id}/dns", t.on(s.inWorkspace)(manage, s.owned("tracking_domains", "id", s.addTrackingDomainDNS)))
		mux.HandleFunc("GET "+t.path+"/{id}/dns/approval", t.on(s.inWorkspace)(manage, s.owned("tracking_domains", "id", s.trackingDomainDNSApproval)))
		mux.HandleFunc("POST "+t.path+"/{id}/dns/approved", t.on(s.inWorkspace)(manage, s.owned("tracking_domains", "id", s.trackingDomainDNSApproved)))
		mux.HandleFunc("DELETE "+t.path+"/{id}", t.on(s.inWorkspace)(manage, s.owned("tracking_domains", "id", s.removeTrackingDomain)))
	}
	// Moving a domain to another brand is the workspace's: it crosses brands.
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/tracking-domains/{id}", s.inWorkspace(manage, s.moveTrackingDomain))
	// Click links in emails. Public: the link id is the authority.
	mux.HandleFunc("GET /c/{id}", s.click)
	// Campaign redirect links. Public: the code is the authority.
	mux.HandleFunc("GET /l/{code}", s.redirect)
}

// trackingDomainView is a tracking domain with its one-click DNS: the
// connections that can add its CNAME and the last run.
type trackingDomainView struct {
	tracking.Domain
	DNSInstall     *connectors.DNSInstall `json:"dns_install"`
	DNSConnections []string               `json:"dns_connections"`
	// PartnerProgramID is the partner program whose portal the domain
	// serves; null for the workspace's email and partner links.
	PartnerProgramID *string `json:"partner_program_id"`
	// HelpCentreID is the help centre the domain serves.
	HelpCentreID *string `json:"help_centre_id"`
	// BrandID is the brand whose links the domain carries.
	BrandID *string `json:"brand_id"`
}

func (s *Server) trackingDomains(ctx context.Context, ws string, id *string) ([]trackingDomainView, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, domain, status, error, last_checked_at, verified_at, created_at, dns_install, https_status, https_error,
			(SELECT p.id::text FROM partner_programs p WHERE p.portal_domain_id = d.id),
			(SELECT h.id::text FROM help_centres h WHERE h.domain_id = d.id), d.brand_id::text FROM tracking_domains d
		WHERE workspace_id = $1 AND ($2::uuid IS NULL OR id = $2) ORDER BY created_at`, ws, id)
	if err != nil {
		return nil, err
	}
	target := s.trackingHost()
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (trackingDomainView, error) {
		v := trackingDomainView{Domain: tracking.Domain{Target: target}}
		err := row.Scan(&v.ID, &v.Domain.Domain, &v.Status, &v.Error, &v.LastCheckedAt, &v.VerifiedAt, &v.CreatedAt, &v.DNSInstall, &v.HTTPSStatus, &v.HTTPSError, &v.PartnerProgramID, &v.HelpCentreID, &v.BrandID)
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
	sel, ok := s.dataBrand(w, r)
	if !ok {
		return
	}
	list, err := s.trackingDomains(r.Context(), a.WorkspaceID, nil)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	// A domain belongs to one brand and carries its links only.
	list = slices.DeleteFunc(list, func(d trackingDomainView) bool { return !sel.matches(d.BrandID) })
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
		// A partner program's id or slug: the domain serves its portal.
		PartnerProgram string `json:"partner_program"`
		// The brand whose links it carries: the route's, else this one,
		// else the workspace's first.
		BrandID *string `json:"brand_id"`
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
	brand, err := s.activationBrand(ctx, a, req.BrandID)
	if !s.activationError(w, r, err) {
		return
	}
	var program *string
	if req.PartnerProgram != "" {
		if !a.has(auth.ScopePartnersManage) {
			writeError(w, r, http.StatusForbidden, "forbidden", s.missingScope(a, auth.ScopePartnersManage))
			return
		}
		var id string
		err := s.pool.QueryRow(ctx, `SELECT id::text FROM partner_programs WHERE workspace_id = $1 AND (id::text = lower($2) OR slug = lower($2))`,
			a.WorkspaceID, req.PartnerProgram).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "partner_program: no such partner program")
			return
		}
		if err != nil {
			s.internal(w, r, err)
			return
		}
		program = &id
	}
	if !s.allowQuota(w, r, a.WorkspaceID, quota.TrackingDomains, 1) {
		return
	}
	var id string
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `INSERT INTO tracking_domains (workspace_id, domain, brand_id) VALUES ($1, $2, $3) ON CONFLICT (domain) DO NOTHING RETURNING id`,
			a.WorkspaceID, domain, brand).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return errInvalid(domain + " is already in use")
		}
		if err != nil {
			return err
		}
		if program != nil {
			tag, err := tx.Exec(ctx, `UPDATE partner_programs SET portal_domain_id = $2, updated_at = now() WHERE id = $1 AND portal_domain_id IS NULL`, *program, id)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				return errInvalid("the partner program already has a domain; remove it first")
			}
			s.forgetHosts()
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "tracking_domain.added", "tracking_domain", id, map[string]any{"domain": domain, "brand_id": brand})
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

// moveTrackingDomain moves a tracking domain to another brand, whose
// emails then use it for their links. The caller manages both brands. A
// domain serving a partner portal or help centre stays with that brand.
func (s *Server) moveTrackingDomain(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	var req struct {
		BrandID *string `json:"brand_id"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.BrandID == nil || strings.TrimSpace(*req.BrandID) == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "brand_id is required")
		return
	}
	ctx := r.Context()
	var cur *string
	var domain string
	var tied bool
	err := errNotFound
	if validUUID(id) {
		err = s.pool.QueryRow(ctx, `SELECT brand_id::text, domain,
				EXISTS (SELECT 1 FROM partner_programs p WHERE p.portal_domain_id = d.id) OR EXISTS (SELECT 1 FROM help_centres h WHERE h.domain_id = d.id)
			FROM tracking_domains d WHERE workspace_id = $1 AND id = $2`, a.WorkspaceID, id).Scan(&cur, &domain, &tied)
	}
	if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, errNotFound) || (err == nil && !a.sees(cur)) {
		writeError(w, r, http.StatusNotFound, "not_found", "tracking domain not found")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if !a.writes(cur) {
		writeError(w, r, http.StatusForbidden, "forbidden", "you can't manage the brand this domain belongs to")
		return
	}
	brand, err := s.activationBrand(ctx, a, req.BrandID)
	if !s.activationError(w, r, err) {
		return
	}
	if cur != nil && *cur == brand {
		s.verifyTrackingDomain(w, r, a.WorkspaceID, id, http.StatusOK)
		return
	}
	if tied {
		writeError(w, r, http.StatusConflict, "conflict", domain+" serves a partner portal or help centre, so it stays with that brand")
		return
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE tracking_domains SET brand_id = $3 WHERE workspace_id = $1 AND id = $2`, a.WorkspaceID, id, brand); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "tracking_domain.moved", "tracking_domain", id, map[string]any{"from_brand_id": cur, "brand_id": brand})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	s.forgetHosts()
	d, err := s.trackingDomain(ctx, a.WorkspaceID, id)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
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

// trackingFailAfter is how long a verified domain's lookups must keep
// failing before it counts as no longer pointing here: longer than the
// negative cache time DNS hosts set (Cloudflare's is 30 minutes).
const trackingFailAfter = time.Hour

// checkTracking looks for the domain's CNAME now and saves the outcome.
func (s *Server) checkTracking(ctx context.Context, ws, id string) (trackingDomainView, error) {
	d, err := s.trackingDomain(ctx, ws, id)
	if err != nil {
		return d, err
	}
	var failingSince *time.Time
	if err := s.pool.QueryRow(ctx, `SELECT failing_since FROM tracking_domains WHERE id = $1 AND workspace_id = $2`, id, ws).Scan(&failingSince); err != nil {
		return d, err
	}
	state, reason := "verified", ""
	var since *time.Time
	if err := tracking.Verify(ctx, s.cname, d.Domain.Domain, d.Target); err != nil {
		state, reason = "failed", err.Error()
		switch d.Status {
		case "pending":
			state = "pending" // not set up yet, rather than broken
		case "verified":
			// A caching resolver can answer "no record" for a while after
			// the record was added, or between two lookups that found it,
			// so a verified domain stays verified until its lookups have
			// failed for longer than any negative cache keeps an answer.
			now := time.Now()
			if since = failingSince; since == nil {
				since = &now
			}
			if now.Sub(*since) < trackingFailAfter {
				state, reason = "verified", ""
			} else {
				since = nil
			}
		}
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			UPDATE tracking_domains SET status = $2, error = $3, last_checked_at = now(), failing_since = $4,
				verified_at = CASE WHEN $2 = 'verified' THEN COALESCE(verified_at, now()) ELSE NULL END
			WHERE id = $1`, id, state, reason, since); err != nil {
			return err
		}
		// Only a domain that points here gets a certificate, so nobody
		// can ask for them for domains they don't control.
		if state == "verified" && (d.HTTPSStatus == nil || *d.HTTPSStatus == "failed") {
			return s.queueTrackingHTTPS(ctx, tx, ws, id)
		}
		// Checking a domain whose certificate is still pending looks at
		// the certificate again too, unless a look is already on its way.
		if state == "verified" && *d.HTTPSStatus == "pending" {
			return s.resumeTrackingHTTPS(ctx, tx, ws, id)
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
