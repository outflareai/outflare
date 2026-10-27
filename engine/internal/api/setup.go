package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/audit"
	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/crawl"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Brand setup: one brand's checklist, from what every brand needs (the
// website script, a verified site) to the tools fewer brands use (ads,
// social, a CRM). Each step says how to do it the quickest way: one click
// through the workspace's own Tag Manager or DNS host, a message for a
// developer, or a prompt for an AI coding tool. Whether a step is done is
// read from the workspace (a visit arrived, the site's proof was found, a
// tool is connected), so it ticks itself off however it was done. People
// can leave it at any step and come back; only skips, closing it and what
// a one-click install last did are stored.
//
// The one-click installs call the workspace's own accounts, so they run as
// jobs: the request queues the install and the checklist shows it running.

const (
	stepBusinessModel = "business_model"
	stepSiteScript    = "site_script"
	stepVerifySite    = "verify_site"

	jobSetupInstall = "setup.install"

	// How long Outflare keeps looking for a site's proof after a one-click
	// DNS install or a person asking it to.
	verifyWatch = 72 * time.Hour
)

func (s *Server) routesSetup(mux *http.ServeMux) {
	read, manage := auth.ScopeWorkspaceRead, auth.ScopeWorkspaceManage
	mux.HandleFunc("GET /v1/workspaces/{ws}/brands/{brand}/setup", s.inBrand(read, s.getBrandSetup))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/brands/{brand}/setup", s.inBrand(manage, s.updateBrandSetup))
	mux.HandleFunc("POST /v1/workspaces/{ws}/brands/{brand}/setup/prepare", s.inBrand(manage, s.prepareBrandSetup))
	mux.HandleFunc("POST /v1/workspaces/{ws}/brands/{brand}/setup/install", s.inBrand(manage, s.installBrandSetup))
	mux.HandleFunc("POST /v1/workspaces/{ws}/brands/{brand}/setup/check", s.inBrand(manage, s.checkBrandSetup))
	mux.HandleFunc("GET /v1/workspaces/{ws}/brands/{brand}/setup/tag-containers", s.inBrand(read, s.listTagContainers))
	mux.HandleFunc("GET /v1/workspaces/{ws}/install-health", s.inBrand(read, s.getInstallHealth))
}

type setupStepDef struct {
	id, title, detail, page, tool string
	// For a step done by connecting a tool: the connectors that do it,
	// most used first, and the categories whose other connectors count too.
	suggest    []string
	categories []string
}

// setupSteps are in the order a brand needs them: what every brand wants
// first, the least common last.
var setupSteps = []setupStepDef{
	{id: stepBusinessModel, title: "Say what the brand does",
		detail: "Pick what the brand does and how its customers pay, so its reports, funnels and suggestions fit the business. Change it any time.",
		page:   "setup", tool: "update_brand"},
	{id: stepSiteScript, title: "Add the website script",
		detail: "One line on every page counts visits, sign-ups and where they came from, so every channel's results show up in one place.",
		page:   "setup", tool: "install_brand_setup"},
	{id: stepVerifySite, title: "Verify your website",
		detail: "A DNS record proves the site is yours, so Outflare can audit every page and trust your site's events.",
		page:   "setup", tool: "install_brand_setup"},
	{id: "analytics", title: "Connect your analytics",
		detail: "Your traffic history from the tool already on your site, so reports start full instead of empty.",
		page:   "connectors", tool: "request_connection",
		suggest: []string{"google_analytics", "posthog", "plausible"}, categories: []string{connectors.CategoryAnalytics}},
	{id: "search", title: "Connect Search Console",
		detail: "What people search to find you and where each page ranks.",
		page:   "connectors", tool: "request_connection",
		suggest: []string{"google_search_console", "bing_webmaster"}},
	{id: "email", title: "Set up email",
		detail: "Send receipts, onboarding and outreach from your own domain and provider.",
		page:   "providers", tool: "request_connection",
		suggest: []string{"amazon_ses", "postmark", "resend"}, categories: []string{connectors.CategoryEmailSending}},
	{id: "payments", title: "Connect payments",
		detail: "Revenue on every customer, so each channel shows what it earns.",
		page:   "connectors", tool: "request_connection",
		suggest: []string{"stripe"}, categories: []string{connectors.CategoryPayments}},
	{id: "ads", title: "Connect your ad accounts",
		detail: "Spend and conversions beside your other channels.",
		page:   "ads", tool: "request_connection",
		suggest: []string{"google_ads", "meta_ads", "linkedin_ads"}, categories: []string{connectors.CategoryAds}},
	{id: "social", title: "Connect your social accounts",
		detail: "Followers, posts and replies from the brand's own accounts.",
		page:   "social", tool: "request_connection",
		suggest: []string{"x", "linkedin", "reddit"}, categories: []string{connectors.CategorySocial}},
	{id: "crm", title: "Connect your CRM",
		detail: "Deals and pipeline on the same customer records.",
		page:   "connectors", tool: "request_connection",
		suggest: []string{"hubspot", "attio"}, categories: []string{connectors.CategoryCRM}},
}

// searchConnectors do the search step; they are analytics connectors, but
// not the ones the analytics step means.
var searchConnectors = []string{"google_search_console", "bing_webmaster"}

func setupStep(id string) bool {
	return slices.ContainsFunc(setupSteps, func(st setupStepDef) bool { return st.id == id })
}

// setupInstallRun is what a one-click install last did.
type setupInstallRun struct {
	Method string `json:"method"`
	// queued, done or failed.
	Status string    `json:"status"`
	Error  string    `json:"error,omitempty"`
	Link   string    `json:"link,omitempty"`
	At     time.Time `json:"at"`
	// published is false when a tag waits in Tag Manager for a person to
	// publish it.
	Published *bool `json:"published,omitempty"`
}

type setupMessage struct {
	Subject string `json:"subject"`
	Body    string `json:"body"`
}

// setupInstall is how to do an install step, every way at once.
type setupInstall struct {
	// The ways, quickest first: gtm or dns (one click through a connection),
	// developer (a message to send), ai (a prompt for an AI coding tool) and
	// manual (do it yourself).
	Methods []string `json:"methods"`
	// The site key or site this step installs; null until prepared.
	SiteKeyID *string `json:"site_key_id"`
	SiteID    *string `json:"site_id"`
	Snippet   string  `json:"snippet,omitempty"`
	// The DNS record that proves the site, and the other proofs.
	Record    *connectors.DNSRecord `json:"record"`
	MetaTag   string                `json:"meta_tag,omitempty"`
	FileURL   string                `json:"file_url,omitempty"`
	FileBody  string                `json:"file_body,omitempty"`
	AIPrompt  string                `json:"ai_prompt"`
	Developer setupMessage          `json:"developer"`
	// The connector a one-click install uses, and the brand's connections
	// to it (ids).
	Connector   string   `json:"connector"`
	Connections []string `json:"connections"`
	// Whether the server can sign in to that connector with OAuth.
	OAuthReady bool             `json:"oauth_ready"`
	Last       *setupInstallRun `json:"last"`
}

type setupStepView struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Detail string `json:"detail"`
	// todo, waiting (done its part, waiting for a visit or the DNS),
	// done or skipped.
	Status  string `json:"status"`
	Done    bool   `json:"done"`
	Skipped bool   `json:"skipped"`
	// Why the step is waiting, in a line.
	Waiting string `json:"waiting,omitempty"`
	Page    string `json:"page"`
	Tool    string `json:"tool"`
	// For connect steps: the connectors to offer, most used first.
	Connectors []string      `json:"connectors"`
	Install    *setupInstall `json:"install"`
}

type brandSetupView struct {
	BrandID string `json:"brand_id"`
	Brand   string `json:"brand"`
	Website string `json:"website"`
	// What the brand does and how its customers pay (PATCH .../brands/{brand}).
	BusinessModel *string         `json:"business_model"`
	RevenueModels []string        `json:"revenue_models"`
	Steps         []setupStepView `json:"steps"`
	Done          int             `json:"done"`
	Total         int             `json:"total"`
	// The first step neither done nor skipped; null when none is left.
	NextStep *string `json:"next_step"`
	Complete bool    `json:"complete"`
	// Someone closed the checklist; Home stops showing it.
	Dismissed bool `json:"dismissed"`
	// Problems with what is already installed (GET .../install-health).
	Issues int `json:"issues"`
}

type brandSetupRow struct {
	skipped   []string
	dismissed *time.Time
	installs  map[string]setupInstallRun
}

func (s *Server) brandSetupRow(ctx context.Context, q jobs.Querier, brand string) (brandSetupRow, error) {
	var row brandSetupRow
	var raw []byte
	err := q.QueryRow(ctx, `SELECT skipped, dismissed_at, installs FROM brand_setup WHERE brand_id = $1`, brand).Scan(&row.skipped, &row.dismissed, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return brandSetupRow{installs: map[string]setupInstallRun{}}, nil
	}
	if err != nil {
		return row, err
	}
	row.installs = map[string]setupInstallRun{}
	_ = json.Unmarshal(raw, &row.installs)
	return row, nil
}

// websiteHost is the host of a brand's website, or "".
func websiteHost(website string) string {
	w := strings.TrimSpace(website)
	if w == "" {
		return ""
	}
	if !strings.Contains(w, "://") {
		w = "https://" + w
	}
	u, err := url.Parse(w)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

type setupSiteKey struct {
	id, key, version string
	origins          []string
	lastUsed         *time.Time
	createdAt        time.Time
}

// setupSiteKeyFor is the brand's site key: its own, or a key with no brand
// when it is the workspace's only brand.
func (s *Server) setupSiteKeyFor(ctx context.Context, q jobs.Querier, ws, brand string) (*setupSiteKey, error) {
	var k setupSiteKey
	err := q.QueryRow(ctx, `
		SELECT id::text, key, script_version, origins, last_used_at, created_at FROM site_keys k
		WHERE workspace_id = $1 AND revoked_at IS NULL
		  AND (brand_id = $2 OR (brand_id IS NULL
		       AND NOT EXISTS (SELECT 1 FROM brands b WHERE b.workspace_id = $1 AND b.status = 'active' AND b.id <> $2)))
		ORDER BY (brand_id IS NULL), last_used_at DESC NULLS LAST, created_at LIMIT 1`, ws, brand).
		Scan(&k.id, &k.key, &k.version, &k.origins, &k.lastUsed, &k.createdAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &k, err
}

type setupSite struct {
	id, host, startURL, token string
	verified                  bool
	checked, watch            *time.Time
}

// setupSiteFor is the brand's site for its website: the one whose host is
// the website's, else the brand's first verified site.
func (s *Server) setupSiteFor(ctx context.Context, q jobs.Querier, ws, brand, host string) (*setupSite, error) {
	var v setupSite
	err := q.QueryRow(ctx, `
		SELECT id::text, host, start_url, verify_token, verified_at IS NOT NULL, verify_checked_at, verify_watch_until FROM seo_sites
		WHERE workspace_id = $1 AND (brand_id = $2 OR host = $3 OR host = 'www.' || $3 OR 'www.' || host = $3)
		ORDER BY (host = $3 OR host = 'www.' || $3 OR 'www.' || host = $3) DESC, verified_at IS NOT NULL DESC, created_at LIMIT 1`, ws, brand, host).
		Scan(&v.id, &v.host, &v.startURL, &v.token, &v.verified, &v.checked, &v.watch)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &v, err
}

// brandConnections are the brand's connections, by connector.
func (s *Server) brandConnections(ctx context.Context, ws, brand string) (map[string][]connectors.Integration, error) {
	out := map[string][]connectors.Integration{}
	if s.integrations == nil {
		return out, nil
	}
	all, err := s.integrations.List(ctx, s.pool, ws, "")
	if err != nil {
		return nil, err
	}
	for _, i := range connectors.ForBrand(all, brand) {
		out[i.Connector] = append(out[i.Connector], i)
	}
	return out, nil
}

func (s *Server) loadBrandSetup(ctx context.Context, ws string, b *brandRecord) (brandSetupView, error) {
	v := brandSetupView{BrandID: b.ID, Brand: b.Name, Website: b.Website, BusinessModel: b.BusinessModel, RevenueModels: b.RevenueModels, Steps: []setupStepView{}}
	row, err := s.brandSetupRow(ctx, s.pool, b.ID)
	if err != nil {
		return v, err
	}
	conns, err := s.brandConnections(ctx, ws, b.ID)
	if err != nil {
		return v, err
	}
	key, err := s.setupSiteKeyFor(ctx, s.pool, ws, b.ID)
	if err != nil {
		return v, err
	}
	host := websiteHost(b.Website)
	site, err := s.setupSiteFor(ctx, s.pool, ws, b.ID, host)
	if err != nil {
		return v, err
	}
	var sending, issues int
	if err := s.pool.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM sending_domains WHERE workspace_id = $1 AND brand_id = $2)
		     + (SELECT count(*) FROM email_providers WHERE workspace_id = $1),
		       (SELECT count(*) FROM install_issues WHERE workspace_id = $1 AND resolved_at IS NULL AND (brand_id = $2 OR brand_id IS NULL))`,
		ws, b.ID).Scan(&sending, &issues); err != nil {
		return v, err
	}
	v.Issues = issues

	connected := func(st setupStepDef) bool {
		for id := range conns {
			d, ok := s.integrations.Registry.Definition(id)
			if !ok {
				continue
			}
			switch {
			case st.id == "search" && slices.Contains(searchConnectors, id):
				return true
			case st.id == "analytics" && slices.Contains(searchConnectors, id):
			case slices.Contains(st.suggest, id) || slices.Contains(st.categories, d.Category):
				return true
			}
		}
		return false
	}
	base := s.appPath(ctx, ws, "")
	for _, st := range setupSteps {
		sv := setupStepView{ID: st.id, Title: st.title, Detail: st.detail, Tool: st.tool, Page: base + st.page, Connectors: []string{}}
		for _, id := range st.suggest {
			if d, ok := s.integrations.Registry.Definition(id); ok && d.Status == connectors.Available {
				sv.Connectors = append(sv.Connectors, id)
			}
		}
		switch st.id {
		case stepBusinessModel:
			sv.Done = b.BusinessModel != nil
		case stepSiteScript:
			sv.Done = key != nil && key.lastUsed != nil
			if key != nil && !sv.Done {
				sv.Waiting = "Waiting for the first visit. Open your site once the script is on it."
			}
			sv.Install = s.siteScriptInstall(b, key, conns, row.installs[st.id])
		case stepVerifySite:
			sv.Done = site != nil && site.verified
			if site != nil && !sv.Done && site.watch != nil && site.watch.After(time.Now()) {
				sv.Waiting = "Looking for the DNS record. Outflare checks every minute at first; DNS changes can take up to an hour."
			}
			sv.Install = s.verifySiteInstall(b, site, conns, row.installs[st.id])
		case "email":
			sv.Done = sending > 0
		default:
			sv.Done = connected(st)
		}
		sv.Skipped = !sv.Done && slices.Contains(row.skipped, st.id)
		switch {
		case sv.Done:
			sv.Status = "done"
			v.Done++
		case sv.Skipped:
			sv.Status = "skipped"
		case sv.Waiting != "":
			sv.Status = "waiting"
		default:
			sv.Status = "todo"
		}
		if !sv.Done && !sv.Skipped && v.NextStep == nil {
			id := st.id
			v.NextStep = &id
		}
		v.Steps = append(v.Steps, sv)
	}
	v.Total = len(v.Steps)
	v.Complete = v.NextStep == nil
	v.Dismissed = row.dismissed != nil
	return v, nil
}

// ids are the working connections' ids: one whose sign-in broke is left
// out, so the step offers to connect again instead of retrying it.
func ids(list []connectors.Integration) []string {
	out := []string{}
	for _, i := range list {
		if i.Status != connectors.StatusConnected {
			continue
		}
		out = append(out, i.ID)
	}
	return out
}

func (s *Server) oauthReady(connector string) bool {
	if s.integrations == nil {
		return false
	}
	_, ok := s.integrations.OAuthApps[connector]
	return ok
}

func lastRun(r setupInstallRun) *setupInstallRun {
	if r.Status == "" {
		return nil
	}
	return &r
}

func (s *Server) siteScriptInstall(b *brandRecord, key *setupSiteKey, conns map[string][]connectors.Integration, last setupInstallRun) *setupInstall {
	in := &setupInstall{Methods: []string{"gtm", "developer", "ai", "manual"}, Connector: "google_tag_manager",
		Connections: ids(conns["google_tag_manager"]), OAuthReady: s.oauthReady("google_tag_manager"), Last: lastRun(last)}
	site := b.Website
	if site == "" {
		site = "the website"
	}
	snippet := "<script async src=\"" + strings.TrimRight(s.publicURL, "/") + "/site.js\" data-key=\"(made when you prepare this step)\"></script>"
	if key != nil {
		in.SiteKeyID = &key.id
		snippet = s.siteSnippet(key.key, key.origins)
		in.Snippet = snippet
	}
	in.AIPrompt = "Add Outflare's website tracking script to " + site + ".\n\n" +
		"Put this tag inside the <head> of every page, once, before any other analytics script:\n\n" + snippet + "\n\n" +
		"Where it goes depends on the site:\n" +
		"- A shared layout or template (Next.js app/layout or _document, Astro or Nuxt layout, a Rails or Django base template): add it there.\n" +
		"- A single-page app: add it to index.html; it records page changes by itself.\n" +
		"- WordPress, Webflow, Shopify, Squarespace or Wix: use the site's \"custom code in <head>\" setting.\n" +
		"- If the site has a Content-Security-Policy, allow " + strings.TrimRight(s.publicURL, "/") + " in script-src and connect-src.\n\n" +
		"Copy the tag exactly: the data-key is public and is meant to be in the page. Don't install it with npm or bundle it; it updates itself. " +
		"When it is deployed, open the site once and tell me; Outflare ticks the step off when the first visit arrives."
	in.Developer = setupMessage{
		Subject: "Please add Outflare's tracking script to " + site,
		Body: "Hi,\n\nCould you add this one line inside the <head> of every page on " + site + "?\n\n" + snippet + "\n\n" +
			"It loads asynchronously, sets no cookie until visitors consent where consent is needed, and updates itself. " +
			"If the site has a Content-Security-Policy, allow " + strings.TrimRight(s.publicURL, "/") + " in script-src and connect-src.\n\n" +
			"Once it's live, opening the site once is enough: Outflare sees the first visit and ticks it off.\n\n" +
			"Instructions: https://outflare.ai/docs/guides/website-tracking/\n\nThanks!",
	}
	return in
}

func (s *Server) verifySiteInstall(b *brandRecord, site *setupSite, conns map[string][]connectors.Integration, last setupInstallRun) *setupInstall {
	in := &setupInstall{Methods: []string{"dns", "developer", "ai", "manual"}, Connector: "cloudflare",
		Connections: ids(conns["cloudflare"]), OAuthReady: s.oauthReady("cloudflare"), Last: lastRun(last)}
	name, value := websiteHost(b.Website), crawl.VerifyName+"=(made when you prepare this step)"
	if site != nil {
		p := crawl.ProofsFor(site.startURL, site.host, site.token)
		in.SiteID = &site.id
		in.Record = &connectors.DNSRecord{Type: "TXT", Name: p.DNSName, Content: p.DNSValue}
		in.MetaTag, in.FileURL, in.FileBody = p.MetaTag, p.FileURL, p.FileBody
		name, value = p.DNSName, p.DNSValue
	}
	if name == "" {
		name = "your domain"
	}
	in.AIPrompt = "Add a DNS TXT record so Outflare can verify that I own " + name + ".\n\n" +
		"Record type: TXT\nName (host): " + name + " (the domain itself; many DNS dashboards write this as @)\nValue: " + value + "\n\n" +
		"Add it next to any TXT records already on the domain (such as SPF or Google's verification): never change or delete those. " +
		"If you can't reach the DNS host, tell me which one it is and what to click instead. " +
		"Don't ask me to paste passwords or API keys into this chat; if a sign-in is needed, stop and let me do it."
	in.Developer = setupMessage{
		Subject: "Please add a DNS record for " + name,
		Body: "Hi,\n\nCould you add this DNS record to " + name + "? It proves to Outflare that we own the site.\n\n" +
			"Type: TXT\nName: " + name + " (@)\nValue: " + value + "\n\n" +
			"It goes alongside any TXT records already there; nothing else changes. Outflare checks for it on its own and ticks it off once it's visible.\n\nThanks!",
	}
	return in
}

func (s *Server) getBrandSetup(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	b, ok := s.brandFor(w, r, a)
	if !ok {
		return
	}
	v, err := s.loadBrandSetup(r.Context(), a.WorkspaceID, b)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

type brandSetupInput struct {
	Skip      []string `json:"skip"`
	Unskip    []string `json:"unskip"`
	Dismissed *bool    `json:"dismissed"`
}

func (s *Server) updateBrandSetup(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	b, ok := s.brandFor(w, r, a)
	if !ok {
		return
	}
	if !a.writes(nil, &b.ID) {
		writeError(w, r, http.StatusForbidden, "forbidden", string(errSharedRecord))
		return
	}
	var req brandSetupInput
	if !decode(w, r, &req) {
		return
	}
	for _, id := range append(slices.Clone(req.Skip), req.Unskip...) {
		if !setupStep(id) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "no setup step "+id+": use "+setupStepList())
			return
		}
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO brand_setup (brand_id, workspace_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, b.ID, a.WorkspaceID); err != nil {
			return err
		}
		var skipped []string
		var dismissed *time.Time
		if err := tx.QueryRow(ctx, `SELECT skipped, dismissed_at FROM brand_setup WHERE brand_id = $1 FOR UPDATE`, b.ID).Scan(&skipped, &dismissed); err != nil {
			return err
		}
		var added, removed []string
		for _, id := range req.Skip {
			if !slices.Contains(skipped, id) {
				skipped = append(skipped, id)
				added = append(added, id)
			}
		}
		for _, id := range req.Unskip {
			if i := slices.Index(skipped, id); i >= 0 {
				skipped = slices.Delete(skipped, i, i+1)
				removed = append(removed, id)
			}
		}
		closing := req.Dismissed != nil && *req.Dismissed && dismissed == nil
		opening := req.Dismissed != nil && !*req.Dismissed && dismissed != nil
		if len(added) == 0 && len(removed) == 0 && !closing && !opening {
			return nil
		}
		if _, err := tx.Exec(ctx, `
			UPDATE brand_setup SET skipped = $2,
				dismissed_at = CASE WHEN $3 THEN now() WHEN $4 THEN NULL ELSE dismissed_at END,
				updated_at = now(), updated_by = $5
			WHERE brand_id = $1`, b.ID, skipped, closing, opening, nullIfEmpty(a.ActorID)); err != nil {
			return err
		}
		data := func(m map[string]any) map[string]any { m["brand_id"] = b.ID; return m }
		for _, id := range added {
			if err := record(ctx, tx, &a.principal, a.WorkspaceID, "brand_setup.step_skipped", "brand", b.ID, data(map[string]any{"step": id})); err != nil {
				return err
			}
		}
		for _, id := range removed {
			if err := record(ctx, tx, &a.principal, a.WorkspaceID, "brand_setup.step_unskipped", "brand", b.ID, data(map[string]any{"step": id})); err != nil {
				return err
			}
		}
		if closing {
			return record(ctx, tx, &a.principal, a.WorkspaceID, "brand_setup.dismissed", "brand", b.ID, data(map[string]any{}))
		}
		if opening {
			return record(ctx, tx, &a.principal, a.WorkspaceID, "brand_setup.reopened", "brand", b.ID, data(map[string]any{}))
		}
		return nil
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	s.getBrandSetup(w, r)
}

func setupStepList() string {
	out := make([]string, 0, len(setupSteps))
	for _, st := range setupSteps {
		out = append(out, st.id)
	}
	return strings.Join(out, ", ")
}

type setupStepInput struct {
	Step string `json:"step"`
}

// prepareSetupStep makes what an install step installs, when the brand
// has none yet: a site key for the website script, a site to verify for
// the website. It is idempotent.
func (s *Server) prepareSetupStep(ctx context.Context, tx pgx.Tx, a *access, b *brandRecord, step string) error {
	switch step {
	case stepSiteScript:
		key, err := s.setupSiteKeyFor(ctx, tx, a.WorkspaceID, b.ID)
		if err != nil || key != nil {
			return err
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM site_keys WHERE workspace_id = $1 AND revoked_at IS NULL`, a.WorkspaceID).Scan(&n); err != nil {
			return err
		}
		if n >= maxSiteKeys {
			return errInvalid("a workspace can have at most 20 site keys; revoke one on the Website tracking page first")
		}
		name := websiteHost(b.Website)
		if name == "" {
			name = b.Name
		}
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO site_keys (workspace_id, brand_id, name, key, created_by) VALUES ($1, $2, $3, $4, $5) RETURNING id::text`,
			a.WorkspaceID, b.ID, name, newSiteKey(), nullIfEmpty(a.ActorID)).Scan(&id); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "site_key.created", "site_key", id, map[string]any{"brand_id": b.ID, "origins": 0, "from": "setup"})
	case stepVerifySite:
		host := websiteHost(b.Website)
		if host == "" {
			return errInvalid("add the brand's website first (PATCH .../brands/{brand} with website)")
		}
		site, err := s.setupSiteFor(ctx, tx, a.WorkspaceID, b.ID, host)
		if err != nil || site != nil {
			return err
		}
		start, siteHost, err := s.siteAddress(host)
		if err != nil {
			return err
		}
		var id string
		err = tx.QueryRow(ctx, `
			INSERT INTO seo_sites (workspace_id, brand_id, host, start_url, verify_token, created_by)
			VALUES ($1, $2, $3, $4, $5, $6) ON CONFLICT (workspace_id, host) DO NOTHING RETURNING id`,
			a.WorkspaceID, b.ID, siteHost, start, crawl.NewVerifyToken(), nullIfEmpty(a.ActorID)).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "seo_site.created", "seo_site", id, map[string]any{
			"brand_id": b.ID, "max_pages": 100, "schedule": "off", "from": "setup"})
	}
	return errInvalid("only " + stepSiteScript + " and " + stepVerifySite + " have something to prepare")
}

func (s *Server) setupBrandWrite(w http.ResponseWriter, r *http.Request) (*access, *brandRecord, bool) {
	a := accessFrom(r.Context())
	b, ok := s.brandFor(w, r, a)
	if !ok {
		return nil, nil, false
	}
	if !a.writes(nil, &b.ID) {
		writeError(w, r, http.StatusForbidden, "forbidden", string(errSharedRecord))
		return nil, nil, false
	}
	return a, b, true
}

func (s *Server) prepareBrandSetup(w http.ResponseWriter, r *http.Request) {
	a, b, ok := s.setupBrandWrite(w, r)
	if !ok {
		return
	}
	var req setupStepInput
	if !decode(w, r, &req) {
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error { return s.prepareSetupStep(ctx, tx, a, b, req.Step) })
	if s.allocError(w, r, err) {
		return
	}
	s.getBrandSetup(w, r)
}

type setupInstallInput struct {
	Step string `json:"step"`
	// gtm (site_script) or dns (verify_site).
	Method        string `json:"method"`
	IntegrationID string `json:"integration_id"`
	// The Tag Manager container (GET .../setup/tag-containers).
	Container string `json:"container"`
	// Publish the Tag Manager change at once; false leaves it for a person
	// to publish. Default true.
	Publish *bool `json:"publish"`
}

type setupJob struct {
	BrandID       string `json:"brand_id"`
	Step          string `json:"step"`
	Method        string `json:"method"`
	IntegrationID string `json:"integration_id"`
	Container     string `json:"container,omitempty"`
	Publish       bool   `json:"publish"`
	ActorID       string `json:"actor_id,omitempty"`
	ActorType     string `json:"actor_type,omitempty"`
}

// setupConnection checks the connection is the brand's and can do method.
func (s *Server) setupConnection(ctx context.Context, ws, brand, id, capability string) error {
	if s.integrations == nil || !validUUID(id) {
		return errInvalid("integration_id is the id of a connection to " + map[string]string{connectors.CapTagInstall: "Google Tag Manager", connectors.CapDNSWrite: "your DNS host"}[capability])
	}
	all, err := s.integrations.List(ctx, s.pool, ws, "")
	if err != nil {
		return err
	}
	for _, i := range all {
		if i.ID != id {
			continue
		}
		d, _ := s.integrations.Registry.Definition(i.Connector)
		if !d.Has(capability) {
			return errInvalid(d.Name + " can't do this step")
		}
		if !i.Serves(brand, all) {
			return errInvalid("that connection isn't used by this brand; share it with the brand on the Connectors page")
		}
		return nil
	}
	return errInvalid("no connection " + id + " in this workspace")
}

func setInstall(ctx context.Context, q jobs.Querier, ws, brand, step string, run setupInstallRun) error {
	b, _ := json.Marshal(map[string]setupInstallRun{step: run})
	_, err := q.Exec(ctx, `
		INSERT INTO brand_setup (brand_id, workspace_id, installs) VALUES ($1, $2, $3)
		ON CONFLICT (brand_id) DO UPDATE SET installs = brand_setup.installs || EXCLUDED.installs, updated_at = now()`, brand, ws, b)
	return err
}

func (s *Server) installBrandSetup(w http.ResponseWriter, r *http.Request) {
	a, b, ok := s.setupBrandWrite(w, r)
	if !ok {
		return
	}
	var req setupInstallInput
	if !decode(w, r, &req) {
		return
	}
	ctx := r.Context()
	capability := ""
	switch {
	case req.Step == stepSiteScript && req.Method == "gtm":
		capability = connectors.CapTagInstall
		if req.Container == "" {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "container is required: one of GET .../setup/tag-containers")
			return
		}
	case req.Step == stepVerifySite && req.Method == "dns":
		capability = connectors.CapDNSWrite
	default:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "step and method are site_script with gtm, or verify_site with dns")
		return
	}
	if s.allocError(w, r, s.setupConnection(ctx, a.WorkspaceID, b.ID, req.IntegrationID, capability)) {
		return
	}
	publish := req.Publish == nil || *req.Publish
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := s.prepareSetupStep(ctx, tx, a, b, req.Step); err != nil {
			return err
		}
		if err := setInstall(ctx, tx, a.WorkspaceID, b.ID, req.Step, setupInstallRun{Method: req.Method, Status: "queued", At: time.Now().UTC()}); err != nil {
			return err
		}
		if _, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: jobSetupInstall, WorkspaceID: &a.WorkspaceID, MaxAttempts: 5,
			Payload: setupJob{BrandID: b.ID, Step: req.Step, Method: req.Method, IntegrationID: req.IntegrationID, Container: req.Container,
				Publish: publish, ActorID: a.ActorID, ActorType: a.ActorType}}); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "brand_setup.install_started", "brand", b.ID, map[string]any{
			"brand_id": b.ID, "step": req.Step, "method": req.Method, "integration_id": req.IntegrationID, "publish": publish})
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]bool{"queued": true})
}

// setupInstallJob runs a one-click install with the workspace's own
// connection, and records what happened on the checklist.
func (s *Server) setupInstallJob(ctx context.Context, job jobs.Job) error {
	var p setupJob
	if err := json.Unmarshal(job.Payload, &p); err != nil || job.WorkspaceID == nil || !validUUID(p.BrandID) || !validUUID(p.IntegrationID) {
		return fmt.Errorf("%w: bad payload", jobs.ErrPermanent)
	}
	ws := *job.WorkspaceID
	run := setupInstallRun{Method: p.Method, Status: "done", At: time.Now().UTC()}
	result, err := s.runSetupInstall(ctx, ws, p, &run)
	if err != nil {
		kind := connectors.KindOf(err)
		retry := (kind == connectors.ErrUnavailable || kind == connectors.ErrQuota) && job.Attempt < job.MaxAttempts
		var bad errInvalid
		if errors.As(err, &bad) {
			retry = false
		}
		if retry {
			return err
		}
		run.Status, run.Error = "failed", err.Error()
		if errors.As(err, &bad) {
			kind = "invalid"
		}
		track.Fail(ctx, s.log, track.Event{Name: "brand_setup.install_failed", WorkspaceID: ws, ActorType: "system", Surface: "job",
			Props: map[string]any{"step": p.Step, "method": p.Method}}, "install_"+kind, err)
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := setInstall(ctx, tx, ws, p.BrandID, p.Step, run); err != nil {
			return err
		}
		action := "brand_setup.installed"
		data := map[string]any{"brand_id": p.BrandID, "step": p.Step, "method": p.Method, "integration_id": p.IntegrationID}
		for k, v := range result {
			data[k] = v
		}
		if run.Status == "failed" {
			action = "brand_setup.install_failed"
		}
		return audit.Record(ctx, tx, audit.Event{WorkspaceID: ws, ActorID: p.ActorID, ActorType: firstNonEmpty(p.ActorType, "system"),
			Action: action, TargetType: "brand", TargetID: p.BrandID, BrandID: p.BrandID, Data: data})
	})
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func (s *Server) runSetupInstall(ctx context.Context, ws string, p setupJob, run *setupInstallRun) (map[string]any, error) {
	var website, name string
	if err := s.pool.QueryRow(ctx, `SELECT website, name FROM brands WHERE workspace_id = $1 AND id = $2`, ws, p.BrandID).Scan(&website, &name); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errInvalid("the brand is gone")
		}
		return nil, err
	}
	o, err := s.integrations.Open(ctx, s.pool, ws, p.IntegrationID)
	if errors.Is(err, connectors.ErrNotFound) {
		return nil, errInvalid("the connection is gone; connect it again")
	}
	if err != nil {
		return nil, err
	}
	switch p.Step {
	case stepSiteScript:
		tm, ok := o.Connector.(connectors.TagInstaller)
		if !ok {
			return nil, errInvalid(o.Connector.Definition().Name + " can't install tags")
		}
		key, err := s.setupSiteKeyFor(ctx, s.pool, ws, p.BrandID)
		if err != nil {
			return nil, err
		}
		if key == nil {
			return nil, errInvalid("the brand's site key was revoked; start again")
		}
		res, err := tm.InstallTag(ctx, o.Client, o.Creds, p.Container, "Outflare", s.siteSnippet(key.key, key.origins), p.Publish)
		if err != nil {
			return nil, err
		}
		run.Link, run.Published = res.Link, &res.Published
		return map[string]any{"published": res.Published, "updated": res.Updated}, nil
	case stepVerifySite:
		dns, ok := o.Connector.(connectors.DNSHost)
		if !ok {
			return nil, errInvalid(o.Connector.Definition().Name + " can't add DNS records")
		}
		site, err := s.setupSiteFor(ctx, s.pool, ws, p.BrandID, websiteHost(website))
		if err != nil {
			return nil, err
		}
		if site == nil {
			return nil, errInvalid("the brand's site is gone; start again")
		}
		pr := crawl.ProofsFor(site.startURL, site.host, site.token)
		res, err := dns.EnsureRecords(ctx, o.Client, o.Creds, []connectors.DNSRecord{{Type: "TXT", Name: pr.DNSName, Content: pr.DNSValue}})
		if err != nil {
			return nil, err
		}
		if _, err := s.pool.Exec(ctx, `UPDATE seo_sites SET verify_watch_until = now() + $2::interval, verify_checked_at = NULL WHERE id = $1`,
			site.id, fmt.Sprintf("%d seconds", int(verifyWatch.Seconds()))); err != nil {
			return nil, err
		}
		return map[string]any{"added": res.Added, "existing": res.Existing}, nil
	}
	return nil, errInvalid("unknown step")
}

// checkBrandSetup looks for the site's proof now, and keeps looking for
// three days when it isn't there yet.
func (s *Server) checkBrandSetup(w http.ResponseWriter, r *http.Request) {
	a, b, ok := s.setupBrandWrite(w, r)
	if !ok {
		return
	}
	var req setupStepInput
	if !decode(w, r, &req) {
		return
	}
	if req.Step != stepVerifySite {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "only verify_site can be checked; the website script ticks itself off on the first visit")
		return
	}
	ctx := r.Context()
	site, err := s.setupSiteFor(ctx, s.pool, a.WorkspaceID, b.ID, websiteHost(b.Website))
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if site == nil {
		writeError(w, r, http.StatusConflict, "conflict", "prepare the step first (POST .../setup/prepare with step verify_site)")
		return
	}
	if !site.verified {
		if _, err := s.verifySetupSite(ctx, a.WorkspaceID, site, a.Test, &a.principal); err != nil {
			s.internal(w, r, err)
			return
		}
		if _, err := s.pool.Exec(ctx, `UPDATE seo_sites SET verify_watch_until = greatest(coalesce(verify_watch_until, now()), now() + $2::interval) WHERE id = $1 AND verified_at IS NULL`,
			site.id, fmt.Sprintf("%d seconds", int(verifyWatch.Seconds()))); err != nil {
			s.internal(w, r, err)
			return
		}
	}
	s.getBrandSetup(w, r)
}

// verifySetupSite looks for a site's proof and records it when found. p is
// who asked; nil is the background check.
func (s *Server) verifySetupSite(ctx context.Context, ws string, site *setupSite, test bool, p *principal) (bool, error) {
	method, err := s.crawler.Check(ctx, test, ws, site.startURL, site.host, site.token)
	if errors.Is(err, crawl.ErrNotVerified) {
		_, err := s.pool.Exec(ctx, `UPDATE seo_sites SET verify_checked_at = now() WHERE id = $1`, site.id)
		return false, err
	}
	if err != nil {
		return false, err
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE seo_sites SET `+crawl.ProvedDefaults()+`, verified_at = now(), verify_method = $2, verify_checked_at = now(), verify_watch_until = NULL, updated_at = now()
			WHERE id = $1 AND verified_at IS NULL`, site.id, method)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		if p != nil {
			return record(ctx, tx, p, ws, "seo_site.verified", "seo_site", site.id, map[string]any{"method": method})
		}
		return audit.Record(ctx, tx, audit.Event{WorkspaceID: ws, ActorType: "system", Action: "seo_site.verified",
			TargetType: "seo_site", TargetID: site.id, Data: map[string]any{"method": method}})
	})
	return err == nil, err
}

// listTagContainers lists the Tag Manager containers a connection sees,
// the ones on the brand's website first.
func (s *Server) listTagContainers(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	b, ok := s.brandFor(w, r, a)
	if !ok {
		return
	}
	ctx := r.Context()
	id := r.URL.Query().Get("integration_id")
	if s.allocError(w, r, s.setupConnection(ctx, a.WorkspaceID, b.ID, id, connectors.CapTagInstall)) {
		return
	}
	o, err := s.integrations.Open(ctx, s.pool, a.WorkspaceID, id)
	if err != nil {
		connectorError(w, r, err)
		return
	}
	list, err := o.Connector.(connectors.TagInstaller).Containers(ctx, o.Client, o.Creds)
	if err != nil {
		connectorError(w, r, err)
		return
	}
	host := strings.TrimPrefix(websiteHost(b.Website), "www.")
	matches := func(c connectors.TagContainer) bool {
		for _, d := range c.Domains {
			d = strings.TrimPrefix(strings.ToLower(d), "www.")
			if host != "" && (d == host || strings.HasSuffix(host, "."+d)) {
				return true
			}
		}
		return false
	}
	slices.SortStableFunc(list, func(x, y connectors.TagContainer) int {
		switch mx, my := matches(x), matches(y); {
		case mx && !my:
			return -1
		case my && !mx:
			return 1
		}
		return strings.Compare(strings.ToLower(x.Name), strings.ToLower(y.Name))
	})
	out := make([]map[string]any, 0, len(list))
	for _, c := range list {
		out = append(out, map[string]any{"id": c.ID, "name": c.Name, "public_id": c.PublicID, "account": c.Account, "domains": c.Domains, "matches_website": matches(c)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out})
}

// RunSetup keeps looking for the proof of sites waiting on a DNS record,
// and works out install health, until ctx ends.
func (s *Server) RunSetup(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	var lastHealth time.Time
	for {
		s.watchSiteProofs(ctx)
		if time.Since(lastHealth) >= installHealthEvery {
			s.refreshInstallIssues(ctx)
			lastHealth = time.Now()
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// CheckInstalls runs one pass of RunSetup now.
func (s *Server) CheckInstalls(ctx context.Context) {
	s.watchSiteProofs(ctx)
	s.refreshInstallIssues(ctx)
}

// watchSiteProofs checks sites waiting for their proof: every minute for
// the first 15 minutes of the watch, then hourly.
func (s *Server) watchSiteProofs(ctx context.Context) {
	rows, err := s.pool.Query(ctx, `
		SELECT s.workspace_id::text, s.id::text, s.host, s.start_url, s.verify_token, w.test FROM seo_sites s JOIN workspaces w ON w.id = s.workspace_id
		WHERE s.verified_at IS NULL AND s.verify_watch_until > now()
		  AND (s.verify_checked_at IS NULL OR s.verify_checked_at < now() - CASE
		       WHEN now() < s.verify_watch_until - $1::interval + interval '15 minutes' THEN interval '50 seconds' ELSE interval '1 hour' END)
		ORDER BY s.verify_checked_at NULLS FIRST LIMIT 50`, fmt.Sprintf("%d seconds", int(verifyWatch.Seconds())))
	if err != nil {
		track.Fail(ctx, s.log, track.Event{Name: "brand_setup.verify_check_failed", ActorType: "system", Surface: "system"}, "internal", err)
		return
	}
	type due struct {
		ws   string
		site setupSite
		test bool
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (due, error) {
		var d due
		err := row.Scan(&d.ws, &d.site.id, &d.site.host, &d.site.startURL, &d.site.token, &d.test)
		return d, err
	})
	if err != nil {
		track.Fail(ctx, s.log, track.Event{Name: "brand_setup.verify_check_failed", ActorType: "system", Surface: "system"}, "internal", err)
		return
	}
	for _, d := range list {
		if _, err := s.verifySetupSite(ctx, d.ws, &d.site, d.test, nil); err != nil {
			track.Fail(ctx, s.log, track.Event{Name: "brand_setup.verify_check_failed", WorkspaceID: d.ws, ActorType: "system", Surface: "system"}, "internal", err)
		}
	}
}
