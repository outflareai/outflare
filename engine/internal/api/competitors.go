package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/crawl"
	"github.com/outflareai/outflare/engine/internal/seosync"
)

// SEO competitors: the sites each SEO site competes with in search. SEO
// connections suggest them each week (package seosync); people track or
// dismiss suggestions, or add their own. A tracked competitor gets a weekly
// light look from OutflareBot (crawl.Glance: robots.txt, sitemaps and the
// home page only) and its numbers from the workspace's own SEO
// connections, and what changed is a feed of events.

// maxTrackedCompetitors is how many competitors one site may track.
const maxTrackedCompetitors = 20

var errAlreadyTracked = errors.New("already tracked")

func (s *Server) routesCompetitors(mux *http.ServeMux) {
	read, manage := auth.ScopeSEORead, auth.ScopeSEOManage
	mux.HandleFunc("GET /v1/workspaces/{ws}/seo-competitors", s.inBrand(read, s.listCompetitors))
	mux.HandleFunc("POST /v1/workspaces/{ws}/seo-competitors", s.inBrand(manage, s.addCompetitor))
	mux.HandleFunc("GET /v1/workspaces/{ws}/seo-competitors/events", s.inBrand(read, s.listCompetitorEvents))
	mux.HandleFunc("GET /v1/workspaces/{ws}/seo-competitors/gaps", s.inBrand(read, s.competitorGaps))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/seo-competitors/{competitor}", s.inBrand(manage, s.updateCompetitor))
	mux.HandleFunc("DELETE /v1/workspaces/{ws}/seo-competitors/{competitor}", s.inBrand(manage, s.removeCompetitor))
	mux.HandleFunc("POST /v1/workspaces/{ws}/seo-competitors/{competitor}/check", s.inBrand(manage, s.checkCompetitor))
}

type competitorNumbers struct {
	Connector string          `json:"connector"`
	Day       string          `json:"day"`
	Metrics   json.RawMessage `json:"metrics"`
}

type competitorView struct {
	ID             string             `json:"id"`
	SiteID         string             `json:"site_id"`
	BrandID        *string            `json:"brand_id"`
	Host           string             `json:"host"`
	Name           string             `json:"name"`
	Aliases        []string           `json:"aliases"`
	Status         string             `json:"status"`
	Source         string             `json:"source"`
	CommonKeywords *float64           `json:"common_keywords"`
	RecordID       *string            `json:"record_id"`
	Title          string             `json:"title"`
	Description    string             `json:"description"`
	Pages          int                `json:"pages"`
	CheckedAt      *time.Time         `json:"checked_at"`
	CheckError     *string            `json:"check_error"`
	Numbers        *competitorNumbers `json:"numbers"`
	CreatedAt      time.Time          `json:"created_at"`
	UpdatedAt      time.Time          `json:"updated_at"`
}

// competitorCols reads a competitor and, from the workspace's SEO
// snapshots, the latest overview any connection took of its domain.
const competitorCols = `c.id::text, c.site_id::text, c.brand_id::text, c.host, c.name, c.aliases, c.status, c.source, c.common_keywords, c.record_id::text,
	c.title, c.description, c.pages, c.checked_at, c.check_error, c.created_at, c.updated_at, n.connector, n.day::text, n.metrics
	FROM seo_competitors c LEFT JOIN LATERAL (
		SELECT connector, day, metrics FROM seo_snapshots
		WHERE workspace_id = c.workspace_id AND host = c.host AND report = 'overview' AND key = ''
		ORDER BY day DESC, connector LIMIT 1) n ON true`

func scanCompetitor(row pgx.Row) (competitorView, error) {
	var v competitorView
	var conn, day *string
	var metrics []byte
	err := row.Scan(&v.ID, &v.SiteID, &v.BrandID, &v.Host, &v.Name, &v.Aliases, &v.Status, &v.Source, &v.CommonKeywords, &v.RecordID,
		&v.Title, &v.Description, &v.Pages, &v.CheckedAt, &v.CheckError, &v.CreatedAt, &v.UpdatedAt, &conn, &day, &metrics)
	if conn != nil && day != nil {
		v.Numbers = &competitorNumbers{Connector: *conn, Day: *day, Metrics: metrics}
	}
	return v, err
}

func (s *Server) loadCompetitor(r *http.Request, ws, id string, sel brandSel) (*competitorView, error) {
	args := []any{ws, id}
	v, err := scanCompetitor(s.pool.QueryRow(r.Context(), `SELECT `+competitorCols+` WHERE c.workspace_id = $1 AND c.id = $2`+sel.sql("c.brand_id", &args), args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &v, err
}

func (s *Server) competitorFor(w http.ResponseWriter, r *http.Request, a *access) (*competitorView, bool) {
	id := r.PathValue("competitor")
	if validUUID(id) {
		c, err := s.loadCompetitor(r, a.WorkspaceID, id, brandSel{in: a.Brands})
		if err != nil {
			s.internal(w, r, err)
			return nil, false
		}
		if c != nil {
			return c, true
		}
	}
	writeError(w, r, http.StatusNotFound, "not_found", "competitor not found")
	return nil, false
}

func (s *Server) listCompetitors(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	sel, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	site, ok := siteParam(w, r)
	if !ok {
		return
	}
	status := r.URL.Query().Get("status")
	if status != "" && status != "suggested" && status != "tracked" && status != "dismissed" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status is suggested, tracked or dismissed")
		return
	}
	limit, err := limitParam(r, 100, 500)
	if s.allocError(w, r, err) {
		return
	}
	offset, ok := offsetParam(w, r)
	if !ok {
		return
	}
	args := []any{a.WorkspaceID, site, status}
	where := `c.workspace_id = $1 AND ($2 = '' OR c.site_id = NULLIF($2, '')::uuid) AND ($3 = '' OR c.status = $3)` + sel.sql("c.brand_id", &args)
	var total int
	if err := s.pool.QueryRow(r.Context(), `SELECT count(*) FROM seo_competitors c WHERE `+where, args...).Scan(&total); err != nil {
		s.internal(w, r, err)
		return
	}
	args = append(args, limit, offset)
	rows, err := s.pool.Query(r.Context(), `SELECT `+competitorCols+` WHERE `+where+`
		ORDER BY CASE c.status WHEN 'tracked' THEN 0 WHEN 'suggested' THEN 1 ELSE 2 END, c.common_keywords DESC NULLS LAST, c.host
		LIMIT $`+strconv.Itoa(len(args)-1)+` OFFSET $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (competitorView, error) { return scanCompetitor(row) })
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if list == nil {
		list = []competitorView{}
	}
	var next *int
	if offset+len(list) < total {
		n := offset + len(list)
		next = &n
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "total": total, "next_offset": next})
}

// trackable checks a site may track one more competitor.
func trackable(r *http.Request, tx pgx.Tx, ws, site string) error {
	var n int
	if err := tx.QueryRow(r.Context(), `SELECT count(*) FROM seo_competitors WHERE workspace_id = $1 AND site_id = $2 AND status = 'tracked'`,
		ws, site).Scan(&n); err != nil {
		return err
	}
	if n >= maxTrackedCompetitors {
		return errInvalid("a site tracks at most " + strconv.Itoa(maxTrackedCompetitors) + " competitors; dismiss one first")
	}
	return nil
}

// competitorNames checks a competitor's name and other names: at most 100
// characters each and 10 others, trimmed, without repeats.
func competitorNames(w http.ResponseWriter, r *http.Request, name *string, aliases *[]string) bool {
	if name != nil {
		*name = strings.TrimSpace(*name)
		if len(*name) > 100 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "name is at most 100 characters")
			return false
		}
	}
	if aliases != nil {
		out := []string{}
		for _, v := range *aliases {
			v = strings.TrimSpace(v)
			if v == "" || slices.ContainsFunc(out, func(o string) bool { return strings.EqualFold(o, v) }) {
				continue
			}
			if len(v) > 100 {
				writeError(w, r, http.StatusBadRequest, "invalid_request", "each alias is at most 100 characters")
				return false
			}
			out = append(out, v)
		}
		if len(out) > 10 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "aliases holds at most 10 names")
			return false
		}
		*aliases = out
	}
	return true
}

func (s *Server) addCompetitor(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		SiteID  string    `json:"site_id"`
		Host    string    `json:"host"`
		Name    *string   `json:"name"`
		Aliases *[]string `json:"aliases"`
	}
	if !decode(w, r, &req) {
		return
	}
	if !competitorNames(w, r, req.Name, req.Aliases) {
		return
	}
	if !validUUID(req.SiteID) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "site_id is an SEO site's id")
		return
	}
	host := seosync.CompetitorHost(req.Host)
	if host == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "host is a domain, like example.com")
		return
	}
	ctx := r.Context()
	sites, err := s.loadSEOSites(ctx, a.WorkspaceID, &req.SiteID, brandSel{in: a.Brands})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if len(sites) != 1 {
		writeError(w, r, http.StatusNotFound, "not_found", "site not found")
		return
	}
	site := sites[0]
	if !a.writes(site.BrandID) {
		writeError(w, r, http.StatusForbidden, "forbidden", string(errSharedRecord))
		return
	}
	own := seosync.LinkTarget(site.Host)
	if host == own || strings.HasSuffix(host, "."+own) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "that is the site itself")
		return
	}
	var id string
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var was string
		err := tx.QueryRow(ctx, `SELECT status FROM seo_competitors WHERE site_id = $1 AND host = $2 FOR UPDATE`, site.ID, host).Scan(&was)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if was == "tracked" {
			return errAlreadyTracked
		}
		if err := trackable(r, tx, a.WorkspaceID, site.ID); err != nil {
			return err
		}
		err = tx.QueryRow(ctx, `
			INSERT INTO seo_competitors (workspace_id, site_id, brand_id, host, name, aliases, status, source, created_by)
			VALUES ($1, $2, $3, $4, COALESCE($6, ''), COALESCE($7::text[], '{}'), 'tracked', 'manual', $5)
			ON CONFLICT (site_id, host) DO UPDATE SET status = 'tracked', name = COALESCE($6, seo_competitors.name),
				aliases = COALESCE($7::text[], seo_competitors.aliases), updated_at = now()
			RETURNING id::text`, a.WorkspaceID, site.ID, site.BrandID, host, a.ActorID, req.Name, req.Aliases).Scan(&id)
		if err != nil {
			return err
		}
		if err := crawl.StartGlance(ctx, tx, a.WorkspaceID, id, time.Now()); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "seo_competitor.added", "seo_competitor", id, map[string]any{
			"site_id": site.ID, "brand_id": site.BrandID, "was": was,
		})
	})
	if errors.Is(err, errAlreadyTracked) {
		writeError(w, r, http.StatusConflict, "conflict", host+" is already tracked for this site")
		return
	}
	if s.allocError(w, r, err) {
		return
	}
	if err := seosync.LinkCompetitorRecords(ctx, s.pool, a.WorkspaceID, site.ID); err != nil {
		s.internal(w, r, err)
		return
	}
	c, err := s.loadCompetitor(r, a.WorkspaceID, id, brandSel{})
	if err != nil || c == nil {
		s.internal(w, r, errors.Join(err, errNotFound))
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

func (s *Server) updateCompetitor(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		Status  string    `json:"status"`
		Name    *string   `json:"name"`
		Aliases *[]string `json:"aliases"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Status != "" && req.Status != "tracked" && req.Status != "dismissed" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status is tracked or dismissed")
		return
	}
	if req.Status == "" && req.Name == nil && req.Aliases == nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "set status, name or aliases")
		return
	}
	if !competitorNames(w, r, req.Name, req.Aliases) {
		return
	}
	c, ok := s.competitorFor(w, r, a)
	if !ok {
		return
	}
	if !a.writes(c.BrandID) {
		writeError(w, r, http.StatusForbidden, "forbidden", string(errSharedRecord))
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if (req.Status == "" || req.Status == c.Status) && req.Name == nil && req.Aliases == nil {
			return nil
		}
		if req.Status == "tracked" && c.Status != "tracked" {
			if err := trackable(r, tx, a.WorkspaceID, c.SiteID); err != nil {
				return err
			}
			if err := crawl.StartGlance(ctx, tx, a.WorkspaceID, c.ID, time.Now()); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE seo_competitors SET status = COALESCE(NULLIF($3, ''), status), name = COALESCE($4, name),
			aliases = COALESCE($5::text[], aliases), updated_at = now() WHERE id = $1 AND workspace_id = $2`,
			c.ID, a.WorkspaceID, req.Status, req.Name, req.Aliases); err != nil {
			return err
		}
		status := req.Status
		if status == "" {
			status = c.Status
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "seo_competitor.updated", "seo_competitor", c.ID, map[string]any{
			"site_id": c.SiteID, "status": status, "was": c.Status, "name_set": req.Name != nil, "aliases_set": req.Aliases != nil,
		})
	})
	if s.allocError(w, r, err) {
		return
	}
	c, err = s.loadCompetitor(r, a.WorkspaceID, c.ID, brandSel{})
	if err != nil || c == nil {
		s.internal(w, r, errors.Join(err, errNotFound))
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) removeCompetitor(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	c, ok := s.competitorFor(w, r, a)
	if !ok {
		return
	}
	if !a.writes(c.BrandID) {
		writeError(w, r, http.StatusForbidden, "forbidden", string(errSharedRecord))
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM seo_competitors WHERE id = $1 AND workspace_id = $2`, c.ID, a.WorkspaceID); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "seo_competitor.removed", "seo_competitor", c.ID, map[string]any{"site_id": c.SiteID})
	})
	if s.allocError(w, r, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// checkCompetitor queues OutflareBot's light look at a tracked competitor
// now, at most once an hour.
func (s *Server) checkCompetitor(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	c, ok := s.competitorFor(w, r, a)
	if !ok {
		return
	}
	if !a.writes(c.BrandID) {
		writeError(w, r, http.StatusForbidden, "forbidden", string(errSharedRecord))
		return
	}
	if c.Status != "tracked" {
		writeError(w, r, http.StatusConflict, "conflict", "only a tracked competitor is looked at; track it first")
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := crawl.StartGlance(ctx, tx, a.WorkspaceID, c.ID, time.Now()); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "seo_competitor.check_requested", "seo_competitor", c.ID, map[string]any{"site_id": c.SiteID})
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"queued": true})
}

type competitorEventView struct {
	ID           string          `json:"id"`
	SiteID       string          `json:"site_id"`
	CompetitorID string          `json:"competitor_id"`
	Host         string          `json:"host"`
	Kind         string          `json:"kind"`
	Connector    string          `json:"connector"`
	Data         json.RawMessage `json:"data"`
	At           time.Time       `json:"at"`
}

var competitorEventKinds = []string{"pages_added", "pages_removed", "title_changed", "description_changed",
	"rating_changed", "traffic_changed", "keywords_changed", "links_changed"}

func (s *Server) listCompetitorEvents(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	sel, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	site, ok := siteParam(w, r)
	if !ok {
		return
	}
	comp := q.Get("competitor_id")
	if comp != "" && !validUUID(comp) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "competitor_id is a competitor's id")
		return
	}
	kind := q.Get("kind")
	if kind != "" && !slices.Contains(competitorEventKinds, kind) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "kind is one of "+strings.Join(competitorEventKinds, ", "))
		return
	}
	days := 90
	if v := q.Get("days"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 730 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "days is 1 to 730")
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
	args := []any{a.WorkspaceID, site, comp, kind, days}
	where := `e.workspace_id = $1 AND ($2 = '' OR e.site_id = NULLIF($2, '')::uuid) AND ($3 = '' OR e.competitor_id = NULLIF($3, '')::uuid)
		AND ($4 = '' OR e.kind = $4) AND e.at > now() - make_interval(days => $5)` + sel.sql("c.brand_id", &args)
	args = append(args, limit+1, offset)
	rows, err := s.pool.Query(r.Context(), `
		SELECT e.id::text, e.site_id::text, e.competitor_id::text, c.host, e.kind, e.connector, e.data, e.at
		FROM competitor_events e JOIN seo_competitors c ON c.id = e.competitor_id
		WHERE `+where+` ORDER BY e.at DESC, e.id DESC
		LIMIT $`+strconv.Itoa(len(args)-1)+` OFFSET $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (competitorEventView, error) {
		var v competitorEventView
		var data []byte
		err := row.Scan(&v.ID, &v.SiteID, &v.CompetitorID, &v.Host, &v.Kind, &v.Connector, &data, &v.At)
		v.Data = data
		return v, err
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
		list = []competitorEventView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "next_offset": next})
}

// Gaps: what tracked competitors have that the site doesn't, from the
// keywords and linking domains the weekly snapshot reads of up to five of
// them (seosync.MaxGapCompetitors). Reads only what is stored.

type gapRank struct {
	Host     string   `json:"host"`
	Position *float64 `json:"position"`
	URL      string   `json:"url"`
	Traffic  *float64 `json:"traffic"`
}

type keywordGap struct {
	Keyword      string    `json:"keyword"`
	Volume       *float64  `json:"volume"`
	Kind         string    `json:"kind"`
	BestPosition *float64  `json:"best_position"`
	Competitors  []gapRank `json:"competitors"`
	You          *gapRank  `json:"you"`
}

type linkGap struct {
	Domain      string   `json:"domain"`
	Rating      *float64 `json:"rating"`
	Competitors []string `json:"competitors"`
}

// A keyword is a gap when a competitor ranks within gapTop and the site
// either doesn't rank for it or ranks gapBehind places or more below.
const (
	gapTop    = 20
	gapBehind = 3
)

type snapRow struct {
	host, key, url string
	m              connectors.SEOMetrics
	day            string
}

// latestRows reads the newest snapshot of a report for each host.
func (s *Server) latestRows(r *http.Request, ws, report string, hosts []string) ([]snapRow, error) {
	rows, err := s.pool.Query(r.Context(), `
		SELECT DISTINCT ON (s.host, s.key) s.host, s.key, s.url, s.metrics, s.day::text
		FROM seo_snapshots s
		WHERE s.workspace_id = $1 AND s.report = $2 AND s.host = ANY($3) AND s.key <> ''
		  AND s.day = (SELECT max(day) FROM seo_snapshots x WHERE x.workspace_id = $1 AND x.report = $2 AND x.host = s.host)
		ORDER BY s.host, s.key, s.connector`, ws, report, hosts)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (snapRow, error) {
		var v snapRow
		var raw []byte
		err := row.Scan(&v.host, &v.key, &v.url, &raw, &v.day)
		_ = json.Unmarshal(raw, &v.m)
		return v, err
	})
}

func (s *Server) competitorGaps(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	q := r.URL.Query()
	siteID := q.Get("site_id")
	if !validUUID(siteID) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "site_id is an SEO site's id")
		return
	}
	kind := q.Get("kind")
	if kind == "" {
		kind = "keywords"
	}
	if kind != "keywords" && kind != "links" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "kind is keywords or links")
		return
	}
	limit, err := limitParam(r, 100, 500)
	if s.allocError(w, r, err) {
		return
	}
	offset, ok := offsetParam(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	sites, err := s.loadSEOSites(ctx, a.WorkspaceID, &siteID, brandSel{in: a.Brands})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if len(sites) != 1 {
		writeError(w, r, http.StatusNotFound, "not_found", "site not found")
		return
	}
	target := seosync.LinkTarget(sites[0].Host)
	rows, err := s.pool.Query(ctx, `SELECT host FROM seo_competitors WHERE workspace_id = $1 AND site_id = $2 AND status = 'tracked' ORDER BY host`, a.WorkspaceID, siteID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	comps, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		s.internal(w, r, err)
		return
	}
	theirs, err := s.latestRows(r, a.WorkspaceID, map[string]string{"keywords": connectors.SEOKeywords, "links": connectors.SEOBacklinks}[kind], comps)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	read := map[string]bool{}
	for _, t := range theirs {
		read[t.host] = true
	}
	compared := []string{}
	for _, c := range comps {
		if read[c] {
			compared = append(compared, c)
		}
	}
	out := map[string]any{"site_id": siteID, "kind": kind, "competitors": compared}
	if kind == "keywords" {
		ours, err := s.latestRows(r, a.WorkspaceID, connectors.SEOKeywords, []string{target, "www." + target, hostOnly(sites[0].Host)})
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
		gaps := map[string]*keywordGap{}
		for _, t := range theirs {
			if t.m.Position == nil || *t.m.Position > gapTop {
				continue
			}
			k := strings.ToLower(t.key)
			g := gaps[k]
			if g == nil {
				g = &keywordGap{Keyword: t.key, Volume: t.m.Volume}
				gaps[k] = g
			}
			g.Competitors = append(g.Competitors, gapRank{Host: t.host, Position: t.m.Position, URL: t.url, Traffic: t.m.Traffic})
			if better(t.m.Position, g.BestPosition) {
				g.BestPosition = t.m.Position
			}
		}
		list := []keywordGap{}
		for k, g := range gaps {
			if o, ok := mine[k]; ok {
				if o.m.Position != nil && *o.m.Position-*g.BestPosition < gapBehind {
					continue // you rank about as well as they do
				}
				g.Kind, g.You = "behind", &gapRank{Host: o.host, Position: o.m.Position, URL: o.url, Traffic: o.m.Traffic}
			} else {
				g.Kind = "missing"
			}
			slices.SortFunc(g.Competitors, func(x, y gapRank) int { return cmpPos(x.Position, y.Position) })
			list = append(list, *g)
		}
		slices.SortFunc(list, func(x, y keywordGap) int {
			if d := len(y.Competitors) - len(x.Competitors); d != 0 {
				return d
			}
			if d := cmpNum(y.Volume, x.Volume); d != 0 {
				return d
			}
			return strings.Compare(x.Keyword, y.Keyword)
		})
		out["data"], out["next_offset"] = page(list, offset, limit)
		out["total"] = len(list)
		out["your_keywords"] = len(mine)
		writeJSON(w, http.StatusOK, out)
		return
	}
	var have []string
	rows, err = s.pool.Query(ctx, `SELECT DISTINCT source_domain FROM backlinks WHERE workspace_id = $1 AND site_id = $2 AND status = 'live'`, a.WorkspaceID, siteID)
	if err == nil {
		have, err = pgx.CollectRows(rows, pgx.RowTo[string])
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	skip := map[string]bool{}
	for _, d := range have {
		skip[d] = true
	}
	for _, c := range comps {
		skip[c] = true
	}
	gaps := map[string]*linkGap{}
	for _, t := range theirs {
		d := strings.TrimPrefix(strings.ToLower(t.key), "www.")
		if skip[d] || d == target || strings.HasSuffix(d, "."+target) {
			continue
		}
		g := gaps[d]
		if g == nil {
			g = &linkGap{Domain: d}
			gaps[d] = g
		}
		if !slices.Contains(g.Competitors, t.host) {
			g.Competitors = append(g.Competitors, t.host)
		}
		if cmpNum(t.m.DomainRating, g.Rating) > 0 {
			g.Rating = t.m.DomainRating
		}
	}
	list := []linkGap{}
	for _, g := range gaps {
		slices.Sort(g.Competitors)
		list = append(list, *g)
	}
	slices.SortFunc(list, func(x, y linkGap) int {
		if d := len(y.Competitors) - len(x.Competitors); d != 0 {
			return d
		}
		if d := cmpNum(y.Rating, x.Rating); d != 0 {
			return d
		}
		return strings.Compare(x.Domain, y.Domain)
	})
	out["data"], out["next_offset"] = page(list, offset, limit)
	out["total"] = len(list)
	out["your_domains"] = len(have)
	writeJSON(w, http.StatusOK, out)
}

// page cuts one page out of a sorted list, with the offset of the next
// page or nil after the last.
func page[T any](list []T, offset, limit int) ([]T, *int) {
	from := min(offset, len(list))
	to := min(from+limit, len(list))
	if to < len(list) {
		return list[from:to], &to
	}
	return list[from:to], nil
}

// better reports whether position a ranks above b; nothing ranks below
// everything.
func better(a, b *float64) bool { return a != nil && (b == nil || *a < *b) }

func cmpPos(a, b *float64) int {
	switch {
	case better(a, b):
		return -1
	case better(b, a):
		return 1
	}
	return 0
}

func cmpNum(a, b *float64) int {
	switch {
	case a == nil && b == nil:
		return 0
	case a == nil:
		return -1
	case b == nil:
		return 1
	case *a < *b:
		return -1
	case *a > *b:
		return 1
	}
	return 0
}
