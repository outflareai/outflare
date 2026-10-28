package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/crawl"
	"github.com/outflareai/outflare/engine/internal/jobs"
)

// A brand's DNS, all in one place: every record Outflare asks for on the
// brand's domains (the site's proof, a sending domain's records, a
// tracking domain's CNAME), so a person adds them in one go instead of
// once per page. GET lists them with what is still missing; POST adds
// every missing one through the workspace's own DNS host connection, one
// job per group, in a single request. Approving them at the DNS provider
// (Domain Connect) stays per group, through each group's own
// .../dns/approval, because a provider approves one template at a time.

func (s *Server) routesBrandDNS(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/workspaces/{ws}/brands/{brand}/dns", s.inBrand(auth.ScopeWorkspaceRead, s.getBrandDNS))
	mux.HandleFunc("POST /v1/workspaces/{ws}/brands/{brand}/dns", s.inBrand(auth.ScopeWorkspaceManage, s.addBrandDNS))
}

// brandDNSRecord is one record, and whether DNS shows it yet.
type brandDNSRecord struct {
	Type     string `json:"type"`
	Name     string `json:"name"`
	Value    string `json:"value"`
	Priority *int   `json:"priority"`
	// in_place once DNS shows it; missing until then.
	Status string `json:"status"`
}

// brandDNSGroup is the records one thing needs.
type brandDNSGroup struct {
	// What they are for: seo_site (the site's proof), email_domain (a
	// sending domain) or tracking_domain. ID is that thing's id, for its
	// own .../dns/approval.
	Subject string `json:"subject"`
	ID      string `json:"id"`
	Title   string `json:"title"`
	// Why the records are needed, in a line.
	Why     string           `json:"why"`
	Domain  string           `json:"domain"`
	Records []brandDNSRecord `json:"records"`
	Missing int              `json:"missing"`
	// The last one-click run or approval for this group.
	Last *connectors.DNSInstall `json:"last"`
}

type brandDNSView struct {
	BrandID string          `json:"brand_id"`
	Domain  string          `json:"domain"`
	Groups  []brandDNSGroup `json:"groups"`
	// Records still missing across every group.
	Missing int `json:"missing"`
	// The brand's connections that can add DNS records (Cloudflare).
	Connections []string `json:"connections"`
	// Every missing record, as a plain list to send a developer or paste
	// into an AI coding tool.
	Developer setupMessage `json:"developer"`
	AIPrompt  string       `json:"ai_prompt"`
}

// underDomain says whether host is domain or one of its subdomains.
func underDomain(host, domain string) bool {
	host, domain = strings.TrimSuffix(strings.ToLower(host), "."), strings.TrimSuffix(strings.ToLower(domain), ".")
	return domain != "" && (host == domain || strings.HasSuffix(host, "."+domain))
}

func (s *Server) loadBrandDNS(ctx context.Context, ws string, b *brandRecord) (brandDNSView, error) {
	host := strings.TrimPrefix(websiteHost(b.Website), "www.")
	v := brandDNSView{BrandID: b.ID, Domain: host, Groups: []brandDNSGroup{}, Connections: []string{}}
	domains := append([]string{}, b.Domains...)
	if host != "" {
		domains = append(domains, host)
	}
	ours := func(name string) bool {
		for _, d := range domains {
			if underDomain(name, d) {
				return true
			}
		}
		return false
	}

	// The site's proof of ownership.
	site, err := s.setupSiteFor(ctx, s.pool, ws, b.ID, websiteHost(b.Website))
	if err != nil {
		return v, err
	}
	if site != nil {
		p := crawl.ProofsFor(site.startURL, site.host, site.token)
		g := brandDNSGroup{Subject: "seo_site", ID: site.id, Title: "Website ownership", Domain: p.DNSName,
			Why:     "Proves the site is yours, so Outflare can audit every page and trust its events.",
			Records: []brandDNSRecord{{Type: "TXT", Name: p.DNSName, Value: p.DNSValue, Status: "missing"}}}
		if site.verified {
			g.Records[0].Status = "in_place"
		}
		var raw []byte
		if err := s.pool.QueryRow(ctx, `SELECT dns_install FROM seo_sites WHERE id = $1`, site.id).Scan(&raw); err != nil {
			return v, err
		}
		g.Last = dnsRun(raw)
		v.Groups = append(v.Groups, g)
	}

	// Sending domains: the brand's own, or any on its domains.
	mail, err := s.queryEmailDomains(ctx, ws, nil, nil)
	if err != nil {
		return v, err
	}
	for _, d := range mail {
		if !(d.BrandID != nil && *d.BrandID == b.ID) && !ours(d.Domain) {
			continue
		}
		g := brandDNSGroup{Subject: "email_domain", ID: d.ID, Title: "Sending email from " + d.Domain, Domain: d.Domain, Last: d.DNSInstall,
			Why: "Lets your email provider send from the domain, so receipts and onboarding emails reach the inbox."}
		for _, r := range d.Records {
			rec := brandDNSRecord{Type: strings.ToUpper(r.Type), Name: r.Name, Value: r.Value, Status: "missing"}
			if strings.EqualFold(r.Type, "MX") {
				p := r.Priority
				rec.Priority = &p
			}
			if r.Status == "verified" {
				rec.Status = "in_place"
			}
			g.Records = append(g.Records, rec)
		}
		v.Groups = append(v.Groups, g)
	}

	// Tracking domains on the brand's domains.
	tracked, err := s.trackingDomains(ctx, ws, nil)
	if err != nil {
		return v, err
	}
	for _, d := range tracked {
		if !ours(d.Domain.Domain) {
			continue
		}
		g := brandDNSGroup{Subject: "tracking_domain", ID: d.ID, Title: "Tracking links on " + d.Domain.Domain, Domain: d.Domain.Domain, Last: d.DNSInstall,
			Why:     "Puts the links and opens in your emails on your own domain instead of Outflare's.",
			Records: []brandDNSRecord{{Type: "CNAME", Name: d.Domain.Domain, Value: d.Target, Status: "missing"}}}
		if d.Status == "verified" {
			g.Records[0].Status = "in_place"
		}
		v.Groups = append(v.Groups, g)
	}

	var list strings.Builder
	for i := range v.Groups {
		g := &v.Groups[i]
		for _, r := range g.Records {
			if r.Status == "in_place" {
				continue
			}
			g.Missing++
			list.WriteString("Type: " + r.Type + "\nName: " + r.Name + "\nValue: ")
			if r.Priority != nil {
				list.WriteString(strconv.Itoa(*r.Priority) + " ")
			}
			list.WriteString(r.Value + "\n\n")
		}
		v.Missing += g.Missing
	}
	conns, err := s.dnsConnections(ctx, ws)
	if err != nil {
		return v, err
	}
	v.Connections = conns(b.ID)
	name := v.Domain
	if name == "" {
		name = b.Name
	}
	v.Developer = setupMessage{
		Subject: "Please add DNS records for " + name,
		Body: "Hi,\n\nCould you add these DNS records for " + name + "? They let Outflare verify our site and send email from our domain.\n\n" + list.String() +
			"Add them alongside the records already there: never change or delete one. CNAME records should be DNS only (not proxied). " +
			"If a name already has an SPF or DMARC record, merge into it instead of adding a second one. Outflare checks for them on its own.\n\nThanks!",
	}
	v.AIPrompt = "Add these DNS records for " + name + " where its DNS is managed.\n\n" + list.String() +
		"Rules:\n- Add them next to what is already there; never change or delete an existing record.\n" +
		"- CNAME records are DNS only (not proxied).\n- If a name already has an SPF or DMARC record, merge into it instead of adding a second one.\n" +
		"- If you can't reach the DNS host, tell me which one it is and what to click instead.\n" +
		"- Don't ask me to paste passwords or API keys into this chat; if a sign-in is needed, stop and let me do it."
	return v, nil
}

// dnsRun reads a stored last run, or nil.
func dnsRun(raw []byte) *connectors.DNSInstall {
	if len(raw) == 0 {
		return nil
	}
	var run connectors.DNSInstall
	if json.Unmarshal(raw, &run) != nil || run.Status == "" {
		return nil
	}
	return &run
}

func (s *Server) getBrandDNS(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	b, ok := s.brandFor(w, r, a)
	if !ok {
		return
	}
	v, err := s.loadBrandDNS(r.Context(), a.WorkspaceID, b)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// addBrandDNS adds every missing record with one DNS host connection:
// one job per group, all queued together.
func (s *Server) addBrandDNS(w http.ResponseWriter, r *http.Request) {
	a, b, ok := s.setupBrandWrite(w, r)
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
	if s.allocError(w, r, s.setupConnection(ctx, a.WorkspaceID, b.ID, req.IntegrationID, connectors.CapDNSWrite)) {
		return
	}
	// The site's proof first, so a brand with only a website still has
	// something to add.
	if websiteHost(b.Website) != "" {
		if err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error { return s.prepareSetupStep(ctx, tx, a, b, stepVerifySite) }); s.allocError(w, r, err) {
			return
		}
	}
	v, err := s.loadBrandDNS(ctx, a.WorkspaceID, b)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if v.Missing == 0 {
		writeError(w, r, http.StatusConflict, "nothing_to_add", "every DNS record this brand needs is already in place")
		return
	}
	queued := 0
	run, _ := json.Marshal(connectors.DNSInstall{Status: "queued", IntegrationID: req.IntegrationID, At: time.Now().UTC()})
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		for _, g := range v.Groups {
			if g.Missing == 0 {
				continue
			}
			switch g.Subject {
			case "email_domain":
				if err := s.emails.InstallDNS(ctx, tx, s.emailWho(ctx, a), a.WorkspaceID, g.ID, req.IntegrationID); err != nil {
					return err
				}
			default:
				if _, err := tx.Exec(ctx, `UPDATE `+dnsSubjects[g.Subject].table+` SET dns_install = $3 WHERE workspace_id = $1 AND `+dnsSubjects[g.Subject].key+` = $2`, a.WorkspaceID, g.ID, run); err != nil {
					return err
				}
				if _, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: jobDNSAdd, WorkspaceID: &a.WorkspaceID, MaxAttempts: 4,
					Payload: dnsAddJob{Subject: g.Subject, ID: g.ID, BrandID: b.ID, IntegrationID: req.IntegrationID, ActorID: a.ActorID, ActorType: a.ActorType}}); err != nil {
					return err
				}
			}
			queued++
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "brand.dns_requested", "brand", b.ID, map[string]any{
			"brand_id": b.ID, "integration_id": req.IntegrationID, "groups": queued, "records": v.Missing})
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, http.StatusConflict, "conflict", "a sending domain was removed meanwhile; try again")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"queued": true, "groups": queued, "records": v.Missing})
}
