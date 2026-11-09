package api

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/outflareai/outflare/engine/internal/mailbox"
	"github.com/outflareai/outflare/engine/internal/tracking"
)

// Free public tools: checks anyone can run without an account, which the
// website's free tools pages call from the browser. They read public DNS
// only, answer from a short cache, and are limited per network, so they
// can't be used to hammer DNS or the blocklists through us.

const (
	domainCheckTTL     = 10 * time.Minute
	domainCheckMax     = 5000 // cached answers before the cache starts over
	domainCheckTimeout = 8 * time.Second
)

type domainCheckView struct {
	Domain       string              `json:"domain"`
	Health       string              `json:"health"`
	Issues       []mailbox.Issue     `json:"issues"`
	MXProvider   string              `json:"mx_provider"`
	MXHosts      []string            `json:"mx_hosts"`
	SPF          string              `json:"spf"`
	SPFRecord    *string             `json:"spf_record"`
	DKIM         string              `json:"dkim"`
	DKIMSelector *string             `json:"dkim_selector"`
	DMARC        string              `json:"dmarc"`
	DMARCRecord  *string             `json:"dmarc_record"`
	DMARCPolicy  *string             `json:"dmarc_policy"`
	Blocklists   []mailbox.Blocklist `json:"blocklists"`
	CheckedAt    time.Time           `json:"checked_at"`
}

type domainCheckCache struct {
	mu      sync.Mutex
	entries map[string]domainCheckView
}

func (c *domainCheckCache) get(domain string) (domainCheckView, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.entries[domain]
	if !ok || time.Since(v.CheckedAt) > domainCheckTTL {
		return domainCheckView{}, false
	}
	return v, true
}

func (c *domainCheckCache) put(v domainCheckView) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil || len(c.entries) >= domainCheckMax {
		c.entries = map[string]domainCheckView{}
	}
	c.entries[v.Domain] = v
}

func (s *Server) routesTools(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/tools/domain-check", s.checkDomainPublic)
}

// checkDomainPublic checks a domain's email authentication (MX, SPF, DKIM,
// DMARC) and domain blocklists, with the fix for each problem.
func (s *Server) checkDomainPublic(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if !s.toolsIPLimit.allow(w, r, networkKey(clientIP(r))) {
		return
	}
	domain, ok := tracking.ValidDomain(r.URL.Query().Get("domain"))
	if !ok {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "domain: give a domain name such as acme.example")
		return
	}
	v, cached := s.domainChecks.get(domain)
	if !cached {
		var res mailbox.Resolver
		if s.mail != nil {
			res = s.mail.Resolver
		}
		if res == nil {
			writeError(w, r, http.StatusServiceUnavailable, "unavailable", "domain checks are not available on this server")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), domainCheckTimeout)
		defer cancel()
		v = viewDomainCheck(mailbox.CheckDomain(ctx, res, domain), time.Now().UTC())
		s.domainChecks.put(v)
	}
	w.Header().Set("Cache-Control", "public, max-age=300")
	writeJSON(w, http.StatusOK, v)
}

func viewDomainCheck(h mailbox.DomainHealth, at time.Time) domainCheckView {
	issues := mailbox.DomainIssues(mailbox.DomainFacts{
		Domain: h.Domain, Checked: true, Mailboxes: 1, MXProvider: h.MXProvider,
		SPF: h.SPF, SPFRecord: h.SPFRecord, DKIM: h.DKIM, DMARC: h.DMARC, Blocklists: h.Blocklists,
	})
	if issues == nil {
		issues = []mailbox.Issue{}
	}
	if h.MXHosts == nil {
		h.MXHosts = []string{}
	}
	if h.Blocklists == nil {
		h.Blocklists = []mailbox.Blocklist{}
	}
	opt := func(s string) *string {
		if s == "" {
			return nil
		}
		return &s
	}
	return domainCheckView{
		Domain: h.Domain, Health: mailbox.Health(true, issues), Issues: issues,
		MXProvider: h.MXProvider, MXHosts: h.MXHosts,
		SPF: h.SPF, SPFRecord: opt(h.SPFRecord),
		DKIM: h.DKIM, DKIMSelector: opt(h.DKIMSelector),
		DMARC: h.DMARC, DMARCRecord: opt(h.DMARCRecord), DMARCPolicy: opt(h.DMARCPolicy),
		Blocklists: h.Blocklists, CheckedAt: at,
	}
}
