package api

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/metrics"
)

// The consent banner lives in the website script (site.js), so tracking,
// ad send-back and the banner read one consent state. Each site key has
// its own banner: the laws and frameworks it follows (each on by default,
// each can be turned off), the categories it asks about, how it looks and
// what it says. The script asks GET /v1/site/consent for the settings and
// sends each choice to POST /v1/site/consent, which keeps it in the CDP's
// consents: on the visitor's anonymous id until identify joins it to their
// record. Channel web holds the categories; channel ads holds Google
// Consent Mode's ad purposes, which ad send-back (engine/internal/adconv)
// reads.
//
// A visitor's choice is customer data, like their page views, not a
// product event: it is the consent row itself, with what they were shown
// as evidence.

func (s *Server) routesConsentBanner(mux *http.ServeMux) {
	read, manage := auth.ScopeCustomersRead, auth.ScopeCustomersManage
	mux.HandleFunc("GET /v1/workspaces/{ws}/site-keys/{id}/consent-banner", s.inWorkspace(read, s.getConsentBanner))
	mux.HandleFunc("PUT /v1/workspaces/{ws}/site-keys/{id}/consent-banner", s.inWorkspace(manage, s.updateConsentBanner))
	mux.HandleFunc("POST /v1/workspaces/{ws}/site-keys/{id}/consent-banner/reask", s.inWorkspace(manage, s.reaskConsentBanner))
	// Public: the key is the authority.
	mux.HandleFunc("GET /v1/site/consent", s.siteConsentConfig)
	mux.HandleFunc("POST /v1/site/consent", s.siteConsentChoice)
	mux.HandleFunc("OPTIONS /v1/site/consent", s.siteEventsPreflight)
}

// Region rules: what the banner does for visitors in a region.
const (
	// Ask first: optional categories stay off until the visitor accepts.
	ruleOptIn = "opt_in"
	// On until the visitor opts out of the sale or sharing of their data
	// (US state laws); the banner says so once, with the opt-out button.
	ruleOptOut = "opt_out"
	// On, and the banner says so once.
	ruleNotice = "notice"
	// On, and no banner.
	ruleNone = "none"
)

var (
	bannerRules     = []string{ruleOptIn, ruleOptOut, ruleNotice, ruleNone}
	bannerLayouts   = []string{"bar", "box", "modal"}
	bannerPositions = []string{"bottom", "top", "bottom_left", "bottom_right", "center"}
	bannerThemes    = []string{"light", "dark", "brand"}
	// What a choice was: both buttons, the per-category save, an opt-out
	// where the law allows one, the browser's Global Privacy Control, or
	// the site's own code calling outflare.consent.
	bannerActions = []string{"accept_all", "reject_all", "custom", "opt_out", "gpc", "api"}
	bannerRegions = []string{"eea", "us", "other"}
	bannerLangRe  = regexp.MustCompile(`^[a-z]{2}(-[a-z]{2})?$`)
	countryRe     = regexp.MustCompile(`^[A-Z]{2}$`)
)

// The banner's settings. Fields left out of a saved banner take these
// defaults, so a law or framework added later is on for every banner.
type bannerConfig struct {
	// Whether the site shows the banner. Off, the script keeps its
	// data-consent behaviour and a site's own banner can still call
	// outflare.consent.
	Enabled bool `json:"enabled"`
	// What the banner does in each region: opt_in, opt_out, notice or none.
	Regions bannerRegionRules `json:"regions"`
	// The signals the banner follows and sets.
	Frameworks bannerFrameworks `json:"frameworks"`
	// The categories it asks about, besides necessary (always on).
	Categories bannerCategories `json:"categories"`
	Layout     string           `json:"layout"`
	Position   string           `json:"position"`
	// light and dark are neutral; brand uses the colours below, else the
	// brand's or the company's (the Company hub).
	Theme string `json:"theme"`
	// Override the theme's colours; "" for the theme's own.
	Colors   lookColors `json:"colors"`
	ShowLogo bool       `json:"show_logo"`
	// The logo shown; "" for the brand's or the company's.
	LogoURL string `json:"logo_url"`
	// A small button, after the visitor chose, to change their mind.
	ReopenButton bool `json:"reopen_button"`
	// The cookie policy the banner links to; "" for none.
	PolicyURL string `json:"policy_url"`
	// After this many days an opt-in choice is asked again; 0 for never.
	ReaskAfterDays int `json:"reask_after_days"`
	// The banner's words by language (en, de, pt-br); what is left out
	// comes from Outflare's own, in the visitor's language where it has
	// them.
	Texts map[string]bannerTexts `json:"texts"`
	// Added after the preset's styles, inside the banner.
	CustomCSS string `json:"custom_css"`
	// Replaces the banner's markup: buttons with data-of-consent="accept",
	// "reject", "customize" or "save", checkboxes with
	// data-of-category="analytics", or anything calling outflare.consent().
	CustomHTML string `json:"custom_html"`
}

type bannerRegionRules struct {
	// The EEA, the UK and Switzerland.
	EEA string `json:"eea"`
	// The United States.
	US string `json:"us"`
	// Everywhere else.
	Other string `json:"other"`
}

type bannerFrameworks struct {
	// Honour the browser's Global Privacy Control: no marketing.
	GPC bool `json:"gpc"`
	// Set Google Consent Mode v2 (ad_storage, ad_user_data,
	// ad_personalization, analytics_storage) through gtag.
	GoogleConsentMode bool `json:"google_consent_mode"`
	// fbq('consent', 'grant' | 'revoke').
	Meta bool `json:"meta"`
	// ttq.holdConsent, grantConsent, revokeConsent.
	TikTok bool `json:"tiktok"`
	// Microsoft UET consent mode (uetq).
	Microsoft bool `json:"microsoft"`
	// IAB TCF v2.2 is left out for now: it needs Outflare registered with
	// IAB Europe as a CMP, and can be added if a publisher needs it.
}

type bannerCategories struct {
	Analytics   bool `json:"analytics"`
	Marketing   bool `json:"marketing"`
	Preferences bool `json:"preferences"`
}

func defaultBanner() bannerConfig {
	return bannerConfig{
		Regions:        bannerRegionRules{EEA: ruleOptIn, US: ruleOptOut, Other: ruleNone},
		Frameworks:     bannerFrameworks{GPC: true, GoogleConsentMode: true, Meta: true, TikTok: true, Microsoft: true},
		Categories:     bannerCategories{Analytics: true, Marketing: true, Preferences: true},
		Layout:         "bar",
		Position:       "bottom",
		Theme:          "light",
		ShowLogo:       true,
		ReopenButton:   true,
		ReaskAfterDays: 365,
		Texts:          map[string]bannerTexts{},
	}
}

const (
	maxBannerCustom = 20 << 10
	maxBannerBody   = 128 << 10
)

// clean checks a banner's settings and puts them in their usual form.
func (c *bannerConfig) clean() error {
	for _, rule := range []*string{&c.Regions.EEA, &c.Regions.US, &c.Regions.Other} {
		if !slices.Contains(bannerRules, *rule) {
			return errInvalid("regions: each region's rule is opt_in, opt_out, notice or none")
		}
	}
	switch {
	case !slices.Contains(bannerLayouts, c.Layout):
		return errInvalid("layout is bar, box or modal")
	case !slices.Contains(bannerPositions, c.Position):
		return errInvalid("position is bottom, top, bottom_left, bottom_right or center")
	case !slices.Contains(bannerThemes, c.Theme):
		return errInvalid("theme is light, dark or brand")
	case c.ReaskAfterDays < 0 || c.ReaskAfterDays > 730:
		return errInvalid("reask_after_days is 0 (never) to 730")
	case len(c.CustomCSS) > maxBannerCustom || len(c.CustomHTML) > maxBannerCustom:
		return errInvalid("custom_css and custom_html must each be under 20 KB")
	}
	if err := c.Colors.clean(); err != nil {
		return err
	}
	var err error
	if c.LogoURL, err = cleanLogoURL(c.LogoURL); err != nil {
		return err
	}
	if c.PolicyURL = strings.TrimSpace(c.PolicyURL); c.PolicyURL != "" {
		if !strings.HasPrefix(c.PolicyURL, "https://") && !strings.HasPrefix(c.PolicyURL, "/") || len(c.PolicyURL) > 2000 ||
			strings.ContainsAny(c.PolicyURL, " \"'<>") {
			return errInvalid("policy_url is an https address or a path on the site, such as /cookies")
		}
	}
	if c.Texts == nil {
		c.Texts = map[string]bannerTexts{}
	}
	if len(c.Texts) > 40 {
		return errInvalid("texts: at most 40 languages")
	}
	for lang, t := range c.Texts {
		if !bannerLangRe.MatchString(lang) {
			return errInvalid("texts are keyed by language, such as en, de or pt-br, in lowercase")
		}
		for _, f := range t.fields() {
			*f = strings.TrimSpace(*f)
			if len(*f) > 2000 {
				return errInvalid("texts: each text must be under 2,000 characters")
			}
		}
		c.Texts[lang] = t
	}
	return nil
}

type consentBannerView struct {
	SiteKeyID string       `json:"site_key_id"`
	Config    bannerConfig `json:"config"`
	// Goes up on every save.
	Version int `json:"version"`
	// Goes up when visitors are to be asked again.
	ConsentVersion int `json:"consent_version"`
	// False until the banner is first saved: config is then the defaults.
	Saved bool `json:"saved"`
	// The logo and colours the brand preset uses when the banner sets
	// none: the key's brand's, else the company's (the Company hub).
	Look      bannerLook `json:"look"`
	UpdatedAt *time.Time `json:"updated_at"`
}

type bannerLook struct {
	LogoURL string     `json:"logo_url"`
	Colors  lookColors `json:"colors"`
}

// loadBanner reads a site key's banner, with the defaults under what was
// saved, and the look of its brand and company.
func loadBanner(ctx context.Context, q pgx.Tx, ws, keyID string, lock bool) (consentBannerView, error) {
	v := consentBannerView{SiteKeyID: keyID, Config: defaultBanner(), Version: 0, ConsentVersion: 1}
	var raw []byte
	var version, consentVersion *int
	var brandLogo, companyLogo string
	var brandColors, companyColors lookColors
	sql := `SELECT b.config, b.version, b.consent_version, b.updated_at,
			COALESCE(br.logo_url, ''), COALESCE(br.colors, '{}'), COALESCE(c.logo_url, ''), COALESCE(c.colors, '{}')
		FROM site_keys k
		LEFT JOIN consent_banners b ON b.site_key_id = k.id
		LEFT JOIN brands br ON br.id = k.brand_id
		LEFT JOIN workspace_context c ON c.workspace_id = k.workspace_id
		WHERE k.id = $1 AND k.workspace_id = $2 AND k.revoked_at IS NULL`
	if lock {
		sql += ` FOR UPDATE OF k`
	}
	err := q.QueryRow(ctx, sql, keyID, ws).Scan(&raw, &version, &consentVersion, &v.UpdatedAt, &brandLogo, &brandColors, &companyLogo, &companyColors)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, errNotFound
	}
	if err != nil {
		return v, err
	}
	if version != nil {
		v.Saved, v.Version, v.ConsentVersion = true, *version, *consentVersion
		if err := json.Unmarshal(raw, &v.Config); err != nil {
			return v, err
		}
	}
	v.Look = bannerLook{LogoURL: brandLogo, Colors: brandColors.or(companyColors)}
	if v.Look.LogoURL == "" {
		v.Look.LogoURL = companyLogo
	}
	return v, nil
}

func (s *Server) getConsentBanner(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	key, ok := s.oneSiteKey(w, r)
	if !ok {
		return
	}
	var v consentBannerView
	err := s.readTx(r.Context(), func(tx pgx.Tx) error {
		var err error
		v, err = loadBanner(r.Context(), tx, a.WorkspaceID, key.ID, false)
		return err
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// updateConsentBanner changes a banner's settings. Fields left out keep
// their value; texts is replaced language by language. A category the
// banner didn't ask about before means asking everyone again.
func (s *Server) updateConsentBanner(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	key, ok := s.oneSiteKey(w, r)
	if !ok {
		return
	}
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		writeError(w, r, http.StatusUnsupportedMediaType, "invalid_request", "send a JSON body with Content-Type: application/json")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBannerBody))
	if err != nil {
		writeError(w, r, http.StatusRequestEntityTooLarge, "invalid_request", "the body must be under 128 KB")
		return
	}
	ctx := r.Context()
	var v consentBannerView
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := loadBanner(ctx, tx, a.WorkspaceID, key.ID, true)
		if err != nil {
			return err
		}
		next := cur.Config
		// A copy, so the request's languages don't write into cur's map.
		next.Texts = make(map[string]bannerTexts, len(cur.Config.Texts))
		for l, t := range cur.Config.Texts {
			next.Texts[l] = t
		}
		dec := json.NewDecoder(bytes.NewReader(body))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&next); err != nil {
			return errInvalid("invalid JSON: " + err.Error())
		}
		if err := next.clean(); err != nil {
			return err
		}
		// The banner's own markup and styles run on the site, so changing
		// them takes the workspace's highest settings scope.
		if (next.CustomHTML != cur.Config.CustomHTML || next.CustomCSS != cur.Config.CustomCSS) && !a.has(auth.ScopeWorkspaceManage) {
			return errDenied("changing custom_html or custom_css needs the workspace.manage scope: they run on the website")
		}
		reask := next.Categories.Analytics && !cur.Config.Categories.Analytics ||
			next.Categories.Marketing && !cur.Config.Categories.Marketing ||
			next.Categories.Preferences && !cur.Config.Categories.Preferences
		consentVersion := cur.ConsentVersion
		if reask && cur.Saved {
			consentVersion++
		}
		if _, err := tx.Exec(ctx, `INSERT INTO consent_banners (workspace_id, site_key_id, config, version, consent_version, updated_by)
			VALUES ($1, $2, $3, 1, $4, $5)
			ON CONFLICT (site_key_id) DO UPDATE SET config = EXCLUDED.config, version = consent_banners.version + 1,
				consent_version = EXCLUDED.consent_version, updated_by = EXCLUDED.updated_by, updated_at = now()`,
			a.WorkspaceID, key.ID, next, consentVersion, nullIfEmpty(a.ActorID)); err != nil {
			return err
		}
		action := "consent_banner.updated"
		switch {
		case next.Enabled && !cur.Config.Enabled:
			action = "consent_banner.enabled"
		case !next.Enabled && cur.Config.Enabled:
			action = "consent_banner.disabled"
		}
		if err := recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, action, "site_key", key.ID, map[string]any{
			"enabled": next.Enabled, "reasked": consentVersion != cur.ConsentVersion,
		}, bannerProps(next)); err != nil {
			return err
		}
		v, err = loadBanner(ctx, tx, a.WorkspaceID, key.ID, false)
		return err
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// bannerProps are a banner's settings as product event properties: enums
// and switches only, never its words or markup.
func bannerProps(c bannerConfig) map[string]any {
	return map[string]any{
		"rule_eea": c.Regions.EEA, "rule_us": c.Regions.US, "rule_other": c.Regions.Other,
		"layout": c.Layout, "position": c.Position, "theme": c.Theme,
		"gpc": c.Frameworks.GPC, "google_consent_mode": c.Frameworks.GoogleConsentMode, "meta": c.Frameworks.Meta,
		"tiktok": c.Frameworks.TikTok, "microsoft": c.Frameworks.Microsoft,
		"analytics": c.Categories.Analytics, "marketing": c.Categories.Marketing, "preferences": c.Categories.Preferences,
		"custom_css": c.CustomCSS != "", "custom_html": c.CustomHTML != "", "languages": len(c.Texts),
	}
}

// reaskConsentBanner asks every visitor again, such as after the site
// starts using a new kind of cookie: choices made before are shown the
// banner again where the region's rule asks first.
func (s *Server) reaskConsentBanner(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	key, ok := s.oneSiteKey(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	var v consentBannerView
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := loadBanner(ctx, tx, a.WorkspaceID, key.ID, true)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO consent_banners (workspace_id, site_key_id, config, version, consent_version, updated_by)
			VALUES ($1, $2, $3, 1, $4, $5)
			ON CONFLICT (site_key_id) DO UPDATE SET consent_version = consent_banners.consent_version + 1,
				updated_by = EXCLUDED.updated_by, updated_at = now()`,
			a.WorkspaceID, key.ID, cur.Config, cur.ConsentVersion+1, nullIfEmpty(a.ActorID)); err != nil {
			return err
		}
		if err := record(ctx, tx, &a.principal, a.WorkspaceID, "consent_banner.reasked", "site_key", key.ID,
			map[string]any{"consent_version": cur.ConsentVersion + 1}); err != nil {
			return err
		}
		v, err = loadBanner(ctx, tx, a.WorkspaceID, key.ID, false)
		return err
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// siteBannerRow is what the public endpoints read about a key and its
// banner.
type siteBannerRow struct {
	siteKeyRow
	banner consentBannerView
}

// siteBanner finds the site key a browser sent and its banner. When it
// can't, it answers the request itself and says how it went (refused or
// error); "" means found. Unknown keys from one address are limited as
// for events.
func (s *Server) siteBanner(w http.ResponseWriter, r *http.Request, key string) (siteBannerRow, string) {
	ctx := r.Context()
	conn := clientIP(r)
	if s.siteIPLimit.over(conn) {
		s.siteIPLimit.allow(w, r, conn)
		return siteBannerRow{}, "refused"
	}
	var k siteBannerRow
	if strings.HasPrefix(key, auth.PrefixSiteKey) && len(key) <= 100 {
		err := s.pool.QueryRow(ctx, `SELECT id::text, workspace_id::text, brand_id::text, origins, proxy_secret_hash FROM site_keys
			WHERE revoked_at IS NULL AND (key = $1 OR (previous_key = $1 AND previous_expires_at > now()))`, key).
			Scan(&k.id, &k.workspaceID, &k.brandID, &k.origins, &k.proxyHash)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			s.internal(w, r, err)
			return k, "error"
		}
	}
	if k.id == "" {
		if s.siteIPLimit.allow(w, r, conn) {
			writeError(w, r, http.StatusUnauthorized, "unauthenticated", "the site key is not valid: copy the snippet again from the workspace's website tracking settings")
		}
		return k, "refused"
	}
	if !s.siteConsentLimit.allow(w, r, k.id) {
		return k, "refused"
	}
	err := s.readTx(ctx, func(tx pgx.Tx) error {
		var err error
		k.banner, err = loadBanner(ctx, tx, k.workspaceID, k.id, false)
		return err
	})
	if err != nil {
		s.internal(w, r, err)
		return k, "error"
	}
	return k, ""
}

// siteCountry is the visitor's country when the site's own proxy, holding
// the key's proxy secret, says it in X-Outflare-Country (from its CDN's
// geolocation); "" otherwise, and the script goes by the time zone.
func siteCountry(r *http.Request, k siteKeyRow) string {
	if k.proxyHash == nil {
		return ""
	}
	secret := r.Header.Get("X-Outflare-Proxy")
	if secret == "" || subtle.ConstantTimeCompare(auth.HashToken(secret), k.proxyHash) != 1 {
		return ""
	}
	c := strings.ToUpper(strings.TrimSpace(r.Header.Get("X-Outflare-Country")))
	if !countryRe.MatchString(c) {
		return ""
	}
	return c
}

// siteBannerView is the banner as the script gets it: settings resolved
// for one language, with the look filled in.
type siteBannerView struct {
	Enabled        bool              `json:"enabled"`
	Version        int               `json:"version"`
	ConsentVersion int               `json:"consent_version"`
	Country        string            `json:"country"`
	Regions        bannerRegionRules `json:"regions"`
	Frameworks     bannerFrameworks  `json:"frameworks"`
	Categories     bannerCategories  `json:"categories"`
	Layout         string            `json:"layout"`
	Position       string            `json:"position"`
	Theme          string            `json:"theme"`
	Colors         lookColors        `json:"colors"`
	LogoURL        string            `json:"logo_url"`
	ReopenButton   bool              `json:"reopen_button"`
	PolicyURL      string            `json:"policy_url"`
	ReaskAfterDays int               `json:"reask_after_days"`
	Lang           string            `json:"lang"`
	Texts          bannerTexts       `json:"texts"`
	CustomCSS      string            `json:"custom_css"`
	CustomHTML     string            `json:"custom_html"`
}

// siteConsentConfig is what the script asks on a visitor's first page,
// and again every few minutes: the key's banner, in the visitor's
// language. ?key=ofk_...&lang=de.
func (s *Server) siteConsentConfig(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	outcome := "refused"
	defer func() { metrics.SiteConsentRequest("config", outcome, time.Since(start)) }()
	siteCORS(w)
	k, failed := s.siteBanner(w, r, r.URL.Query().Get("key"))
	if failed != "" {
		outcome = failed
		return
	}
	c := k.banner.Config
	v := siteBannerView{
		Enabled: c.Enabled && k.banner.Saved, Version: k.banner.Version, ConsentVersion: k.banner.ConsentVersion,
		Country: siteCountry(r, k.siteKeyRow), Regions: c.Regions, Frameworks: c.Frameworks, Categories: c.Categories,
		Layout: c.Layout, Position: c.Position, Theme: c.Theme, ReopenButton: c.ReopenButton, PolicyURL: c.PolicyURL,
		ReaskAfterDays: c.ReaskAfterDays, CustomCSS: c.CustomCSS, CustomHTML: c.CustomHTML,
	}
	v.Colors = c.Colors
	if c.Theme == "brand" {
		v.Colors = c.Colors.or(k.banner.Look.Colors)
	}
	if c.ShowLogo {
		v.LogoURL = c.LogoURL
		if v.LogoURL == "" {
			v.LogoURL = k.banner.Look.LogoURL
		}
	}
	v.Lang = bannerLanguage(r.URL.Query().Get("lang"), c.Texts)
	v.Texts = bannerTextsFor(v.Lang, c.Texts)
	// Each visitor's browser keeps it a few minutes; the country makes it
	// one visitor's.
	w.Header().Set("Cache-Control", "private, max-age=300")
	outcome = "ok"
	writeJSON(w, http.StatusOK, v)
}

// siteConsentChoice keeps a visitor's choice: one consent per category
// the banner asks about (channel web) and one per Google Consent Mode ad
// purpose (channel ads, granted with marketing), on the visitor's record
// when they are known and on their anonymous id until then. The answer
// sets the of_consent cookie on the site's own domain, and of_aid when
// analytics is allowed, which browsers keep when the request came through
// the site's own domain.
func (s *Server) siteConsentChoice(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	outcome := "refused"
	defer func() { metrics.SiteConsentRequest("choice", outcome, time.Since(start)) }()
	siteCORS(w)
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8<<10))
	if err != nil {
		writeError(w, r, http.StatusRequestEntityTooLarge, "invalid_request", "the body must be under 8 KB")
		return
	}
	var in struct {
		Key         string `json:"key"`
		AnonymousID string `json:"anonymousId"`
		Analytics   *bool  `json:"analytics"`
		Marketing   *bool  `json:"marketing"`
		Preferences *bool  `json:"preferences"`
		Action      string `json:"action"`
		Region      string `json:"region"`
		GPC         bool   `json:"gpc"`
		// The banner's version and consent version the visitor saw.
		Version        int    `json:"version"`
		ConsentVersion int    `json:"consent_version"`
		Lang           string `json:"lang"`
		// The page's path, without its query.
		Path string `json:"path"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", `send {"key": "ofk_...", "anonymousId": "...", "analytics": true, "marketing": false, "preferences": true, "action": "custom"}`)
		return
	}
	k, failed := s.siteBanner(w, r, in.Key)
	if failed != "" {
		outcome = failed
		return
	}
	if !originAllowed(k.origins, r.Header.Get("Origin")) {
		writeError(w, r, http.StatusForbidden, "forbidden", "this site key does not take requests from this origin; add it to the key's origins")
		return
	}
	if k.proxyHash != nil && !s.siteVisitorLimit.allow(w, r, k.id+" "+siteClientIP(r, k.siteKeyRow)) {
		return
	}
	anon := strings.TrimSpace(in.AnonymousID)
	switch {
	case !siteCookieRe.MatchString(anon):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "anonymousId is required: the visitor's id from the script")
		return
	case in.Analytics == nil && in.Marketing == nil && in.Preferences == nil:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "send at least one of analytics, marketing and preferences, true or false")
		return
	case !slices.Contains(bannerActions, in.Action):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "action is accept_all, reject_all, custom, opt_out, gpc or api")
		return
	case in.Region != "" && !slices.Contains(bannerRegions, in.Region):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "region is eea, us or other")
		return
	}
	if in.Region == "" {
		in.Region = "other"
	}
	lang := strings.ToLower(in.Lang)
	if !bannerLangRe.MatchString(lang) {
		lang = ""
	}
	path := in.Path
	if i := strings.IndexAny(path, "?#"); i >= 0 {
		path = path[:i]
	}
	if !strings.HasPrefix(path, "/") || len(path) > 300 {
		path = ""
	}
	cfg := k.banner.Config
	// Without a saved banner the site's own banner is asking, about every
	// category.
	asked := map[string]bool{"analytics": true, "marketing": true, "preferences": true}
	if k.banner.Saved {
		asked = map[string]bool{"analytics": cfg.Categories.Analytics, "marketing": cfg.Categories.Marketing, "preferences": cfg.Categories.Preferences}
	}
	// Categories the banner doesn't ask about are left as they are.
	for name, v := range map[string]**bool{"analytics": &in.Analytics, "marketing": &in.Marketing, "preferences": &in.Preferences} {
		if !asked[name] {
			*v = nil
		}
	}
	type row struct {
		channel, purpose string
		granted          bool
	}
	var rows []row
	for _, c := range []struct {
		name string
		v    *bool
	}{{"analytics", in.Analytics}, {"marketing", in.Marketing}, {"preferences", in.Preferences}} {
		if c.v != nil {
			rows = append(rows, row{"web", c.name, *c.v})
		}
	}
	if in.Marketing != nil {
		for _, p := range []string{"ad_storage", "ad_user_data", "ad_personalization"} {
			rows = append(rows, row{"ads", p, *in.Marketing})
		}
	}
	if len(rows) == 0 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "this banner doesn't ask about those categories")
		return
	}
	choiceID := newID()
	evidence := map[string]any{
		"choice_id": choiceID, "action": in.Action, "region": in.Region, "gpc": in.GPC,
		"banner_version": k.banner.Version, "consent_version": k.banner.ConsentVersion,
		"seen_version": in.Version, "seen_consent_version": in.ConsentVersion,
		"site_key_id": k.id, "lang": lang, "path": path, "country": siteCountry(r, k.siteKeyRow),
	}
	ctx := r.Context()
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		a := &access{principal: principal{ActorType: "site_key", TokenID: k.id}, WorkspaceID: k.workspaceID}
		rec, _, err := ownerOf(ctx, tx, a, "anonymous_id", anon)
		if err != nil {
			return err
		}
		for _, x := range rows {
			status := "withdrawn"
			if x.granted {
				status = "granted"
			}
			if _, err := tx.Exec(ctx, `INSERT INTO consents (workspace_id, record_id, anonymous_id, channel, topic, purpose, status, source, evidence, request_id)
				VALUES ($1, $2, $3, $4, '', $5, $6, 'consent_banner', $7, $8)`,
				k.workspaceID, rec, anon, x.channel, x.purpose, status, evidence, RequestID(ctx)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		outcome = "error"
		s.internal(w, r, err)
		return
	}
	metrics.SiteConsentChoice(in.Action, in.Region)
	origin := r.Header.Get("Origin")
	setConsentCookie(w, origin, consentCookieValue(k.banner.ConsentVersion, in.Analytics, in.Marketing, in.Preferences, time.Now()))
	if in.Analytics != nil && *in.Analytics {
		setSiteCookie(w, origin, anon)
	}
	outcome = "ok"
	writeJSON(w, http.StatusOK, map[string]any{"recorded": len(rows), "choice_id": choiceID})
}

// consentCookieValue is the of_consent cookie: the consent version, one
// digit each for analytics, marketing and preferences (1 allowed, 0 not, 2
// not asked), and when, in Unix seconds: 3.101.1767225600.
func consentCookieValue(version int, analytics, marketing, preferences *bool, at time.Time) string {
	digit := func(b *bool) string {
		switch {
		case b == nil:
			return "2"
		case *b:
			return "1"
		}
		return "0"
	}
	return strconv.Itoa(version) + "." + digit(analytics) + digit(marketing) + digit(preferences) + "." + strconv.FormatInt(at.Unix(), 10)
}

// setConsentCookie keeps the visitor's choice in a cookie on the site's
// own domain, set by the server so browsers keep it the full year.
// Remembering a consent choice is necessary, so it needs no consent.
func setConsentCookie(w http.ResponseWriter, origin, value string) {
	setSiteDomainCookie(w, origin, "of_consent", value)
}
