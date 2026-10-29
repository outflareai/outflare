package api

import (
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/seointel"
)

// A keyword target for each page of an SEO site. People and agents set
// the keyword a page should rank for, or accept the one Search Console
// suggests (the query that brings the page the most clicks). The site's
// latest crawl is checked against each target, and a keyword missing from
// the title, H1 or description is a fix on the workspace's fixes list
// (source seo_keywords), checked again after every crawl. Pages that
// compete for one search, or target the same keyword, are fixes too.

func (s *Server) routesSEOTargets(mux *http.ServeMux) {
	read, manage := auth.ScopeSEORead, auth.ScopeSEOManage
	mux.HandleFunc("GET /v1/workspaces/{ws}/seo-sites/{site}/targets", s.inBrand(read, s.listPageTargets))
	mux.HandleFunc("PUT /v1/workspaces/{ws}/seo-sites/{site}/targets", s.inBrand(manage, s.putPageTarget))
	mux.HandleFunc("POST /v1/workspaces/{ws}/seo-sites/{site}/targets/accept", s.inBrand(manage, s.acceptPageTargets))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/seo-sites/{site}/targets/{target}", s.inBrand(manage, s.updatePageTarget))
	mux.HandleFunc("DELETE /v1/workspaces/{ws}/seo-sites/{site}/targets/{target}", s.inBrand(manage, s.deletePageTarget))
	mux.HandleFunc("GET /v1/workspaces/{ws}/seo-sites/{site}/cannibalisation", s.inBrand(read, s.siteCannibalisation))
	mux.HandleFunc("GET /v1/workspaces/{ws}/seo-sites/{site}/internal-links", s.inBrand(read, s.siteInternalLinks))
}

// siteInternalLinks suggests internal links to add to the site's target
// pages, from its latest crawl.
func (s *Server) siteInternalLinks(w http.ResponseWriter, r *http.Request) {
	site, src, ok := s.searchSource(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	list, skipped, err := seointel.LinkSuggestions(ctx, s.pool, accessFrom(ctx).WorkspaceID, site.ID, site.Host, "", src)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if skipped == nil {
		skipped = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "not_crawled": skipped})
}

// siteCannibalisation lists the searches several of a site's pages compete
// for in Search Console, the most shown first, each with the page to keep.
func (s *Server) siteCannibalisation(w http.ResponseWriter, r *http.Request) {
	limit, err := limitParam(r, 50, 200)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "limit must be 1 to 200")
		return
	}
	site, src, ok := s.searchSource(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	brand := ""
	if site.BrandID != nil {
		if err := s.pool.QueryRow(ctx, `SELECT name FROM brands WHERE id = $1 AND workspace_id = $2`, *site.BrandID, accessFrom(ctx).WorkspaceID).Scan(&brand); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			s.internal(w, r, err)
			return
		}
	}
	list, err := seointel.Cannibalised(ctx, s.pool, accessFrom(ctx).WorkspaceID, site.ID, site.Host, brand, src, limit)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"source": src, "data": list})
}

type targetSearch struct {
	Clicks      int     `json:"clicks"`
	Impressions int     `json:"impressions"`
	Position    float64 `json:"position"`
}

type pageTargetView struct {
	ID        *string         `json:"id"`
	URL       string          `json:"url"`
	Keyword   string          `json:"keyword"`
	Secondary []string        `json:"secondary"`
	Status    string          `json:"status"`
	Origin    string          `json:"origin"`
	Checks    seointel.Checks `json:"checks"`
	// The keyword on this page in Search Console, over the 28 days to the
	// newest day it sent. nil when no property covers the site.
	Search    *targetSearch `json:"search"`
	CreatedAt *time.Time    `json:"created_at"`
	UpdatedAt *time.Time    `json:"updated_at"`
}

var targetStatuses = []string{"set", "suggested", "dismissed"}

// maxTargets is how many targets a site lists at once.
const maxTargets = 1000

// loadPageTargets reads a site's targets (one when id is set), checked
// against its latest crawl, with their Search Console numbers.
func (s *Server) loadPageTargets(r *http.Request, site *seoSite, src *seointel.Source, status, id string) ([]pageTargetView, error) {
	ctx := r.Context()
	ws := accessFrom(ctx).WorkspaceID
	integ, prop, newest := "", "", "1970-01-01"
	if src != nil && src.NewestDay != nil {
		integ, prop, newest = src.IntegrationID, src.Property, *src.NewestDay
	}
	list := []pageTargetView{}
	var urls []string
	rows, err := s.pool.Query(ctx, `
		SELECT t.id::text, t.url, t.keyword, t.secondary, t.status, t.origin, t.created_at, t.updated_at,
			sd.clicks, sd.impressions, sd.position
		FROM page_targets t
		LEFT JOIN LATERAL (
			SELECT COALESCE(sum(d.clicks), 0)::int AS clicks, COALESCE(sum(d.impressions), 0)::int AS impressions,
				COALESCE(sum(d.position * d.impressions) / NULLIF(sum(d.impressions), 0), 0) AS position
			FROM search_daily d
			WHERE d.workspace_id = $1 AND d.integration_id = NULLIF($3, '')::uuid AND d.site = $4 AND d.page = t.url AND d.query = t.keyword
			  AND d.day > $5::date - 28 AND d.day <= $5::date
		) sd ON $3 <> '' AND t.status = 'set'
		WHERE t.workspace_id = $1 AND t.site_id = $2 AND ($6 = '' OR t.status = $6) AND ($7 = '' OR t.id = NULLIF($7, '')::uuid)
		ORDER BY t.url LIMIT $8`, ws, site.ID, integ, prop, newest, status, id, maxTargets)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var v pageTargetView
		var tid string
		var created, updated time.Time
		var clicks, impressions *int
		var position *float64
		if err := rows.Scan(&tid, &v.URL, &v.Keyword, &v.Secondary, &v.Status, &v.Origin, &created, &updated, &clicks, &impressions, &position); err != nil {
			rows.Close()
			return nil, err
		}
		v.ID, v.CreatedAt, v.UpdatedAt = &tid, &created, &updated
		if clicks != nil {
			v.Search = &targetSearch{Clicks: *clicks, Impressions: *impressions, Position: *position}
		}
		if v.Secondary == nil {
			v.Secondary = []string{}
		}
		list = append(list, v)
		urls = append(urls, v.URL)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	crawlID, err := seointel.LatestCrawl(ctx, s.pool, ws, site.ID)
	if err != nil {
		return nil, err
	}
	pages, err := seointel.CrawledPages(ctx, s.pool, ws, crawlID, urls)
	if err != nil {
		return nil, err
	}
	for i := range list {
		if list[i].Status == "set" {
			list[i].Checks = seointel.Check(pages[list[i].URL], list[i].Keyword)
		}
	}
	return list, nil
}

func suggestedView(g seointel.Suggestion) pageTargetView {
	return pageTargetView{URL: g.URL, Keyword: g.Keyword, Secondary: []string{}, Status: "suggested", Origin: "suggested",
		Search: &targetSearch{Clicks: g.Clicks, Impressions: g.Impressions, Position: g.Position}}
}

func (s *Server) listPageTargets(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	if status != "" && !slices.Contains(targetStatuses, status) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status is "+strings.Join(targetStatuses, ", "))
		return
	}
	site, src, ok := s.searchSource(w, r)
	if !ok {
		return
	}
	list := []pageTargetView{}
	if status != "suggested" {
		got, err := s.loadPageTargets(r, site, src, status, "")
		if err != nil {
			s.internal(w, r, err)
			return
		}
		list = got
	}
	suggested := 0
	if status == "" || status == "suggested" {
		sug, err := seointel.Suggestions(r.Context(), s.pool, accessFrom(r.Context()).WorkspaceID, site.ID, site.Host, src, nil, maxTargets)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		for _, g := range sug {
			list = append(list, suggestedView(g))
		}
		suggested = len(sug)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "source": src, "suggested": suggested})
}

// targetURL checks that raw is a page of the site and returns it without
// its fragment.
func targetURL(site *seoSite, raw string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || len(raw) > 2000 {
		return "", false
	}
	if !slices.Contains(seointel.PageHosts(site.Host), strings.ToLower(u.Hostname())) {
		return "", false
	}
	u.Fragment = ""
	return u.String(), true
}

type targetInput struct {
	URL       *string   `json:"url"`
	Keyword   *string   `json:"keyword"`
	Secondary *[]string `json:"secondary"`
	Status    *string   `json:"status"`
}

// keywords checks a target's keyword and secondary keywords.
func (in targetInput) keywords(w http.ResponseWriter, r *http.Request) (kw *string, sec *[]string, ok bool) {
	if in.Keyword != nil {
		k := seointel.Keyword(*in.Keyword)
		if len(k) > seointel.MaxKeyword {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "keyword is at most 200 characters")
			return nil, nil, false
		}
		kw = &k
	}
	if in.Secondary != nil {
		if len(*in.Secondary) > 10 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "secondary has at most 10 keywords")
			return nil, nil, false
		}
		list := []string{}
		for _, x := range *in.Secondary {
			k := seointel.Keyword(x)
			if len(k) > seointel.MaxKeyword {
				writeError(w, r, http.StatusBadRequest, "invalid_request", "a secondary keyword is at most 200 characters")
				return nil, nil, false
			}
			if k != "" && !slices.Contains(list, k) {
				list = append(list, k)
			}
		}
		sec = &list
	}
	if in.Status != nil && *in.Status != "set" && *in.Status != "dismissed" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status is set or dismissed")
		return nil, nil, false
	}
	return kw, sec, true
}

// targetSite loads the site for a target write the caller may make.
func (s *Server) targetSite(w http.ResponseWriter, r *http.Request) (*seoSite, bool) {
	a := accessFrom(r.Context())
	site, ok := s.seoSiteFor(w, r, a)
	if !ok {
		return nil, false
	}
	if !a.writes(site.BrandID) {
		writeError(w, r, http.StatusForbidden, "forbidden", string(errSharedRecord))
		return nil, false
	}
	return site, true
}

// writeTarget answers a target write with the target as it is now.
func (s *Server) writeTarget(w http.ResponseWriter, r *http.Request, site *seoSite, id string) {
	src, err := seointel.SourceFor(r.Context(), s.pool, accessFrom(r.Context()).WorkspaceID, site.Host)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := s.loadPageTargets(r, site, src, "", id)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if len(list) != 1 {
		writeError(w, r, http.StatusNotFound, "not_found", "target not found")
		return
	}
	writeJSON(w, http.StatusOK, list[0])
}

func (s *Server) putPageTarget(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in targetInput
	if !decode(w, r, &in) {
		return
	}
	site, ok := s.targetSite(w, r)
	if !ok {
		return
	}
	if in.URL == nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "url is required")
		return
	}
	page, ok := targetURL(site, *in.URL)
	if !ok {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "url is a page of the site ("+site.Host+")")
		return
	}
	kw, sec, ok := in.keywords(w, r)
	if !ok {
		return
	}
	status := "set"
	if in.Status != nil {
		status = *in.Status
	}
	if status == "set" && (kw == nil || *kw == "") {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "keyword is required")
		return
	}
	keyword, secondary := "", []string{}
	if kw != nil {
		keyword = *kw
	}
	if sec != nil {
		secondary = *sec
	}
	ctx := r.Context()
	var id string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var createdBy *string
		if a.ActorID != "" {
			createdBy = &a.ActorID
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO page_targets (workspace_id, site_id, url, keyword, secondary, status, origin, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, 'manual', $7)
			ON CONFLICT (site_id, url) DO UPDATE SET keyword = EXCLUDED.keyword, secondary = EXCLUDED.secondary,
				status = EXCLUDED.status, origin = 'manual', updated_at = now()
			RETURNING id::text`, a.WorkspaceID, site.ID, page, keyword, secondary, status, createdBy).Scan(&id); err != nil {
			return err
		}
		action := "page_target.set"
		if status == "dismissed" {
			action = "page_target.dismissed"
		}
		if err := record(ctx, tx, &a.principal, a.WorkspaceID, action, "page_target", id, map[string]any{"site_id": site.ID, "secondary": len(secondary)}); err != nil {
			return err
		}
		_, err := seointel.Refresh(ctx, tx, a.WorkspaceID, site.ID, "", seointel.TriggerChange)
		return err
	})
	if s.allocError(w, r, err) {
		return
	}
	s.writeTarget(w, r, site, id)
}

func (s *Server) updatePageTarget(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in targetInput
	if !decode(w, r, &in) {
		return
	}
	site, ok := s.targetSite(w, r)
	if !ok {
		return
	}
	if in.URL != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "url cannot change; set a target for the other page")
		return
	}
	kw, sec, ok := in.keywords(w, r)
	if !ok {
		return
	}
	id := r.PathValue("target")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "target not found")
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var keyword, status string
		err := tx.QueryRow(ctx, `
			UPDATE page_targets SET keyword = COALESCE($4, keyword), secondary = COALESCE($5, secondary), status = COALESCE($6, status),
				origin = CASE WHEN $4::text IS NULL THEN origin ELSE 'manual' END, updated_at = now()
			WHERE id = $1 AND workspace_id = $2 AND site_id = $3
			RETURNING keyword, status`, id, a.WorkspaceID, site.ID, kw, sec, in.Status).Scan(&keyword, &status)
		if err != nil {
			return err
		}
		if status == "set" && keyword == "" {
			return errTargetNoKeyword
		}
		var fields []string
		for name, set := range map[string]bool{"keyword": kw != nil, "secondary": sec != nil, "status": in.Status != nil} {
			if set {
				fields = append(fields, name)
			}
		}
		slices.Sort(fields)
		if err := record(ctx, tx, &a.principal, a.WorkspaceID, "page_target.updated", "page_target", id, map[string]any{"site_id": site.ID, "fields": fields}); err != nil {
			return err
		}
		_, err = seointel.Refresh(ctx, tx, a.WorkspaceID, site.ID, "", seointel.TriggerChange)
		return err
	})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		writeError(w, r, http.StatusNotFound, "not_found", "target not found")
		return
	case errors.Is(err, errTargetNoKeyword):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "keyword is required")
		return
	case s.allocError(w, r, err):
		return
	}
	s.writeTarget(w, r, site, id)
}

var errTargetNoKeyword = errors.New("target without a keyword")

func (s *Server) deletePageTarget(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	site, ok := s.targetSite(w, r)
	if !ok {
		return
	}
	id := r.PathValue("target")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "target not found")
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM page_targets WHERE id = $1 AND workspace_id = $2 AND site_id = $3`, id, a.WorkspaceID, site.ID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return pgx.ErrNoRows
		}
		if err := record(ctx, tx, &a.principal, a.WorkspaceID, "page_target.removed", "page_target", id, map[string]any{"site_id": site.ID}); err != nil {
			return err
		}
		_, err = seointel.Refresh(ctx, tx, a.WorkspaceID, site.ID, "", seointel.TriggerChange)
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, http.StatusNotFound, "not_found", "target not found")
		return
	}
	if s.allocError(w, r, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// acceptPageTargets sets the suggested keyword as the target of some pages
// (urls), or of every page with a suggestion.
func (s *Server) acceptPageTargets(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in struct {
		URLs *[]string `json:"urls"`
	}
	if r.ContentLength != 0 && !decode(w, r, &in) {
		return
	}
	site, ok := s.targetSite(w, r)
	if !ok {
		return
	}
	var urls []string
	if in.URLs != nil {
		if len(*in.URLs) == 0 || len(*in.URLs) > maxTargets {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "urls has 1 to 1000 pages; leave it out to accept every suggestion")
			return
		}
		for _, raw := range *in.URLs {
			u, ok := targetURL(site, raw)
			if !ok {
				writeError(w, r, http.StatusBadRequest, "invalid_request", "urls are pages of the site ("+site.Host+")")
				return
			}
			urls = append(urls, u)
		}
	}
	ctx := r.Context()
	src, err := seointel.SourceFor(ctx, s.pool, a.WorkspaceID, site.Host)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var ids []string
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		sug, err := seointel.Suggestions(ctx, tx, a.WorkspaceID, site.ID, site.Host, src, urls, maxTargets)
		if err != nil || len(sug) == 0 {
			return err
		}
		var createdBy *string
		if a.ActorID != "" {
			createdBy = &a.ActorID
		}
		for _, g := range sug {
			var id string
			if err := tx.QueryRow(ctx, `
				INSERT INTO page_targets (workspace_id, site_id, url, keyword, status, origin, created_by)
				VALUES ($1, $2, $3, $4, 'set', 'suggested', $5)
				ON CONFLICT (site_id, url) DO NOTHING RETURNING id::text`, a.WorkspaceID, site.ID, g.URL, g.Keyword, createdBy).Scan(&id); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					continue
				}
				return err
			}
			ids = append(ids, id)
		}
		if err := record(ctx, tx, &a.principal, a.WorkspaceID, "page_target.accepted", "seo_site", site.ID, map[string]any{"site_id": site.ID, "count": len(ids)}); err != nil {
			return err
		}
		_, err = seointel.Refresh(ctx, tx, a.WorkspaceID, site.ID, "", seointel.TriggerChange)
		return err
	})
	if s.allocError(w, r, err) {
		return
	}
	accepted := []pageTargetView{}
	if len(ids) > 0 {
		all, err := s.loadPageTargets(r, site, src, "set", "")
		if err != nil {
			s.internal(w, r, err)
			return
		}
		for _, v := range all {
			if slices.Contains(ids, *v.ID) {
				accepted = append(accepted, v)
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": accepted, "accepted": len(accepted)})
}
