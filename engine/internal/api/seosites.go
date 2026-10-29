package api

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/crawl"
)

// SEO sites and crawls: the websites a workspace audits, the proof that it
// owns them, and OutflareBot's crawls of them (package crawl). The existing
// /seo routes are the SEO tool connections (DataForSEO, Ahrefs); these
// live beside them as /seo-sites and /crawls.

func (s *Server) routesSEOSites(mux *http.ServeMux) {
	read, manage := auth.ScopeSEORead, auth.ScopeSEOManage
	mux.HandleFunc("GET /v1/workspaces/{ws}/seo-sites", s.inBrand(read, s.listSEOSites))
	mux.HandleFunc("POST /v1/workspaces/{ws}/seo-sites", s.inBrand(manage, s.createSEOSite))
	mux.HandleFunc("GET /v1/workspaces/{ws}/seo-sites/{site}", s.inBrand(read, s.getSEOSite))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/seo-sites/{site}", s.inBrand(manage, s.updateSEOSite))
	mux.HandleFunc("DELETE /v1/workspaces/{ws}/seo-sites/{site}", s.inBrand(manage, s.deleteSEOSite))
	mux.HandleFunc("POST /v1/workspaces/{ws}/seo-sites/{site}/verify", s.inBrand(manage, s.verifySEOSite))
	mux.HandleFunc("POST /v1/workspaces/{ws}/seo-sites/{site}/dns", s.inBrand(manage, s.addSEOSiteDNS))
	mux.HandleFunc("GET /v1/workspaces/{ws}/seo-sites/{site}/dns/approval", s.inBrand(manage, s.seoSiteDNSApproval))
	mux.HandleFunc("POST /v1/workspaces/{ws}/seo-sites/{site}/dns/approved", s.inBrand(manage, s.seoSiteDNSApproved))
	mux.HandleFunc("POST /v1/workspaces/{ws}/seo-sites/{site}/crawls", s.inBrand(manage, s.startCrawl))
	mux.HandleFunc("GET /v1/workspaces/{ws}/crawls", s.inBrand(read, s.listCrawls))
	mux.HandleFunc("GET /v1/workspaces/{ws}/crawls/{crawl}", s.inBrand(read, s.getCrawl))
	mux.HandleFunc("POST /v1/workspaces/{ws}/crawls/{crawl}/stop", s.inBrand(manage, s.stopCrawl))
	mux.HandleFunc("GET /v1/workspaces/{ws}/crawls/{crawl}/pages", s.inBrand(read, s.listCrawlPages))
	mux.HandleFunc("GET /v1/workspaces/{ws}/crawls/{crawl}/pages/{page}", s.inBrand(read, s.getCrawlPage))
	mux.HandleFunc("GET /v1/workspaces/{ws}/crawls/{crawl}/issues", s.inBrand(read, s.listCrawlIssues))
	mux.HandleFunc("GET /v1/workspaces/{ws}/crawls/{crawl}/issues/{rule}", s.inBrand(read, s.getCrawlIssue))
	mux.HandleFunc("GET /v1/workspaces/{ws}/seo-rules", s.inBrand(read, s.listSEORules))
	// The fixes list holds only site audits' fixes so far, so it takes the
	// SEO scopes; a second source brings scopes of the list's own.
	mux.HandleFunc("GET /v1/workspaces/{ws}/fixes", s.inBrand(read, s.listFixes))
	mux.HandleFunc("GET /v1/workspaces/{ws}/fixes/{fix}", s.inBrand(read, s.getFix))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/fixes/{fix}", s.inBrand(manage, s.updateFix))
	mux.HandleFunc("GET /v1/workspaces/{ws}/fixes/{fix}/prompt", s.inBrand(read, s.getFixPrompt))
	mux.HandleFunc("POST /v1/workspaces/{ws}/fixes/{fix}/verify", s.inBrand(manage, s.verifyFix))
}

var (
	siteSchedules = []string{"off", "daily", "weekly"}
	siteStatuses  = []string{"active", "paused"}
)

type siteLimits struct {
	MaxPages        int  `json:"max_pages"`
	MinCrawlDelayMS int  `json:"min_crawl_delay_ms"`
	MayIgnoreRobots bool `json:"may_ignore_robots"`
}

type seoSite struct {
	ID           string  `json:"id"`
	BrandID      *string `json:"brand_id"`
	Host         string  `json:"host"`
	StartURL     string  `json:"start_url"`
	Status       string  `json:"status"`
	Schedule     string  `json:"schedule"`
	MaxPages     int     `json:"max_pages"`
	CrawlDelayMS int     `json:"crawl_delay_ms"`
	ObeyRobots   bool    `json:"obey_robots"`
	// How it is crawled beyond its limits, and when a scheduled crawl
	// starts.
	crawl.Settings
	ScheduleHour *int   `json:"schedule_hour"`
	ScheduleDay  *int   `json:"schedule_day"`
	ScheduleTZ   string `json:"schedule_tz"`
	// The connections that can run pages' scripts (render_js), for this
	// site's brand.
	RenderConnections []string     `json:"render_connections"`
	Verified          bool         `json:"verified"`
	VerifiedAt        *time.Time   `json:"verified_at"`
	VerifyMethod      *string      `json:"verify_method"`
	VerifyCheckedAt   *time.Time   `json:"verify_checked_at"`
	Verification      crawl.Proofs `json:"verification"`
	// One click for the DNS proof: the connections that can add it and
	// the last run.
	DNSInstall     *connectors.DNSInstall `json:"dns_install"`
	DNSConnections []string               `json:"dns_connections"`
	Limits         siteLimits             `json:"limits"`
	LastCrawl      *crawlRunView          `json:"last_crawl"`
	CreatedAt      time.Time              `json:"created_at"`
	UpdatedAt      time.Time              `json:"updated_at"`
}

type issueCounts struct {
	Errors   int `json:"errors"`
	Warnings int `json:"warnings"`
	Notices  int `json:"notices"`
}

type crawlRunView struct {
	ID              string  `json:"id"`
	SiteID          string  `json:"site_id"`
	Host            string  `json:"host"`
	Status          string  `json:"status"`
	Trigger         string  `json:"trigger"`
	Reason          *string `json:"reason"`
	ErrorCode       *string `json:"error_code"`
	MaxPages        int     `json:"max_pages"`
	DelayMS         int     `json:"delay_ms"`
	ObeyRobots      bool    `json:"obey_robots"`
	PagesFetched    int     `json:"pages_fetched"`
	PagesBlocked    int     `json:"pages_blocked"`
	PagesFailed     int     `json:"pages_failed"`
	PagesFound      int     `json:"pages_found"`
	PagesFallback   int     `json:"pages_fallback"`
	PagesFirewalled int     `json:"pages_firewalled"`
	FallbackHint    string  `json:"fallback_hint"`
	PagesExcluded   int     `json:"pages_excluded"`
	PagesRendered   int     `json:"pages_rendered"`
	// The settings the crawl ran with.
	Settings crawl.Settings `json:"settings"`
	// The audit, once the crawl has finished.
	HealthScore *int        `json:"health_score"`
	Issues      issueCounts `json:"issues"`
	Bytes       int64       `json:"bytes"`
	CreatedAt   time.Time   `json:"created_at"`
	StartedAt   *time.Time  `json:"started_at"`
	FinishedAt  *time.Time  `json:"finished_at"`
}

const crawlRunCols = `r.id, r.site_id, r.host, r.status, r.trigger, NULLIF(r.reason, ''), NULLIF(r.error_code, ''), r.max_pages,
	r.delay_ms, r.obey_robots, r.pages_fetched, r.pages_blocked, r.pages_failed, r.pages_found, r.pages_fallback, r.pages_firewalled, r.fallback_hint, r.pages_excluded, r.pages_rendered, r.settings, r.health_score, r.issues_errors, r.issues_warnings, r.issues_notices,
	r.bytes, r.created_at, r.started_at, r.finished_at`

func scanCrawlRun(row pgx.Row, v *crawlRunView, extra ...any) error {
	var settings []byte
	if err := row.Scan(append([]any{&v.ID, &v.SiteID, &v.Host, &v.Status, &v.Trigger, &v.Reason, &v.ErrorCode, &v.MaxPages,
		&v.DelayMS, &v.ObeyRobots, &v.PagesFetched, &v.PagesBlocked, &v.PagesFailed, &v.PagesFound, &v.PagesFallback, &v.PagesFirewalled, &v.FallbackHint, &v.PagesExcluded, &v.PagesRendered, &settings, &v.HealthScore, &v.Issues.Errors, &v.Issues.Warnings, &v.Issues.Notices,
		&v.Bytes, &v.CreatedAt, &v.StartedAt, &v.FinishedAt}, extra...)...); err != nil {
		return err
	}
	// A run from before settings were kept ({}) crawled with the defaults.
	var err error
	v.Settings, err = crawl.SettingsFrom(settings)
	return err
}

// loadSEOSites returns the workspace's sites, or one when id is set, kept
// to the brands sel lets through.
func (s *Server) loadSEOSites(ctx context.Context, ws string, id *string, sel brandSel) ([]seoSite, error) {
	args := []any{ws, id}
	rows, err := s.pool.Query(ctx, `
		SELECT s.id, s.brand_id::text, s.host, s.start_url, s.status, s.schedule, s.max_pages, s.crawl_delay_ms,
		       s.obey_robots, s.verify_token, s.verified_at, NULLIF(s.verify_method, ''), s.verify_checked_at,
		       s.created_at, s.updated_at, s.dns_install,
		       s.render_js, s.use_sitemaps, s.follow_nofollow, s.ignore_params, s.max_depth, s.include_patterns, s.exclude_patterns,
		       s.schedule_hour, s.schedule_day, s.schedule_tz,
		       (SELECT r.id FROM crawl_runs r WHERE r.site_id = s.id ORDER BY r.created_at DESC LIMIT 1)
		FROM seo_sites s
		WHERE s.workspace_id = $1 AND ($2::uuid IS NULL OR s.id = $2)`+sel.sql("s.brand_id", &args)+`
		ORDER BY s.host`, args...)
	if err != nil {
		return nil, err
	}
	type withRun struct {
		seoSite
		run *string
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (withRun, error) {
		var v withRun
		var token string
		err := row.Scan(&v.ID, &v.BrandID, &v.Host, &v.StartURL, &v.Status, &v.Schedule, &v.MaxPages, &v.CrawlDelayMS,
			&v.ObeyRobots, &token, &v.VerifiedAt, &v.VerifyMethod, &v.VerifyCheckedAt, &v.CreatedAt, &v.UpdatedAt, &v.DNSInstall,
			&v.RenderJS, &v.UseSitemaps, &v.FollowNofollow, &v.IgnoreParams, &v.MaxDepth, &v.Include, &v.Exclude,
			&v.ScheduleHour, &v.ScheduleDay, &v.ScheduleTZ, &v.run)
		v.Verified = v.VerifiedAt != nil
		v.Verification = crawl.ProofsFor(v.StartURL, v.Host, token)
		l := crawl.LimitsFor(v.Verified)
		v.Limits = siteLimits{MaxPages: l.MaxPages, MinCrawlDelayMS: l.MinDelayMS, MayIgnoreRobots: l.MayIgnoreRobots}
		return v, err
	})
	if err != nil {
		return nil, err
	}
	dns, err := s.connectionsWith(ctx, ws, connectors.CapDNSWrite)
	if err != nil {
		return nil, err
	}
	render, err := s.connectionsWith(ctx, ws, connectors.CapCrawlFetch)
	if err != nil {
		return nil, err
	}
	out := make([]seoSite, 0, len(list))
	for _, v := range list {
		v.DNSConnections, v.RenderConnections = dns(deref(v.BrandID)), render(deref(v.BrandID))
		if v.run != nil {
			var run crawlRunView
			if err := scanCrawlRun(s.pool.QueryRow(ctx, `SELECT `+crawlRunCols+` FROM crawl_runs r WHERE r.id = $1`, *v.run), &run); err != nil {
				return nil, err
			}
			v.LastCrawl = &run
		}
		out = append(out, v.seoSite)
	}
	return out, nil
}

// seoSiteFor loads the {site} path value's site, or writes a 404.
func (s *Server) seoSiteFor(w http.ResponseWriter, r *http.Request, a *access) (*seoSite, bool) {
	id := r.PathValue("site")
	if validUUID(id) {
		list, err := s.loadSEOSites(r.Context(), a.WorkspaceID, &id, brandSel{in: a.Brands})
		if err != nil {
			s.internal(w, r, err)
			return nil, false
		}
		if len(list) == 1 {
			return &list[0], true
		}
	}
	writeError(w, r, http.StatusNotFound, "not_found", "site not found")
	return nil, false
}

func (s *Server) listSEOSites(w http.ResponseWriter, r *http.Request) {
	sel, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	list, err := s.loadSEOSites(r.Context(), accessFrom(r.Context()).WorkspaceID, nil, sel)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

func (s *Server) getSEOSite(w http.ResponseWriter, r *http.Request) {
	if site, ok := s.seoSiteFor(w, r, accessFrom(r.Context())); ok {
		writeJSON(w, http.StatusOK, site)
	}
}

// siteAddress turns what someone typed (geonode.com, https://www.geonode.com/pricing)
// into the site's start URL and host. The start URL keeps the path, so a
// site can be a section such as /docs/.
func (s *Server) siteAddress(raw string) (startURL, host string, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", errInvalid("url is required: the site's address, like https://example.com")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	if err := s.crawler.CheckSite(raw); err != nil {
		return "", "", errInvalid(err.Error())
	}
	u, _ := url.Parse(raw)
	start, ok := crawl.Normalize(u, raw)
	if !ok {
		return "", "", errInvalid("url is a web address, like https://example.com")
	}
	su, _ := url.Parse(start)
	su.RawQuery = ""
	return su.String(), strings.ToLower(su.Host), nil
}

type seoSiteInput struct {
	URL          *string          `json:"url"`
	BrandID      optional[string] `json:"brand_id"`
	MaxPages     *int             `json:"max_pages"`
	CrawlDelayMS *int             `json:"crawl_delay_ms"`
	ObeyRobots   *bool            `json:"obey_robots"`
	Schedule     *string          `json:"schedule"`
	Status       *string          `json:"status"`

	RenderJS        *string       `json:"render_js"`
	UseSitemaps     *bool         `json:"use_sitemaps"`
	FollowNofollow  *bool         `json:"follow_nofollow"`
	IgnoreParams    *bool         `json:"ignore_params"`
	MaxDepth        *int          `json:"max_depth"`
	IncludePatterns *[]string     `json:"include_patterns"`
	ExcludePatterns *[]string     `json:"exclude_patterns"`
	ScheduleHour    optional[int] `json:"schedule_hour"`
	ScheduleDay     optional[int] `json:"schedule_day"`
	ScheduleTZ      *string       `json:"schedule_tz"`
}

// check validates the settings against a site's limits.
func (in *seoSiteInput) check(verified bool) error {
	lim := crawl.LimitsFor(verified)
	if in.MaxPages != nil && (*in.MaxPages < 1 || *in.MaxPages > lim.MaxPages) {
		if !verified && *in.MaxPages > lim.MaxPages {
			return errInvalid(fmt.Sprintf("max_pages is at most %d until you prove you own the site (POST .../verify), then up to %d",
				lim.MaxPages, crawl.LimitsFor(true).MaxPages))
		}
		return errInvalid(fmt.Sprintf("max_pages is 1 to %d", lim.MaxPages))
	}
	if in.CrawlDelayMS != nil && (*in.CrawlDelayMS < lim.MinDelayMS || *in.CrawlDelayMS > 60000) {
		return errInvalid(fmt.Sprintf("crawl_delay_ms is %d to 60000 for this site (proving you own it lowers the minimum to %d)",
			lim.MinDelayMS, crawl.LimitsFor(true).MinDelayMS))
	}
	if in.ObeyRobots != nil && !*in.ObeyRobots && !lim.MayIgnoreRobots {
		return errInvalid("obey_robots can only be turned off for a site you have proved you own (POST .../verify)")
	}
	if in.Schedule != nil && !slices.Contains(siteSchedules, *in.Schedule) {
		return errInvalid("schedule is off, daily or weekly")
	}
	if in.Status != nil && !slices.Contains(siteStatuses, *in.Status) {
		return errInvalid("status is active or paused")
	}
	if in.RenderJS != nil && !slices.Contains(crawl.RenderModes, *in.RenderJS) {
		return errInvalid("render_js is off, auto or always")
	}
	if in.MaxDepth != nil && (*in.MaxDepth < 0 || *in.MaxDepth > 100) {
		return errInvalid("max_depth is 0 (no limit) to 100")
	}
	for name, list := range map[string]*[]string{"include_patterns": in.IncludePatterns, "exclude_patterns": in.ExcludePatterns} {
		if list == nil {
			continue
		}
		if *list == nil {
			*list = []string{}
		}
		if err := crawl.CheckPatterns(name, *list); err != nil {
			return errInvalid(err.Error())
		}
	}
	if v := in.ScheduleHour.Value; v != nil && (*v < 0 || *v > 23) {
		return errInvalid("schedule_hour is 0 to 23, or null for any time")
	}
	if v := in.ScheduleDay.Value; v != nil && (*v < 0 || *v > 6) {
		return errInvalid("schedule_day is 0 (Sunday) to 6, or null for any day")
	}
	if in.ScheduleTZ != nil {
		if _, err := time.LoadLocation(*in.ScheduleTZ); err != nil || *in.ScheduleTZ == "" || *in.ScheduleTZ == "Local" {
			return errInvalid("schedule_tz is a time zone name such as Australia/Sydney or UTC")
		}
	}
	return nil
}

// settingsSQL is the SET clause for the crawl settings in seoSiteInput,
// with its arguments numbered from n.
func (in *seoSiteInput) settingsSQL(n int) (string, []any) {
	return fmt.Sprintf(`render_js = COALESCE($%d, render_js), use_sitemaps = COALESCE($%d, use_sitemaps),
		follow_nofollow = COALESCE($%d, follow_nofollow), ignore_params = COALESCE($%d, ignore_params),
		max_depth = COALESCE($%d, max_depth), include_patterns = COALESCE($%d, include_patterns),
		exclude_patterns = COALESCE($%d, exclude_patterns),
		schedule_hour = CASE WHEN $%d THEN $%d ELSE schedule_hour END,
		schedule_day = CASE WHEN $%d THEN $%d ELSE schedule_day END, schedule_tz = COALESCE($%d, schedule_tz)`,
			n, n+1, n+2, n+3, n+4, n+5, n+6, n+7, n+8, n+9, n+10, n+11),
		[]any{in.RenderJS, in.UseSitemaps, in.FollowNofollow, in.IgnoreParams, in.MaxDepth, in.IncludePatterns, in.ExcludePatterns,
			in.ScheduleHour.Set, in.ScheduleHour.Value, in.ScheduleDay.Set, in.ScheduleDay.Value, in.ScheduleTZ}
}

// checkTZ makes sure Postgres knows a time zone the schedule names too.
func checkTZ(ctx context.Context, tx pgx.Tx, tz *string) error {
	if tz == nil {
		return nil
	}
	if _, err := tx.Exec(ctx, `SELECT now() AT TIME ZONE $1`, *tz); err != nil {
		return errInvalid("schedule_tz is a time zone name such as Australia/Sydney or UTC")
	}
	return nil
}

// settingsFields names the crawl settings an input sets, for the audit
// log.
func (in *seoSiteInput) settingsFields() map[string]bool {
	return map[string]bool{"render_js": in.RenderJS != nil, "use_sitemaps": in.UseSitemaps != nil,
		"follow_nofollow": in.FollowNofollow != nil, "ignore_params": in.IgnoreParams != nil, "max_depth": in.MaxDepth != nil,
		"include_patterns": in.IncludePatterns != nil, "exclude_patterns": in.ExcludePatterns != nil,
		"schedule_hour": in.ScheduleHour.Set, "schedule_day": in.ScheduleDay.Set, "schedule_tz": in.ScheduleTZ != nil}
}

func (s *Server) createSEOSite(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req seoSiteInput
	if !decode(w, r, &req) {
		return
	}
	if req.Status != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status is only for updates")
		return
	}
	if err := req.check(false); s.allocError(w, r, err) {
		return
	}
	var raw string
	if req.URL != nil {
		raw = *req.URL
	}
	start, host, err := s.siteAddress(raw)
	if s.allocError(w, r, err) {
		return
	}
	ctx := r.Context()
	var id string
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var brand *string
		if req.BrandID.Set {
			b, err := s.brandRef(ctx, a.WorkspaceID, req.BrandID.Value)
			if err != nil {
				return err
			}
			brand = b
		} else {
			// The brand whose domains include the host, when one does.
			err := tx.QueryRow(ctx, `
				SELECT id::text FROM brands WHERE workspace_id = $1 AND status = 'active' AND $2 = ANY(domains)
				ORDER BY created_at LIMIT 1`, a.WorkspaceID, strings.TrimPrefix(hostOnly(host), "www.")).Scan(&brand)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			if brand != nil && !a.writes(brand) {
				brand = nil
			}
		}
		brand, err := brandWrite(ctx, tx, a, brand)
		if err != nil {
			return err
		}
		maxPages, delay, schedule := 100, 2000, "off"
		if req.MaxPages != nil {
			maxPages = *req.MaxPages
		}
		if req.CrawlDelayMS != nil {
			delay = *req.CrawlDelayMS
		}
		if req.Schedule != nil {
			schedule = *req.Schedule
		}
		err = tx.QueryRow(ctx, `
			INSERT INTO seo_sites (workspace_id, brand_id, host, start_url, verify_token, max_pages, crawl_delay_ms, schedule, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING id`,
			a.WorkspaceID, brand, host, start, crawl.NewVerifyToken(), maxPages, delay, schedule, a.ActorID).Scan(&id)
		if isUnique(err) {
			return errInvalid("this workspace already has the site " + host)
		}
		if err != nil {
			return err
		}
		if err := checkTZ(ctx, tx, req.ScheduleTZ); err != nil {
			return err
		}
		set, args := req.settingsSQL(2)
		if _, err := tx.Exec(ctx, `UPDATE seo_sites SET `+set+` WHERE id = $1`, append([]any{id}, args...)...); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "seo_site.created", "seo_site", id, map[string]any{
			"brand_id": brand, "max_pages": maxPages, "schedule": schedule,
		})
	})
	if s.allocError(w, r, err) {
		return
	}
	list, err := s.loadSEOSites(ctx, a.WorkspaceID, &id, brandSel{})
	if err != nil || len(list) != 1 {
		s.internal(w, r, errors.Join(err, errNotFound))
		return
	}
	writeJSON(w, http.StatusCreated, list[0])
}

// hostOnly drops a port from a host.
func hostOnly(host string) string {
	if i := strings.LastIndexByte(host, ':'); i >= 0 && !strings.Contains(host[i:], "]") {
		return host[:i]
	}
	return host
}

func (s *Server) updateSEOSite(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req seoSiteInput
	if !decode(w, r, &req) {
		return
	}
	site, ok := s.seoSiteFor(w, r, a)
	if !ok {
		return
	}
	if !a.writes(site.BrandID) {
		writeError(w, r, http.StatusForbidden, "forbidden", string(errSharedRecord))
		return
	}
	// The start address may move within the site; another host is a site
	// of its own.
	var startURL *string
	if req.URL != nil {
		start, host, err := s.siteAddress(*req.URL)
		if s.allocError(w, r, err) {
			return
		}
		if host != site.Host {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "url must stay on "+site.Host+"; add "+host+" as its own site")
			return
		}
		startURL = &start
	}
	if err := req.check(site.Verified); s.allocError(w, r, err) {
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		brand := site.BrandID
		if req.BrandID.Set {
			b, err := s.brandRef(ctx, a.WorkspaceID, req.BrandID.Value)
			if err != nil {
				return err
			}
			if b, err = brandWrite(ctx, tx, a, b); err != nil {
				return err
			}
			brand = b
		}
		if err := checkTZ(ctx, tx, req.ScheduleTZ); err != nil {
			return err
		}
		settings, settingsArgs := req.settingsSQL(10)
		_, err := tx.Exec(ctx, `
			UPDATE seo_sites SET brand_id = $3::uuid, max_pages = COALESCE($4, max_pages), crawl_delay_ms = COALESCE($5, crawl_delay_ms),
				obey_robots = COALESCE($6, obey_robots), schedule = COALESCE($7, schedule), status = COALESCE($8, status),
				start_url = COALESCE($9, start_url), `+settings+`, updated_at = now()
			WHERE id = $1 AND workspace_id = $2`,
			append([]any{site.ID, a.WorkspaceID, brand, req.MaxPages, req.CrawlDelayMS, req.ObeyRobots, req.Schedule, req.Status, startURL},
				settingsArgs...)...)
		if err != nil {
			return err
		}
		// The site's fixes follow its brand.
		if _, err := tx.Exec(ctx, `UPDATE fixes SET brand_id = $3::uuid WHERE workspace_id = $1 AND subject_type = 'seo_site' AND subject_id = $2`,
			a.WorkspaceID, site.ID, brand); err != nil {
			return err
		}
		for _, table := range []string{"backlinks", "seo_competitors"} {
			if _, err := tx.Exec(ctx, `UPDATE `+table+` SET brand_id = $3::uuid WHERE workspace_id = $1 AND site_id = $2`,
				a.WorkspaceID, site.ID, brand); err != nil {
				return err
			}
		}
		data := map[string]any{}
		var fields []string
		changed := req.settingsFields()
		maps.Copy(changed, map[string]bool{"brand_id": req.BrandID.Set, "max_pages": req.MaxPages != nil,
			"crawl_delay_ms": req.CrawlDelayMS != nil, "obey_robots": req.ObeyRobots != nil, "schedule": req.Schedule != nil,
			"status": req.Status != nil, "url": req.URL != nil})
		for name, set := range changed {
			if set {
				fields = append(fields, name)
			}
		}
		if len(fields) == 0 {
			return nil
		}
		slices.Sort(fields)
		data["fields"] = fields
		// Choosing to ignore robots.txt is an owner's call; say so plainly.
		if req.ObeyRobots != nil {
			data["obey_robots"] = *req.ObeyRobots
		}
		if req.Schedule != nil {
			data["schedule"] = *req.Schedule
		}
		if req.RenderJS != nil {
			data["render_js"] = *req.RenderJS
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "seo_site.updated", "seo_site", site.ID, data)
	})
	if s.allocError(w, r, err) {
		return
	}
	list, err := s.loadSEOSites(ctx, a.WorkspaceID, &site.ID, brandSel{})
	if err != nil || len(list) != 1 {
		s.internal(w, r, errors.Join(err, errNotFound))
		return
	}
	writeJSON(w, http.StatusOK, list[0])
}

func (s *Server) deleteSEOSite(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	site, ok := s.seoSiteFor(w, r, a)
	if !ok {
		return
	}
	if !a.writes(site.BrandID) {
		writeError(w, r, http.StatusForbidden, "forbidden", string(errSharedRecord))
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM seo_sites WHERE id = $1 AND workspace_id = $2`, site.ID, a.WorkspaceID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM fix_guidelines WHERE workspace_id = $1 AND subject_id = $2`, a.WorkspaceID, site.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM fixes WHERE workspace_id = $1 AND subject_type = 'seo_site' AND subject_id = $2`, a.WorkspaceID, site.ID); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "seo_site.deleted", "seo_site", site.ID, map[string]any{})
	})
	if s.allocError(w, r, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// verifySEOSite looks for the site's proof of ownership now: the DNS
// record, the meta tag on the start page, or the file.
// addSEOSiteDNS adds the site's TXT proof with the workspace's own DNS
// host connection; the site is then looked for every minute until proved.
func (s *Server) addSEOSiteDNS(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	site, ok := s.seoSiteFor(w, r, a)
	if !ok {
		return
	}
	if !a.writes(site.BrandID) {
		writeError(w, r, http.StatusForbidden, "forbidden", string(errSharedRecord))
		return
	}
	s.queueDNSAdd(w, r, a, "seo_site", site.ID, deref(site.BrandID))
}

func (s *Server) verifySEOSite(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	site, ok := s.seoSiteFor(w, r, a)
	if !ok {
		return
	}
	if !a.writes(site.BrandID) {
		writeError(w, r, http.StatusForbidden, "forbidden", string(errSharedRecord))
		return
	}
	ctx := r.Context()
	var token string
	if err := s.pool.QueryRow(ctx, `SELECT verify_token FROM seo_sites WHERE id = $1`, site.ID).Scan(&token); err != nil {
		s.internal(w, r, err)
		return
	}
	method, err := s.crawler.Check(ctx, a.Test, a.WorkspaceID, site.StartURL, site.Host, token)
	if errors.Is(err, crawl.ErrNotVerified) {
		if _, err := s.pool.Exec(ctx, `UPDATE seo_sites SET verify_checked_at = now() WHERE id = $1`, site.ID); err != nil {
			s.internal(w, r, err)
			return
		}
		p := site.Verification
		writeError(w, r, http.StatusConflict, "verification_failed", fmt.Sprintf(
			"no proof found for %s yet. Add one of these, then try again: a DNS TXT record on %s with the value %s; "+
				"the tag %s in the <head> of %s; or a file at %s holding %s. DNS changes can take a while to appear.",
			site.Host, p.DNSName, p.DNSValue, p.MetaTag, site.StartURL, p.FileURL, p.FileBody))
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			UPDATE seo_sites SET `+crawl.ProvedDefaults()+`, verified_at = COALESCE(verified_at, now()), verify_method = $2, verify_checked_at = now(), updated_at = now()
			WHERE id = $1`, site.ID, method); err != nil {
			return err
		}
		if site.Verified {
			return nil // checked again; nothing changed
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "seo_site.verified", "seo_site", site.ID, map[string]any{"method": method})
	})
	if s.allocError(w, r, err) {
		return
	}
	list, err := s.loadSEOSites(ctx, a.WorkspaceID, &site.ID, brandSel{})
	if err != nil || len(list) != 1 {
		s.internal(w, r, errors.Join(err, errNotFound))
		return
	}
	writeJSON(w, http.StatusOK, list[0])
}

func (s *Server) startCrawl(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	site, ok := s.seoSiteFor(w, r, a)
	if !ok {
		return
	}
	if !a.writes(site.BrandID) {
		writeError(w, r, http.StatusForbidden, "forbidden", string(errSharedRecord))
		return
	}
	ctx := r.Context()
	var id string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		id, err = crawl.StartRun(ctx, tx, a.WorkspaceID, site.ID, "manual", &a.ActorID)
		if err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "crawl.started", "crawl", id, map[string]any{"site_id": site.ID, "trigger": "manual"})
	})
	if s.crawlStartError(w, r, err) {
		return
	}
	if s.allocError(w, r, err) {
		return
	}
	run, err := s.loadCrawl(ctx, a.WorkspaceID, id)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, run)
}

func (s *Server) loadCrawl(ctx context.Context, ws, id string) (crawlRunView, error) {
	var v crawlRunView
	err := scanCrawlRun(s.pool.QueryRow(ctx, `SELECT `+crawlRunCols+` FROM crawl_runs r WHERE r.id = $1 AND r.workspace_id = $2`, id, ws), &v)
	return v, err
}

// crawlFor loads the {crawl} path value's run, or writes a 404. It also
// returns the site's brand.
func (s *Server) crawlFor(w http.ResponseWriter, r *http.Request, a *access) (*crawlRunView, *string, bool) {
	id := r.PathValue("crawl")
	if validUUID(id) {
		var v crawlRunView
		var brand *string
		err := scanCrawlRun(s.pool.QueryRow(r.Context(), `SELECT `+crawlRunCols+`, s.brand_id::text
			FROM crawl_runs r JOIN seo_sites s ON s.id = r.site_id WHERE r.id = $1 AND r.workspace_id = $2`, id, a.WorkspaceID), &v, &brand)
		if err == nil && a.sees(brand) {
			return &v, brand, true
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			s.internal(w, r, err)
			return nil, nil, false
		}
	}
	writeError(w, r, http.StatusNotFound, "not_found", "crawl not found")
	return nil, nil, false
}

func (s *Server) listCrawls(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	q := r.URL.Query()
	limit, err := limitParam(r, 50, 200)
	if s.allocError(w, r, err) {
		return
	}
	site := q.Get("site")
	if site != "" && !validUUID(site) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "site is a site's id")
		return
	}
	before := q.Get("before")
	if before != "" && !validUUID(before) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "before is a crawl id from next_before")
		return
	}
	sel, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	args := []any{a.WorkspaceID, site, before, limit}
	rows, err := s.pool.Query(r.Context(), `SELECT `+crawlRunCols+` FROM crawl_runs r JOIN seo_sites s ON s.id = r.site_id
		WHERE r.workspace_id = $1 AND ($2 = '' OR r.site_id::text = $2)
		  AND ($3 = '' OR (r.created_at, r.id) < (SELECT created_at, id FROM crawl_runs WHERE id::text = $3 AND workspace_id = $1))`+
		sel.sql("s.brand_id", &args)+`
		ORDER BY r.created_at DESC, r.id DESC LIMIT $4`, args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (crawlRunView, error) {
		var v crawlRunView
		return v, scanCrawlRun(row, &v)
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var next *string
	if len(list) == limit {
		next = &list[len(list)-1].ID
	}
	if list == nil {
		list = []crawlRunView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "next_before": next})
}

func (s *Server) getCrawl(w http.ResponseWriter, r *http.Request) {
	if run, _, ok := s.crawlFor(w, r, accessFrom(r.Context())); ok {
		writeJSON(w, http.StatusOK, run)
	}
}

func (s *Server) stopCrawl(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	run, brand, ok := s.crawlFor(w, r, a)
	if !ok {
		return
	}
	if !a.writes(brand) {
		writeError(w, r, http.StatusForbidden, "forbidden", string(errSharedRecord))
		return
	}
	ctx := r.Context()
	stopped := false
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE crawl_runs SET status = 'stopped', reason = 'stopped', finished_at = now(), lease_until = NULL
			WHERE id = $1 AND workspace_id = $2 AND status IN ('queued', 'running')`, run.ID, a.WorkspaceID)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		stopped = true
		if _, err := tx.Exec(ctx, `UPDATE crawl_pages SET status = 'skipped', reason = 'stopped' WHERE run_id = $1 AND status = 'queued'`, run.ID); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "crawl.stopped", "crawl", run.ID, map[string]any{"site_id": run.SiteID})
	})
	if s.allocError(w, r, err) {
		return
	}
	if !stopped {
		writeError(w, r, http.StatusConflict, "conflict", "the crawl already ended ("+run.Status+")")
		return
	}
	v, err := s.loadCrawl(ctx, a.WorkspaceID, run.ID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

type crawlPageView struct {
	ID          string     `json:"id"`
	URL         string     `json:"url"`
	Depth       int        `json:"depth"`
	Source      string     `json:"source"`
	FoundOn     *string    `json:"found_on"`
	Status      string     `json:"status"`
	Reason      *string    `json:"reason"`
	HTTPStatus  *int       `json:"http_status"`
	ContentType *string    `json:"content_type"`
	Bytes       int        `json:"bytes"`
	DurationMS  int        `json:"duration_ms"`
	RedirectTo  *string    `json:"redirect_to"`
	Noindex     bool       `json:"noindex"`
	Nofollow    bool       `json:"nofollow"`
	LinksOut    int        `json:"links_out"`
	LinksIn     int        `json:"links_in"`
	FetchedAt   *time.Time `json:"fetched_at"`
	XRobotsTag  *string    `json:"x_robots_tag"`

	// On-page facts of an HTML page.
	Title           *string          `json:"title"`
	TitleCount      int              `json:"title_count"`
	MetaDescription *string          `json:"meta_description"`
	Canonical       *string          `json:"canonical"`
	CanonicalCount  int              `json:"canonical_count"`
	H1              []string         `json:"h1"`
	Lang            *string          `json:"lang"`
	Hreflang        []crawl.Hreflang `json:"hreflang"`
	SchemaTypes     []string         `json:"schema_types"`
	WordCount       int              `json:"word_count"`
	Images          int              `json:"images"`
	ImagesNoAlt     int              `json:"images_no_alt"`
	ContentHash     *string          `json:"content_hash"`
	Viewport        bool             `json:"viewport"`
	FetchedVia      *string          `json:"fetched_via"`
}

const crawlPageCols = `id::text, url, depth, source, NULLIF(found_on, ''), status, NULLIF(reason, ''), http_status, NULLIF(content_type, ''),
	bytes, duration_ms, NULLIF(redirect_to, ''), noindex, nofollow, links_out, links_in, fetched_at, NULLIF(x_robots_tag, ''),
	NULLIF(title, ''), title_count, NULLIF(meta_description, ''), NULLIF(canonical, ''), canonical_count, h1, NULLIF(lang, ''),
	hreflang, schema_types, word_count, images, images_no_alt, NULLIF(content_hash, ''), viewport,
	NULLIF(fetched_via, '')`

func scanCrawlPage(row pgx.Row) (crawlPageView, error) {
	var v crawlPageView
	err := row.Scan(&v.ID, &v.URL, &v.Depth, &v.Source, &v.FoundOn, &v.Status, &v.Reason, &v.HTTPStatus, &v.ContentType,
		&v.Bytes, &v.DurationMS, &v.RedirectTo, &v.Noindex, &v.Nofollow, &v.LinksOut, &v.LinksIn, &v.FetchedAt, &v.XRobotsTag,
		&v.Title, &v.TitleCount, &v.MetaDescription, &v.Canonical, &v.CanonicalCount, &v.H1, &v.Lang,
		&v.Hreflang, &v.SchemaTypes, &v.WordCount, &v.Images, &v.ImagesNoAlt, &v.ContentHash, &v.Viewport,
		&v.FetchedVia)
	return v, err
}

var crawlPageStatuses = []string{"queued", "fetched", "blocked", "failed", "skipped"}

func (s *Server) listCrawlPages(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	run, _, ok := s.crawlFor(w, r, a)
	if !ok {
		return
	}
	q := r.URL.Query()
	limit, err := limitParam(r, 100, 1000)
	if s.allocError(w, r, err) {
		return
	}
	status := q.Get("status")
	if status != "" && !slices.Contains(crawlPageStatuses, status) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status is "+strings.Join(crawlPageStatuses, ", "))
		return
	}
	var after int64
	if v := q.Get("after"); v != "" {
		after, err = strconv.ParseInt(v, 10, 64)
		if err != nil || after < 0 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "after is a page id from next_after")
			return
		}
	}
	httpStatus := 0
	if v := q.Get("http_status"); v != "" {
		httpStatus, err = strconv.Atoi(v)
		if err != nil || httpStatus < 100 || httpStatus > 599 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "http_status is an HTTP status, like 404")
			return
		}
	}
	rows, err := s.pool.Query(r.Context(), `SELECT `+crawlPageCols+`
		FROM crawl_pages WHERE run_id = $1 AND workspace_id = $2 AND ($3 = '' OR status = $3) AND id > $4
		  AND ($6 = 0 OR http_status = $6)
		ORDER BY id LIMIT $5`, run.ID, a.WorkspaceID, status, after, limit, httpStatus)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (crawlPageView, error) { return scanCrawlPage(row) })
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var next *string
	if len(list) == limit {
		next = &list[len(list)-1].ID
	}
	if list == nil {
		list = []crawlPageView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "next_after": next})
}

type crawlLinkView struct {
	URL      string `json:"url"`
	Anchor   string `json:"anchor"`
	Internal bool   `json:"internal"`
	Nofollow bool   `json:"nofollow"`
	// The linked page's status in this crawl; null for a page it did not
	// read (another site, or not reached).
	HTTPStatus *int `json:"http_status"`
}

// maxPageLinks is how many links in and out one page's answer lists.
const maxPageLinks = 500

// getCrawlPage answers one page with its links out and the pages that
// link to it.
func (s *Server) getCrawlPage(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	run, _, ok := s.crawlFor(w, r, a)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("page"), 10, 64)
	ctx := r.Context()
	var page crawlPageView
	if err == nil {
		page, err = scanCrawlPage(s.pool.QueryRow(ctx, `SELECT `+crawlPageCols+` FROM crawl_pages
			WHERE id = $1 AND run_id = $2 AND workspace_id = $3`, id, run.ID, a.WorkspaceID))
	}
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, strconv.ErrSyntax) || errors.Is(err, strconv.ErrRange) {
			writeError(w, r, http.StatusNotFound, "not_found", "page not found")
			return
		}
		s.internal(w, r, err)
		return
	}
	scan := func(row pgx.CollectableRow) (crawlLinkView, error) {
		var v crawlLinkView
		return v, row.Scan(&v.URL, &v.Anchor, &v.Internal, &v.Nofollow, &v.HTTPStatus)
	}
	rows, err := s.pool.Query(ctx, `
		SELECT l.to_url, l.anchor, l.internal, l.nofollow, p.http_status
		FROM crawl_links l LEFT JOIN crawl_pages p ON p.run_id = l.run_id AND p.url = l.to_url
		WHERE l.from_page = $1 AND l.run_id = $2 ORDER BY l.id LIMIT $3`, id, run.ID, maxPageLinks)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out, err := pgx.CollectRows(rows, scan)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	rows, err = s.pool.Query(ctx, `
		SELECT f.url, l.anchor, true, l.nofollow, f.http_status
		FROM crawl_links l JOIN crawl_pages f ON f.id = l.from_page
		WHERE l.run_id = $1 AND l.to_url = $2 ORDER BY l.id LIMIT $3`, run.ID, page.URL, maxPageLinks)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	in, err := pgx.CollectRows(rows, scan)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if out == nil {
		out = []crawlLinkView{}
	}
	if in == nil {
		in = []crawlLinkView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"page": page, "links_out": out, "links_in": in})
}

// searchConsoleOwns reports whether one of the workspace's Search Console
// connections owns a property covering host, which proves the site
// without a DNS record, file or tag.
func (s *Server) searchConsoleOwns(ctx context.Context, ws, host string) (bool, error) {
	rows, err := s.pool.Query(ctx, `SELECT id FROM integrations WHERE workspace_id = $1 AND connector = 'google_search_console' AND status = 'connected' ORDER BY created_at`, ws)
	if err != nil {
		return false, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return false, err
	}
	var errs []error
	for _, id := range ids {
		o, err := s.integrations.Open(ctx, s.pool, ws, id)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		c, _ := s.integrations.Registry.Get(o.Integration.Connector)
		reader, ok := c.(connectors.AnalyticsReader)
		if !ok {
			continue
		}
		sites, err := s.sites(ctx, o, reader, true)
		if err != nil {
			_ = s.integrations.Checked(ctx, s.pool, ws, id, nil, err)
			errs = append(errs, err)
			continue
		}
		for _, site := range sites {
			if site.Owner && crawl.SearchConsoleCovers(site.ID, host) {
				return true, nil
			}
		}
	}
	return false, errors.Join(errs...)
}
