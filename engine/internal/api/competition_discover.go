package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/ranks"
)

// Find more competitors: read the search results pages of the brand's
// keyword gaps (keywords tracked competitors rank in the top 20 for) on
// the workspace's own SERP connection, keep them, and suggest the domains
// that rank across several of them, to say Yes or No to. It runs by
// itself once a day; this asks for a batch now. A gap keyword's stored
// results are what its row on the Keyword gaps tab opens.

func (s *Server) routesCompetitionDiscover(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/brands/{brand}/competition/discover", s.onBrand(auth.ScopeSEOManage, s.discoverCompetitors))
	mux.HandleFunc("GET /v1/workspaces/{ws}/seo-competitors/results", s.inBrand(auth.ScopeSEORead, s.keywordResults))
}

type competitionDiscoverView struct {
	Queued bool `json:"queued"`
	// Sites is how many of the brand's SEO sites are being read.
	Sites int `json:"sites"`
	// SERPConnection is the connection reading the results pages.
	SERPConnection string `json:"serp_integration_id"`
	// Keywords are the gap keywords asked for, if any.
	Keywords []string `json:"keywords"`
}

func (s *Server) discoverCompetitors(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	var body struct {
		Keywords []string `json:"keywords"`
	}
	if r.ContentLength != 0 && !decode(w, r, &body) {
		return
	}
	if len(body.Keywords) > ranks.GapKeywordsPerRun {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "ask for at most 25 keywords at a time")
		return
	}
	keywords := []string{}
	for _, k := range body.Keywords {
		if k = strings.TrimSpace(k); k != "" && len(k) <= 200 {
			keywords = append(keywords, k)
		}
	}
	out := competitionDiscoverView{Keywords: keywords}
	var noSERP, noTracked bool
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT t.id::text FROM seo_sites t WHERE t.workspace_id = $1 AND t.brand_id = $2 AND t.status = 'active'
			AND EXISTS (SELECT 1 FROM seo_competitors c WHERE c.workspace_id = t.workspace_id AND c.site_id = t.id AND c.status = 'tracked')
			ORDER BY t.host`, a.WorkspaceID, a.Brand)
		if err != nil {
			return err
		}
		sites, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		if len(sites) == 0 {
			noTracked = true
			return nil
		}
		if s.integrations != nil {
			if out.SERPConnection, err = ranks.GapConnection(ctx, tx, s.integrations.Registry, a.WorkspaceID); err != nil {
				return err
			}
		}
		if out.SERPConnection == "" {
			noSERP = true
			return nil
		}
		for _, id := range sites {
			if _, err := ranks.EnqueueGapReads(ctx, tx, a.WorkspaceID, id, keywords, ""); err != nil {
				return err
			}
		}
		out.Sites = len(sites)
		return record(ctx, tx, &a.principal, a.WorkspaceID, "competition.discover_requested", "brand", a.Brand, map[string]any{
			"brand_id": a.Brand, "sites": out.Sites, "keywords": len(keywords),
		})
	})
	if s.allocError(w, r, err) {
		return
	}
	switch {
	case noTracked:
		writeError(w, r, http.StatusConflict, "conflict", "track a competitor first: more are found from the keywords tracked competitors rank for")
	case noSERP:
		writeError(w, r, http.StatusConflict, "connector_needed", "finding more competitors reads search results on your own SERP connection: connect DataForSEO or Geonode on the Connectors page")
	default:
		out.Queued = true
		writeJSON(w, http.StatusAccepted, out)
	}
}

type resultItem struct {
	Position int    `json:"position"`
	URL      string `json:"url"`
	Domain   string `json:"domain"`
	Title    string `json:"title"`
	// You: the result is on the site itself.
	You bool `json:"you"`
	// Competitor is the domain's row on the site's competitor list.
	Competitor *resultCompetitor `json:"competitor"`
}

type resultCompetitor struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

type keywordResultsView struct {
	SiteID  string  `json:"site_id"`
	Keyword string  `json:"keyword"`
	ReadAt  *string `json:"read_at"`
	Engine  string  `json:"engine"`
	Country string  `json:"country"`
	// Source is where the page came from: keyword_gaps (read for a keyword
	// gap) or rank_tracking (a tracked keyword's latest check, top 10).
	Source string       `json:"source"`
	Items  []resultItem `json:"items"`
	// Stale: some results were Google redirect links that couldn't be
	// followed and are left out; reading again replaces them.
	Stale bool `json:"stale"`
}

// keywordResults is the newest stored results page of one keyword for a
// site: a gap read, else its rank tracking check. Reads only what is
// stored; read_at is null when neither has read it.
func (s *Server) keywordResults(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	q := r.URL.Query()
	siteID, keyword := q.Get("site_id"), strings.ToLower(strings.TrimSpace(q.Get("keyword")))
	if !validUUID(siteID) || keyword == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "site_id is an SEO site's id and keyword the keyword")
		return
	}
	sites, err := s.loadSEOSites(ctx, a.WorkspaceID, &siteID, brandSel{in: a.Brands})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if len(sites) != 1 {
		writeError(w, r, http.StatusNotFound, "not_found", "site not found")
		return
	}
	out := keywordResultsView{SiteID: siteID, Keyword: keyword, Items: []resultItem{}}
	var raw []byte
	var readAt string
	err = s.pool.QueryRow(ctx, `SELECT src, to_char(at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'), engine, country, items FROM (
			SELECT 'keyword_gaps' AS src, read_at AS at, engine, country, items FROM keyword_serps
			WHERE workspace_id = $1 AND site_id = $2 AND keyword = $3
			UNION ALL
			SELECT 'rank_tracking', o.checked_at, o.engine, k.country, o.top FROM tracked_keywords k
			JOIN rank_observations o ON o.tracked_id = k.id AND o.workspace_id = k.workspace_id
			WHERE k.workspace_id = $1 AND k.site_id = $2 AND lower(k.keyword) = $3 AND o.top IS NOT NULL
		) x ORDER BY at DESC LIMIT 1`, a.WorkspaceID, siteID, keyword).Scan(&out.Source, &readAt, &out.Engine, &out.Country, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusOK, out)
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out.ReadAt = &readAt
	var items []connectors.SERPItem
	_ = json.Unmarshal(raw, &items)
	rows, err := s.pool.Query(ctx, `SELECT host, id::text, status FROM seo_competitors WHERE workspace_id = $1 AND site_id = $2`, a.WorkspaceID, siteID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	comps := map[string]resultCompetitor{}
	var host string
	var c resultCompetitor
	if _, err := pgx.ForEachRow(rows, []any{&host, &c.ID, &c.Status}, func() error { comps[host] = c; return nil }); err != nil {
		s.internal(w, r, err)
		return
	}
	target := strings.TrimPrefix(hostOnly(sites[0].Host), "www.")
	for _, it := range items {
		// Pages read before redirects were resolved: unwrap the ones that
		// carry the address, leave out the rest (reading again fixes them).
		if connectors.IsGoogleRedirect(it.URL) {
			u := connectors.UnwrapGoogleRedirect(it.URL)
			if u == "" {
				out.Stale = true
				continue
			}
			it.URL, it.Domain = u, connectors.ResultDomain(u)
		}
		d := strings.TrimPrefix(strings.ToLower(it.Domain), "www.")
		v := resultItem{Position: it.Position, URL: it.URL, Domain: it.Domain, Title: it.Title, You: d == target || strings.HasSuffix(d, "."+target)}
		if m, ok := comps[d]; ok {
			v.Competitor = &resultCompetitor{ID: m.ID, Status: m.Status}
		} else if m, ok := comps["www."+d]; ok {
			v.Competitor = &resultCompetitor{ID: m.ID, Status: m.Status}
		}
		out.Items = append(out.Items, v)
	}
	writeJSON(w, http.StatusOK, out)
}
