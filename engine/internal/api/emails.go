package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/mail"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/emails"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/missions"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Lifecycle and transactional email (internal/emails): providers, sending
// domains, templates and the emails themselves. Providers are made by
// connecting an email-sending integration; everything else is here.

func (s *Server) routesEmails(mux *http.ServeMux) {
	read, send, manage := auth.ScopeEmailsRead, auth.ScopeEmailsSend, auth.ScopeEmailsManage
	mux.HandleFunc("GET /v1/workspaces/{ws}/email-overview", s.inWorkspace(read, s.emailOverview))
	mux.HandleFunc("GET /v1/workspaces/{ws}/email-provider-guides", s.inWorkspace(read, s.emailGuides))
	mux.HandleFunc("GET /v1/workspaces/{ws}/email-providers", s.inWorkspace(read, s.listEmailProviders))
	mux.HandleFunc("GET /v1/workspaces/{ws}/email-providers/{id}", s.inWorkspace(read, s.getEmailProvider))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/email-providers/{id}", s.inWorkspace(manage, s.updateEmailProvider))
	mux.HandleFunc("POST /v1/workspaces/{ws}/email-providers/{id}/events", s.inWorkspace(manage, s.setUpEmailEvents))
	mux.HandleFunc("GET /v1/workspaces/{ws}/email-providers/{id}/callback", s.inWorkspace(manage, s.getEmailCallback))
	mux.HandleFunc("PUT /v1/workspaces/{ws}/email-providers/{id}/signing-secret", s.inWorkspace(manage, s.setEmailSigningSecret))

	mux.HandleFunc("GET /v1/workspaces/{ws}/email-provider-domains", s.inWorkspace(read, s.listEmailDomains))
	mux.HandleFunc("POST /v1/workspaces/{ws}/email-provider-domains", s.inWorkspace(manage, s.addEmailDomain))
	mux.HandleFunc("GET /v1/workspaces/{ws}/email-provider-domains/{id}", s.inWorkspace(read, s.getEmailProviderDomain))
	mux.HandleFunc("DELETE /v1/workspaces/{ws}/email-provider-domains/{id}", s.inWorkspace(manage, s.deleteEmailDomain))
	mux.HandleFunc("POST /v1/workspaces/{ws}/email-provider-domains/{id}/check", s.inWorkspace(manage, s.checkEmailDomain))
	mux.HandleFunc("POST /v1/workspaces/{ws}/email-provider-domains/{id}/dns", s.inWorkspace(manage, s.addEmailDomainDNS))
	mux.HandleFunc("GET /v1/workspaces/{ws}/email-provider-domains/{id}/dns/approval", s.inWorkspace(manage, s.emailDomainDNSApproval))
	mux.HandleFunc("POST /v1/workspaces/{ws}/email-provider-domains/{id}/dns/approved", s.inWorkspace(manage, s.emailDomainDNSApproved))

	mux.HandleFunc("GET /v1/workspaces/{ws}/email-templates", s.inWorkspace(read, s.listEmailTemplates))
	mux.HandleFunc("POST /v1/workspaces/{ws}/email-templates", s.inWorkspace(manage, s.createEmailTemplate))
	mux.HandleFunc("POST /v1/workspaces/{ws}/email-templates/preview", s.inWorkspace(read, s.previewEmailTemplate))
	mux.HandleFunc("GET /v1/workspaces/{ws}/email-templates/{id}", s.inWorkspace(read, s.getEmailTemplate))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/email-templates/{id}", s.inWorkspace(manage, s.updateEmailTemplate))
	mux.HandleFunc("DELETE /v1/workspaces/{ws}/email-templates/{id}", s.inWorkspace(manage, s.deleteEmailTemplate))
	mux.HandleFunc("POST /v1/workspaces/{ws}/email-templates/{id}/publish", s.inWorkspace(manage, s.publishEmailTemplate))
	mux.HandleFunc("GET /v1/workspaces/{ws}/email-templates/{id}/versions", s.inWorkspace(read, s.listEmailTemplateVersions))

	mux.HandleFunc("GET /v1/workspaces/{ws}/email-topics", s.inWorkspace(read, s.listEmailTopics))
	mux.HandleFunc("POST /v1/workspaces/{ws}/email-topics", s.inWorkspace(manage, s.createEmailTopic))
	mux.HandleFunc("GET /v1/workspaces/{ws}/email-topics/{id}", s.inWorkspace(read, s.getEmailTopic))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/email-topics/{id}", s.inWorkspace(manage, s.updateEmailTopic))

	mux.HandleFunc("GET /v1/workspaces/{ws}/email-broadcasts", s.inWorkspace(read, s.listEmailBroadcasts))
	mux.HandleFunc("POST /v1/workspaces/{ws}/email-broadcasts", s.inWorkspace(manage, s.createEmailBroadcast))
	mux.HandleFunc("GET /v1/workspaces/{ws}/email-broadcasts/{id}", s.inWorkspace(read, s.getEmailBroadcast))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/email-broadcasts/{id}", s.inWorkspace(manage, s.updateEmailBroadcast))
	mux.HandleFunc("POST /v1/workspaces/{ws}/email-broadcasts/{id}/send", s.inWorkspace(send, s.sendEmailBroadcast))
	mux.HandleFunc("POST /v1/workspaces/{ws}/email-broadcasts/{id}/cancel", s.inWorkspace(send, s.cancelEmailBroadcast))
	mux.HandleFunc("GET /v1/workspaces/{ws}/email-broadcasts/{id}/recipients", s.inWorkspace(read, s.listEmailBroadcastRecipients))

	mux.HandleFunc("GET /v1/workspaces/{ws}/emails", s.inWorkspace(read, s.listEmails))
	mux.HandleFunc("POST /v1/workspaces/{ws}/emails", s.inWorkspace(send, s.sendEmail))
	mux.HandleFunc("GET /v1/workspaces/{ws}/emails/{id}", s.inWorkspace(read, s.getEmail))
	mux.HandleFunc("POST /v1/workspaces/{ws}/emails/{id}/cancel", s.inWorkspace(send, s.cancelEmail))
	mux.HandleFunc("GET /v1/workspaces/{ws}/email-recipients/{address}", s.inWorkspace(read, s.getEmailRecipient))
	// The open pixel in emails that track opens.
	mux.HandleFunc("GET /o/{token}", s.openPixel)

	// Providers' delivery events. The token in the path is the secret.
	mux.HandleFunc("POST /v1/email-events/{provider}/{token}", s.emailCallback)
}

func (s *Server) emailWho(ctx context.Context, a *access) emails.Who {
	return emails.Who{ActorID: a.ActorID, ActorType: a.ActorType, UserID: a.personID(), RequestID: RequestID(ctx), MissionID: missions.ID(ctx)}
}

// ---- Overview ----

type emailCounts struct {
	Queued     int `json:"queued"`
	Sent       int `json:"sent"`
	Delivered  int `json:"delivered"`
	Bounced    int `json:"bounced"`
	Complained int `json:"complained"`
	Failed     int `json:"failed"`
	Suppressed int `json:"suppressed"`
}

type emailDay struct {
	Date string `json:"date"`
	emailCounts
}

// emailOverview answers what the Lifecycle overview shows: how far setup
// has got, and the last 30 days of sending.
func (s *Server) emailOverview(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	var setup struct {
		Providers      int  `json:"providers"`
		EventsActive   bool `json:"events_active"`
		Domains        int  `json:"domains"`
		DomainsVerfied int  `json:"domains_verified"`
		Templates      int  `json:"templates"`
		TestSent       bool `json:"test_sent"`
		Sent           bool `json:"sent"`
	}
	err := s.pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM email_providers WHERE workspace_id = $1)::int,
		EXISTS (SELECT 1 FROM email_providers WHERE workspace_id = $1 AND events_status = 'active'),
		(SELECT count(*) FROM email_domains WHERE workspace_id = $1)::int,
		(SELECT count(*) FROM email_domains WHERE workspace_id = $1 AND status = 'verified')::int,
		(SELECT count(*) FROM email_templates WHERE workspace_id = $1 AND status = 'active')::int,
		EXISTS (SELECT 1 FROM emails WHERE workspace_id = $1 AND test AND sent_at IS NOT NULL),
		EXISTS (SELECT 1 FROM emails WHERE workspace_id = $1 AND NOT test AND sent_at IS NOT NULL)`, a.WorkspaceID).
		Scan(&setup.Providers, &setup.EventsActive, &setup.Domains, &setup.DomainsVerfied, &setup.Templates, &setup.TestSent, &setup.Sent)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	rows, err := s.pool.Query(ctx, `
		SELECT to_char(d, 'YYYY-MM-DD'),
		       count(e.id) FILTER (WHERE e.status IN ('queued', 'scheduled', 'sending'))::int,
		       count(e.id) FILTER (WHERE e.status IN ('sent', 'delivered', 'delayed', 'bounced', 'complained'))::int,
		       count(e.id) FILTER (WHERE e.status IN ('delivered', 'complained'))::int,
		       count(e.id) FILTER (WHERE e.status = 'bounced')::int,
		       count(e.id) FILTER (WHERE e.status = 'complained')::int,
		       count(e.id) FILTER (WHERE e.status = 'failed')::int,
		       count(e.id) FILTER (WHERE e.status = 'suppressed')::int
		FROM generate_series((now() AT TIME ZONE 'UTC')::date - 29, (now() AT TIME ZONE 'UTC')::date, interval '1 day') d
		LEFT JOIN emails e ON e.workspace_id = $1 AND NOT e.test AND (e.created_at AT TIME ZONE 'UTC')::date = d::date
		GROUP BY d ORDER BY d`, a.WorkspaceID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	days, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (emailDay, error) {
		var d emailDay
		c := &d.emailCounts
		return d, row.Scan(&d.Date, &c.Queued, &c.Sent, &c.Delivered, &c.Bounced, &c.Complained, &c.Failed, &c.Suppressed)
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var total emailCounts
	for _, d := range days {
		total.Queued += d.Queued
		total.Sent += d.Sent
		total.Delivered += d.Delivered
		total.Bounced += d.Bounced
		total.Complained += d.Complained
		total.Failed += d.Failed
		total.Suppressed += d.Suppressed
	}
	writeJSON(w, http.StatusOK, map[string]any{"setup": setup, "totals_30d": total, "daily": days})
}

func (s *Server) workspaceSlug(ctx context.Context, ws string) string {
	var slug string
	if err := s.pool.QueryRow(ctx, `SELECT slug FROM workspaces WHERE id = $1`, ws).Scan(&slug); err != nil || slug == "" {
		return ws
	}
	return slug
}

func (s *Server) emailGuides(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	domain := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("domain")))
	if domain != "" && !emailDomainRE.MatchString(domain) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "domain must be a domain name such as example.com")
		return
	}
	base := s.publicURL + "/" + s.workspaceSlug(r.Context(), a.WorkspaceID) + "/providers/"
	list := []emails.Guide{}
	for _, c := range []string{"amazon_ses", "postmark", "resend"} {
		if g, ok := emails.GuideFor(c, emails.GuideParams{ConnectURL: base + c, Domain: domain}); ok {
			list = append(list, g)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

// ---- Providers ----

type emailProviderView struct {
	ID              string            `json:"id"`
	IntegrationID   string            `json:"integration_id"`
	Connector       string            `json:"connector"`
	Name            string            `json:"name"`
	BrandID         *string           `json:"brand_id"`
	Channel         string            `json:"channel"`
	Status          string            `json:"status"`
	Purposes        []string          `json:"purposes"`
	IsDefault       bool              `json:"is_default"`
	EventsStatus    string            `json:"events_status"`
	EventsError     *string           `json:"events_error"`
	LastEventAt     *time.Time        `json:"last_event_at"`
	Connection      string            `json:"connection"` // connected or error, from the integration
	ConnectionError *string           `json:"connection_error"`
	Account         map[string]string `json:"account"`
	Domains         int               `json:"domains"`
	DomainsVerified int               `json:"domains_verified"`
	Sent30d         int               `json:"sent_30d"`
	CreatedAt       time.Time         `json:"created_at"`
	UpdatedAt       time.Time         `json:"updated_at"`
}

const emailProviderSelect = `
	SELECT p.id, p.integration_id, p.connector, i.name, p.brand_id, p.channel, p.status, p.purposes, p.is_default,
	       p.events_status, p.events_error, p.last_event_at, i.status, i.last_error, i.account,
	       (SELECT count(*) FROM email_domains d WHERE d.provider_id = p.id)::int,
	       (SELECT count(*) FROM email_domains d WHERE d.provider_id = p.id AND d.status = 'verified')::int,
	       (SELECT count(*) FROM emails e WHERE e.provider_id = p.id AND e.sent_at > now() - interval '30 days' AND NOT e.test)::int,
	       p.created_at, p.updated_at
	FROM email_providers p JOIN integrations i ON i.id = p.integration_id
	WHERE p.workspace_id = $1 AND ($2::uuid IS NULL OR p.id = $2)
	ORDER BY p.is_default DESC, p.created_at`

func (s *Server) queryEmailProviders(ctx context.Context, ws string, id *string) ([]emailProviderView, error) {
	rows, err := s.pool.Query(ctx, emailProviderSelect, ws, id)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (emailProviderView, error) {
		var v emailProviderView
		var account []byte
		err := row.Scan(&v.ID, &v.IntegrationID, &v.Connector, &v.Name, &v.BrandID, &v.Channel, &v.Status, &v.Purposes, &v.IsDefault,
			&v.EventsStatus, &v.EventsError, &v.LastEventAt, &v.Connection, &v.ConnectionError, &account,
			&v.Domains, &v.DomainsVerified, &v.Sent30d, &v.CreatedAt, &v.UpdatedAt)
		if err != nil {
			return v, err
		}
		var acct connectors.Account
		_ = json.Unmarshal(account, &acct)
		v.Account = acct.Details
		if v.Account == nil {
			v.Account = map[string]string{}
		}
		return v, nil
	})
}

func (s *Server) listEmailProviders(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	sel, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	list, err := s.queryEmailProviders(r.Context(), a.WorkspaceID, nil)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list = slices.DeleteFunc(list, func(p emailProviderView) bool { return !sel.matches(p.BrandID) })
	if list == nil {
		list = []emailProviderView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

func (s *Server) findEmailProvider(w http.ResponseWriter, r *http.Request, ws, id string) (*emailProviderView, bool) {
	if validUUID(id) {
		list, err := s.queryEmailProviders(r.Context(), ws, &id)
		if err != nil {
			s.internal(w, r, err)
			return nil, false
		}
		if len(list) == 1 {
			return &list[0], true
		}
	}
	writeError(w, r, http.StatusNotFound, "not_found", "email provider not found")
	return nil, false
}

func (s *Server) getEmailProvider(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	if v, ok := s.findEmailProvider(w, r, a.WorkspaceID, r.PathValue("id")); ok {
		writeJSON(w, http.StatusOK, v)
	}
}

var emailPurposes = []string{"transactional", "lifecycle"}

func (s *Server) updateEmailProvider(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.findEmailProvider(w, r, a.WorkspaceID, r.PathValue("id"))
	if !ok {
		return
	}
	var req struct {
		Status    *string          `json:"status"`
		IsDefault *bool            `json:"is_default"`
		Purposes  *[]string        `json:"purposes"`
		BrandID   optional[string] `json:"brand_id"`
	}
	if !decode(w, r, &req) {
		return
	}
	switch {
	case req.Status != nil && *req.Status != "active" && *req.Status != "paused":
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status must be active or paused")
		return
	case req.IsDefault != nil && !*req.IsDefault:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "is_default can only be set to true; make another provider the default instead")
		return
	case req.Purposes != nil && (len(*req.Purposes) == 0 || slices.ContainsFunc(*req.Purposes, func(p string) bool { return !slices.Contains(emailPurposes, p) })):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "purposes must list transactional, lifecycle or both")
		return
	case req.BrandID.Value != nil && !validUUID(*req.BrandID.Value):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "brand_id must be a brand id, or null")
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if req.BrandID.Value != nil {
			var ok bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM brands WHERE workspace_id = $1 AND id = $2)`, a.WorkspaceID, *req.BrandID.Value).Scan(&ok); err != nil {
				return err
			}
			if !ok {
				return errNotFound
			}
		}
		if req.IsDefault != nil {
			if _, err := tx.Exec(ctx, `UPDATE email_providers SET is_default = false WHERE workspace_id = $1 AND channel = $2 AND is_default AND id <> $3`,
				a.WorkspaceID, cur.Channel, cur.ID); err != nil {
				return err
			}
		}
		var purposes []string
		if req.Purposes != nil {
			purposes = slices.Compact(slices.Sorted(slices.Values(*req.Purposes)))
		}
		if _, err := tx.Exec(ctx, `UPDATE email_providers SET status = COALESCE($3, status), is_default = COALESCE($4, is_default),
				purposes = COALESCE($5, purposes), brand_id = CASE WHEN $6 THEN $7 ELSE brand_id END, updated_at = now()
			WHERE workspace_id = $1 AND id = $2`, a.WorkspaceID, cur.ID, req.Status, req.IsDefault, purposes, req.BrandID.Set, req.BrandID.Value); err != nil {
			return err
		}
		action, data := "email_provider.updated", map[string]any{"connector": cur.Connector}
		switch {
		case req.Status != nil && *req.Status != cur.Status:
			action = map[string]string{"paused": "email_provider.paused", "active": "email_provider.resumed"}[*req.Status]
		case req.IsDefault != nil && !cur.IsDefault:
			action = "email_provider.made_default"
		}
		if req.Purposes != nil {
			data["purposes"] = purposes
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, action, "email_provider", cur.ID, data)
	})
	switch {
	case errors.Is(err, errNotFound):
		writeError(w, r, http.StatusNotFound, "not_found", "brand not found")
		return
	case err != nil:
		s.internal(w, r, err)
		return
	}
	if v, ok := s.findEmailProvider(w, r, a.WorkspaceID, cur.ID); ok {
		writeJSON(w, http.StatusOK, v)
	}
}

// setUpEmailEvents queues setting up delivery events again, after fixing
// a key's permissions say.
func (s *Server) setUpEmailEvents(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.findEmailProvider(w, r, a.WorkspaceID, r.PathValue("id"))
	if !ok {
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE email_providers SET events_status = 'setting_up', events_error = NULL, updated_at = now() WHERE id = $1`, cur.ID); err != nil {
			return err
		}
		if _, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: emails.JobEventsSetup, WorkspaceID: &a.WorkspaceID, Payload: map[string]string{"provider_id": cur.ID}, MaxAttempts: 5}); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "email_provider.events_requested", "email_provider", cur.ID, map[string]any{"connector": cur.Connector})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"queued": true})
}

func (s *Server) getEmailCallback(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.findEmailProvider(w, r, a.WorkspaceID, r.PathValue("id"))
	if !ok {
		return
	}
	url, err := s.emails.ProviderCallback(r.Context(), a.WorkspaceID, cur.ID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"url": url})
}

func (s *Server) setEmailSigningSecret(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.findEmailProvider(w, r, a.WorkspaceID, r.PathValue("id"))
	if !ok {
		return
	}
	var req struct {
		Secret string `json:"secret"`
	}
	if !decode(w, r, &req) {
		return
	}
	req.Secret = strings.TrimSpace(req.Secret)
	if req.Secret == "" || len(req.Secret) > 500 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "secret must be the webhook signing secret from the provider, up to 500 characters")
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := s.emails.SetSigningSecret(ctx, tx, a.WorkspaceID, cur.ID, req.Secret); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "email_provider.signing_secret_set", "email_provider", cur.ID, map[string]any{"connector": cur.Connector})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if v, ok := s.findEmailProvider(w, r, a.WorkspaceID, cur.ID); ok {
		writeJSON(w, http.StatusOK, v)
	}
}

// emailCallback takes a provider's delivery events.
func (s *Server) emailCallback(w http.ResponseWriter, r *http.Request) {
	if s.emails == nil || !validUUID(r.PathValue("provider")) {
		writeError(w, r, http.StatusNotFound, "not_found", "no such callback")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		writeError(w, r, http.StatusRequestEntityTooLarge, "invalid_request", "the body is limited to 1 MiB")
		return
	}
	n, err := s.emails.HandleCallback(r.Context(), r.PathValue("provider"), r.PathValue("token"), r, body)
	switch {
	case errors.Is(err, emails.ErrBadCallback):
		writeError(w, r, http.StatusNotFound, "not_found", "no such callback")
	case err != nil && connectors.KindOf(err) == connectors.ErrUnavailable:
		s.internal(w, r, err)
	case err != nil:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "the callback could not be read: "+err.Error())
	default:
		writeJSON(w, http.StatusOK, map[string]any{"received": n})
	}
}

// ---- Sending domains ----

var emailDomainRE = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)

type emailDomainView struct {
	ID           string                    `json:"id"`
	ProviderID   string                    `json:"provider_id"`
	Connector    string                    `json:"connector"`
	BrandID      *string                   `json:"brand_id"`
	Domain       string                    `json:"domain"`
	Status       string                    `json:"status"`
	StatusReason *string                   `json:"status_reason"`
	Records      []connectors.DomainRecord `json:"records"`
	CheckedAt    *time.Time                `json:"checked_at"`
	VerifiedAt   *time.Time                `json:"verified_at"`
	CreatedAt    time.Time                 `json:"created_at"`
	// The ways to add the records besides copying them: one click through
	// a DNS host connection, a prompt for an AI agent, a message for a
	// developer.
	DNSInstall     *emails.DNSInstall `json:"dns_install"`
	DNSConnections []string           `json:"dns_connections"`
	AIPrompt       string             `json:"ai_prompt"`
	Developer      setupMessage       `json:"developer"`
}

func (s *Server) queryEmailDomains(ctx context.Context, ws string, id, provider *string) ([]emailDomainView, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT d.id, d.provider_id, p.connector, d.brand_id, d.domain, d.status, d.status_reason, d.records, d.checked_at, d.verified_at, d.created_at, d.dns_install
		FROM email_domains d JOIN email_providers p ON p.id = d.provider_id
		WHERE d.workspace_id = $1 AND ($2::uuid IS NULL OR d.id = $2) AND ($3::uuid IS NULL OR d.provider_id = $3)
		ORDER BY d.domain, d.created_at`, ws, id, provider)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (emailDomainView, error) {
		var v emailDomainView
		var records []byte
		if err := row.Scan(&v.ID, &v.ProviderID, &v.Connector, &v.BrandID, &v.Domain, &v.Status, &v.StatusReason, &records, &v.CheckedAt, &v.VerifiedAt, &v.CreatedAt, &v.DNSInstall); err != nil {
			return v, err
		}
		v.Records = []connectors.DomainRecord{}
		return v, json.Unmarshal(records, &v.Records)
	})
	if err != nil || len(list) == 0 {
		return list, err
	}
	dns, err := s.dnsConnections(ctx, ws)
	if err != nil {
		return nil, err
	}
	for i := range list {
		v := &list[i]
		v.DNSConnections = dns(deref(v.BrandID))
		v.AIPrompt, v.Developer = emailDomainHelp(v.Domain, v.Records)
	}
	return list, nil
}

// emailDomainHelp is the prompt for an AI agent and the message for a
// developer that add a sending domain's records still missing.
func emailDomainHelp(domain string, records []connectors.DomainRecord) (string, setupMessage) {
	var list strings.Builder
	n := 0
	for _, r := range records {
		if r.Status == "verified" {
			continue
		}
		n++
		fmt.Fprintf(&list, "%d. Type: %s\n   Name (host): %s\n", n, r.Type, r.Name)
		if r.Type == "MX" {
			fmt.Fprintf(&list, "   Priority: %d\n", max(r.Priority, 10))
		}
		fmt.Fprintf(&list, "   Value: %s\n", r.Value)
		if r.Purpose == "dmarc" {
			list.WriteString("   Only if the domain has no DMARC record yet. If it has one, keep it as it is.\n")
		}
	}
	if n == 0 {
		done := "All of " + domain + "'s DNS records are in place."
		return done, setupMessage{Subject: domain + "'s DNS records are in place", Body: done}
	}
	prompt := "Add these DNS records for " + domain + ", so my email provider can send email from it.\n\n" + list.String() + "\n" +
		"Rules:\n" +
		"- Add each record exactly as written. The names are full names: if the DNS dashboard adds " + domain + " to the name by itself, enter only the part before " + domain + ".\n" +
		"- CNAME records must not be proxied (in Cloudflare, DNS only, grey cloud).\n" +
		"- Never change or delete records that are already there. A name may hold only one SPF and one DMARC record: if one is already there, merge into it instead of adding a second.\n" +
		"- If you can't reach the DNS host, tell me which one it is and what to click instead.\n" +
		"- Don't ask me to paste passwords or API keys into this chat; if a sign-in is needed, stop and let me do it.\n" +
		"When you're done, tell me, and I'll press Check now in Outflare."
	msg := setupMessage{
		Subject: "Please add DNS records for " + domain,
		Body: "Hi,\n\nCould you add these DNS records to " + domain + "? They let our email provider send from the domain.\n\n" + list.String() + "\n" +
			"CNAME records should be DNS only (not proxied). They go alongside the records already there; if " + domain + " already has an SPF or DMARC record on the same name, merge into it rather than adding a second one. Outflare checks for them on its own and ticks them off once they're visible.\n\nThanks!",
	}
	return prompt, msg
}

// addEmailDomainDNS adds a sending domain's records with the workspace's
// own DNS host connection, in a job.
func (s *Server) addEmailDomainDNS(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.findEmailDomain(w, r, a.WorkspaceID, r.PathValue("id"))
	if !ok {
		return
	}
	var req struct {
		IntegrationID string `json:"integration_id"`
	}
	if !decode(w, r, &req) {
		return
	}
	ctx := r.Context()
	if s.allocError(w, r, s.setupConnection(ctx, a.WorkspaceID, deref(cur.BrandID), req.IntegrationID, connectors.CapDNSWrite)) {
		return
	}
	if len(cur.Records) == 0 {
		writeError(w, r, http.StatusConflict, "domain_not_ready", "the provider hasn't sent this domain's DNS records yet; try again in a few seconds")
		return
	}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		return s.emails.InstallDNS(ctx, tx, s.emailWho(ctx, a), a.WorkspaceID, cur.ID, req.IntegrationID)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, http.StatusNotFound, "not_found", "sending domain not found")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"queued": true})
}

func (s *Server) listEmailDomains(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var provider *string
	if p := r.URL.Query().Get("provider_id"); p != "" {
		if !validUUID(p) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "provider_id must be an email provider id")
			return
		}
		provider = &p
	}
	list, err := s.queryEmailDomains(r.Context(), a.WorkspaceID, nil, provider)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if list == nil {
		list = []emailDomainView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

func (s *Server) findEmailDomain(w http.ResponseWriter, r *http.Request, ws, id string) (*emailDomainView, bool) {
	if validUUID(id) {
		list, err := s.queryEmailDomains(r.Context(), ws, &id, nil)
		if err != nil {
			s.internal(w, r, err)
			return nil, false
		}
		if len(list) == 1 {
			return &list[0], true
		}
	}
	writeError(w, r, http.StatusNotFound, "not_found", "sending domain not found")
	return nil, false
}

func (s *Server) getEmailProviderDomain(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	if v, ok := s.findEmailDomain(w, r, a.WorkspaceID, r.PathValue("id")); ok {
		writeJSON(w, http.StatusOK, v)
	}
}

// defaultEmailProvider is the workspace's default email provider, or "".
func defaultEmailProvider(ctx context.Context, q jobs.Querier, ws string) (string, error) {
	var id string
	err := q.QueryRow(ctx, `SELECT id FROM email_providers WHERE workspace_id = $1 AND channel = 'email' ORDER BY is_default DESC, created_at LIMIT 1`, ws).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// bareDomain takes what people paste (https://Acme-com.example/, billing@acme.example)
// down to the domain.
func bareDomain(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	if i := strings.Index(v, "://"); i >= 0 {
		v = v[i+3:]
	}
	if i := strings.LastIndex(v, "@"); i >= 0 {
		v = v[i+1:]
	}
	if i := strings.IndexAny(v, "/?#:"); i >= 0 {
		v = v[:i]
	}
	return strings.TrimSuffix(v, ".")
}

func (s *Server) addEmailDomain(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		ProviderID *string `json:"provider_id"`
		Domain     string  `json:"domain"`
		BrandID    *string `json:"brand_id"`
	}
	if !decode(w, r, &req) {
		return
	}
	req.Domain = bareDomain(req.Domain)
	switch {
	case !emailDomainRE.MatchString(req.Domain) || len(req.Domain) > 253:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "domain must be a domain name such as example.com or mail.example.com, without https://")
		return
	case req.ProviderID != nil && !validUUID(*req.ProviderID):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "provider_id must be an email provider id")
		return
	case req.BrandID != nil && !validUUID(*req.BrandID):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "brand_id must be a brand id")
		return
	}
	ctx := r.Context()
	var id string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		provider := ""
		if req.ProviderID != nil {
			provider = *req.ProviderID
		} else {
			var err error
			if provider, err = defaultEmailProvider(ctx, tx, a.WorkspaceID); err != nil {
				return err
			}
			if provider == "" {
				return errNoEmailProvider
			}
		}
		var err error
		id, err = s.emails.AddDomain(ctx, tx, s.emailWho(ctx, a), a.WorkspaceID, provider, req.Domain, req.BrandID)
		return err
	})
	switch {
	case errors.Is(err, errNoEmailProvider):
		writeError(w, r, http.StatusConflict, "no_email_provider", "connect an email provider (Amazon SES, Postmark or Resend) before adding a sending domain")
		return
	case errors.Is(err, pgx.ErrNoRows):
		writeError(w, r, http.StatusNotFound, "not_found", "email provider not found")
		return
	case errors.Is(err, emails.ErrDomainTaken):
		writeError(w, r, http.StatusConflict, "conflict", req.Domain+" is already added to this provider")
		return
	case err != nil:
		s.internal(w, r, err)
		return
	}
	if v, ok := s.findEmailDomain(w, r, a.WorkspaceID, id); ok {
		writeJSON(w, http.StatusCreated, v)
	}
}

var errNoEmailProvider = errors.New("no email provider")

func (s *Server) deleteEmailDomain(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.findEmailDomain(w, r, a.WorkspaceID, r.PathValue("id"))
	if !ok {
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM email_domains WHERE workspace_id = $1 AND id = $2`, a.WorkspaceID, cur.ID); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "email_domain.removed", "email_domain", cur.ID, map[string]any{"provider_id": cur.ProviderID})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) checkEmailDomain(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.findEmailDomain(w, r, a.WorkspaceID, r.PathValue("id"))
	if !ok {
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := s.emails.CheckDomain(ctx, tx, a.WorkspaceID, cur.ID); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "email_domain.check_requested", "email_domain", cur.ID, nil)
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"queued": true})
}

// ---- Templates ----

var templateHandleRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

type emailTemplateView struct {
	ID                string            `json:"id"`
	Handle            string            `json:"handle"`
	Name              string            `json:"name"`
	BrandID           *string           `json:"brand_id"`
	Channel           string            `json:"channel"`
	Purpose           string            `json:"purpose"`
	Status            string            `json:"status"`
	From              string            `json:"from"`
	ReplyTo           string            `json:"reply_to"`
	Subject           string            `json:"subject"`
	HTML              string            `json:"html"`
	Text              string            `json:"text"`
	Variables         []emails.Variable `json:"variables"`
	TrackOpens        bool              `json:"track_opens"`
	TrackClicks       bool              `json:"track_clicks"`
	Topic             *topicRef         `json:"topic"`
	PublishedVersion  *int              `json:"published_version"`
	UnpublishedChange bool              `json:"unpublished_changes"`
	Sent30d           int               `json:"sent_30d"`
	CreatedAt         time.Time         `json:"created_at"`
	UpdatedAt         time.Time         `json:"updated_at"`
}

const emailTemplateSelect = `
	SELECT t.id, t.handle, t.name, t.brand_id, t.channel, t.purpose, t.status, t.from_address, t.reply_to, t.subject, t.html, t.text_body,
	       t.variables, t.track_opens, t.track_clicks, tp.id, tp.key, tp.name, t.published_version,
	       v.version IS NULL OR v.subject <> t.subject OR v.html <> t.html OR v.text_body <> t.text_body OR v.variables <> t.variables
	         OR v.from_address <> t.from_address OR v.reply_to <> t.reply_to,
	       (SELECT count(*) FROM emails e WHERE e.template_id = t.id AND e.created_at > now() - interval '30 days' AND NOT e.test)::int,
	       t.created_at, t.updated_at
	FROM email_templates t
	LEFT JOIN email_template_versions v ON v.template_id = t.id AND v.version = t.published_version
	LEFT JOIN email_topics tp ON tp.id = t.topic_id
	WHERE t.workspace_id = $1`

func scanEmailTemplate(row pgx.Row) (emailTemplateView, error) {
	var v emailTemplateView
	var vars []byte
	var topic nullTopic
	err := row.Scan(&v.ID, &v.Handle, &v.Name, &v.BrandID, &v.Channel, &v.Purpose, &v.Status, &v.From, &v.ReplyTo, &v.Subject, &v.HTML, &v.Text,
		&vars, &v.TrackOpens, &v.TrackClicks, &topic.ID, &topic.Key, &topic.Name, &v.PublishedVersion, &v.UnpublishedChange, &v.Sent30d, &v.CreatedAt, &v.UpdatedAt)
	if err != nil {
		return v, err
	}
	v.Topic = topic.ref()
	v.Variables = []emails.Variable{}
	return v, json.Unmarshal(vars, &v.Variables)
}

func (s *Server) listEmailTemplates(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	status := r.URL.Query().Get("status")
	if status != "" && status != "active" && status != "archived" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status is active or archived")
		return
	}
	sel, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	args := []any{a.WorkspaceID, status}
	rows, err := s.pool.Query(r.Context(), emailTemplateSelect+` AND ($2 = '' OR t.status = $2)`+sel.sql("t.brand_id", &args)+` ORDER BY t.name, t.handle`, args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (emailTemplateView, error) { return scanEmailTemplate(row) })
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if list == nil {
		list = []emailTemplateView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

// findEmailTemplate finds a template by id or handle.
func (s *Server) findEmailTemplate(w http.ResponseWriter, r *http.Request, ws, ref string) (*emailTemplateView, bool) {
	v, err := scanEmailTemplate(s.pool.QueryRow(r.Context(), emailTemplateSelect+` AND (t.id::text = $2 OR t.handle = $2)`, ws, ref))
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, http.StatusNotFound, "not_found", "email template not found")
		return nil, false
	}
	if err != nil {
		s.internal(w, r, err)
		return nil, false
	}
	return &v, true
}

func (s *Server) getEmailTemplate(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	if v, ok := s.findEmailTemplate(w, r, a.WorkspaceID, r.PathValue("id")); ok {
		writeJSON(w, http.StatusOK, v)
	}
}

type templateFields struct {
	Handle    *string            `json:"handle"`
	Name      *string            `json:"name"`
	BrandID   optional[string]   `json:"brand_id"`
	Purpose   *string            `json:"purpose"`
	Status    *string            `json:"status"`
	From      *string            `json:"from"`
	ReplyTo   *string            `json:"reply_to"`
	Subject   *string            `json:"subject"`
	HTML      *string            `json:"html"`
	Text      *string            `json:"text"`
	Variables *[]emails.Variable `json:"variables"`
	// Tracking is a setting, not content: it changes at once, without a
	// publish.
	TrackOpens  *bool `json:"track_opens"`
	TrackClicks *bool `json:"track_clicks"`
	// Topic is a setting too: the topic's id or key, or null for none.
	Topic   optional[string] `json:"topic"`
	Publish bool             `json:"publish"`
	Note    string           `json:"note"`
}

// validate checks the fields given; it returns a message for a 400.
func (f *templateFields) validate() string {
	switch {
	case f.Handle != nil && !templateHandleRE.MatchString(*f.Handle):
		return "handle must be 1 to 63 lowercase letters, digits, dashes or underscores, such as password-reset"
	case f.Name != nil && (strings.TrimSpace(*f.Name) == "" || len(*f.Name) > 200):
		return "name must be 1 to 200 characters"
	case f.BrandID.Value != nil && !validUUID(*f.BrandID.Value):
		return "brand_id must be a brand id, or null"
	case f.Purpose != nil && !slices.Contains(emailPurposes, *f.Purpose):
		return "purpose must be transactional or lifecycle"
	case f.Status != nil && *f.Status != "active" && *f.Status != "archived":
		return "status must be active or archived"
	case f.From != nil && *f.From != "" && !validFrom(*f.From):
		return "from must be an address such as billing@example.com or Example <billing@example.com>"
	case f.ReplyTo != nil && *f.ReplyTo != "" && !validFrom(*f.ReplyTo):
		return "reply_to must be an email address"
	case f.Subject != nil && len(*f.Subject) > 998:
		return "subject must be at most 998 characters"
	case f.HTML != nil && len(*f.HTML) > 512<<10:
		return "html must be at most 512 KB"
	case f.Text != nil && len(*f.Text) > 256<<10:
		return "text must be at most 256 KB"
	case f.Variables != nil && len(*f.Variables) > 100:
		return "variables must list at most 100 variables"
	case len(f.Note) > 500:
		return "note must be at most 500 characters"
	}
	if f.Variables != nil {
		seen := map[string]bool{}
		for _, v := range *f.Variables {
			if !variableNameRE.MatchString(v.Name) || seen[v.Name] {
				return "variables must each have a unique name of letters, digits and underscores, such as first_name"
			}
			seen[v.Name] = true
		}
	}
	if f.Subject != nil || f.HTML != nil || f.Text != nil {
		if err := emails.Check(deref(f.Subject), deref(f.HTML), deref(f.Text)); err != nil {
			return "the template has a mistake in " + err.Error()
		}
	}
	return ""
}

var variableNameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)

func validFrom(s string) bool {
	a, err := mail.ParseAddress(s)
	return err == nil && len(s) <= 320 && strings.Contains(a.Address, "@")
}

// declare adds a variable entry for every variable the content uses that
// is not listed, so a template's variables are always complete.
func declare(vars []emails.Variable, subject, html, text string) []emails.Variable {
	have := map[string]bool{}
	for _, v := range vars {
		have[v.Name] = true
	}
	for _, name := range emails.Used(subject, html, text) {
		root, _, _ := strings.Cut(name, ".")
		if !have[root] {
			vars = append(vars, emails.Variable{Name: root})
			have[root] = true
		}
	}
	return vars
}

func (s *Server) createEmailTemplate(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var f templateFields
	if !decode(w, r, &f) {
		return
	}
	if f.Name == nil || strings.TrimSpace(*f.Name) == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "name is required")
		return
	}
	if f.Handle == nil {
		h := strings.ReplaceAll(slugFrom(*f.Name), "_", "-")
		if len(h) > 63 {
			h = strings.Trim(h[:63], "-")
		}
		if h == "" {
			h = "template"
		}
		f.Handle = &h
	}
	if msg := f.validate(); msg != "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", msg)
		return
	}
	purpose := "transactional"
	if f.Purpose != nil {
		purpose = *f.Purpose
	}
	if f.Topic.Value != nil && *f.Topic.Value != "" && purpose != "lifecycle" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "only lifecycle templates have a topic: transactional email goes to everyone who asked for it")
		return
	}
	vars := []emails.Variable{}
	if f.Variables != nil {
		vars = *f.Variables
	}
	vars = declare(vars, deref(f.Subject), deref(f.HTML), deref(f.Text))
	varsJSON, _ := json.Marshal(vars)
	ctx := r.Context()
	var id string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		topic, err := topicID(ctx, tx, a.WorkspaceID, deref(f.Topic.Value))
		if err != nil {
			return err
		}
		err = tx.QueryRow(ctx, `
			INSERT INTO email_templates (workspace_id, brand_id, handle, name, purpose, from_address, reply_to, subject, html, text_body, variables,
				track_opens, track_clicks, topic_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, COALESCE($12, false), COALESCE($13, false), $14)
			ON CONFLICT (workspace_id, handle) DO NOTHING RETURNING id`,
			a.WorkspaceID, f.BrandID.Value, *f.Handle, strings.TrimSpace(*f.Name), purpose, deref(f.From), deref(f.ReplyTo),
			deref(f.Subject), deref(f.HTML), deref(f.Text), varsJSON, f.TrackOpens, f.TrackClicks, topic).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return errConflict
		}
		if err != nil {
			return err
		}
		if err := record(ctx, tx, &a.principal, a.WorkspaceID, "email_template.created", "email_template", id,
			map[string]any{"purpose": purpose, "track_opens": boolOr(f.TrackOpens, false), "track_clicks": boolOr(f.TrackClicks, false), "topic_id": topic}); err != nil {
			return err
		}
		if f.Publish {
			_, err = s.publishTemplate(ctx, tx, a, id, f.Note)
		}
		return err
	})
	var inv errInvalid
	switch {
	case errors.As(err, &inv):
		writeError(w, r, http.StatusBadRequest, "invalid_request", string(inv))
		return
	case errors.Is(err, errConflict):
		writeError(w, r, http.StatusConflict, "conflict", "another template already has the handle "+*f.Handle)
		return
	case errors.Is(err, errEmptyTemplate):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "a template needs a subject and an HTML or text body before it can be published")
		return
	case err != nil:
		s.internal(w, r, err)
		return
	}
	if v, ok := s.findEmailTemplate(w, r, a.WorkspaceID, id); ok {
		writeJSON(w, http.StatusCreated, v)
	}
}

func (s *Server) updateEmailTemplate(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.findEmailTemplate(w, r, a.WorkspaceID, r.PathValue("id"))
	if !ok {
		return
	}
	var f templateFields
	if !decode(w, r, &f) {
		return
	}
	// Check the content as it will be, not only the parts sent.
	merged := f
	if merged.Subject == nil {
		merged.Subject = &cur.Subject
	}
	if merged.HTML == nil {
		merged.HTML = &cur.HTML
	}
	if merged.Text == nil {
		merged.Text = &cur.Text
	}
	if msg := merged.validate(); msg != "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", msg)
		return
	}
	purpose := cur.Purpose
	if f.Purpose != nil {
		purpose = *f.Purpose
	}
	hasTopic := cur.Topic != nil
	if f.Topic.Set {
		hasTopic = f.Topic.Value != nil && *f.Topic.Value != ""
	}
	if hasTopic && purpose != "lifecycle" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "only lifecycle templates have a topic: transactional email goes to everyone who asked for it")
		return
	}
	vars := cur.Variables
	if f.Variables != nil {
		vars = *f.Variables
	}
	vars = declare(vars, *merged.Subject, *merged.HTML, *merged.Text)
	varsJSON, _ := json.Marshal(vars)
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		topic, err := topicID(ctx, tx, a.WorkspaceID, deref(f.Topic.Value))
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `
			UPDATE email_templates SET handle = COALESCE($3, handle), name = COALESCE($4, name),
				brand_id = CASE WHEN $5 THEN $6 ELSE brand_id END, purpose = COALESCE($7, purpose), status = COALESCE($8, status),
				from_address = COALESCE($9, from_address), reply_to = COALESCE($10, reply_to),
				subject = $11, html = $12, text_body = $13, variables = $14,
				track_opens = COALESCE($15, track_opens), track_clicks = COALESCE($16, track_clicks),
				topic_id = CASE WHEN $17 THEN $18 ELSE topic_id END, updated_at = now()
			WHERE workspace_id = $1 AND id = $2
			  AND NOT EXISTS (SELECT 1 FROM email_templates o WHERE o.workspace_id = $1 AND o.handle = $3 AND o.id <> $2)`,
			a.WorkspaceID, cur.ID, f.Handle, f.Name, f.BrandID.Set, f.BrandID.Value, f.Purpose, f.Status, f.From, f.ReplyTo,
			*merged.Subject, *merged.HTML, *merged.Text, varsJSON, f.TrackOpens, f.TrackClicks, f.Topic.Set, topic)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errConflict
		}
		action := "email_template.updated"
		if f.Status != nil && *f.Status != cur.Status {
			action = map[string]string{"archived": "email_template.archived", "active": "email_template.restored"}[*f.Status]
		}
		var data map[string]any
		if f.TrackOpens != nil || f.TrackClicks != nil {
			data = map[string]any{"track_opens": boolOr(f.TrackOpens, cur.TrackOpens), "track_clicks": boolOr(f.TrackClicks, cur.TrackClicks)}
		}
		if f.Topic.Set {
			if data == nil {
				data = map[string]any{}
			}
			data["topic_id"] = topic
		}
		if err := record(ctx, tx, &a.principal, a.WorkspaceID, action, "email_template", cur.ID, data); err != nil {
			return err
		}
		if f.Publish {
			_, err = s.publishTemplate(ctx, tx, a, cur.ID, f.Note)
		}
		return err
	})
	var inv errInvalid
	switch {
	case errors.As(err, &inv):
		writeError(w, r, http.StatusBadRequest, "invalid_request", string(inv))
		return
	case errors.Is(err, errConflict):
		writeError(w, r, http.StatusConflict, "conflict", "another template already has the handle "+deref(f.Handle))
		return
	case errors.Is(err, errEmptyTemplate):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "a template needs a subject and an HTML or text body before it can be published")
		return
	case err != nil:
		s.internal(w, r, err)
		return
	}
	if v, ok := s.findEmailTemplate(w, r, a.WorkspaceID, cur.ID); ok {
		writeJSON(w, http.StatusOK, v)
	}
}

var errEmptyTemplate = errors.New("empty template")

// publishTemplate saves the draft as the next version and makes it the one
// sends use. It returns the version.
func (s *Server) publishTemplate(ctx context.Context, tx pgx.Tx, a *access, id, note string) (int, error) {
	var version int
	err := tx.QueryRow(ctx, `
		INSERT INTO email_template_versions (template_id, workspace_id, version, from_address, reply_to, subject, html, text_body, variables, note, published_by)
		SELECT t.id, t.workspace_id, COALESCE((SELECT max(version) FROM email_template_versions v WHERE v.template_id = t.id), 0) + 1,
		       t.from_address, t.reply_to, t.subject, t.html, t.text_body, t.variables, $3, $4
		FROM email_templates t WHERE t.workspace_id = $1 AND t.id = $2 AND t.subject <> '' AND (t.html <> '' OR t.text_body <> '')
		RETURNING version`, a.WorkspaceID, id, note, a.ActorID).Scan(&version)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, errEmptyTemplate
	}
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `UPDATE email_templates SET published_version = $3, updated_at = now() WHERE workspace_id = $1 AND id = $2`, a.WorkspaceID, id, version); err != nil {
		return 0, err
	}
	return version, record(ctx, tx, &a.principal, a.WorkspaceID, "email_template.published", "email_template", id, map[string]any{"version": version})
}

func (s *Server) publishEmailTemplate(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.findEmailTemplate(w, r, a.WorkspaceID, r.PathValue("id"))
	if !ok {
		return
	}
	var req struct {
		Note string `json:"note"`
	}
	if r.ContentLength != 0 && !decode(w, r, &req) {
		return
	}
	if len(req.Note) > 500 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "note must be at most 500 characters")
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := s.publishTemplate(ctx, tx, a, cur.ID, req.Note)
		return err
	})
	switch {
	case errors.Is(err, errEmptyTemplate):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "a template needs a subject and an HTML or text body before it can be published")
		return
	case err != nil:
		s.internal(w, r, err)
		return
	}
	if v, ok := s.findEmailTemplate(w, r, a.WorkspaceID, cur.ID); ok {
		writeJSON(w, http.StatusOK, v)
	}
}

type emailTemplateVersionView struct {
	Version     int               `json:"version"`
	From        string            `json:"from"`
	ReplyTo     string            `json:"reply_to"`
	Subject     string            `json:"subject"`
	HTML        string            `json:"html"`
	Text        string            `json:"text"`
	Variables   []emails.Variable `json:"variables"`
	Note        string            `json:"note"`
	PublishedBy *string           `json:"published_by"`
	PublishedAt time.Time         `json:"published_at"`
}

func (s *Server) listEmailTemplateVersions(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.findEmailTemplate(w, r, a.WorkspaceID, r.PathValue("id"))
	if !ok {
		return
	}
	rows, err := s.pool.Query(r.Context(), `
		SELECT v.version, v.from_address, v.reply_to, v.subject, v.html, v.text_body, v.variables, v.note, ac.display_name, v.published_at
		FROM email_template_versions v LEFT JOIN actors ac ON ac.id = v.published_by
		WHERE v.workspace_id = $1 AND v.template_id = $2 ORDER BY v.version DESC LIMIT 100`, a.WorkspaceID, cur.ID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (emailTemplateVersionView, error) {
		var v emailTemplateVersionView
		var vars []byte
		if err := row.Scan(&v.Version, &v.From, &v.ReplyTo, &v.Subject, &v.HTML, &v.Text, &vars, &v.Note, &v.PublishedBy, &v.PublishedAt); err != nil {
			return v, err
		}
		v.Variables = []emails.Variable{}
		return v, json.Unmarshal(vars, &v.Variables)
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if list == nil {
		list = []emailTemplateVersionView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

func (s *Server) deleteEmailTemplate(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.findEmailTemplate(w, r, a.WorkspaceID, r.PathValue("id"))
	if !ok {
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM email_templates WHERE workspace_id = $1 AND id = $2`, a.WorkspaceID, cur.ID); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "email_template.deleted", "email_template", cur.ID, nil)
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// previewEmailTemplate fills content with sample data, without saving or
// sending anything: what the editor shows as you type.
func (s *Server) previewEmailTemplate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Subject   string            `json:"subject"`
		HTML      string            `json:"html"`
		Text      string            `json:"text"`
		Variables []emails.Variable `json:"variables"`
		Data      map[string]any    `json:"data"`
	}
	if !decodeLarge(w, r, &req, 2<<20) {
		return
	}
	data := req.Data
	if data == nil {
		// No data: show each variable's example, so the preview reads well.
		data = map[string]any{}
		for _, v := range req.Variables {
			if v.Example != "" {
				data[v.Name] = v.Example
			}
		}
	}
	data["unsubscribe_url"] = s.publicURL + "/u/preview"
	data["email_id"] = "00000000-0000-4000-8000-000000000000"
	out, err := emails.Render(req.Subject, req.HTML, req.Text, req.Variables, data)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "the template has a mistake in "+err.Error())
		return
	}
	if out.Text == "" && out.HTML != "" {
		out.Text = emails.PlainText(out.HTML)
	}
	writeJSON(w, http.StatusOK, out)
}

// ---- Emails ----

// stringList takes one address or a list of them.
type stringList []string

func (l *stringList) UnmarshalJSON(b []byte) error {
	var one string
	if err := json.Unmarshal(b, &one); err == nil {
		*l = []string{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return errors.New("must be an email address or a list of them")
	}
	*l = many
	return nil
}

type emailView struct {
	ID                string         `json:"id"`
	ProviderID        *string        `json:"provider_id"`
	Connector         *string        `json:"connector"`
	BrandID           *string        `json:"brand_id"`
	Channel           string         `json:"channel"`
	Purpose           string         `json:"purpose"`
	Status            string         `json:"status"`
	StatusReason      *string        `json:"status_reason"`
	BounceType        *string        `json:"bounce_type"`
	From              string         `json:"from"`
	To                []string       `json:"to"`
	Cc                []string       `json:"cc"`
	Bcc               []string       `json:"bcc"`
	ReplyTo           []string       `json:"reply_to"`
	Subject           string         `json:"subject"`
	TemplateID        *string        `json:"template_id"`
	TemplateHandle    *string        `json:"template_handle"`
	TemplateVersion   *int           `json:"template_version"`
	Topic             *topicRef      `json:"topic"`
	Tags              []string       `json:"tags"`
	Metadata          map[string]any `json:"metadata"`
	Test              bool           `json:"test"`
	ProviderMessageID *string        `json:"provider_message_id"`
	SendAt            *time.Time     `json:"send_at"`
	SentAt            *time.Time     `json:"sent_at"`
	DeliveredAt       *time.Time     `json:"delivered_at"`
	Attempts          int            `json:"attempts"`
	Trigger           emailTrigger   `json:"trigger"`
	TrackOpens        bool           `json:"track_opens"`
	TrackClicks       bool           `json:"track_clicks"`
	Opens             int            `json:"opens"`
	Clicks            int            `json:"clicks"`
	OpenedAt          *time.Time     `json:"opened_at"`
	ClickedAt         *time.Time     `json:"clicked_at"`
	CreatedAt         time.Time      `json:"created_at"`
	UpdatedAt         time.Time      `json:"updated_at"`
	// Only on GET .../emails/{id}.
	HTML   *string           `json:"html,omitempty"`
	Text   *string           `json:"text,omitempty"`
	Events *[]emailEventView `json:"events,omitempty"`
}

// emailTrigger says what sent an email: an API call (from the web app,
// the API, MCP, the CLI or a mission), a test send, and in time an event,
// a journey or a broadcast.
type emailTrigger struct {
	Kind      string  `json:"kind"`
	Ref       *string `json:"ref"`
	RequestID *string `json:"request_id"`
	Surface   *string `json:"surface"`
	TokenID   *string `json:"token_id"`
	TokenName *string `json:"token_name"`
	MissionID *string `json:"mission_id"`
	ActorID   *string `json:"actor_id"`
	ActorName *string `json:"actor_name"`
}

type emailEventView struct {
	Type       string    `json:"type"`
	Recipient  *string   `json:"recipient"`
	Detail     *string   `json:"detail"`
	OccurredAt time.Time `json:"occurred_at"`
}

const emailSelect = `
	SELECT e.id, e.provider_id, p.connector, e.brand_id, e.channel, e.purpose, e.status, e.status_reason, e.bounce_type,
	       e.from_address, e.to_addresses, e.cc, e.bcc, e.reply_to, e.subject, e.template_id, t.handle, e.template_version,
	       e.tags, e.metadata, e.test, e.provider_message_id, e.send_at, e.sent_at, e.delivered_at, e.attempts,
	       e.trigger_kind, e.trigger_ref, e.request_id, e.surface, e.token_id, tk.name, e.mission_id, e.created_by, ac.display_name,
	       e.track_opens, e.track_clicks, e.opens, e.clicks, e.opened_at, e.clicked_at, e.created_at, e.updated_at,
	       tp.id, tp.key, tp.name
	FROM emails e
	LEFT JOIN email_providers p ON p.id = e.provider_id
	LEFT JOIN email_templates t ON t.id = e.template_id
	LEFT JOIN api_tokens tk ON tk.id = e.token_id AND tk.workspace_id = e.workspace_id
	LEFT JOIN actors ac ON ac.id = e.created_by
	LEFT JOIN email_topics tp ON tp.id = e.topic_id
	WHERE e.workspace_id = $1`

func scanEmail(row pgx.Row) (emailView, error) {
	var v emailView
	var meta []byte
	var topic nullTopic
	err := row.Scan(&v.ID, &v.ProviderID, &v.Connector, &v.BrandID, &v.Channel, &v.Purpose, &v.Status, &v.StatusReason, &v.BounceType,
		&v.From, &v.To, &v.Cc, &v.Bcc, &v.ReplyTo, &v.Subject, &v.TemplateID, &v.TemplateHandle, &v.TemplateVersion,
		&v.Tags, &meta, &v.Test, &v.ProviderMessageID, &v.SendAt, &v.SentAt, &v.DeliveredAt, &v.Attempts,
		&v.Trigger.Kind, &v.Trigger.Ref, &v.Trigger.RequestID, &v.Trigger.Surface, &v.Trigger.TokenID, &v.Trigger.TokenName, &v.Trigger.MissionID,
		&v.Trigger.ActorID, &v.Trigger.ActorName,
		&v.TrackOpens, &v.TrackClicks, &v.Opens, &v.Clicks, &v.OpenedAt, &v.ClickedAt, &v.CreatedAt, &v.UpdatedAt,
		&topic.ID, &topic.Key, &topic.Name)
	if err != nil {
		return v, err
	}
	v.Topic = topic.ref()
	v.Metadata = map[string]any{}
	return v, json.Unmarshal(meta, &v.Metadata)
}

var emailTriggers = []string{"api", "test", "event", "journey", "broadcast", "system"}

var emailStatuses = []string{"queued", "scheduled", "sending", "sent", "delivered", "delayed", "bounced", "complained", "suppressed", "failed", "cancelled"}

// listEmails pages emails newest first: ?before=<email id>&limit=<n>,
// filtered by status, purpose, template, topic, provider, recipient and test.
func (s *Server) listEmails(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	q := r.URL.Query()
	limit := 50
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 200 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "limit must be 1 to 200")
			return
		}
		limit = n
	}
	var before, template, provider *string
	for _, p := range []struct {
		name string
		dst  **string
	}{{"before", &before}, {"provider_id", &provider}} {
		if v := q.Get(p.name); v != "" {
			if !validUUID(v) {
				writeError(w, r, http.StatusBadRequest, "invalid_request", p.name+" must be an id")
				return
			}
			*p.dst = &v
		}
	}
	if v := q.Get("template"); v != "" {
		template = &v
	}
	status, purpose, to := q.Get("status"), q.Get("purpose"), strings.ToLower(strings.TrimSpace(q.Get("to")))
	var test *bool
	switch q.Get("test") {
	case "":
	case "true", "false":
		t := q.Get("test") == "true"
		test = &t
	default:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "test must be true or false")
		return
	}
	trigger := q.Get("trigger")
	switch {
	case trigger != "" && !slices.Contains(emailTriggers, trigger):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "trigger is one of "+strings.Join(emailTriggers, ", "))
		return
	case status != "" && !slices.Contains(emailStatuses, status):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status is one of "+strings.Join(emailStatuses, ", "))
		return
	case purpose != "" && !slices.Contains(emailPurposes, purpose):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "purpose is transactional or lifecycle")
		return
	}
	sel, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	args := []any{a.WorkspaceID, before, status, purpose, template, provider, to, test, limit, trigger, q.Get("request_id"), q.Get("topic"), q.Get("broadcast")}
	rows, err := s.pool.Query(r.Context(), emailSelect+`
		AND ($2::uuid IS NULL OR (e.created_at, e.id) < (SELECT created_at, id FROM emails WHERE workspace_id = $1 AND id = $2))
		AND ($3 = '' OR e.status = $3) AND ($4 = '' OR e.purpose = $4)
		AND ($5::text IS NULL OR e.template_id::text = $5 OR t.handle = $5)
		AND ($6::uuid IS NULL OR e.provider_id = $6)
		AND ($7 = '' OR e.recipients @> ARRAY[$7])
		AND ($8::boolean IS NULL OR e.test = $8)
		AND ($10 = '' OR e.trigger_kind = $10) AND ($11 = '' OR e.request_id = $11)
		AND ($12 = '' OR tp.id::text = $12 OR tp.key = $12)
		AND ($13 = '' OR e.broadcast_id::text = $13)`+sel.sql("e.brand_id", &args)+`
		ORDER BY e.created_at DESC, e.id DESC LIMIT $9`, args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (emailView, error) { return scanEmail(row) })
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if list == nil {
		list = []emailView{}
	}
	var next *string
	if len(list) == limit {
		next = &list[len(list)-1].ID
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "next_before": next})
}

func (s *Server) findEmail(w http.ResponseWriter, r *http.Request, ws, id string, full bool) (*emailView, bool) {
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "email not found")
		return nil, false
	}
	ctx := r.Context()
	v, err := scanEmail(s.pool.QueryRow(ctx, emailSelect+` AND e.id = $2`, ws, id))
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, http.StatusNotFound, "not_found", "email not found")
		return nil, false
	}
	if err != nil {
		s.internal(w, r, err)
		return nil, false
	}
	if !full {
		return &v, true
	}
	var html, text string
	if err := s.pool.QueryRow(ctx, `SELECT html, text_body FROM emails WHERE id = $1`, id).Scan(&html, &text); err != nil {
		s.internal(w, r, err)
		return nil, false
	}
	if v.TrackOpens || v.TrackClicks {
		var err error
		if html, text, err = emails.Untracked(ctx, s.pool, ws, id, html, text); err != nil {
			s.internal(w, r, err)
			return nil, false
		}
	}
	rows, err := s.pool.Query(ctx, `SELECT type, recipient, detail, occurred_at FROM email_events WHERE workspace_id = $1 AND email_id = $2 ORDER BY occurred_at, id`, ws, id)
	if err != nil {
		s.internal(w, r, err)
		return nil, false
	}
	events, err := pgx.CollectRows(rows, pgx.RowToStructByPos[emailEventView])
	if err != nil {
		s.internal(w, r, err)
		return nil, false
	}
	if events == nil {
		events = []emailEventView{}
	}
	v.HTML, v.Text, v.Events = &html, &text, &events
	return &v, true
}

func (s *Server) getEmail(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	if v, ok := s.findEmail(w, r, a.WorkspaceID, r.PathValue("id"), true); ok {
		writeJSON(w, http.StatusOK, v)
	}
}

func (s *Server) cancelEmail(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.findEmail(w, r, a.WorkspaceID, r.PathValue("id"), false)
	if !ok {
		return
	}
	ctx := r.Context()
	var done bool
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		done, err = s.emails.Cancel(ctx, tx, s.emailWho(ctx, a), a.WorkspaceID, cur.ID)
		return err
	})
	switch {
	case err != nil:
		s.internal(w, r, err)
		return
	case !done:
		writeError(w, r, http.StatusConflict, "email_not_cancellable", "the email is "+cur.Status+"; only queued or scheduled emails can be cancelled")
		return
	}
	if v, ok := s.findEmail(w, r, a.WorkspaceID, cur.ID, false); ok {
		writeJSON(w, http.StatusOK, v)
	}
}

// sendEmail queues one email, from a template or from content given.
func (s *Server) sendEmail(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		To         stringList     `json:"to"`
		Cc         stringList     `json:"cc"`
		Bcc        stringList     `json:"bcc"`
		ReplyTo    stringList     `json:"reply_to"`
		From       string         `json:"from"`
		Subject    *string        `json:"subject"`
		HTML       *string        `json:"html"`
		Text       *string        `json:"text"`
		Template   string         `json:"template"`
		Version    *int           `json:"version"`
		Draft      bool           `json:"draft"`
		Data       map[string]any `json:"data"`
		Purpose    string         `json:"purpose"`
		ProviderID *string        `json:"provider_id"`
		BrandID    *string        `json:"brand_id"`
		Tags       []string       `json:"tags"`
		Metadata   map[string]any `json:"metadata"`
		SendAt     *time.Time     `json:"send_at"`
		Test       bool           `json:"test"`
		// The topic's id or key; overrides the template's.
		Topic *string `json:"topic"`
		// Overrides the template's tracking settings for this send.
		Track *struct {
			Opens  *bool `json:"opens"`
			Clicks *bool `json:"clicks"`
		} `json:"track"`
	}
	if !decodeLarge(w, r, &req, 2<<20) {
		return
	}
	bad := func(msg string) { writeError(w, r, http.StatusBadRequest, "invalid_request", msg) }
	clean := func(name string, in []string) ([]string, bool) {
		out := []string{}
		seen := map[string]bool{}
		for _, raw := range in {
			addr, ok := validEmail(raw)
			if !ok {
				bad(name + " must be email addresses; " + strconvQuote(raw) + " is not one")
				return nil, false
			}
			if k := strings.ToLower(addr); !seen[k] {
				seen[k] = true
				out = append(out, addr)
			}
		}
		return out, true
	}
	to, ok := clean("to", req.To)
	if !ok {
		return
	}
	cc, ok := clean("cc", req.Cc)
	if !ok {
		return
	}
	bcc, ok := clean("bcc", req.Bcc)
	if !ok {
		return
	}
	replyTo, ok := clean("reply_to", req.ReplyTo)
	if !ok {
		return
	}
	metaJSON, _ := json.Marshal(req.Metadata)
	switch {
	case len(to) == 0:
		bad("to is required: one email address or a list of them")
		return
	case len(to)+len(cc)+len(bcc) > 50:
		bad("to, cc and bcc may have at most 50 addresses together; send one email per person for more")
		return
	case req.Template != "" && (req.Subject != nil || req.HTML != nil || req.Text != nil):
		bad("template cannot be combined with subject, html or text: change the template, or send content without one")
		return
	case req.Template == "" && (req.Subject == nil || strings.TrimSpace(*req.Subject) == ""):
		bad("subject is required, or name a template")
		return
	case req.Template == "" && deref(req.HTML) == "" && deref(req.Text) == "":
		bad("html or text is required, or name a template")
		return
	case req.Template == "" && (req.Version != nil || req.Draft):
		bad("version and draft only apply with a template")
		return
	case req.Version != nil && req.Draft:
		bad("version and draft cannot be combined")
		return
	case req.Purpose != "" && !slices.Contains(emailPurposes, req.Purpose):
		bad("purpose must be transactional or lifecycle")
		return
	case req.ProviderID != nil && !validUUID(*req.ProviderID):
		bad("provider_id must be an email provider id")
		return
	case req.BrandID != nil && !validUUID(*req.BrandID):
		bad("brand_id must be a brand id")
		return
	case len(req.Tags) > 10 || slices.ContainsFunc(req.Tags, func(t string) bool { return t == "" || len(t) > 64 }):
		bad("tags must be at most 10 labels of 1 to 64 characters")
		return
	case len(metaJSON) > 8<<10:
		bad("metadata must be at most 8 KB of JSON")
		return
	case req.SendAt != nil && req.SendAt.After(time.Now().Add(30*24*time.Hour)):
		bad("send_at must be within 30 days")
		return
	case req.Subject != nil && len(*req.Subject) > 998:
		bad("subject must be at most 998 characters")
		return
	case len(deref(req.HTML)) > 512<<10 || len(deref(req.Text)) > 256<<10:
		bad("html must be at most 512 KB and text at most 256 KB")
		return
	}
	ctx := r.Context()

	d := emails.Draft{NewEmail: emails.NewEmail{ID: emails.NewID(), WorkspaceID: a.WorkspaceID, To: to, Cc: cc, Bcc: bcc, ReplyTo: replyTo,
		From: req.From, Purpose: req.Purpose, ProviderID: deref(req.ProviderID), Tags: req.Tags, Metadata: req.Metadata, SendAt: req.SendAt,
		Test: req.Test, BrandID: req.BrandID, TokenID: a.TokenID, Surface: "api"},
		Version: req.Version, UseDraft: req.Draft, Subject: req.Subject, HTML: req.HTML, Text: req.Text, Data: req.Data}
	if i := reqInfoFrom(ctx); i != nil {
		d.Surface = surface(r, i)
	}
	if req.Template != "" {
		t, ok := s.findEmailTemplate(w, r, a.WorkspaceID, req.Template)
		if !ok {
			return
		}
		d.TemplateID = t.ID
	}
	if req.Topic != nil {
		id, err := topicID(ctx, s.pool, a.WorkspaceID, *req.Topic)
		var inv errInvalid
		if errors.As(err, &inv) {
			bad(string(inv))
			return
		}
		if err != nil {
			s.internal(w, r, err)
			return
		}
		d.TopicID, d.TopicSet = id, true
	}
	if req.Track != nil {
		d.TrackOpens, d.TrackClicks = req.Track.Opens, req.Track.Clicks
	}
	e, err := s.emails.Compose(ctx, s.pool, d)
	if !s.composeOK(w, r, err) {
		return
	}

	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := s.emails.Queue(ctx, tx, s.emailWho(ctx, a), e)
		return err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if v, ok := s.findEmail(w, r, a.WorkspaceID, e.ID, false); ok {
		writeJSON(w, http.StatusAccepted, v)
	}
}

// boolOr is *b, or def when b is nil.
func boolOr(b *bool, def bool) bool {
	if b == nil {
		return def
	}
	return *b
}

func strconvQuote(s string) string {
	if len(s) > 80 {
		s = s[:80] + "…"
	}
	return strconv.Quote(s)
}

// ---- One recipient ----

type emailRecipientEvent struct {
	EmailID        string    `json:"email_id"`
	Subject        string    `json:"subject"`
	TemplateHandle *string   `json:"template_handle"`
	Purpose        string    `json:"purpose"`
	Trigger        string    `json:"trigger"`
	Type           string    `json:"type"`
	Detail         *string   `json:"detail"`
	OccurredAt     time.Time `json:"occurred_at"`
}

// getEmailRecipient is everything one address was sent: counts, whether
// it is suppressed, the lead it belongs to, and every email's events,
// newest first (?before=<occurred_at of the last one>&limit=).
func (s *Server) getEmailRecipient(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	addr, ok := validEmail(r.PathValue("address"))
	if !ok {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "address must be an email address")
		return
	}
	addr = strings.ToLower(addr)
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 500 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "limit must be 1 to 500")
			return
		}
		limit = n
	}
	var before *time.Time
	if v := r.URL.Query().Get("before"); v != "" {
		t, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "before must be a time such as 2026-01-02T15:04:05Z")
			return
		}
		before = &t
	}
	out := map[string]any{"address": addr}
	var c struct {
		Emails, Delivered, Bounced, Complained, Opened, Clicked int
		First, Last                                             *time.Time
	}
	err := s.pool.QueryRow(ctx, `
		SELECT count(*)::int, count(*) FILTER (WHERE delivered_at IS NOT NULL)::int, count(*) FILTER (WHERE status = 'bounced')::int,
		       count(*) FILTER (WHERE status = 'complained')::int, count(*) FILTER (WHERE opens > 0)::int, count(*) FILTER (WHERE clicks > 0)::int,
		       min(created_at), max(created_at)
		FROM emails WHERE workspace_id = $1 AND recipients @> ARRAY[$2] AND NOT test`, a.WorkspaceID, addr).
		Scan(&c.Emails, &c.Delivered, &c.Bounced, &c.Complained, &c.Opened, &c.Clicked, &c.First, &c.Last)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out["counts"] = map[string]int{"emails": c.Emails, "delivered": c.Delivered, "bounced": c.Bounced, "complained": c.Complained, "opened": c.Opened, "clicked": c.Clicked}
	out["first_sent_at"], out["last_sent_at"] = c.First, c.Last
	var sup struct {
		Reason, Source string
		At             time.Time
	}
	err = s.pool.QueryRow(ctx, `SELECT reason, source, created_at FROM suppressions WHERE workspace_id = $1 AND kind = 'email' AND value = $2`, a.WorkspaceID, addr).
		Scan(&sup.Reason, &sup.Source, &sup.At)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		out["suppressed"] = nil
	case err != nil:
		s.internal(w, r, err)
		return
	default:
		out["suppressed"] = map[string]any{"reason": sup.Reason, "source": sup.Source, "created_at": sup.At}
	}
	var lead *string
	if err := s.pool.QueryRow(ctx, `SELECT id::text FROM leads WHERE workspace_id = $1 AND lower(email) = $2 LIMIT 1`, a.WorkspaceID, addr).Scan(&lead); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		s.internal(w, r, err)
		return
	}
	out["lead_id"] = lead
	rows, err := s.pool.Query(ctx, `
		SELECT e.id, e.subject, t.handle, e.purpose, e.trigger_kind, ev.type, ev.detail, ev.occurred_at
		FROM email_events ev
		JOIN emails e ON e.id = ev.email_id
		LEFT JOIN email_templates t ON t.id = e.template_id
		WHERE e.workspace_id = $1 AND e.recipients @> ARRAY[$2] AND (ev.recipient IS NULL OR lower(ev.recipient) = $2)
		  AND ($3::timestamptz IS NULL OR ev.occurred_at < $3)
		ORDER BY ev.occurred_at DESC, ev.id DESC LIMIT $4`, a.WorkspaceID, addr, before, limit)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	events, err := pgx.CollectRows(rows, pgx.RowToStructByPos[emailRecipientEvent])
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if events == nil {
		events = []emailRecipientEvent{}
	}
	out["events"] = events
	var next *time.Time
	if len(events) == limit {
		next = &events[len(events)-1].OccurredAt
	}
	out["next_before"] = next
	writeJSON(w, http.StatusOK, out)
}

// openPixel is the one-pixel image in emails that track opens. Public:
// the signed token is the authority.
var transparentGIF = []byte("GIF89a\x01\x00\x01\x00\x80\x00\x00\x00\x00\x00\x00\x00\x00!\xf9\x04\x01\x00\x00\x00\x00,\x00\x00\x00\x00\x01\x00\x01\x00\x00\x02\x02D\x01\x00;")

func (s *Server) openPixel(w http.ResponseWriter, r *http.Request) {
	if s.emails != nil {
		if id, ok := s.emails.ParseOpen(r.PathValue("token")); ok {
			if err := s.emails.Opened(r.Context(), id, r.UserAgent(), time.Now()); err != nil {
				track.Fail(r.Context(), s.log, track.Event{Name: "email.open_failed", Surface: "system", Props: map[string]any{"email_id": id}}, "internal", err)
			}
		}
	}
	w.Header().Set("Content-Type", "image/gif")
	w.Header().Set("Cache-Control", "no-store, max-age=0")
	w.Write(transparentGIF) //nolint:errcheck
}

// composeOK reports whether emails.Compose took the request, and answers
// it when not.
func (s *Server) composeOK(w http.ResponseWriter, r *http.Request, err error) bool {
	var ce *emails.ComposeError
	switch {
	case err == nil:
		return true
	case errors.As(err, &ce):
		writeError(w, r, ce.Status, ce.Code, ce.Message)
	default:
		s.internal(w, r, err)
	}
	return false
}
