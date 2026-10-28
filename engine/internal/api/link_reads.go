package api

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/seosync"
)

// Link reads: who links to a page or domain that isn't the workspace's
// own, read from its own SEO connection (paid, within the connection's
// budget; estimate first) and kept, so the reports over them cost nothing.
// Link building settings hold the pitch cool-down and the weekly read of
// competitors' dead pages (package seosync).

func (s *Server) routesLinkReads(mux *http.ServeMux) {
	read, manage := auth.ScopeSEORead, auth.ScopeSEOManage
	mux.HandleFunc("GET /v1/workspaces/{ws}/link-building/settings", s.inWorkspace(read, s.getLinkSettings))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/link-building/settings", s.inWorkspace(manage, s.updateLinkSettings))
	mux.HandleFunc("GET /v1/workspaces/{ws}/link-reads", s.inBrand(read, s.listLinkReads))
	mux.HandleFunc("POST /v1/workspaces/{ws}/link-reads", s.inBrand(manage, s.readLinks))
	mux.HandleFunc("GET /v1/workspaces/{ws}/link-reads/dead-pages", s.inBrand(read, s.deadPagesReport))
	mux.HandleFunc("GET /v1/workspaces/{ws}/link-reads/top-pages", s.inBrand(read, s.topPagesReport))
	mux.HandleFunc("GET /v1/workspaces/{ws}/link-reads/{read}", s.inBrand(read, s.getLinkRead))
}

func (s *Server) getLinkSettings(w http.ResponseWriter, r *http.Request) {
	v, err := seosync.LoadLinkSettings(r.Context(), s.pool, accessFrom(r.Context()).WorkspaceID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) updateLinkSettings(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in struct {
		PitchCooldownDays   *int  `json:"pitch_cooldown_days"`
		WeeklyReads         *bool `json:"weekly_reads"`
		WeeklyBudgetPercent *int  `json:"weekly_budget_percent"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.PitchCooldownDays != nil && (*in.PitchCooldownDays < 0 || *in.PitchCooldownDays > 365) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "pitch_cooldown_days must be 0 to 365")
		return
	}
	if in.WeeklyBudgetPercent != nil && (*in.WeeklyBudgetPercent < 1 || *in.WeeklyBudgetPercent > 100) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "weekly_budget_percent must be 1 to 100")
		return
	}
	ctx := r.Context()
	before, err := seosync.LoadLinkSettings(ctx, s.pool, a.WorkspaceID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	after := before
	if in.PitchCooldownDays != nil {
		after.PitchCooldownDays = *in.PitchCooldownDays
	}
	if in.WeeklyReads != nil {
		after.WeeklyReads = *in.WeeklyReads
	}
	if in.WeeklyBudgetPercent != nil {
		after.WeeklyBudgetPercent = *in.WeeklyBudgetPercent
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE link_building_settings SET pitch_cooldown_days = $2, weekly_reads = $3, weekly_budget_percent = $4,
				updated_by = $5, updated_at = now() WHERE workspace_id = $1`,
			a.WorkspaceID, after.PitchCooldownDays, after.WeeklyReads, after.WeeklyBudgetPercent, nullIfEmpty(a.ActorID)); err != nil {
			return err
		}
		data := map[string]any{"pitch_cooldown_days": after.PitchCooldownDays, "weekly_reads": after.WeeklyReads,
			"weekly_budget_percent": after.WeeklyBudgetPercent}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "link_building.settings_updated", "workspace", a.WorkspaceID, data, data)
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	v, err := seosync.LoadLinkSettings(ctx, s.pool, a.WorkspaceID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

type linkReadView struct {
	ID               string     `json:"id"`
	SiteID           *string    `json:"site_id"`
	BrandID          *string    `json:"brand_id"`
	IntegrationID    *string    `json:"integration_id"`
	Connector        string     `json:"connector"`
	Target           string     `json:"target"`
	Scope            string     `json:"scope"`
	Reason           string     `json:"reason"`
	CompetitorID     *string    `json:"competitor_id"`
	Status           string     `json:"status"`
	ErrorCode        *string    `json:"error_code"`
	TotalLinks       *float64   `json:"total_links"`
	Links            int        `json:"links"`
	ReferringDomains int        `json:"referring_domains"`
	Cost             float64    `json:"cost"`
	Unit             string     `json:"unit"`
	CreatedAt        time.Time  `json:"created_at"`
	Rows             *[]linkRow `json:"rows,omitempty"`
}

type linkRow struct {
	SourceURL     string   `json:"source_url"`
	SourceDomain  string   `json:"source_domain"`
	TargetURL     string   `json:"target_url"`
	Anchor        string   `json:"anchor"`
	Nofollow      bool     `json:"nofollow"`
	Lost          bool     `json:"lost"`
	SourceRating  *float64 `json:"source_rating"`
	URLRating     *float64 `json:"url_rating"`
	DomainTraffic *float64 `json:"domain_traffic"`
	SpamScore     *int     `json:"spam_score"`
	Title         string   `json:"title"`
	FirstSeen     string   `json:"first_seen"`
}

const linkReadCols = `x.id::text, x.site_id::text, x.brand_id::text, x.integration_id::text, x.connector, x.target, x.scope, x.reason,
	x.competitor_id::text, x.status, x.error_code, x.total_links, x.links, x.referring_domains, x.cost, x.unit, x.created_at`

func scanLinkRead(row pgx.Row) (linkReadView, error) {
	var v linkReadView
	err := row.Scan(&v.ID, &v.SiteID, &v.BrandID, &v.IntegrationID, &v.Connector, &v.Target, &v.Scope, &v.Reason, &v.CompetitorID,
		&v.Status, &v.ErrorCode, &v.TotalLinks, &v.Links, &v.ReferringDomains, &v.Cost, &v.Unit, &v.CreatedAt)
	return v, err
}

func (s *Server) listLinkReads(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	sel, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	site, ok := siteParam(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	args := []any{a.WorkspaceID, site, q.Get("target"), q.Get("reason")}
	if v := args[3].(string); v != "" && v != "requested" && v != "dead_page" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "reason is requested or dead_page")
		return
	}
	where := `x.workspace_id = $1 AND ($2 = '' OR x.site_id = NULLIF($2, '')::uuid) AND ($3 = '' OR x.target = $3) AND ($4 = '' OR x.reason = $4)` +
		sel.sql("x.brand_id", &args)
	limit, err := limitParam(r, 50, 200)
	if s.allocError(w, r, err) {
		return
	}
	offset, ok := offsetParam(w, r)
	if !ok {
		return
	}
	args = append(args, limit+1, offset)
	rows, err := s.pool.Query(r.Context(), `SELECT `+linkReadCols+` FROM link_reads x WHERE `+where+`
		ORDER BY x.created_at DESC, x.id LIMIT $`+strconv.Itoa(len(args)-1)+` OFFSET $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (linkReadView, error) { return scanLinkRead(row) })
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
	if list == nil {
		list = []linkReadView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "next_offset": next})
}

func (s *Server) getLinkRead(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("read")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "link read not found")
		return
	}
	v, err := s.loadLinkRead(r.Context(), a, id)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if v == nil {
		writeError(w, r, http.StatusNotFound, "not_found", "link read not found")
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// loadLinkRead is a read with its rows, strongest first, or nil.
func (s *Server) loadLinkRead(ctx context.Context, a *access, id string) (*linkReadView, error) {
	args := []any{a.WorkspaceID, id}
	v, err := scanLinkRead(s.pool.QueryRow(ctx, `SELECT `+linkReadCols+` FROM link_reads x WHERE x.workspace_id = $1 AND x.id = $2`+
		brandSel{in: a.Brands}.sql("x.brand_id", &args), args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT source_url, source_domain, target_url, anchor, nofollow, lost, source_rating, url_rating,
			domain_traffic, spam_score, title, first_seen
		FROM link_read_rows WHERE read_id = $1 AND workspace_id = $2
		ORDER BY source_rating DESC NULLS LAST, url_rating DESC NULLS LAST, source_url`, id, a.WorkspaceID)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (linkRow, error) {
		var l linkRow
		return l, row.Scan(&l.SourceURL, &l.SourceDomain, &l.TargetURL, &l.Anchor, &l.Nofollow, &l.Lost, &l.SourceRating, &l.URLRating,
			&l.DomainTraffic, &l.SpamScore, &l.Title, &l.FirstSeen)
	})
	if err != nil {
		return nil, err
	}
	if list == nil {
		list = []linkRow{}
	}
	v.Rows = &list
	return &v, nil
}

// linkReader opens the workspace's SEO connection that reads single links:
// integrationID when set, else the first connected one. Without one it
// answers 409 connector_needed: the workspace connects its own.
func (s *Server) linkReader(w http.ResponseWriter, r *http.Request, ws, integrationID string) (*connectors.Opened, connectors.SEOReader, bool) {
	if !s.needIntegrations(w, r) {
		return nil, nil, false
	}
	var ids []string
	for _, d := range s.integrations.Registry.All() {
		if c, ok := s.integrations.Registry.Get(d.ID); ok {
			if sr, ok := c.(connectors.SEOReader); ok && connectors.SupportsSEO(sr, connectors.SEOLinks) {
				ids = append(ids, d.ID)
			}
		}
	}
	var id string
	err := s.pool.QueryRow(r.Context(), `SELECT id::text FROM integrations
		WHERE workspace_id = $1 AND connector = ANY($2) AND status = 'connected' AND ($3 = '' OR id::text = $3)
		ORDER BY connector = 'ahrefs' DESC, created_at LIMIT 1`, ws, ids, integrationID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		if integrationID != "" {
			writeError(w, r, http.StatusNotFound, "not_found", "integration_id is not a connected SEO connection that reads links")
			return nil, nil, false
		}
		writeError(w, r, http.StatusConflict, "connector_needed",
			"reading who links to a page needs the workspace's own Ahrefs or DataForSEO connection; add one under Connectors")
		return nil, nil, false
	}
	if err != nil {
		s.internal(w, r, err)
		return nil, nil, false
	}
	o, err := s.integrations.Open(r.Context(), s.pool, ws, id)
	if err != nil {
		connectorError(w, r, err)
		return nil, nil, false
	}
	return o, o.Connector.(connectors.SEOReader), true
}

func (s *Server) readLinks(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in struct {
		Target        string `json:"target"`
		SiteID        string `json:"site_id"`
		IntegrationID string `json:"integration_id"`
		Limit         int    `json:"limit"`
		Fresh         bool   `json:"fresh"`
		EstimateOnly  bool   `json:"estimate_only"`
	}
	if !decode(w, r, &in) {
		return
	}
	bad := func(msg string) { writeError(w, r, http.StatusBadRequest, "invalid_request", msg) }
	req := seosync.LinkReadRequest{Reason: "requested", Limit: in.Limit, Fresh: in.Fresh}
	// A URL with a path is one page; anything else is a whole domain.
	if page, ok := pageURL(in.Target); ok {
		req.Target, req.Page = page, true
	} else if h := seosync.Host(in.Target); h != "" {
		req.Target = h
	} else {
		bad("target is a page's URL, like https://rival.example/guide, or a domain, like rival.example")
		return
	}
	if in.Limit < 0 || in.Limit > seosync.MaxLinkReadRows {
		bad("limit is 1 to " + strconv.Itoa(seosync.MaxLinkReadRows))
		return
	}
	if in.IntegrationID != "" && !validUUID(in.IntegrationID) {
		bad("integration_id is an SEO connection's id")
		return
	}
	ctx := r.Context()
	if in.SiteID != "" {
		if !validUUID(in.SiteID) {
			bad("site_id is an SEO site's id")
			return
		}
		sites, err := s.loadSEOSites(ctx, a.WorkspaceID, &in.SiteID, brandSel{in: a.Brands})
		if err != nil {
			s.internal(w, r, err)
			return
		}
		if len(sites) != 1 {
			writeError(w, r, http.StatusNotFound, "not_found", "site not found")
			return
		}
		req.SiteID, req.BrandID = &sites[0].ID, sites[0].BrandID
	}
	if !a.writes(nil, req.BrandID) {
		writeError(w, r, http.StatusForbidden, "forbidden", "brand-limited callers read links for one of their brands' sites; pass site_id")
		return
	}
	o, reader, ok := s.linkReader(w, r, a.WorkspaceID, in.IntegrationID)
	if !ok {
		return
	}
	est, budget, err := seosync.EstimateLinkRead(ctx, s.pool, o, reader, req)
	if err != nil {
		s.seoFailed(w, r, o, err)
		return
	}
	if in.EstimateOnly {
		writeJSON(w, http.StatusOK, map[string]any{"target": req.Target, "scope": map[bool]string{true: "page", false: "domain"}[req.Page],
			"integration_id": o.ID, "connector": o.Connector.Definition().ID, "estimate": est, "budget": budget})
		return
	}
	actor := a.ActorID
	req.ActorID = &actor
	req.Rec = s.prospectRecorder(a)
	res, err := seosync.ReadLinks(ctx, s.pool, o, reader, a.WorkspaceID, req)
	if err != nil {
		s.seoFailed(w, r, o, err)
		return
	}
	v, err := s.loadLinkRead(ctx, a, res.ID)
	if err != nil || v == nil {
		s.internal(w, r, errors.Join(err, errNotFound))
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

// pageURL is raw as a page's URL when it names a page (a path or a
// query) rather than a whole site.
func pageURL(raw string) (string, bool) {
	page, _ := seosync.ProspectPage(raw)
	u, err := url.Parse(page)
	if page == "" || err != nil || (u.Path == "/" && u.RawQuery == "") {
		return "", false
	}
	return page, true
}

type deadPageView struct {
	URL              string     `json:"url"`
	CompetitorID     string     `json:"competitor_id"`
	Host             string     `json:"host"`
	SiteID           string     `json:"site_id"`
	GoneAt           time.Time  `json:"gone_at"`
	ReadID           *string    `json:"read_id"`
	ReadAt           *time.Time `json:"read_at"`
	Links            *int       `json:"links"`
	ReferringDomains *int       `json:"referring_domains"`
	BestRating       *float64   `json:"best_rating"`
}

// deadPagesReport lists pages gone from tracked competitors' sitemaps, with
// who links to them from the latest read of each: the ones with the most
// linking domains first, then the ones not read yet.
func (s *Server) deadPagesReport(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	sel, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	site, ok := siteParam(w, r)
	if !ok {
		return
	}
	days := 90
	if v := r.URL.Query().Get("days"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 365 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "days is 1 to 365")
			return
		}
		days = n
	}
	limit, err := limitParam(r, 100, 500)
	if s.allocError(w, r, err) {
		return
	}
	offset, ok := offsetParam(w, r)
	if !ok {
		return
	}
	args := []any{a.WorkspaceID, site, days}
	where := `c.workspace_id = $1 AND ($2 = '' OR c.site_id = NULLIF($2, '')::uuid) AND c.status = 'tracked'
		AND p.gone_at > now() - make_interval(days => $3)` + sel.sql("c.brand_id", &args)
	args = append(args, limit+1, offset)
	rows, err := s.pool.Query(r.Context(), `
		SELECT p.url, c.id::text, c.host, c.site_id::text, p.gone_at, x.id::text, x.created_at, x.links, x.referring_domains,
			(SELECT max(source_rating) FROM link_read_rows WHERE read_id = x.id)
		FROM competitor_pages p JOIN seo_competitors c ON c.id = p.competitor_id
		LEFT JOIN LATERAL (SELECT id, created_at, links, referring_domains FROM link_reads
			WHERE workspace_id = $1 AND target = p.url AND scope = 'page' AND status = 'done' ORDER BY created_at DESC LIMIT 1) x ON true
		WHERE `+where+`
		ORDER BY x.referring_domains DESC NULLS LAST, p.gone_at DESC, p.url
		LIMIT $`+strconv.Itoa(len(args)-1)+` OFFSET $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (deadPageView, error) {
		var v deadPageView
		return v, row.Scan(&v.URL, &v.CompetitorID, &v.Host, &v.SiteID, &v.GoneAt, &v.ReadID, &v.ReadAt, &v.Links, &v.ReferringDomains, &v.BestRating)
	})
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
	if list == nil {
		list = []deadPageView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "next_offset": next})
}

type topPageView struct {
	URL              string   `json:"url"`
	Links            int      `json:"links"`
	ReferringDomains int      `json:"referring_domains"`
	BestRating       *float64 `json:"best_rating"`
}

// topPagesReport ranks a domain's pages by the domains linking to them, in
// its latest stored read (POST .../link-reads with the domain): the pages
// worth making a better version of.
func (s *Server) topPagesReport(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	target := seosync.Host(r.URL.Query().Get("target"))
	if target == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "target is a domain that was read, like rival.example")
		return
	}
	limit, err := limitParam(r, 50, 500)
	if s.allocError(w, r, err) {
		return
	}
	offset, ok := offsetParam(w, r)
	if !ok {
		return
	}
	args := []any{a.WorkspaceID, target}
	brand := brandSel{in: a.Brands}.sql("brand_id", &args)
	var readID *string
	var readAt *time.Time
	if err := s.pool.QueryRow(r.Context(), `SELECT id::text, created_at FROM link_reads
		WHERE workspace_id = $1 AND target = $2 AND scope = 'domain' AND status = 'done'`+brand+`
		ORDER BY created_at DESC LIMIT 1`, args...).Scan(&readID, &readAt); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		s.internal(w, r, err)
		return
	}
	list := []topPageView{}
	if readID != nil {
		rows, err := s.pool.Query(r.Context(), `SELECT target_url, count(*)::int, count(DISTINCT source_domain)::int, max(source_rating)
			FROM link_read_rows WHERE read_id = $1 AND workspace_id = $2 AND NOT lost
			GROUP BY target_url ORDER BY count(DISTINCT source_domain) DESC, max(source_rating) DESC NULLS LAST, target_url LIMIT $3 OFFSET $4`,
			*readID, a.WorkspaceID, limit+1, offset)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		list, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (topPageView, error) {
			var v topPageView
			return v, row.Scan(&v.URL, &v.Links, &v.ReferringDomains, &v.BestRating)
		})
		if err != nil {
			s.internal(w, r, err)
			return
		}
	}
	var next *int
	if len(list) > limit {
		list = list[:limit]
		n := offset + limit
		next = &n
	}
	writeJSON(w, http.StatusOK, map[string]any{"target": target, "read_id": readID, "read_at": readAt, "data": list, "next_offset": next})
}
