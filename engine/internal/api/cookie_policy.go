package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/cookies"
	"github.com/outflareai/outflare/engine/internal/metrics"
)

// The cookie policy that keeps itself up to date (engine/internal/cookies):
// what was found on each site key's site, a person's word on what the
// catalogue didn't know, scans on demand, and the policy itself for the
// workspace, the site's own pages and a hosted page.

func (s *Server) routesCookiePolicy(mux *http.ServeMux) {
	read, manage := auth.ScopeCustomersRead, auth.ScopeCustomersManage
	mux.HandleFunc("GET /v1/workspaces/{ws}/site-keys/{id}/cookies", s.inWorkspace(read, s.listSiteCookies))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/site-keys/{id}/cookies/{cookie}", s.inWorkspace(manage, s.updateSiteCookie))
	mux.HandleFunc("POST /v1/workspaces/{ws}/site-keys/{id}/cookie-scans", s.inWorkspace(manage, s.startCookieScan))
	mux.HandleFunc("GET /v1/workspaces/{ws}/site-keys/{id}/cookie-policy", s.inWorkspace(read, s.getCookiePolicy))
	mux.HandleFunc("GET /v1/workspaces/{ws}/site-keys/{id}/cookie-policy/versions", s.inWorkspace(read, s.listCookiePolicyVersions))
	// Public: the key is the authority, or for the hosted page the key's id.
	mux.HandleFunc("GET /v1/site/cookie-policy", s.siteCookiePolicy)
	mux.HandleFunc("POST /v1/site/cookies", s.siteCookieReport)
	mux.HandleFunc("OPTIONS /v1/site/cookies", s.siteEventsPreflight)
	mux.HandleFunc("GET /cookie-policy/{id}", s.hostedCookiePolicy)
}

type siteCookieView struct {
	ID            string     `json:"id"`
	Kind          string     `json:"kind"`
	Name          string     `json:"name"`
	Host          string     `json:"host"`
	Vendor        string     `json:"vendor"`
	VendorName    string     `json:"vendor_name"`
	Category      string     `json:"category"`
	Purpose       string     `json:"purpose"`
	Retention     string     `json:"retention"`
	Status        string     `json:"status"`
	Sources       []string   `json:"sources"`
	BeforeConsent bool       `json:"before_consent"`
	Pages         []string   `json:"pages"`
	ReviewedAt    *time.Time `json:"reviewed_at"`
	FirstSeenAt   time.Time  `json:"first_seen_at"`
	LastSeenAt    time.Time  `json:"last_seen_at"`
}

const siteCookieColumns = `id::text, kind, name, host, vendor, category, purpose, retention, status, sources, before_consent, pages, reviewed_at, first_seen_at, last_seen_at`

func scanSiteCookie(row pgx.CollectableRow) (siteCookieView, error) {
	var v siteCookieView
	err := row.Scan(&v.ID, &v.Kind, &v.Name, &v.Host, &v.Vendor, &v.Category, &v.Purpose, &v.Retention, &v.Status, &v.Sources,
		&v.BeforeConsent, &v.Pages, &v.ReviewedAt, &v.FirstSeenAt, &v.LastSeenAt)
	if vd, ok := cookies.VendorByID(v.Vendor); ok {
		v.VendorName = vd.Name
	}
	return v, err
}

type cookieScanView struct {
	ID         string     `json:"id"`
	SiteKeyID  string     `json:"site_key_id"`
	Status     string     `json:"status"`
	Trigger    string     `json:"trigger"`
	Pages      int        `json:"pages"`
	Found      int        `json:"found"`
	Added      int        `json:"added"`
	Error      string     `json:"error"`
	CreatedAt  time.Time  `json:"created_at"`
	StartedAt  *time.Time `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
}

const cookieScanColumns = `id::text, site_key_id::text, status, trigger, pages, found, added, error, created_at, started_at, finished_at`

// hostedPolicyURL is the policy's own page.
func (s *Server) hostedPolicyURL(keyID string) string { return s.publicURL + "/cookie-policy/" + keyID }

func (s *Server) listSiteCookies(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	key, ok := s.oneSiteKey(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	out := map[string]any{}
	err := s.readTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+siteCookieColumns+` FROM site_cookies WHERE workspace_id = $1 AND site_key_id = $2
			ORDER BY (category = 'unknown' AND status = 'new') DESC, before_consent DESC, category, vendor, kind, name LIMIT 2000`, a.WorkspaceID, key.ID)
		if err != nil {
			return err
		}
		list, err := pgx.CollectRows(rows, scanSiteCookie)
		if err != nil {
			return err
		}
		counts := map[string]int{"total": len(list), "listed": 0, "to_review": 0, "before_consent": 0}
		cutoff := time.Now().Add(-30 * 24 * time.Hour)
		for _, c := range list {
			switch {
			case c.Status == "ignored":
			case c.Category == cookies.Unknown:
				counts["to_review"]++
			case c.LastSeenAt.After(cutoff):
				counts["listed"]++
			}
			if c.BeforeConsent && c.Status != "ignored" {
				counts["before_consent"]++
			}
		}
		if list == nil {
			list = []siteCookieView{}
		}
		out["data"], out["counts"] = list, counts
		out["scan"], out["policy"] = nil, nil
		rows, err = tx.Query(ctx, `SELECT `+cookieScanColumns+` FROM cookie_scans WHERE site_key_id = $1 ORDER BY created_at DESC LIMIT 1`, key.ID)
		if err != nil {
			return err
		}
		if scans, err := pgx.CollectRows(rows, pgx.RowToStructByPos[cookieScanView]); err != nil {
			return err
		} else if len(scans) == 1 {
			out["scan"] = scans[0]
		}
		var version int
		var at time.Time
		err = tx.QueryRow(ctx, `SELECT version, created_at FROM cookie_policies WHERE site_key_id = $1 ORDER BY version DESC LIMIT 1`, key.ID).Scan(&version, &at)
		if err == nil {
			out["policy"] = map[string]any{"version": version, "published_at": at, "url": s.hostedPolicyURL(key.ID)}
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		return nil
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

type siteCookieInput struct {
	Category  *string `json:"category"`
	Purpose   *string `json:"purpose"`
	Retention *string `json:"retention"`
	Status    *string `json:"status"`
}

func (s *Server) updateSiteCookie(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	key, ok := s.oneSiteKey(w, r)
	if !ok {
		return
	}
	var in siteCookieInput
	if !decode(w, r, &in) {
		return
	}
	switch {
	case in.Category != nil && !slices.Contains(cookies.Categories, *in.Category):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "category is necessary, analytics, marketing or preferences")
		return
	case in.Status != nil && *in.Status != "listed" && *in.Status != "ignored":
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status is listed or ignored")
		return
	case in.Purpose != nil && len(*in.Purpose) > 1000, in.Retention != nil && len(*in.Retention) > 100:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "purpose is at most 1,000 characters and retention at most 100")
		return
	}
	id := r.PathValue("cookie")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "cookie not found")
		return
	}
	ctx := r.Context()
	var v siteCookieView
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `UPDATE site_cookies SET category = COALESCE($4, category), purpose = COALESCE($5, purpose),
				retention = COALESCE($6, retention),
				-- Saying what it is lists it, unless it was set aside.
				status = COALESCE($7, CASE WHEN status = 'ignored' THEN status ELSE 'listed' END),
				reviewed_at = now(), reviewed_by = $8
			WHERE id = $1 AND workspace_id = $2 AND site_key_id = $3 RETURNING `+siteCookieColumns,
			id, a.WorkspaceID, key.ID, in.Category, trimmed(in.Purpose), trimmed(in.Retention), in.Status, nullIfEmpty(a.ActorID))
		if err != nil {
			return err
		}
		list, err := pgx.CollectRows(rows, scanSiteCookie)
		if err != nil {
			return err
		}
		if len(list) == 0 {
			return errNotFound
		}
		v = list[0]
		if v.Category == cookies.Unknown && v.Status == "listed" {
			return errInvalid("say which category it is in to list it")
		}
		if err := record(ctx, tx, &a.principal, a.WorkspaceID, "site_cookie.classified", "site_key", key.ID, map[string]any{
			"kind": v.Kind, "category": v.Category, "status": v.Status, "known": v.Vendor != "",
		}); err != nil {
			return err
		}
		_, err = cookies.Publish(ctx, tx, a.WorkspaceID, key.ID, cookies.Who{ActorID: a.ActorID, ActorType: a.ActorType, RequestID: RequestID(ctx)}, time.Now())
		return err
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func trimmed(p *string) *string {
	if p == nil {
		return nil
	}
	t := strings.TrimSpace(*p)
	return &t
}

func (s *Server) startCookieScan(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	key, ok := s.oneSiteKey(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	var v cookieScanView
	started := false
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		trigger := "person"
		if a.ActorType != "human" {
			trigger = "api"
		}
		id, ok, err := cookies.StartScan(ctx, tx, a.WorkspaceID, key.ID, trigger, a.ActorID)
		if err != nil {
			return err
		}
		started = ok
		if ok {
			if err := record(ctx, tx, &a.principal, a.WorkspaceID, "cookie_scan.started", "site_key", key.ID, map[string]any{"scan_id": id}); err != nil {
				return err
			}
		}
		rows, err := tx.Query(ctx, `SELECT `+cookieScanColumns+` FROM cookie_scans WHERE id = $1`, id)
		if err != nil {
			return err
		}
		v, err = pgx.CollectExactlyOneRow(rows, pgx.RowToStructByPos[cookieScanView])
		return err
	})
	if s.allocError(w, r, err) {
		return
	}
	code := http.StatusOK
	if started {
		code = http.StatusCreated
	}
	writeJSON(w, code, v)
}

// policyVersion reads a published version of a site key's policy: the
// latest, or the one asked for.
func policyVersion(ctx context.Context, q pgx.Tx, ws, keyID string, version int) (cookies.Version, error) {
	var v cookies.Version
	var raw []byte
	err := q.QueryRow(ctx, `SELECT version, content, created_at FROM cookie_policies WHERE workspace_id = $1 AND site_key_id = $2
		AND ($3 = 0 OR version = $3) ORDER BY version DESC LIMIT 1`, ws, keyID, version).Scan(&v.Version, &raw, &v.Published)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, errNotFound
	}
	if err != nil {
		return v, err
	}
	return v, json.Unmarshal(raw, &v.Policy)
}

type cookiePolicyView struct {
	Version     int                      `json:"version"`
	PublishedAt time.Time                `json:"published_at"`
	Site        string                   `json:"site"`
	Categories  []cookies.PolicyCategory `json:"categories"`
	Format      string                   `json:"format"`
	Content     string                   `json:"content"`
	URL         string                   `json:"url"`
}

func (s *Server) policyView(v cookies.Version, keyID, format string) cookiePolicyView {
	out := cookiePolicyView{Version: v.Version, PublishedAt: v.Published, Site: v.Site, Categories: v.Categories, Format: format, URL: s.hostedPolicyURL(keyID)}
	switch format {
	case "markdown":
		out.Content = v.Markdown()
	case "text":
		out.Content = v.Text()
	case "html":
		out.Content = v.HTML()
	}
	return out
}

func (s *Server) getCookiePolicy(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	key, ok := s.oneSiteKey(w, r)
	if !ok {
		return
	}
	format := r.URL.Query().Get("format")
	if format == "" {
		format = "markdown"
	}
	if format != "markdown" && format != "text" && format != "html" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "format is markdown, text or html")
		return
	}
	version := 0
	if q := r.URL.Query().Get("version"); q != "" {
		n, err := strconv.Atoi(q)
		if err != nil || n < 1 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "version is a policy version number")
			return
		}
		version = n
	}
	var v cookies.Version
	err := s.readTx(r.Context(), func(tx pgx.Tx) error {
		var err error
		v, err = policyVersion(r.Context(), tx, a.WorkspaceID, key.ID, version)
		return err
	})
	if errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "no cookie policy yet: scan the site (POST .../cookie-scans) or wait for visitors' browsers to report")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, s.policyView(v, key.ID, format))
}

func (s *Server) listCookiePolicyVersions(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	key, ok := s.oneSiteKey(w, r)
	if !ok {
		return
	}
	type versionView struct {
		Version     int       `json:"version"`
		PublishedAt time.Time `json:"published_at"`
		Added       []string  `json:"added"`
		Removed     []string  `json:"removed"`
		Reasked     bool      `json:"reasked"`
	}
	var list []versionView
	err := s.readTx(r.Context(), func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `SELECT version, created_at, added, removed, reasked FROM cookie_policies
			WHERE workspace_id = $1 AND site_key_id = $2 ORDER BY version DESC LIMIT 500`, a.WorkspaceID, key.ID)
		if err != nil {
			return err
		}
		list, err = pgx.CollectRows(rows, pgx.RowToStructByPos[versionView])
		return err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if list == nil {
		list = []versionView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

// publicPolicy finds a live key's latest policy.
func (s *Server) publicPolicy(ctx context.Context, where string, arg string) (cookies.Version, string, error) {
	var ws, keyID string
	err := s.pool.QueryRow(ctx, `SELECT workspace_id::text, id::text FROM site_keys WHERE revoked_at IS NULL AND `+where, arg).Scan(&ws, &keyID)
	if errors.Is(err, pgx.ErrNoRows) {
		return cookies.Version{}, "", errNotFound
	}
	if err != nil {
		return cookies.Version{}, "", err
	}
	var v cookies.Version
	err = s.readTx(ctx, func(tx pgx.Tx) error {
		var err error
		v, err = policyVersion(ctx, tx, ws, keyID, 0)
		return err
	})
	return v, keyID, err
}

// siteCookiePolicy is the policy for the site's own pages: the script
// fills data-outflare-cookie-policy elements with its html.
func (s *Server) siteCookiePolicy(w http.ResponseWriter, r *http.Request) {
	siteCORS(w)
	key := r.URL.Query().Get("key")
	conn := clientIP(r)
	if s.siteIPLimit.over(conn) {
		s.siteIPLimit.allow(w, r, conn)
		return
	}
	format := r.URL.Query().Get("format")
	if format == "" {
		format = "json"
	}
	if !slices.Contains([]string{"json", "markdown", "text", "html"}, format) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "format is json, markdown, text or html")
		return
	}
	var v cookies.Version
	var keyID string
	err := errNotFound
	if strings.HasPrefix(key, auth.PrefixSiteKey) && len(key) <= 100 {
		v, keyID, err = s.publicPolicy(r.Context(), `(key = $1 OR (previous_key = $1 AND previous_expires_at > now()))`, key)
	}
	if errors.Is(err, errNotFound) {
		// Keys that don't exist count against the address, as for events.
		if keyID != "" || s.siteIPLimit.allow(w, r, conn) {
			writeError(w, r, http.StatusNotFound, "not_found", "no cookie policy for this site key yet")
		}
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if !s.siteConsentLimit.allow(w, r, keyID) {
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=300")
	switch format {
	case "json":
		writeJSON(w, http.StatusOK, s.policyView(v, keyID, "json"))
	case "markdown":
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		io.WriteString(w, v.Markdown()) //nolint:errcheck
	case "text":
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		io.WriteString(w, v.Text()) //nolint:errcheck
	case "html":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, v.HTML()) //nolint:errcheck
	}
}

// hostedCookiePolicy is the policy as a page of its own, for sites that
// link to it rather than embed it.
func (s *Server) hostedCookiePolicy(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	conn := clientIP(r)
	if !s.siteIPLimit.allow(w, r, conn) {
		return
	}
	err := errNotFound
	var v cookies.Version
	if validUUID(id) {
		v, _, err = s.publicPolicy(r.Context(), `id = $1`, id)
	}
	if errors.Is(err, errNotFound) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, "No cookie policy here.\n") //nolint:errcheck
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "public, max-age=300")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
	io.WriteString(w, v.Page()) //nolint:errcheck
}

// siteCookieReport takes what a browser saw on the site: names only.
func (s *Server) siteCookieReport(w http.ResponseWriter, r *http.Request) {
	siteCORS(w)
	outcome := "refused"
	defer func() { metrics.CookieReport(outcome) }()
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 32<<10))
	if err != nil {
		writeError(w, r, http.StatusRequestEntityTooLarge, "invalid_request", "the body must be under 32 KB")
		return
	}
	var in struct {
		Key     string          `json:"key"`
		Cookies []string        `json:"cookies"`
		Local   []string        `json:"local"`
		Session []string        `json:"session"`
		Scripts []string        `json:"scripts"`
		Iframes []string        `json:"iframes"`
		Path    string          `json:"path"`
		Granted map[string]bool `json:"granted"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", `send {"key": "ofk_...", "cookies": [...], "local": [...], "scripts": [...]}`)
		return
	}
	if len(in.Cookies) > 200 || len(in.Local) > 200 || len(in.Session) > 200 || len(in.Scripts) > 200 || len(in.Iframes) > 100 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "at most 200 names of each kind, and 100 iframes")
		return
	}
	k, failed := s.siteBanner(w, r, in.Key)
	if failed != "" {
		outcome = failed
		return
	}
	origin := r.Header.Get("Origin")
	if !originAllowed(k.origins, origin) {
		writeError(w, r, http.StatusForbidden, "forbidden", "this site key does not take requests from this origin; add it to the key's origins")
		return
	}
	host := ""
	if u, err := url.Parse(origin); err == nil {
		host = u.Hostname()
	}
	var granted map[string]bool
	if k.banner.Saved && k.banner.Config.Enabled {
		granted = map[string]bool{}
		for _, c := range []string{cookies.Analytics, cookies.Marketing, cookies.Preferences} {
			granted[c] = in.Granted[c]
		}
	}
	added, _, err := s.cookieReports.Take(r.Context(), k.workspaceID, k.id, cookies.Report{
		Cookies: in.Cookies, Local: in.Local, Session: in.Session, Scripts: in.Scripts, Iframes: in.Iframes,
		Host: host, Path: in.Path, Granted: granted,
	}, time.Now())
	if err != nil {
		outcome = "error"
		s.internal(w, r, err)
		return
	}
	outcome = "ok"
	writeJSON(w, http.StatusOK, map[string]int{"added": added})
}
