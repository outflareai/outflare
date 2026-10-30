package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/net/publicsuffix"

	"github.com/outflareai/outflare/engine/internal/adtrack"
)

// Website visits. Every page and track event a site key takes belongs to a
// session: a visit that ends after siteSessionIdle without an event, or
// when the visitor comes back through a different campaign. A session
// keeps where the visit came from, so a person's first and last touch are
// read from their sessions once their product identifies them.

const siteSessionIdle = 30 * time.Minute

// siteVisitor is what the request says about the browser, worked out once
// for a request's events.
type siteVisitor struct {
	keyID               string
	brandID             *string
	origins             []string
	device, browser, os string
}

// botUA matches the user agents of crawlers, previews, monitors and
// scripts. Their requests are counted, never stored.
var botUA = regexp.MustCompile(`(?i)bot\b|bot/|crawl|spider|slurp|headless|phantomjs|puppeteer|playwright|selenium|lighthouse|pagespeed|gtmetrix|pingdom|uptime|monitor|preview|facebookexternalhit|embedly|python-|python/|curl/|wget/|go-http-client|java/|okhttp|axios/|node-fetch|undici|scrapy|httpclient|libwww|postman`)

// isBotUA reports a request no person's browser would send. Every browser
// sends a user agent.
func isBotUA(ua string) bool {
	return strings.TrimSpace(ua) == "" || botUA.MatchString(ua)
}

// parseUA reads the device, browser and system from a user agent: rough
// families only, for reports.
func parseUA(ua string) (device, browser, os string) {
	has := func(s ...string) bool {
		for _, x := range s {
			if strings.Contains(ua, x) {
				return true
			}
		}
		return false
	}
	switch {
	case has("iPhone", "iPad", "iPod"):
		os = "ios"
	case has("Android"):
		os = "android"
	case has("Windows"):
		os = "windows"
	case has("CrOS"):
		os = "chromeos"
	case has("Macintosh", "Mac OS X"):
		os = "macos"
	case has("Linux"):
		os = "linux"
	}
	switch {
	case has("iPad", "Tablet") || (os == "android" && !has("Mobile")):
		device = "tablet"
	case has("Mobi", "iPhone", "iPod"):
		device = "mobile"
	case os != "":
		device = "desktop"
	}
	switch {
	case has("Edg/", "EdgA/", "EdgiOS/"):
		browser = "edge"
	case has("OPR/", "Opera"):
		browser = "opera"
	case has("SamsungBrowser/"):
		browser = "samsung"
	case has("Firefox/", "FxiOS/"):
		browser = "firefox"
	case has("CriOS/", "Chrome/"):
		browser = "chrome"
	case has("Safari/"):
		browser = "safari"
	case ua != "":
		browser = "other"
	}
	return device, browser, os
}

// siteTouch is where a visit came from.
type siteTouch struct {
	source, medium, campaign, term, content string
	clickIDs                                map[string]string
	// ad is the ad the tracking template named (adtrack.AdParams).
	ad       map[string]string
	ref, ofc string
	// ofclk is the redirect click that led here (redirect_links.go). It
	// doesn't start a visit by itself: the page it lands on carries the
	// campaign's tags, which do.
	ofclk string
	// cookies are the network pixels' cookies (adtrack.Cookies). They are
	// kept with the click ids but say who the browser is, not how it
	// arrived, so they never start a visit.
	cookies map[string]string
}

// arrived reports whether the event carries anything saying how the
// visitor arrived. One that doesn't continues the visit it is in.
func (t siteTouch) arrived() bool {
	return t.source != "" || t.medium != "" || t.campaign != "" || len(t.clickIDs) > 0 || len(t.ad) > 0 || t.ref != "" || t.ofc != ""
}

// same reports whether two touches are the same arrival.
func (t siteTouch) same(o siteTouch) bool {
	return sameStrings(t.clickIDs, o.clickIDs) && sameStrings(t.ad, o.ad) &&
		t.source == o.source && t.medium == o.medium && t.campaign == o.campaign && t.ref == o.ref && t.ofc == o.ofc
}

func sameStrings(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// splitCookies takes the network cookies out of a visit's click ids.
func splitCookies(ids map[string]string) (clicks, cookies map[string]string) {
	for k, v := range ids {
		if adtrack.IsCookie(k) {
			if cookies == nil {
				cookies = map[string]string{}
			}
			cookies[k] = v
		} else {
			if clicks == nil {
				clicks = map[string]string{}
			}
			clicks[k] = v
		}
	}
	return clicks, cookies
}

// touchOf reads a cleaned browser event's arrival.
func touchOf(props, ctxObj map[string]any) siteTouch {
	str := func(m map[string]any, k string) string {
		v, _ := m[k].(string)
		return v
	}
	camp, _ := ctxObj["campaign"].(map[string]any)
	t := siteTouch{source: str(camp, "source"), medium: str(camp, "medium"), campaign: str(camp, "name"),
		term: str(camp, "term"), content: str(camp, "content"), ref: cutUTF8(str(props, "ref"), 200), ofc: cutUTF8(str(props, "ofc"), 200),
		ofclk: str(props, "ofclk")}
	strs := func(key string) map[string]string {
		obj, _ := props[key].(map[string]any)
		var out map[string]string
		for k, v := range obj {
			if s, ok := v.(string); ok {
				if out == nil {
					out = map[string]string{}
				}
				out[k] = s
			}
		}
		return out
	}
	t.clickIDs, t.cookies = splitCookies(strs("click_ids"))
	t.ad = strs("ad")
	return t
}

// siteSession finds the visit an event belongs to, or starts one, and
// puts its id in the event's context. It reports whether the visit is new.
func siteSession(ctx context.Context, tx pgx.Tx, a *access, v siteVisitor, m eventMessage, now time.Time) (bool, error) {
	anon := m.text("anonymousId")
	props, err := m.object(maxSiteProps, "properties")
	if err != nil {
		return false, err
	}
	ctxObj, err := m.object(maxEventContext, "context")
	if err != nil {
		return false, err
	}
	touch := touchOf(props, ctxObj)
	page := m.text("type") == "page"
	if !page {
		touch.ad = nil
	}

	var id string
	var last time.Time
	var prev siteTouch
	var prevIDs, prevAd map[string]string
	err = tx.QueryRow(ctx, `SELECT id::text, last_seen_at, utm_source, utm_medium, utm_campaign, click_ids, ad, ref, ofc FROM site_sessions
		WHERE workspace_id = $1 AND anonymous_id = $2 ORDER BY last_seen_at DESC LIMIT 1 FOR UPDATE`, a.WorkspaceID, anon).
		Scan(&id, &last, &prev.source, &prev.medium, &prev.campaign, &prevIDs, &prevAd, &prev.ref, &prev.ofc)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, err
	}
	prev.clickIDs, prev.cookies = splitCookies(prevIDs)
	prev.ad = prevAd
	isNew := id == "" || now.Sub(last) > siteSessionIdle || (touch.arrived() && !touch.same(prev))
	if isNew {
		pageObj, _ := ctxObj["page"].(map[string]any)
		str := func(k string) string {
			s, _ := pageObj[k].(string)
			return s
		}
		referrer, refHost := str("referrer"), ""
		if u, err := url.Parse(referrer); err == nil && u.Hostname() != "" {
			refHost = strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
			// A visit from the site's own pages or its other domains is
			// not a referral.
			if sameSite(refHost, str("url")) || (len(v.origins) > 0 && originAllowed(v.origins, u.Scheme+"://"+u.Host)) {
				referrer, refHost = "", ""
			}
		}
		tz, _ := ctxObj["timezone"].(string)
		rec, _, err := ownerOf(ctx, tx, a, "anonymous_id", anon)
		if err != nil {
			return false, err
		}
		// The visit keeps the browser's network cookies, from this event
		// or, on the same browser, from the visit before.
		ids := map[string]string{}
		for k, v := range prev.cookies {
			ids[k] = v
		}
		for k, v := range touch.cookies {
			ids[k] = v
		}
		for k, v := range touch.clickIDs {
			ids[k] = v
		}
		ad := touch.ad
		if ad == nil {
			ad = map[string]string{}
		}
		if err := tx.QueryRow(ctx, `INSERT INTO site_sessions (workspace_id, key_id, brand_id, anonymous_id, record_id, started_at, last_seen_at, events, pageviews,
				landing_url, landing_path, referrer, referrer_host, utm_source, utm_medium, utm_campaign, utm_term, utm_content, click_ids, ref, ofc,
				country, timezone, device, browser, os, ad)
			VALUES ($1, $2, $3, $4, $5, $6, $6, 1, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24, $25) RETURNING id::text`,
			a.WorkspaceID, v.keyID, v.brandID, anon, rec, now, boolInt(page), cutUTF8(str("url"), 2000), cutUTF8(str("path"), 2000),
			cutUTF8(referrer, 2000), cutUTF8(refHost, 253), touch.source, touch.medium, touch.campaign, touch.term, touch.content, ids,
			touch.ref, touch.ofc, zoneCountry[tz], cutUTF8(tz, 100), v.device, v.browser, v.os, ad).Scan(&id); err != nil {
			return false, err
		}

	} else {
		// A pixel often sets its cookie after the first page view: the
		// visit takes it from a later event.
		var cookies map[string]string
		for k, v := range touch.cookies {
			if prev.cookies[k] != v {
				if cookies == nil {
					cookies = map[string]string{}
				}
				cookies[k] = v
			}
		}
		if cookies == nil {
			cookies = map[string]string{}
		}
		if _, err := tx.Exec(ctx, `UPDATE site_sessions SET last_seen_at = $2, events = events + 1, pageviews = pageviews + $3,
				click_ids = click_ids || $4::jsonb WHERE id = $1`,
			id, now, boolInt(page), cookies); err != nil {
			return false, err
		}
	}
	if touch.ofclk != "" {
		if err := claimRedirectClick(ctx, tx, a.WorkspaceID, touch.ofclk, id, now); err != nil {
			return false, err
		}
	}
	ctxObj["sessionId"] = id
	m["context"] = mustJSON(ctxObj)
	return isNew, nil
}

// sameSite reports whether host is on the same registrable domain as the
// page at pageURL.
func sameSite(host, pageURL string) bool {
	u, err := url.Parse(pageURL)
	if err != nil || u.Hostname() == "" {
		return false
	}
	a, err1 := publicsuffix.EffectiveTLDPlusOne(host)
	b, err2 := publicsuffix.EffectiveTLDPlusOne(strings.ToLower(u.Hostname()))
	return err1 == nil && err2 == nil && a == b
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// touchFields are a person's first and last visit, from their sessions on
// the workspace's sites.
var touchFields = []fieldView{
	computedField("first_touch_at", "First visit", "timestamp", "", true, "When they first visited your site, from website tracking."),
	computedField("first_touch_source", "First source", "text", "", true, "Where their first visit came from: its utm_source, the ad platform of its click id, or the site that linked to you."),
	computedField("first_touch_medium", "First medium", "text", "", true, "Their first visit's utm_medium, cpc for an ad click, referral for a link, or none."),
	computedField("first_touch_campaign", "First campaign", "text", "", true, "Their first visit's utm_campaign."),
	computedField("first_touch_referrer", "First referrer", "text", "", false, "The site that linked to their first visit."),
	computedField("first_touch_landing_page", "First landing page", "text", "", false, "The page their first visit started on."),
	computedField("last_touch_at", "Last visit", "timestamp", "", true, "When their latest visit that came from somewhere started; their latest visit if all were direct."),
	computedField("last_touch_source", "Last source", "text", "", true, "Where that visit came from."),
	computedField("last_touch_medium", "Last medium", "text", "", true, "That visit's medium."),
	computedField("last_touch_campaign", "Last campaign", "text", "", true, "That visit's utm_campaign."),
	computedField("last_touch_referrer", "Last referrer", "text", "", false, "The site that linked to that visit."),
	computedField("last_touch_landing_page", "Last landing page", "text", "", false, "The page that visit started on."),
	computedField("referral_code", "Referral code", "text", "", true, "The partner referral code (ref) on their latest visit that had one."),
}

// clickPlatforms are the ad platforms behind each click id.
var clickPlatforms = func() map[string]string {
	out := map[string]string{}
	for _, c := range adtrack.ClickIDs {
		out[c.Param] = c.Source
	}
	return out
}()

// sourceMedium names where a visit came from the way analytics tools do:
// its tags first, then an ad click id, then the linking site.
func sourceMedium(t siteTouch, refHost string) (string, string) {
	source, medium := t.source, t.medium
	if source == "" {
		keys := make([]string, 0, len(t.clickIDs))
		for k := range t.clickIDs {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			if p := clickPlatforms[k]; p != "" {
				source = p
				if medium == "" {
					medium = "cpc"
				}
				break
			}
		}
	}
	if source == "" && refHost != "" {
		source = refHost
		if medium == "" {
			medium = "referral"
		}
	}
	if source == "" && t.ref != "" {
		source, medium = "partner", "affiliate"
	}
	if source == "" {
		source = "direct"
	}
	if medium == "" {
		medium = "none"
	}
	return source, medium
}

// recomputeTouches works out a person's first and last touch from their
// sessions. A workspace's own field with the same key is left alone.
func recomputeTouches(ctx context.Context, tx pgx.Tx, a *access, c *catalog, id string) error {
	rec, err := scanRecord(tx.QueryRow(ctx, `SELECT `+recordColumns+` FROM records r WHERE r.id = $1 AND r.workspace_id = $2 FOR UPDATE`, id, a.WorkspaceID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil || rec.MergedInto != nil || rec.Type != "person" {
		return err
	}
	t := c.recordType(rec.Type)
	if t == nil {
		return nil
	}
	type visit struct {
		at               time.Time
		touch            siteTouch
		refHost, landing string
	}
	rows, err := tx.Query(ctx, `SELECT started_at, utm_source, utm_medium, utm_campaign, click_ids, ref, ofc, referrer_host, landing_path
		FROM site_sessions WHERE workspace_id = $1 AND record_id = $2 ORDER BY started_at, id`, a.WorkspaceID, id)
	if err != nil {
		return err
	}
	visits, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (visit, error) {
		var v visit
		err := row.Scan(&v.at, &v.touch.source, &v.touch.medium, &v.touch.campaign, &v.touch.clickIDs, &v.touch.ref, &v.touch.ofc, &v.refHost, &v.landing)
		return v, err
	})
	if err != nil {
		return err
	}
	if len(visits) == 0 {
		return nil
	}
	enc := func(v any) json.RawMessage {
		if s, ok := v.(string); ok && s == "" {
			return json.RawMessage("null")
		}
		return mustJSON(v)
	}
	vals := map[string]json.RawMessage{}
	put := func(prefix string, v visit) {
		source, medium := sourceMedium(v.touch, v.refHost)
		vals[prefix+"_at"] = enc(v.at.UTC().Format(time.RFC3339Nano))
		vals[prefix+"_source"] = enc(source)
		vals[prefix+"_medium"] = enc(medium)
		vals[prefix+"_campaign"] = enc(v.touch.campaign)
		vals[prefix+"_referrer"] = enc(v.refHost)
		vals[prefix+"_landing_page"] = enc(v.landing)
	}
	put("first_touch", visits[0])
	last := visits[len(visits)-1]
	for i := len(visits) - 1; i >= 0; i-- {
		if visits[i].touch.arrived() || visits[i].refHost != "" {
			last = visits[i]
			break
		}
	}
	put("last_touch", last)
	vals["referral_code"] = json.RawMessage("null")
	for i := len(visits) - 1; i >= 0; i-- {
		if visits[i].touch.ref != "" {
			vals["referral_code"] = enc(visits[i].touch.ref)
			break
		}
	}
	for k := range vals {
		if f := t.field(k); f == nil || !f.Builtin || f.WritePolicy != "computed" {
			delete(vals, k)
		}
	}
	if len(vals) == 0 {
		return nil
	}
	if rec.Attributes == nil {
		rec.Attributes = map[string]any{}
	}
	if rec.AttributeSources == nil {
		rec.AttributeSources = map[string]string{}
	}
	changed, err := applyComputed(ctx, tx, c, t, a, &rec, vals, paymentsFirstPartySrc)
	if err != nil || !changed {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE records SET attributes = $2, attribute_sources = $3, version = version + 1, updated_at = now() WHERE id = $1`,
		id, rec.Attributes, rec.AttributeSources)
	return err
}
