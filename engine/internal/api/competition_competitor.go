package api

import (
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/seosync"
)

// One competitor's search data, in full: their top keywords beside your
// position for each, their top pages, the sites linking to them (and
// whether each links to you too) and the newest pages in their sitemap.
// It reads only what is stored; "Read their search data" reads it now on
// the brand's own SEO connection (Ahrefs first), showing the cost first.
// Link gaps open the exact pages on one linking site that link to each
// competitor, read on demand the same way.

func (s *Server) routesCompetitionCompetitor(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/brands/{brand}/competition/{competitor}/search", s.onBrand(auth.ScopeSEORead, s.competitorSearch))
	mux.HandleFunc("POST /v1/brands/{brand}/competition/{competitor}/read", s.onBrand(auth.ScopeSEOManage, s.readCompetitorNow))
	mux.HandleFunc("GET /v1/workspaces/{ws}/seo-competitors/gaps/links", s.inBrand(auth.ScopeSEORead, s.linkGapPages))
	mux.HandleFunc("POST /v1/workspaces/{ws}/seo-competitors/gaps/links", s.inBrand(auth.ScopeSEOManage, s.readLinkGapPages))
}

type searchKeyword struct {
	Keyword  string   `json:"keyword"`
	Position *float64 `json:"position"`
	Volume   *float64 `json:"volume"`
	Traffic  *float64 `json:"traffic"`
	URL      string   `json:"url"`
	// YourPosition is where your site ranks for it, null when it doesn't.
	YourPosition *float64 `json:"your_position"`
	YourURL      string   `json:"your_url"`
}

type searchPage struct {
	URL      string   `json:"url"`
	Traffic  *float64 `json:"traffic"`
	Keywords *float64 `json:"keywords"`
}

type searchLinkingSite struct {
	Domain string   `json:"domain"`
	Rating *float64 `json:"rating"`
	Links  *float64 `json:"links"`
	// LinksToYou: the site links to yours too (your backlinks).
	LinksToYou bool `json:"links_to_you"`
}

type searchSitemapPage struct {
	URL       string    `json:"url"`
	Lastmod   string    `json:"lastmod"`
	FirstSeen time.Time `json:"first_seen_at"`
}

type searchSection[T any] struct {
	// ReadOn is the day it was read, null when it never was.
	ReadOn *string `json:"read_on"`
	Rows   []T     `json:"rows"`
}

type competitorSearchView struct {
	CompetitorID string                           `json:"competitor_id"`
	Host         string                           `json:"host"`
	Keywords     searchSection[searchKeyword]     `json:"keywords"`
	Pages        searchSection[searchPage]        `json:"pages"`
	LinkingSites searchSection[searchLinkingSite] `json:"linking_sites"`
	Sitemap      struct {
		Total int                 `json:"total"`
		Rows  []searchSitemapPage `json:"rows"`
	} `json:"sitemap"`
	// SEOConnection reads it on "Read their search data"; null when the
	// brand has no SEO connection.
	SEOConnection *string `json:"seo_integration_id"`
}

// competitorSearch is what is stored about one competitor's search.
func (s *Server) competitorSearch(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	c, ok := s.brandCompetitor(w, r, nil, false)
	if !ok {
		return
	}
	var host, siteHost, siteID string
	if err := s.pool.QueryRow(ctx, `SELECT c.host, t.host, t.id::text FROM seo_competitors c JOIN seo_sites t ON t.id = c.site_id AND t.workspace_id = c.workspace_id
		WHERE c.workspace_id = $1 AND c.id = $2`, a.WorkspaceID, c.id).Scan(&host, &siteHost, &siteID); err != nil {
		s.internal(w, r, err)
		return
	}
	out := competitorSearchView{CompetitorID: c.id, Host: host}
	out.Keywords.Rows, out.Pages.Rows, out.LinkingSites.Rows, out.Sitemap.Rows = []searchKeyword{}, []searchPage{}, []searchLinkingSite{}, []searchSitemapPage{}
	day := func(rows []snapRow) *string {
		if len(rows) == 0 {
			return nil
		}
		d := rows[0].day
		return &d
	}
	hosts := []string{host}
	theirs, err := s.latestRows(r, a.WorkspaceID, connectors.SEOKeywords, hosts)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	target := seosync.LinkTarget(siteHost)
	ours, err := s.latestRows(r, a.WorkspaceID, connectors.SEOKeywords, []string{target, "www." + target, hostOnly(siteHost)})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	mine := map[string]snapRow{}
	for _, o := range ours {
		k := strings.ToLower(o.key)
		if prev, ok := mine[k]; !ok || better(o.m.Position, prev.m.Position) {
			mine[k] = o
		}
	}
	out.Keywords.ReadOn = day(theirs)
	for _, t := range theirs {
		k := searchKeyword{Keyword: t.key, Position: t.m.Position, Volume: t.m.Volume, Traffic: t.m.Traffic, URL: t.url}
		if o, ok := mine[strings.ToLower(t.key)]; ok {
			k.YourPosition, k.YourURL = o.m.Position, o.url
		}
		out.Keywords.Rows = append(out.Keywords.Rows, k)
	}
	slices.SortFunc(out.Keywords.Rows, func(x, y searchKeyword) int {
		if d := cmpNum(y.Traffic, x.Traffic); d != 0 {
			return d
		}
		if d := cmpNum(y.Volume, x.Volume); d != 0 {
			return d
		}
		return strings.Compare(x.Keyword, y.Keyword)
	})
	pages, err := s.latestRows(r, a.WorkspaceID, connectors.SEOPages, hosts)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out.Pages.ReadOn = day(pages)
	for _, p := range pages {
		u := p.url
		if u == "" {
			u = p.key
		}
		out.Pages.Rows = append(out.Pages.Rows, searchPage{URL: u, Traffic: p.m.OrganicTraffic, Keywords: p.m.OrganicKeywords})
	}
	slices.SortFunc(out.Pages.Rows, func(x, y searchPage) int {
		if d := cmpNum(y.Traffic, x.Traffic); d != 0 {
			return d
		}
		return strings.Compare(x.URL, y.URL)
	})
	links, err := s.latestRows(r, a.WorkspaceID, connectors.SEOBacklinks, hosts)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT source_domain FROM backlinks WHERE workspace_id = $1 AND site_id = $2 AND status = 'live'`, a.WorkspaceID, siteID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	have, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out.LinkingSites.ReadOn = day(links)
	for _, l := range links {
		d := strings.TrimPrefix(strings.ToLower(l.key), "www.")
		out.LinkingSites.Rows = append(out.LinkingSites.Rows, searchLinkingSite{Domain: d, Rating: l.m.DomainRating, Links: l.m.Backlinks,
			LinksToYou: slices.Contains(have, d) || slices.Contains(have, "www."+d)})
	}
	slices.SortFunc(out.LinkingSites.Rows, func(x, y searchLinkingSite) int {
		if d := cmpNum(y.Rating, x.Rating); d != 0 {
			return d
		}
		return strings.Compare(x.Domain, y.Domain)
	})
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM competitor_pages WHERE workspace_id = $1 AND competitor_id = $2 AND gone_at IS NULL`,
		a.WorkspaceID, c.id).Scan(&out.Sitemap.Total); err != nil {
		s.internal(w, r, err)
		return
	}
	rows, err = s.pool.Query(ctx, `SELECT url, lastmod, first_seen_at FROM competitor_pages WHERE workspace_id = $1 AND competitor_id = $2 AND gone_at IS NULL
		ORDER BY first_seen_at DESC, lastmod DESC, url LIMIT 25`, a.WorkspaceID, c.id)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var p searchSitemapPage
	if _, err := pgx.ForEachRow(rows, []any{&p.URL, &p.Lastmod, &p.FirstSeen}, func() error {
		out.Sitemap.Rows = append(out.Sitemap.Rows, p)
		return nil
	}); err != nil {
		s.internal(w, r, err)
		return
	}
	if err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		out.SEOConnection, err = s.seoReaderFor(ctx, tx, a.WorkspaceID, a.Brand)
		return err
	}); err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

type competitorReadView struct {
	Queued       bool    `json:"queued"`
	CompetitorID string  `json:"competitor_id"`
	Connection   string  `json:"seo_integration_id"`
	Connector    string  `json:"connector"`
	Estimate     float64 `json:"estimate"`
	Unit         string  `json:"unit"`
	// Reports are the reports it reads; Fresh the ones it skips because
	// the connection read them in the last six days.
	Reports []string       `json:"reports"`
	Fresh   []string       `json:"fresh"`
	Budget  seosync.Budget `json:"budget"`
}

// readCompetitorNow reads one tracked competitor's search data now, or
// with estimate_only says what it would read and cost.
func (s *Server) readCompetitorNow(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	var body struct {
		EstimateOnly bool `json:"estimate_only"`
	}
	if r.ContentLength != 0 && !decode(w, r, &body) {
		return
	}
	c, ok := s.brandCompetitor(w, r, nil, false)
	if !ok {
		return
	}
	if c.status != "tracked" {
		writeError(w, r, http.StatusConflict, "conflict", "only a tracked competitor is read; track it first")
		return
	}
	var host string
	if err := s.pool.QueryRow(ctx, `SELECT host FROM seo_competitors WHERE workspace_id = $1 AND id = $2`, a.WorkspaceID, c.id).Scan(&host); err != nil {
		s.internal(w, r, err)
		return
	}
	var id *string
	if err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) (err error) {
		id, err = s.seoReaderFor(ctx, tx, a.WorkspaceID, a.Brand)
		return err
	}); err != nil {
		s.internal(w, r, err)
		return
	}
	if id == nil {
		writeError(w, r, http.StatusConflict, "connector_needed", "reading a competitor's search data needs your own Ahrefs or DataForSEO connection: add one on the Connectors page")
		return
	}
	o, err := s.integrations.Open(ctx, s.pool, a.WorkspaceID, *id)
	if err != nil {
		connectorError(w, r, err)
		return
	}
	reader, ok := o.Connector.(connectors.SEOReader)
	if !ok {
		writeError(w, r, http.StatusConflict, "connector_needed", "that connection doesn't read search data")
		return
	}
	plan, err := seosync.PlanCompetitorRead(ctx, s.pool, o, reader, host)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out := competitorReadView{CompetitorID: c.id, Connection: o.ID, Connector: o.Connector.Definition().ID, Estimate: plan.Estimate,
		Unit: plan.Unit, Reports: plan.Reports, Fresh: plan.Fresh, Budget: plan.Budget}
	if body.EstimateOnly {
		writeJSON(w, http.StatusOK, out)
		return
	}
	if len(plan.Reports) == 0 {
		out.Queued = true // everything is fresh: nothing to read or pay for
		writeJSON(w, http.StatusAccepted, out)
		return
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := seosync.EnqueueCompetitorRead(ctx, tx, a.WorkspaceID, o.ID, c.id); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "seo_competitor.read_requested", "seo_competitor", c.id, map[string]any{
			"brand_id": a.Brand, "connector": out.Connector, "reports": len(plan.Reports), "estimate": plan.Estimate, "unit": plan.Unit,
		})
	})
	if s.allocError(w, r, err) {
		return
	}
	out.Queued = true
	writeJSON(w, http.StatusAccepted, out)
}

type linkGapPage struct {
	SourceURL string   `json:"source_url"`
	Title     string   `json:"title"`
	TargetURL string   `json:"target_url"`
	Anchor    string   `json:"anchor"`
	Nofollow  bool     `json:"nofollow"`
	Rating    *float64 `json:"rating"`
	URLRating *float64 `json:"url_rating"`
	FirstSeen *string  `json:"first_seen"`
}

type linkGapCompetitor struct {
	CompetitorID string `json:"competitor_id"`
	Host         string `json:"host"`
	// ReadAt is when its links from the domain were last read; null when
	// they never were (only the domain is known).
	ReadAt *time.Time    `json:"read_at"`
	Pages  []linkGapPage `json:"pages"`
}

type linkGapPagesView struct {
	SiteID      string              `json:"site_id"`
	Domain      string              `json:"domain"`
	Competitors []linkGapCompetitor `json:"competitors"`
}

// linkGapRows is how many links one competitor's read from a domain keeps.
const linkGapRows = 100

func gapDomain(raw string) string {
	d := seosync.Host(raw)
	return strings.TrimPrefix(d, "www.")
}

// linkGapCompetitors are the site's tracked competitors whose latest
// linking domains include domain, for one link gap.
func (s *Server) linkGapCompetitors(w http.ResponseWriter, r *http.Request, a *access, siteID, domain string) ([]linkGapCompetitor, bool) {
	ctx := r.Context()
	rows, err := s.pool.Query(ctx, `SELECT id::text, host FROM seo_competitors WHERE workspace_id = $1 AND site_id = $2 AND status = 'tracked' ORDER BY host`, a.WorkspaceID, siteID)
	if err != nil {
		s.internal(w, r, err)
		return nil, false
	}
	type comp struct{ id, host string }
	comps, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (comp, error) {
		var c comp
		return c, row.Scan(&c.id, &c.host)
	})
	if err != nil {
		s.internal(w, r, err)
		return nil, false
	}
	hosts := make([]string, 0, len(comps))
	for _, c := range comps {
		hosts = append(hosts, c.host)
	}
	theirs, err := s.latestRows(r, a.WorkspaceID, connectors.SEOBacklinks, hosts)
	if err != nil {
		s.internal(w, r, err)
		return nil, false
	}
	links := map[string]bool{}
	for _, t := range theirs {
		if gapDomain(t.key) == domain {
			links[t.host] = true
		}
	}
	out := []linkGapCompetitor{}
	for _, c := range comps {
		if links[c.host] {
			out = append(out, linkGapCompetitor{CompetitorID: c.id, Host: c.host, Pages: []linkGapPage{}})
		}
	}
	return out, true
}

func (s *Server) linkGapSite(w http.ResponseWriter, r *http.Request, a *access, siteID string) (seoSite, bool) {
	if !validUUID(siteID) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "site_id is an SEO site's id")
		return seoSite{}, false
	}
	sites, err := s.loadSEOSites(r.Context(), a.WorkspaceID, &siteID, brandSel{in: a.Brands})
	if err != nil {
		s.internal(w, r, err)
		return seoSite{}, false
	}
	if len(sites) != 1 {
		writeError(w, r, http.StatusNotFound, "not_found", "site not found")
		return seoSite{}, false
	}
	return sites[0], true
}

// fillLinkGapPages adds each competitor's stored links from domain: any
// read of who links to the competitor kept them.
func (s *Server) fillLinkGapPages(r *http.Request, ws, domain string, comps []linkGapCompetitor) error {
	for i := range comps {
		rows, err := s.pool.Query(r.Context(), `
			SELECT DISTINCT ON (x.source_url, x.target_url) x.source_url, x.title, x.target_url, x.anchor, x.nofollow, x.source_rating, x.url_rating,
				x.first_seen::text, l.created_at
			FROM link_read_rows x JOIN link_reads l ON l.id = x.read_id AND l.workspace_id = x.workspace_id
			WHERE x.workspace_id = $1 AND l.status = 'done' AND l.scope = 'domain' AND l.target = ANY($2)
			  AND (x.source_domain = $3 OR x.source_domain LIKE '%.' || $3) AND NOT x.lost
			ORDER BY x.source_url, x.target_url, l.created_at DESC`, ws, []string{comps[i].Host, "www." + comps[i].Host}, domain)
		if err != nil {
			return err
		}
		var p linkGapPage
		var at time.Time
		if _, err := pgx.ForEachRow(rows, []any{&p.SourceURL, &p.Title, &p.TargetURL, &p.Anchor, &p.Nofollow, &p.Rating, &p.URLRating, &p.FirstSeen, &at}, func() error {
			comps[i].Pages = append(comps[i].Pages, p)
			if comps[i].ReadAt == nil || at.After(*comps[i].ReadAt) {
				t := at
				comps[i].ReadAt = &t
			}
			return nil
		}); err != nil {
			return err
		}
		slices.SortFunc(comps[i].Pages, func(x, y linkGapPage) int {
			if d := cmpNum(y.Rating, x.Rating); d != 0 {
				return d
			}
			return strings.Compare(x.SourceURL, y.SourceURL)
		})
	}
	return nil
}

// linkGapPages are the pages on one linking site that link to each
// competitor, from what is stored.
func (s *Server) linkGapPages(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	q := r.URL.Query()
	site, ok := s.linkGapSite(w, r, a, q.Get("site_id"))
	if !ok {
		return
	}
	domain := gapDomain(q.Get("domain"))
	if domain == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "domain is the linking site, like example.com")
		return
	}
	comps, ok := s.linkGapCompetitors(w, r, a, site.ID, domain)
	if !ok {
		return
	}
	if err := s.fillLinkGapPages(r, a.WorkspaceID, domain, comps); err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, linkGapPagesView{SiteID: site.ID, Domain: domain, Competitors: comps})
}

// readLinkGapPages reads now, on the workspace's own SEO connection, the
// links from one site to each competitor it links to, or with
// estimate_only says what that would cost. A copy under a day old is
// reused.
func (s *Server) readLinkGapPages(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	var in struct {
		SiteID        string `json:"site_id"`
		Domain        string `json:"domain"`
		IntegrationID string `json:"integration_id"`
		EstimateOnly  bool   `json:"estimate_only"`
	}
	if !decode(w, r, &in) {
		return
	}
	site, ok := s.linkGapSite(w, r, a, in.SiteID)
	if !ok {
		return
	}
	domain := gapDomain(in.Domain)
	if domain == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "domain is the linking site, like example.com")
		return
	}
	if in.IntegrationID != "" && !validUUID(in.IntegrationID) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "integration_id is an SEO connection's id")
		return
	}
	if !a.writes(site.BrandID) {
		writeError(w, r, http.StatusForbidden, "forbidden", string(errSharedRecord))
		return
	}
	comps, ok := s.linkGapCompetitors(w, r, a, site.ID, domain)
	if !ok {
		return
	}
	if len(comps) == 0 {
		writeError(w, r, http.StatusNotFound, "not_found", "no tracked competitor is known to get links from that site")
		return
	}
	o, reader, ok := s.linkReader(w, r, a.WorkspaceID, in.IntegrationID)
	if !ok {
		return
	}
	reqs := make([]seosync.LinkReadRequest, 0, len(comps))
	actor := a.ActorID
	var estimate float64
	var budget seosync.Budget
	for _, c := range comps {
		id := c.CompetitorID
		req := seosync.LinkReadRequest{Target: c.Host, From: domain, Limit: linkGapRows, SiteID: &site.ID, BrandID: site.BrandID,
			Reason: "requested", CompetitorID: &id, ActorID: &actor, Rec: s.prospectRecorder(a)}
		est, b, err := seosync.EstimateLinkRead(ctx, s.pool, o, reader, req)
		if err != nil {
			s.seoFailed(w, r, o, err)
			return
		}
		estimate, budget = estimate+est, b
		reqs = append(reqs, req)
	}
	if in.EstimateOnly {
		writeJSON(w, http.StatusOK, map[string]any{"domain": domain, "competitors": len(reqs), "integration_id": o.ID,
			"connector": o.Connector.Definition().ID, "estimate": estimate, "budget": budget})
		return
	}
	for _, req := range reqs {
		if _, err := seosync.ReadLinks(ctx, s.pool, o, reader, a.WorkspaceID, req); err != nil {
			s.seoFailed(w, r, o, err)
			return
		}
	}
	if err := s.fillLinkGapPages(r, a.WorkspaceID, domain, comps); err != nil {
		s.internal(w, r, err)
		return
	}
	now := time.Now().UTC()
	for i := range comps {
		comps[i].ReadAt = &now // read just now, even when it found nothing
	}
	writeJSON(w, http.StatusOK, linkGapPagesView{SiteID: site.ID, Domain: domain, Competitors: comps})
}
