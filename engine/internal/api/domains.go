package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/leads"
	"github.com/outflareai/outflare/engine/internal/mailbox"
)

var errConflict = errors.New("conflict")

func (s *Server) routesDomains(mux *http.ServeMux) {
	read, manage := auth.ScopeMailboxesRead, auth.ScopeMailboxesManage
	mux.HandleFunc("GET /v1/workspaces/{ws}/sending-domains", s.inBrand(read, s.listDomains))
	mux.HandleFunc("POST /v1/workspaces/{ws}/sending-domains", s.inWorkspace(manage, s.addDomain))
	mux.HandleFunc("POST /v1/workspaces/{ws}/sending-domains/check", s.inWorkspace(manage, s.checkAllDomains))
	mux.HandleFunc("GET /v1/workspaces/{ws}/sending-domains/{domain}", s.inBrand(read, s.getDomain))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/sending-domains/{domain}", s.inWorkspace(manage, s.updateDomain))
	mux.HandleFunc("DELETE /v1/workspaces/{ws}/sending-domains/{domain}", s.inWorkspace(manage, s.deleteDomain))
	mux.HandleFunc("POST /v1/workspaces/{ws}/sending-domains/{domain}/check", s.inWorkspace(manage, s.checkDomain))
	mux.HandleFunc("POST /v1/workspaces/{ws}/sending-domains/{domain}/dns", s.inWorkspace(manage, s.addDomainDNS))
	mux.HandleFunc("GET /v1/workspaces/{ws}/sending-domains/{domain}/dns/approval", s.inWorkspace(manage, s.sendingDomainDNSApproval))
	mux.HandleFunc("POST /v1/workspaces/{ws}/sending-domains/{domain}/dns/approved", s.inWorkspace(manage, s.sendingDomainDNSApproved))
}

type mailboxCounts struct {
	Active    int `json:"active"`
	Error     int `json:"error"`
	Paused    int `json:"paused"`
	Verifying int `json:"verifying"`
}

type domainView struct {
	Domain        string              `json:"domain"`
	Status        string              `json:"status"`
	DailyLimit    *int                `json:"daily_limit"`
	Notes         string              `json:"notes"`
	BrandID       *string             `json:"brand_id"`
	Source        string              `json:"source"`
	Health        string              `json:"health"`
	Issues        []mailbox.Issue     `json:"issues"`
	MXProvider    *string             `json:"mx_provider"`
	MailHost      *string             `json:"mail_host"`
	MXHosts       []string            `json:"mx_hosts"`
	SPF           string              `json:"spf"`
	SPFRecord     *string             `json:"spf_record"`
	DKIM          string              `json:"dkim"`
	DKIMSelector  *string             `json:"dkim_selector"`
	DMARC         string              `json:"dmarc"`
	DMARCRecord   *string             `json:"dmarc_record"`
	DMARCPolicy   *string             `json:"dmarc_policy"`
	Blocklists    []mailbox.Blocklist `json:"blocklists"`
	CheckedAt     *time.Time          `json:"checked_at"`
	Mailboxes     int                 `json:"mailboxes"`
	MailboxCounts mailboxCounts       `json:"mailbox_counts"`
	DailyCapacity int                 `json:"daily_capacity"`
	Sent24h       int                 `json:"sent_24h"`
	Sent7d        int                 `json:"sent_7d"`
	Replies7d     int                 `json:"replies_7d"`
	// Leads emailed from this domain in 7 days, and those of them who
	// replied: the reply rate's two halves.
	ContactedLeads7d int          `json:"contacted_leads_7d"`
	RepliedLeads7d   int          `json:"replied_leads_7d"`
	Bounces7d        int          `json:"bounces_7d"`
	Warmup           domainWarmup `json:"warmup"`
	CreatedAt        time.Time    `json:"created_at"`
	UpdatedAt        time.Time    `json:"updated_at"`
}

// domainWarmup counts the domain's mailboxes that are warming up.
type domainWarmup struct {
	Warming int `json:"warming"`
}

type domainDay struct {
	Date    string `json:"date"`
	Sent    int    `json:"sent"`
	Replies int    `json:"replies"`
	Bounces int    `json:"bounces"`
}

type domainDetail struct {
	domainView
	Daily []domainDay `json:"daily"`
	// The last one-click run that added the domain's missing SPF or DMARC
	// record, and the connections that can add them.
	DNSInstall     *connectors.DNSInstall `json:"dns_install"`
	DNSConnections []string               `json:"dns_connections"`
}

// domainSelect reads domains with their mailbox and sending counts. $1 is
// the workspace; $2 is one domain, or null for all of them.
const domainSelect = `
	WITH mb AS (
		SELECT domain, count(*)::int AS total,
		       count(*) FILTER (WHERE status = 'active')::int AS active,
		       count(*) FILTER (WHERE status = 'error')::int AS error,
		       count(*) FILTER (WHERE status = 'paused')::int AS paused,
		       count(*) FILTER (WHERE status = 'verifying')::int AS verifying,
		       COALESCE(sum(daily_limit) FILTER (WHERE status = 'active'), 0)::int AS capacity,
		       CASE WHEN count(DISTINCT provider) = 1 THEN min(provider) FILTER (WHERE provider IN ('google', 'microsoft')) END AS host
		FROM mailboxes WHERE workspace_id = $1 AND ($2::text IS NULL OR domain = $2) GROUP BY domain
	), sent AS (
		SELECT lower(split_part(from_email, '@', 2)) AS domain,
		       count(*) FILTER (WHERE created_at > now() - interval '24 hours')::int AS d1,
		       count(*)::int AS d7,
		       count(DISTINCT lead_id)::int AS leads
		FROM messages WHERE workspace_id = $1 AND status = 'sent' AND created_at > now() - interval '7 days'
		  AND ($2::text IS NULL OR lower(split_part(from_email, '@', 2)) = $2)
		GROUP BY 1
	), inb AS (
		SELECT m.domain,
		       count(*) FILTER (WHERE i.kind = 'reply')::int AS replies,
		       count(*) FILTER (WHERE i.kind = 'bounce')::int AS bounces,
		       count(DISTINCT i.lead_id) FILTER (WHERE i.kind = 'reply')::int AS replied
		FROM inbound_messages i JOIN mailboxes m ON m.id = i.mailbox_id
		WHERE i.workspace_id = $1 AND i.received_at > now() - interval '7 days' AND ($2::text IS NULL OR m.domain = $2)
		GROUP BY m.domain
	), warm AS (
		SELECT m.domain,
		       count(*) FILTER (WHERE m.lifecycle_stage = 'warming' AND m.status IN ('active', 'verifying'))::int AS warming
		FROM mailboxes m
		WHERE m.workspace_id = $1 AND ($2::text IS NULL OR m.domain = $2)
		GROUP BY m.domain
	)
	SELECT d.domain, d.status, d.daily_limit, d.notes, d.brand_id::text, d.source, d.mx_provider, mb.host, d.mx_hosts,
	       d.spf, d.spf_record, d.dkim, d.dkim_selector, d.dmarc, d.dmarc_record, d.dmarc_policy, d.blocklists,
	       d.checked_at, d.created_at, d.updated_at,
	       COALESCE(mb.total, 0), COALESCE(mb.active, 0), COALESCE(mb.error, 0), COALESCE(mb.paused, 0),
	       COALESCE(mb.verifying, 0), COALESCE(mb.capacity, 0),
	       COALESCE(sent.d1, 0), COALESCE(sent.d7, 0), COALESCE(inb.replies, 0), COALESCE(sent.leads, 0), COALESCE(inb.replied, 0), COALESCE(inb.bounces, 0),
	       COALESCE(warm.warming, 0)
	FROM sending_domains d
	LEFT JOIN mb ON mb.domain = d.domain
	LEFT JOIN sent ON sent.domain = d.domain
	LEFT JOIN inb ON inb.domain = d.domain
	LEFT JOIN warm ON warm.domain = d.domain
	WHERE d.workspace_id = $1 AND ($2::text IS NULL OR d.domain = $2)
	ORDER BY d.domain`

func (s *Server) queryDomains(ctx context.Context, ws string, domain *string) ([]domainView, error) {
	rows, err := s.pool.Query(ctx, domainSelect, ws, domain)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (domainView, error) {
		var v domainView
		var lists []byte
		c := &v.MailboxCounts
		err := r.Scan(&v.Domain, &v.Status, &v.DailyLimit, &v.Notes, &v.BrandID, &v.Source, &v.MXProvider, &v.MailHost, &v.MXHosts,
			&v.SPF, &v.SPFRecord, &v.DKIM, &v.DKIMSelector, &v.DMARC, &v.DMARCRecord, &v.DMARCPolicy, &lists,
			&v.CheckedAt, &v.CreatedAt, &v.UpdatedAt,
			&v.Mailboxes, &c.Active, &c.Error, &c.Paused, &c.Verifying, &v.DailyCapacity,
			&v.Sent24h, &v.Sent7d, &v.Replies7d, &v.ContactedLeads7d, &v.RepliedLeads7d, &v.Bounces7d,
			&v.Warmup.Warming)
		if err != nil {
			return v, err
		}
		if err := json.Unmarshal(lists, &v.Blocklists); err != nil {
			return v, err
		}
		switch {
		case v.Status == "paused":
			v.DailyCapacity = 0
		case v.DailyLimit != nil && *v.DailyLimit < v.DailyCapacity:
			v.DailyCapacity = *v.DailyLimit
		}
		facts := mailbox.DomainFacts{
			Domain: v.Domain, Checked: v.CheckedAt != nil, Paused: v.Status == "paused",
			SPF: v.SPF, DKIM: v.DKIM, DMARC: v.DMARC, Blocklists: v.Blocklists,
			Mailboxes: v.Mailboxes, Failing: c.Error, Sent7d: v.Sent7d, Bounces7d: v.Bounces7d,
		}
		if v.MXProvider != nil {
			facts.MXProvider = *v.MXProvider
		}
		if v.SPFRecord != nil {
			facts.SPFRecord = *v.SPFRecord
		}
		if v.MailHost != nil {
			facts.MailHost = *v.MailHost
		}
		v.Issues = mailbox.DomainIssues(facts)
		v.Health = mailbox.Health(facts.Checked, v.Issues)
		return v, nil
	})
}

func (s *Server) listDomains(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	brand, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	list, err := s.queryDomains(r.Context(), a.WorkspaceID, nil)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list = slices.DeleteFunc(list, func(d domainView) bool { return !brand.matches(d.BrandID) })
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

// domainParam reads the {domain} path segment, lowercased.
func domainParam(r *http.Request) string {
	return strings.ToLower(strings.TrimSpace(r.PathValue("domain")))
}

func (s *Server) getDomain(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	d, ok := s.findDomain(w, r, a.WorkspaceID, domainParam(r))
	if !ok {
		return
	}
	// The last 14 days, by UTC date, today last.
	rows, err := s.pool.Query(r.Context(), `
		SELECT to_char(day AT TIME ZONE 'UTC', 'YYYY-MM-DD'),
		       (SELECT count(*)::int FROM messages g
		        WHERE g.workspace_id = $1 AND g.status = 'sent' AND lower(split_part(g.from_email, '@', 2)) = $2
		          AND g.created_at >= day AND g.created_at < day + interval '1 day'),
		       (SELECT count(*)::int FROM inbound_messages i JOIN mailboxes m ON m.id = i.mailbox_id
		        WHERE i.workspace_id = $1 AND m.domain = $2 AND i.kind = 'reply'
		          AND i.received_at >= day AND i.received_at < day + interval '1 day'),
		       (SELECT count(*)::int FROM inbound_messages i JOIN mailboxes m ON m.id = i.mailbox_id
		        WHERE i.workspace_id = $1 AND m.domain = $2 AND i.kind = 'bounce'
		          AND i.received_at >= day AND i.received_at < day + interval '1 day')
		FROM generate_series(date_trunc('day', now(), 'UTC') - interval '13 days', date_trunc('day', now(), 'UTC'), interval '1 day') AS day
		ORDER BY day`, a.WorkspaceID, d.Domain)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	daily, err := pgx.CollectRows(rows, pgx.RowToStructByPos[domainDay])
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out := domainDetail{domainView: *d, Daily: daily}
	if err := s.pool.QueryRow(r.Context(), `SELECT dns_install FROM sending_domains WHERE workspace_id = $1 AND domain = $2`, a.WorkspaceID, d.Domain).Scan(&out.DNSInstall); err != nil {
		s.internal(w, r, err)
		return
	}
	dns, err := s.dnsConnections(r.Context(), a.WorkspaceID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out.DNSConnections = dns(deref(d.BrandID))
	writeJSON(w, http.StatusOK, out)
}

// addDomainDNS adds the domain's missing SPF or DMARC record with the
// workspace's own DNS host connection, then checks the domain again.
func (s *Server) addDomainDNS(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	d, ok := s.findDomain(w, r, a.WorkspaceID, domainParam(r))
	if !ok {
		return
	}
	s.queueDNSAdd(w, r, a, "sending_domain", d.Domain, deref(d.BrandID))
}

func (s *Server) findDomain(w http.ResponseWriter, r *http.Request, ws, domain string) (*domainView, bool) {
	list, err := s.queryDomains(r.Context(), ws, &domain)
	if err != nil {
		s.internal(w, r, err)
		return nil, false
	}
	if len(list) == 0 || !accessFrom(r.Context()).sees(list[0].BrandID) {
		writeError(w, r, http.StatusNotFound, "not_found", "domain not found")
		return nil, false
	}
	return &list[0], true
}

func (s *Server) addDomain(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	if s.mail == nil {
		writeError(w, r, http.StatusServiceUnavailable, "unavailable", "mailboxes are not configured on this server")
		return
	}
	var req struct {
		Domain string `json:"domain"`
	}
	if !decode(w, r, &req) {
		return
	}
	// Accept "acme-com.example", "@acme-com.example", a URL, or an address on the domain.
	kind, v, err := leads.ParseSuppression(req.Domain)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "enter a domain such as acme-com.example")
		return
	}
	if kind == "email" {
		v = v[strings.LastIndexByte(v, '@')+1:]
	}
	ctx := r.Context()
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			INSERT INTO sending_domains (workspace_id, domain, spf, dkim, dmarc, checked_at, source)
			VALUES ($1, $2, 'pending', 'pending', 'pending', NULL, 'manual')
			ON CONFLICT (workspace_id, domain) DO NOTHING`, a.WorkspaceID, v)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errConflict
		}
		if err := s.mail.QueueDomainCheck(ctx, tx, a.WorkspaceID, v); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "domain.added", "domain", v, map[string]any{})
	})
	if errors.Is(err, errConflict) {
		writeError(w, r, http.StatusConflict, "conflict", v+" is already in this workspace")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if d, ok := s.findDomain(w, r, a.WorkspaceID, v); ok {
		writeJSON(w, http.StatusCreated, d)
	}
}

func (s *Server) updateDomain(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	domain := domainParam(r)
	var req struct {
		Status     *string       `json:"status"`
		DailyLimit optional[int] `json:"daily_limit"`
		Notes      *string       `json:"notes"`
		// Applied to every mailbox on the domain.
		MailboxDailyLimit *int `json:"mailbox_daily_limit"`
		MailboxMinGap     *int `json:"mailbox_min_gap_seconds"`
	}
	if !decode(w, r, &req) {
		return
	}
	switch {
	case req.Status != nil && *req.Status != "active" && *req.Status != "paused":
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status must be active or paused")
		return
	case req.DailyLimit.Value != nil && (*req.DailyLimit.Value < 1 || *req.DailyLimit.Value > 100000):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "daily_limit must be 1 to 100000, or null for no domain cap")
		return
	case req.Notes != nil && len(*req.Notes) > 2000:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "notes must be at most 2000 characters")
		return
	case req.MailboxDailyLimit != nil && (*req.MailboxDailyLimit < 1 || *req.MailboxDailyLimit > 500):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "mailbox_daily_limit must be 1 to 500")
		return
	case req.MailboxMinGap != nil && (*req.MailboxMinGap < 0 || *req.MailboxMinGap > 3600):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "mailbox_min_gap_seconds must be 0 to 3600")
		return
	}

	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var oldStatus string
		err := tx.QueryRow(ctx, `SELECT status FROM sending_domains WHERE workspace_id = $1 AND domain = $2 FOR UPDATE`,
			a.WorkspaceID, domain).Scan(&oldStatus)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `
			UPDATE sending_domains SET
				status = COALESCE($3, status),
				daily_limit = CASE WHEN $4 THEN $5 ELSE daily_limit END,
				notes = COALESCE($6, notes),
				updated_at = now()
			WHERE workspace_id = $1 AND domain = $2`,
			a.WorkspaceID, domain, req.Status, req.DailyLimit.Set, req.DailyLimit.Value, req.Notes)
		if err != nil {
			return err
		}
		data := map[string]any{}
		if req.MailboxDailyLimit != nil || req.MailboxMinGap != nil {
			tag, err := tx.Exec(ctx, `
				UPDATE mailboxes SET daily_limit = COALESCE($3, daily_limit), min_gap_seconds = COALESCE($4, min_gap_seconds), updated_at = now()
				WHERE workspace_id = $1 AND domain = $2`, a.WorkspaceID, domain, req.MailboxDailyLimit, req.MailboxMinGap)
			if err != nil {
				return err
			}
			data["mailboxes_updated"] = tag.RowsAffected()
			if req.MailboxDailyLimit != nil {
				data["mailbox_daily_limit"] = *req.MailboxDailyLimit
			}
			if req.MailboxMinGap != nil {
				data["mailbox_min_gap_seconds"] = *req.MailboxMinGap
			}
		}
		if req.DailyLimit.Set {
			data["daily_limit"] = req.DailyLimit.Value
		}
		if req.Notes != nil {
			data["notes"] = *req.Notes
		}
		action := "domain.updated"
		if req.Status != nil && *req.Status != oldStatus {
			action = map[string]string{"paused": "domain.paused", "active": "domain.resumed"}[*req.Status]
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, action, "domain", domain, data)
	})
	switch {
	case errors.Is(err, errNotFound):
		writeError(w, r, http.StatusNotFound, "not_found", "domain not found")
		return
	case err != nil:
		s.internal(w, r, err)
		return
	}
	if d, ok := s.findDomain(w, r, a.WorkspaceID, domain); ok {
		writeJSON(w, http.StatusOK, d)
	}
}

func (s *Server) deleteDomain(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	domain := domainParam(r)
	ctx := r.Context()
	var n int
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM mailboxes WHERE workspace_id = $1 AND domain = $2`, a.WorkspaceID, domain).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return errConflict
		}
		tag, err := tx.Exec(ctx, `DELETE FROM sending_domains WHERE workspace_id = $1 AND domain = $2`, a.WorkspaceID, domain)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errNotFound
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "domain.removed", "domain", domain, map[string]any{})
	})
	switch {
	case errors.Is(err, errNotFound):
		writeError(w, r, http.StatusNotFound, "not_found", "domain not found")
	case errors.Is(err, errConflict):
		writeError(w, r, http.StatusConflict, "conflict", "remove the domain's mailboxes first; "+domain+" still has "+mailbox.Plural(n, "mailbox", "mailboxes"))
	case err != nil:
		s.internal(w, r, err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) checkDomain(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	domain := domainParam(r)
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM sending_domains WHERE workspace_id = $1 AND domain = $2)`, a.WorkspaceID, domain).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return errNotFound
		}
		return s.mail.QueueDomainCheck(ctx, tx, a.WorkspaceID, domain)
	})
	switch {
	case errors.Is(err, errNotFound):
		writeError(w, r, http.StatusNotFound, "not_found", "domain not found")
	case err != nil:
		s.internal(w, r, err)
	default:
		writeJSON(w, http.StatusAccepted, map[string]bool{"queued": true})
	}
}

func (s *Server) checkAllDomains(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	var n int
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT domain FROM sending_domains WHERE workspace_id = $1`, a.WorkspaceID)
		if err != nil {
			return err
		}
		domains, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		for _, d := range domains {
			if err := s.mail.QueueDomainCheck(ctx, tx, a.WorkspaceID, d); err != nil {
				return err
			}
		}
		n = len(domains)
		return nil
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]int{"queued": n})
}
