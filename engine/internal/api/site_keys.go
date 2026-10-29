package api

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/net/publicsuffix"

	"github.com/outflareai/outflare/engine/internal/adtrack"
	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/visitors"
)

// Website tracking: a workspace makes a site key (ofk_), puts the snippet
// (/site.js) on its site, and each visit becomes a page event on the
// visitor's anonymous id, with the landing page's campaign tags, click ids
// and referral code as they came. Nothing is classified in the browser:
// channels and attribution are worked out on the server from these raw
// events. The key is public, so it can only add browser events: it reads
// nothing, never names or merges a person, never starts a journey, and
// opens no other endpoint. Once the workspace's own product identifies the
// visitor (identify with userId and the snippet's anonymousId), their
// earlier visits join their record.

//go:embed site.js
var siteScript []byte

const (
	siteSource = "browser"
	// maxSiteKeys per workspace: one per site or brand is the usual.
	maxSiteKeys = 20
	// A browser request carries a few events; the snippet sends one.
	maxSiteBatch = 20
	maxSiteBody  = 64 << 10
	maxSiteProps = 4 << 10
	// How long a rotated key's old value keeps working.
	siteKeyGrace = 7 * 24 * time.Hour
	// Events one key takes a day; past it, the rest of the day is refused.
	siteKeyDailyEvents = 250_000
	// And all of a workspace's keys together.
	siteWorkspaceDailyEvents = 1_000_000
	// Unapproved event names browsers can add to the catalog. Past it a
	// browser can only send names the workspace already has.
	maxBrowserQuarantined = 100
)

var (
	siteKinds = []string{"page", "track"}
	// The click ids ad platforms add to landing pages, and the cookies of
	// their pixels site.js reads with consent (adtrack).
	siteClickIDs = append(adtrack.Params(), siteCookieKeys()...)
	siteCampaign = []string{"source", "medium", "name", "term", "content", "id"}
	sitePage     = []string{"url", "path", "search", "referrer", "title"}
	originRe     = regexp.MustCompile(`^https?://(\*\.)?[a-z0-9.-]+(:[0-9]{1,5})?$`)
)

func (s *Server) routesSiteKeys(mux *http.ServeMux) {
	read, manage := auth.ScopeCustomersRead, auth.ScopeCustomersManage
	mux.HandleFunc("GET /v1/workspaces/{ws}/site-keys", s.inWorkspace(read, s.listSiteKeys))
	mux.HandleFunc("POST /v1/workspaces/{ws}/site-keys", s.inWorkspace(manage, s.createSiteKey))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/site-keys/{id}", s.inWorkspace(manage, s.updateSiteKey))
	mux.HandleFunc("POST /v1/workspaces/{ws}/site-keys/{id}/rotate", s.inWorkspace(manage, s.rotateSiteKey))
	mux.HandleFunc("DELETE /v1/workspaces/{ws}/site-keys/{id}", s.inWorkspace(manage, s.revokeSiteKey))
	mux.HandleFunc("POST /v1/workspaces/{ws}/site-keys/{id}/proxy-secret", s.inWorkspace(manage, s.createSiteProxySecret))
	mux.HandleFunc("DELETE /v1/workspaces/{ws}/site-keys/{id}/proxy-secret", s.inWorkspace(manage, s.removeSiteProxySecret))
	// Public: the key in the body is the authority.
	mux.HandleFunc("POST /v1/site/events", s.siteEvents)
	mux.HandleFunc("OPTIONS /v1/site/events", s.siteEventsPreflight)
	mux.HandleFunc("GET /site.js", s.siteJS)
}

type siteKeyView struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Key     string   `json:"key"`
	BrandID *string  `json:"brand_id"`
	Origins []string `json:"origins"`
	// Query parameters left out of page addresses, on top of the ones
	// always dropped (site_params.go).
	ExcludedParams    []string   `json:"excluded_params"`
	PreviousExpiresAt *time.Time `json:"previous_expires_at"`
	EventsToday       int        `json:"events_today"`
	BotsToday         int        `json:"bots_today"`
	CreatedAt         time.Time  `json:"created_at"`
	LastUsedAt        *time.Time `json:"last_used_at"`
	RotatedAt         *time.Time `json:"rotated_at"`
	// When the key's proxy secret was made; null when it has none.
	ProxySecretAt *time.Time `json:"proxy_secret_at"`
	Snippet       string     `json:"snippet"`
}

type siteKeyInput struct {
	Name    *string   `json:"name"`
	BrandID *string   `json:"brand_id"`
	Origins *[]string `json:"origins"`
	// Query parameters to leave out of page addresses.
	ExcludedParams *[]string `json:"excluded_params"`
}

func newSiteKey() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return auth.PrefixSiteKey + base64.RawURLEncoding.EncodeToString(b)
}

// siteSnippet is the tag to paste into a site's <head>. When the key's
// origins are on more than one domain, it lists them, so links between
// them keep the visitor.
func (s *Server) siteSnippet(key string, origins []string) string {
	base := strings.TrimRight(s.publicURL, "/")
	domains := []string{}
	for _, o := range origins {
		u, err := url.Parse(strings.Replace(o, "*.", "", 1))
		if err != nil || u.Hostname() == "" {
			continue
		}
		d, err := publicsuffix.EffectiveTLDPlusOne(u.Hostname())
		if err != nil {
			d = u.Hostname()
		}
		if !slices.Contains(domains, d) {
			domains = append(domains, d)
		}
	}
	tag := `<script async src="` + base + `/site.js" data-key="` + key + `"`
	if len(domains) > 1 {
		tag += ` data-domains="` + strings.Join(domains, ",") + `"`
	}
	return tag + `></script>`
}

func (s *Server) loadSiteKeys(ctx context.Context, ws string, id *string) ([]siteKeyView, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT k.id::text, k.name, k.key, k.brand_id::text, k.origins, k.excluded_params, CASE WHEN k.previous_expires_at > now() THEN k.previous_expires_at END,
			coalesce((SELECT d.events FROM site_key_days d WHERE d.key_id = k.id AND d.day = current_date), 0),
			coalesce((SELECT d.bots FROM site_key_days d WHERE d.key_id = k.id AND d.day = current_date), 0),
			k.created_at, k.last_used_at, k.rotated_at, k.proxy_secret_at
		FROM site_keys k WHERE k.workspace_id = $1 AND k.revoked_at IS NULL AND ($2::uuid IS NULL OR k.id = $2)
		ORDER BY k.created_at, k.id`, ws, id)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (siteKeyView, error) {
		var v siteKeyView
		err := row.Scan(&v.ID, &v.Name, &v.Key, &v.BrandID, &v.Origins, &v.ExcludedParams, &v.PreviousExpiresAt, &v.EventsToday, &v.BotsToday, &v.CreatedAt, &v.LastUsedAt, &v.RotatedAt, &v.ProxySecretAt)
		v.Snippet = s.siteSnippet(v.Key, v.Origins)
		return v, err
	})
	if list == nil {
		list = []siteKeyView{}
	}
	return list, err
}

func (s *Server) listSiteKeys(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	sel, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	list, err := s.loadSiteKeys(r.Context(), a.WorkspaceID, nil)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list = slices.DeleteFunc(list, func(k siteKeyView) bool { return !sel.matches(k.BrandID) })
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

// oneSiteKey loads the key in the path, or answers 404.
func (s *Server) oneSiteKey(w http.ResponseWriter, r *http.Request) (siteKeyView, bool) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if validUUID(id) {
		list, err := s.loadSiteKeys(r.Context(), a.WorkspaceID, &id)
		if err != nil {
			s.internal(w, r, err)
			return siteKeyView{}, false
		}
		if len(list) == 1 {
			return list[0], true
		}
	}
	writeError(w, r, http.StatusNotFound, "not_found", "site key not found")
	return siteKeyView{}, false
}

// cleanOrigins checks and normalizes the origins a key takes events from.
// Each is stored in one of two forms:
//
//   - https://*.example.com: example.com and every subdomain, over https on
//     the default port. A bare domain (example.com or *.example.com) is
//     taken as this, since that is what people mean when they type one.
//   - scheme://host[:port]: that exact origin. http is allowed only for
//     localhost, for trying the snippet on a developer's machine.
//
// A wildcard or bare domain must be a registrable domain or below one, so
// "com" or "*.co.uk" can't open a key to every site.
func cleanOrigins(in []string) ([]string, error) {
	if len(in) > 20 {
		return nil, errInvalid("origins: at most 20; example.com covers the site and every subdomain")
	}
	out := []string{}
	for _, raw := range in {
		o, err := cleanOrigin(raw)
		if err != nil {
			return nil, err
		}
		if !slices.Contains(out, o) {
			out = append(out, o)
		}
	}
	return out, nil
}

func cleanOrigin(raw string) (string, error) {
	o := strings.TrimRight(strings.ToLower(strings.TrimSpace(raw)), "/")
	bad := errInvalid("origins are a domain such as example.com (the site and every subdomain), or one exact origin such as https://app.example.com or http://localhost:3000, with no path")
	scheme, rest, explicit := strings.Cut(o, "://")
	if !explicit {
		scheme, rest = "https", "*."+strings.TrimPrefix(o, "*.")
	}
	if !originRe.MatchString(scheme + "://" + rest) {
		return "", bad
	}
	if host, ok := strings.CutPrefix(rest, "*."); ok {
		host = strings.TrimSuffix(host, ".")
		if scheme != "https" || strings.Contains(host, ":") {
			return "", errInvalid("a whole domain (*.example.com) is taken over https on the default port; list another origin exactly, such as http://localhost:3000")
		}
		if d, err := publicsuffix.EffectiveTLDPlusOne(host); err != nil || d == "" || !strings.Contains(host, ".") {
			return "", errInvalid("origins: " + host + " is not a domain a site can be on; use your site's own domain, such as example.com")
		}
		return "https://*." + host, nil
	}
	u, err := url.Parse(scheme + "://" + rest)
	if err != nil || u.Hostname() == "" {
		return "", bad
	}
	host := strings.TrimSuffix(u.Hostname(), ".")
	if scheme == "http" && host != "localhost" && host != "127.0.0.1" {
		return "", errInvalid("origins: http is taken only from localhost; sites send events over https")
	}
	if u.Port() != "" {
		host += ":" + u.Port()
	}
	return scheme + "://" + host, nil
}

// originAllowed reports whether a request's Origin is one of the key's.
func originAllowed(origins []string, origin string) bool {
	if len(origins) == 0 {
		return true
	}
	u, err := url.Parse(strings.ToLower(strings.TrimSpace(origin)))
	if err != nil || u.Hostname() == "" {
		return false
	}
	host := strings.TrimSuffix(u.Hostname(), ".")
	exact := u.Scheme + "://" + host
	if u.Port() != "" {
		exact += ":" + u.Port()
	}
	for _, o := range origins {
		if o == exact {
			return true
		}
		// https://*.example.com takes example.com and every subdomain, on
		// the default port. Keys saved before ports were refused on a whole
		// domain may name one (https://*.example.com:8443); it must match.
		if scheme, domain, ok := strings.Cut(o, "://*."); ok && scheme == u.Scheme {
			domain, port, _ := strings.Cut(domain, ":")
			if port == u.Port() && (host == domain || strings.HasSuffix(host, "."+domain)) {
				return true
			}
		}
	}
	return false
}

func (s *Server) siteKeyBrand(ctx context.Context, ws string, brand *string) error {
	if brand == nil {
		return nil
	}
	var ok bool
	if validUUID(*brand) {
		if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM brands WHERE id = $1 AND workspace_id = $2)`, *brand, ws).Scan(&ok); err != nil {
			return err
		}
	}
	if !ok {
		return errInvalid("brand_id is not a brand in this workspace: GET .../brands lists them")
	}
	return nil
}

func (s *Server) createSiteKey(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req siteKeyInput
	if !decode(w, r, &req) {
		return
	}
	ctx := r.Context()
	name := strings.TrimSpace(deref(req.Name))
	if name == "" || len(name) > 100 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "name is required, up to 100 characters, such as the site's domain")
		return
	}
	origins := []string{}
	if req.Origins != nil {
		var err error
		if origins, err = cleanOrigins(*req.Origins); s.allocError(w, r, err) {
			return
		}
	}
	excluded := []string{}
	if req.ExcludedParams != nil {
		var err error
		if excluded, err = cleanExcludedParams(*req.ExcludedParams); s.allocError(w, r, err) {
			return
		}
	}
	if s.allocError(w, r, s.siteKeyBrand(ctx, a.WorkspaceID, req.BrandID)) {
		return
	}
	var id string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM site_keys WHERE workspace_id = $1 AND revoked_at IS NULL`, a.WorkspaceID).Scan(&n); err != nil {
			return err
		}
		if n >= maxSiteKeys {
			return errInvalid("a workspace can have at most 20 site keys; one key can serve several origins")
		}
		if err := tx.QueryRow(ctx, `INSERT INTO site_keys (workspace_id, brand_id, name, key, origins, excluded_params, created_by) VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id::text`,
			a.WorkspaceID, req.BrandID, name, newSiteKey(), origins, excluded, nullIfEmpty(a.ActorID)).Scan(&id); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "site_key.created", "site_key", id, map[string]any{"brand_id": req.BrandID, "origins": len(origins), "excluded_params": len(excluded)})
	})
	if s.allocError(w, r, err) {
		return
	}
	list, err := s.loadSiteKeys(ctx, a.WorkspaceID, &id)
	if err != nil || len(list) != 1 {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, list[0])
}

func (s *Server) updateSiteKey(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.oneSiteKey(w, r)
	if !ok {
		return
	}
	var req siteKeyInput
	if !decode(w, r, &req) {
		return
	}
	ctx := r.Context()
	name, brand, origins, excluded := cur.Name, cur.BrandID, cur.Origins, cur.ExcludedParams
	if req.Name != nil {
		if name = strings.TrimSpace(*req.Name); name == "" || len(name) > 100 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "name must be 1 to 100 characters")
			return
		}
	}
	if req.Origins != nil {
		var err error
		if origins, err = cleanOrigins(*req.Origins); s.allocError(w, r, err) {
			return
		}
	}
	if req.ExcludedParams != nil {
		var err error
		if excluded, err = cleanExcludedParams(*req.ExcludedParams); s.allocError(w, r, err) {
			return
		}
	}
	if req.BrandID != nil {
		if s.allocError(w, r, s.siteKeyBrand(ctx, a.WorkspaceID, req.BrandID)) {
			return
		}
		brand = req.BrandID
	}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE site_keys SET name = $3, brand_id = $4, origins = $5, excluded_params = $6 WHERE id = $1 AND workspace_id = $2`,
			cur.ID, a.WorkspaceID, name, brand, origins, excluded); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "site_key.updated", "site_key", cur.ID, map[string]any{"brand_id": brand, "origins": len(origins), "excluded_params": len(excluded)})
	})
	if s.allocError(w, r, err) {
		return
	}
	list, err := s.loadSiteKeys(ctx, a.WorkspaceID, &cur.ID)
	if err != nil || len(list) != 1 {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, list[0])
}

// rotateSiteKey gives the key a new value. The old one keeps working for
// seven days, so the site can be updated without losing visits.
func (s *Server) rotateSiteKey(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.oneSiteKey(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE site_keys SET previous_key = key, previous_expires_at = $3, key = $4, rotated_at = now()
			WHERE id = $1 AND workspace_id = $2`, cur.ID, a.WorkspaceID, time.Now().Add(siteKeyGrace), newSiteKey()); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "site_key.rotated", "site_key", cur.ID, nil)
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := s.loadSiteKeys(ctx, a.WorkspaceID, &cur.ID)
	if err != nil || len(list) != 1 {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, list[0])
}

// revokeSiteKey stops a key, and its previous value, at once.
func (s *Server) revokeSiteKey(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.oneSiteKey(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE site_keys SET revoked_at = now(), previous_expires_at = NULL WHERE id = $1 AND workspace_id = $2`,
			cur.ID, a.WorkspaceID); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "site_key.revoked", "site_key", cur.ID, nil)
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// siteCORS lets any page send events: the key, not the page, is checked.
func siteCORS(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	h.Set("Access-Control-Allow-Headers", "Content-Type")
	h.Set("Access-Control-Max-Age", "86400")
}

func (s *Server) siteEventsPreflight(w http.ResponseWriter, r *http.Request) {
	siteCORS(w)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) siteJS(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Content-Type", "text/javascript; charset=utf-8")
	h.Set("Cache-Control", "public, max-age=3600")
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("Cross-Origin-Resource-Policy", "cross-origin")
	w.Write(siteScript)
}

type siteKeyRow struct {
	id, workspaceID string
	brandID         *string
	origins         []string
	// Query parameters the site leaves out of page addresses.
	excludedParams []string
	// The hash of the key's proxy secret, when it has one.
	proxyHash []byte
}

// siteEvents takes a browser's events. The body is JSON, sent as text/plain
// by the snippet so browsers send it without a preflight:
// {"key": "ofk_...", "batch": [{"type": "page", "anonymousId": ..., ...}]}.
// The answer says only how many were taken: a site key reads nothing.
func (s *Server) siteEvents(w http.ResponseWriter, r *http.Request) {
	siteCORS(w)
	ctx := r.Context()
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxSiteBody))
	if err != nil {
		writeError(w, r, http.StatusRequestEntityTooLarge, "invalid_request", "the body must be under 64 KB: send at most 20 events at a time")
		return
	}
	var in struct {
		Key   string            `json:"key"`
		Batch []json.RawMessage `json:"batch"`
		// The visitor accepted the site's analytics cookies.
		Consent bool `json:"consent"`
		// The script's version (siteScriptVersion); copies from before it
		// said send none.
		V string `json:"v"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", `send {"key": "ofk_...", "batch": [...]}`)
		return
	}
	// An address that keeps sending keys that don't exist is turned away.
	// Valid keys are limited per key instead, since a site that forwards
	// events through its own domain sends them all from one address.
	conn := clientIP(r)
	if s.siteIPLimit.over(conn) {
		s.siteIPLimit.allow(w, r, conn)
		return
	}
	var k siteKeyRow
	if strings.HasPrefix(in.Key, auth.PrefixSiteKey) && len(in.Key) <= 100 {
		err = s.pool.QueryRow(ctx, `SELECT id::text, workspace_id::text, brand_id::text, origins, excluded_params, proxy_secret_hash FROM site_keys
			WHERE revoked_at IS NULL AND (key = $1 OR (previous_key = $1 AND previous_expires_at > now()))`, in.Key).
			Scan(&k.id, &k.workspaceID, &k.brandID, &k.origins, &k.excludedParams, &k.proxyHash)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			s.internal(w, r, err)
			return
		}
	}
	if k.id == "" {
		if !s.siteIPLimit.allow(w, r, conn) {
			return
		}
		writeError(w, r, http.StatusUnauthorized, "unauthenticated", "the site key is not valid: copy the snippet again from the workspace's website tracking settings")
		return
	}
	if !originAllowed(k.origins, r.Header.Get("Origin")) {
		writeError(w, r, http.StatusForbidden, "forbidden", "this site key does not take events from this origin; add it to the key's origins")
		return
	}
	if !s.siteKeyLimit.allow(w, r, k.id) {
		return
	}
	// With a proxy secret, each visitor's address is known, so one address
	// can't use up the key's limit.
	if k.proxyHash != nil && !s.siteVisitorLimit.allow(w, r, k.id+" "+siteClientIP(r, k)) {
		return
	}
	if len(in.Batch) == 0 || len(in.Batch) > maxSiteBatch {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "send 1 to 20 events at a time")
		return
	}
	ua := r.Header.Get("User-Agent")
	if isBotUA(ua) {
		// Crawlers and scripts are counted, not stored.
		if _, err := s.pool.Exec(ctx, `INSERT INTO site_key_days (key_id, workspace_id, day, bots) VALUES ($1, $2, current_date, $3)
			ON CONFLICT (key_id, day) DO UPDATE SET bots = site_key_days.bots + EXCLUDED.bots`, k.id, k.workspaceID, len(in.Batch)); err != nil {
			s.internal(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]int{"accepted": 0, "rejected": 0})
		return
	}
	var today int
	if err := s.pool.QueryRow(ctx, `INSERT INTO site_key_days (key_id, workspace_id, day, events) VALUES ($1, $2, current_date, $3)
		ON CONFLICT (key_id, day) DO UPDATE SET events = site_key_days.events + EXCLUDED.events RETURNING events`,
		k.id, k.workspaceID, len(in.Batch)).Scan(&today); err != nil {
		s.internal(w, r, err)
		return
	}
	if today > siteKeyDailyEvents {
		metrics.SiteEventsCapped(len(in.Batch))
		writeError(w, r, http.StatusTooManyRequests, "rate_limited", "this site key took its 250,000 events for today; it takes more tomorrow (UTC)")
		return
	}
	var workspaceToday int
	if err := s.pool.QueryRow(ctx, `SELECT coalesce(sum(events), 0) FROM site_key_days WHERE workspace_id = $1 AND day = current_date`, k.workspaceID).
		Scan(&workspaceToday); err != nil {
		s.internal(w, r, err)
		return
	}
	if workspaceToday > siteWorkspaceDailyEvents {
		metrics.SiteEventsCapped(len(in.Batch))
		writeError(w, r, http.StatusTooManyRequests, "rate_limited", "this workspace's site keys took their 1,000,000 events for today; they take more tomorrow (UTC)")
		return
	}

	if _, err := s.pool.Exec(ctx, `UPDATE site_keys SET last_used_at = now(), script_version = $2
		WHERE id = $1 AND (last_used_at IS NULL OR last_used_at < now() - interval '1 minute' OR script_version <> $2)`, k.id, scriptVersion(in.V)); err != nil {
		s.internal(w, r, err)
		return
	}
	var accepted, rejected int
	var anon string
	if s.siteInbox {
		accepted, rejected, anon, err = s.queueSiteBatch(ctx, k, ua, in.Batch)
	} else {
		err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
			var err error
			accepted, rejected, anon, err = s.storeSiteBatch(ctx, tx, k, ua, in.Batch, time.Now().UTC())
			return err
		})
	}
	if err == nil && in.Consent && accepted > 0 {
		err = s.queueReveal(ctx, k.workspaceID, anon, siteClientIP(r, k))
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if in.Consent && anon != "" {
		setSiteCookie(w, r.Header.Get("Origin"), anon)
	}
	writeJSON(w, http.StatusOK, map[string]int{"accepted": accepted, "rejected": rejected})
}

// queueSiteBatch puts a batch in the inbox for the workers to store, and
// counts what it can tell without the database: events in the wrong shape
// are rejected now, the rest are accepted. A worker may still refuse one
// (an unapproved event name over the workspace's limit, say); the browser
// has nothing to do about that either way.
func (s *Server) queueSiteBatch(ctx context.Context, k siteKeyRow, ua string, batch []json.RawMessage) (accepted, rejected int, anon string, err error) {
	keep := make([]json.RawMessage, 0, len(batch))
	for _, raw := range batch {
		m, _, err := siteMessage(raw, ua, k.excludedParams)
		if err != nil {
			rejected++
			continue
		}
		if anon == "" {
			anon = m.text("anonymousId")
		}
		accepted++
		keep = append(keep, raw)
	}
	metrics.SiteEventsReceived(accepted, rejected)
	if len(keep) == 0 {
		return accepted, rejected, anon, nil
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO site_event_inbox (workspace_id, key_id, user_agent, batch) VALUES ($1, $2, $3, $4)`,
		k.workspaceID, k.id, cutUTF8(ua, 1000), keep)
	if err == nil {
		s.wakeSiteInbox()
	}
	return accepted, rejected, anon, err
}

// storeSiteBatch stores a browser's batch of events for site key k, as if
// they arrived at now: each joins its visitor's session and its person.
// An event that is refused (bad shape, too many unapproved names, a record
// conflict) is counted and skipped; any other error ends the transaction.
func (s *Server) storeSiteBatch(ctx context.Context, tx pgx.Tx, k siteKeyRow, ua string, batch []json.RawMessage, now time.Time) (accepted, rejected int, anon string, err error) {
	a := &access{principal: principal{ActorType: "site_key", TokenID: k.id}, WorkspaceID: k.workspaceID}
	v := siteVisitor{keyID: k.id, brandID: k.brandID, origins: k.origins}
	v.device, v.browser, v.os = parseUA(ua)
	c, err := loadCatalog(ctx, tx, k.workspaceID, false)
	if err != nil {
		return 0, 0, "", err
	}
	known, quarantined, err := browserNames(ctx, tx, k.workspaceID)
	if err != nil {
		return 0, 0, "", err
	}
	set, err := visitors.Load(ctx, tx, k.workspaceID)
	if err != nil {
		return 0, 0, "", err
	}
	totals := ingestTotals{names: map[string]*seenName{}}
	for _, raw := range batch {
		m, token, err := siteMessage(raw, ua, k.excludedParams)
		if err == nil && m.text("type") == "track" {
			name, _ := eventNameFor(m.text("event"))
			if !known[name] {
				if quarantined >= maxBrowserQuarantined {
					err = errInvalid("this workspace has too many unapproved event names; approve or remove some in the catalog first")
				} else {
					known[name] = true
					quarantined++
				}
			}
		}
		if err == nil {
			err = s.ingestSiteOne(ctx, tx, a, c, v, set, m, token, now, &totals)
		}
		if err == nil && anon == "" {
			anon = m.text("anonymousId")
		}
		var bad errInvalid
		switch {
		case err == nil:
			accepted++
		case errors.As(err, &bad):
			rejected++
		default:
			return 0, 0, "", err
		}
	}
	return accepted, rejected, anon, saveEventNames(ctx, tx, k.workspaceID, totals.names)
}

// siteCookieRe is the shape of a visitor id the snippet makes.
var siteCookieRe = regexp.MustCompile(`^[A-Za-z0-9-]{8,64}$`)

// setSiteCookie keeps a consenting visitor's id in a cookie set by the
// server, on the site's own domain. Browsers keep such a cookie for its
// full year, where one a script writes may be cut to days. A browser takes
// it only when events reach Outflare through the site's own domain (a path
// the site forwards, or its own subdomain); sent straight to Outflare it
// is ignored, and the snippet's own cookie stands.
func setSiteCookie(w http.ResponseWriter, origin, anon string) {
	if siteCookieRe.MatchString(anon) {
		setSiteDomainCookie(w, origin, "of_aid", anon)
	}
}

// setSiteDomainCookie sets a year-long cookie on the widest domain of the
// page's origin, for the site's own subdomains to share.
func setSiteDomainCookie(w http.ResponseWriter, origin, name, value string) {
	u, err := url.Parse(origin)
	if err != nil || u.Hostname() == "" {
		return
	}
	host := u.Hostname()
	domain, err := publicsuffix.EffectiveTLDPlusOne(host)
	if err != nil {
		domain = host
	}
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/", Domain: domain, MaxAge: 365 * 24 * 3600,
		Secure: u.Scheme == "https", SameSite: http.SameSiteLaxMode})
}

// ingestSiteOne stores one browser event in its own savepoint, so a bad
// one is refused alone. The event joins the visitor's session, and a new
// session for someone already known updates their last touch. A page view
// also counts toward its visitor, and one that carries a genuine email
// click token first joins the browser to the person the email went to.
func (s *Server) ingestSiteOne(ctx context.Context, tx pgx.Tx, a *access, c *catalog, v siteVisitor, set visitors.Settings, m eventMessage, token string,
	now time.Time, totals *ingestTotals) error {
	sp, err := tx.Begin(ctx)
	if err != nil {
		return err
	}
	var res eventResult
	before := totals.created
	by := ""
	if token != "" {
		var rec string
		if rec, err = s.knownFromClick(ctx, sp, a, c, set, m.text("anonymousId"), token, now, totals); err == nil && rec != "" {
			by = "email_click"
		}
	}
	isNew := false
	if err == nil {
		isNew, err = siteSession(ctx, sp, a, v, m, now)
	}
	if err == nil {
		err = s.ingestOne(ctx, sp, a, c, v.brandID, siteSource, nil, m, now, &res, totals)
	}
	if err == nil && isNew {
		var rec *string
		if rec, _, err = ownerOf(ctx, sp, a, "anonymous_id", m.text("anonymousId")); err == nil && rec != nil {
			err = recomputeTouches(ctx, sp, a, c, *rec)
		}
	}
	if err == nil && res.Status != "duplicate" && m.text("type") == "page" {
		err = s.countVisit(ctx, sp, a.WorkspaceID, set, v.brandID, m, res.RecordID, by, isNew, now)
	}
	if err != nil {
		totals.created = before
		if rb := sp.Rollback(ctx); rb != nil {
			return rb
		}
		var conflict recordConflict
		if errors.As(err, &conflict) || errors.Is(err, errSharedRecord) {
			return errInvalid(err.Error())
		}
		return err
	}
	return sp.Commit(ctx)
}

// browserNames are the event names the workspace has, and how many of them
// wait for approval.
func browserNames(ctx context.Context, tx pgx.Tx, ws string) (map[string]bool, int, error) {
	rows, err := tx.Query(ctx, `SELECT name, status = 'quarantined' FROM event_definitions WHERE workspace_id = $1`, ws)
	if err != nil {
		return nil, 0, err
	}
	known, quarantined := map[string]bool{}, 0
	for rows.Next() {
		var name string
		var q bool
		if err := rows.Scan(&name, &q); err != nil {
			return nil, 0, err
		}
		known[name] = true
		if q {
			quarantined++
		}
	}
	return known, quarantined, rows.Err()
}

// siteMessage keeps what a browser may say: a page view or a track call on
// an anonymous id, its properties and the page and campaign it came from.
// A userId, traits and the browser's clock are dropped: the browser cannot
// say who someone is, and its time is not trusted.
func siteMessage(raw json.RawMessage, ua string, excluded []string) (eventMessage, string, error) {
	m, err := siteMessageOf(raw, ua, excluded)
	if err != nil {
		return nil, "", err
	}
	var props map[string]any
	_ = json.Unmarshal(m["properties"], &props)
	token, _ := props[visitors.Param].(string)
	if _, ok := props[visitors.Param]; ok {
		delete(props, visitors.Param)
		m["properties"] = mustJSON(props)
	}
	if m.text("type") != "page" || len(token) > 60 {
		token = ""
	}
	return m, token, nil
}

// siteCookieKeys are the keys site.js sends network cookies under.
func siteCookieKeys() []string {
	out := make([]string, len(adtrack.Cookies))
	for i, c := range adtrack.Cookies {
		out[i] = c.Key
	}
	return out
}

// siteMessageOf is siteMessage before the click token is taken out.
func siteMessageOf(raw json.RawMessage, ua string, excluded []string) (eventMessage, error) {
	var m eventMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, errInvalid("each event is an object")
	}
	kind := strings.ToLower(m.text("type"))
	if !slices.Contains(siteKinds, kind) {
		return nil, errInvalid("a site key sends page and track events only")
	}
	anon, err := m.id("anonymousId", "anonymous_id")
	if err != nil {
		return nil, err
	}
	if anon == "" {
		return nil, errInvalid("every event needs anonymousId")
	}
	props, err := m.object(maxSiteProps, "properties")
	if err != nil {
		return nil, err
	}
	// Only the known click ids, as strings.
	if ids, ok := props["click_ids"].(map[string]any); ok {
		clean := map[string]any{}
		for _, k := range siteClickIDs {
			if v, ok := ids[k].(string); ok && v != "" {
				clean[k] = cutUTF8(v, 500)
			}
		}
		if len(clean) > 0 {
			props["click_ids"] = clean
		} else {
			delete(props, "click_ids")
		}
	} else {
		delete(props, "click_ids")
	}
	// The ad the tracking template named, with the network's own ids. Only
	// page views carry it: a track event's ad is the site's own property.
	if kind == "page" {
		in, _ := props["ad"].(map[string]any)
		if ad, _ := adtrack.CleanAd(in); len(ad) > 0 {
			props["ad"] = ad
		} else {
			delete(props, "ad")
		}
	}
	ctxIn, err := m.object(maxEventContext, "context")
	if err != nil {
		return nil, err
	}
	ctxOut := map[string]any{}
	pick := func(key string, fields []string, limit int) {
		obj, _ := ctxIn[key].(map[string]any)
		out := map[string]any{}
		for _, f := range fields {
			if v, ok := obj[f].(string); ok && v != "" {
				out[f] = cutUTF8(v, limit)
			}
		}
		if len(out) > 0 {
			ctxOut[key] = out
		}
	}
	pick("page", sitePage, 2000)
	if page, ok := ctxOut["page"].(map[string]any); ok {
		cleanPage(page, excluded)
	}
	pick("campaign", siteCampaign, 500)
	for _, f := range []string{"locale", "timezone"} {
		if v, ok := ctxIn[f].(string); ok && v != "" {
			ctxOut[f] = cutUTF8(v, 100)
		}
	}
	if ua != "" {
		ctxOut["userAgent"] = cutUTF8(ua, 500)
	}
	out := eventMessage{"type": mustJSON(kind), "anonymousId": mustJSON(anon), "properties": mustJSON(props), "context": mustJSON(ctxOut)}
	if id, err := m.id("messageId", "message_id"); err == nil && id != "" {
		out["messageId"] = mustJSON("browser:" + id)
	}
	switch kind {
	case "track":
		// Dotted names (payment.succeeded, subscription.started,
		// page.viewed) are Outflare's own and its connectors'; a browser
		// sends plain names such as Signup Started.
		event := cutUTF8(m.text("event"), 200)
		if name, ok := eventNameFor(event); !ok || strings.Contains(name, ".") {
			return nil, errInvalid("a browser's track event is a plain name such as Signup Started; dotted names are Outflare's own")
		}
		out["event"] = mustJSON(event)
	case "page":
		if n := m.text("name"); n != "" {
			out["name"] = mustJSON(cutUTF8(n, 200))
		}
	}
	return out, nil
}

// cleanPage strips a page event's addresses: the page's own keeps the
// parameters keptParam allows (site_params.go), and the referrer, another
// site's address, only its origin and path. The page's host is kept on its
// own, for reports by site.
func cleanPage(page map[string]any, excluded []string) {
	delete(page, "host")
	if v, ok := page["url"].(string); ok {
		if u, err := url.Parse(v); err == nil && u.Host != "" {
			u.RawQuery, u.Fragment, u.User = cleanQuery(u.RawQuery, excluded), "", nil
			page["url"] = u.String()
			page["host"] = strings.ToLower(u.Hostname())
		} else {
			delete(page, "url")
		}
	}
	if v, ok := page["search"].(string); ok {
		if q := cleanQuery(v, excluded); q != "" {
			page["search"] = "?" + q
		} else {
			delete(page, "search")
		}
	}
	if v, ok := page["referrer"].(string); ok {
		if u, err := url.Parse(v); err == nil && u.Host != "" && (u.Scheme == "http" || u.Scheme == "https") {
			page["referrer"] = u.Scheme + "://" + u.Host + u.EscapedPath()
		} else {
			delete(page, "referrer")
		}
	}
	if v, ok := page["path"].(string); ok {
		if i := strings.IndexAny(v, "?#"); i >= 0 {
			page["path"] = v[:i]
		}
	}
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// siteClientIP is the visitor's address. Behind the site's own proxy every
// request comes from the proxy, so a proxy that sends the key's proxy
// secret (X-Outflare-Proxy) can pass the visitor's address on in
// X-Outflare-Client-IP. Without the secret, or with an address that doesn't
// parse or isn't a public one (loopback, private, link-local), it is the
// connecting address, as for any other request.
func siteClientIP(r *http.Request, k siteKeyRow) string {
	if k.proxyHash != nil {
		if secret := r.Header.Get("X-Outflare-Proxy"); secret != "" &&
			subtle.ConstantTimeCompare(auth.HashToken(secret), k.proxyHash) == 1 {
			if ip, err := netip.ParseAddr(strings.TrimSpace(r.Header.Get("X-Outflare-Client-IP"))); err == nil {
				ip = ip.Unmap()
				if ip.IsGlobalUnicast() && !ip.IsPrivate() {
					return ip.String()
				}
			}
		}
	}
	return clientIP(r)
}

type siteProxySecretView struct {
	// Shown once: put it in the proxy's configuration, never in a page.
	Secret  string      `json:"secret"`
	SiteKey siteKeyView `json:"site_key"`
}

// createSiteProxySecret makes the key a new proxy secret, replacing any it
// had at once.
func (s *Server) createSiteProxySecret(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.oneSiteKey(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	secret, hash := auth.NewToken(auth.PrefixSiteProxy)
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE site_keys SET proxy_secret_hash = $3, proxy_secret_at = now() WHERE id = $1 AND workspace_id = $2`,
			cur.ID, a.WorkspaceID, hash); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "site_key.proxy_secret_created", "site_key", cur.ID, nil)
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := s.loadSiteKeys(ctx, a.WorkspaceID, &cur.ID)
	if err != nil || len(list) != 1 {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, siteProxySecretView{Secret: secret, SiteKey: list[0]})
}

// removeSiteProxySecret stops trusting the key's proxy: visitor addresses
// are the connecting ones again.
func (s *Server) removeSiteProxySecret(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.oneSiteKey(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE site_keys SET proxy_secret_hash = NULL, proxy_secret_at = NULL WHERE id = $1 AND workspace_id = $2`,
			cur.ID, a.WorkspaceID); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "site_key.proxy_secret_removed", "site_key", cur.ID, nil)
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
