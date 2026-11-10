package api

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/outflareai/outflare/engine/internal/geoip"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/tokens"
	"github.com/outflareai/outflare/engine/internal/tracking"
	"github.com/outflareai/outflare/engine/internal/trafficsources"
	"github.com/outflareai/outflare/engine/internal/utm"
)

// Redirect links are short links (/l/{code}) to a campaign link, for places
// a site's script can't run or a long tagged URL doesn't fit: a bio link,
// a partner's page, a QR code. A click is counted, then the person goes on
// to the tagged destination with the click's key (ofclk), so the site's
// script still sees the campaign and a later conversion can name the click.
// The site's script stays the default; a redirect link is opt-in.

var (
	redirectCodeRe = regexp.MustCompile(`^[a-z0-9]{8}$`)
	clickKeyRe     = regexp.MustCompile(`^[a-z0-9]{16}$`)
)

const (
	redirectCodeLen = 8
	clickKeyLen     = 16
	// ClickKeyParam carries a redirect click's key to the destination.
	clickKeyParam = "ofclk"
)

// addRedirectCode gives a link a redirect code. Codes are random from 32^8;
// a clash with another link is retried inside a savepoint.
func addRedirectCode(ctx context.Context, tx pgx.Tx, ws, link string) (*string, error) {
	for range 5 {
		code := utm.NewShortCode(redirectCodeLen)
		sp, err := tx.Begin(ctx)
		if err != nil {
			return nil, err
		}
		_, err = sp.Exec(ctx, `UPDATE tracking_links SET redirect_code = $3 WHERE workspace_id = $1 AND id = $2`, ws, link, code)
		var pg *pgconn.PgError
		if errors.As(err, &pg) && pg.Code == "23505" {
			if err := sp.Rollback(ctx); err != nil {
				return nil, err
			}
			continue
		}
		if err != nil {
			return nil, err
		}
		if err := sp.Commit(ctx); err != nil {
			return nil, err
		}
		return &code, nil
	}
	return nil, errors.New("no free redirect code after 5 tries")
}

// redirect serves a redirect link. Public: the code is the authority, as
// the link id is for email clicks. An archived campaign's links still
// redirect, since they may be printed.
func (s *Server) redirect(w http.ResponseWriter, r *http.Request) {
	code := r.PathValue("code")
	if !redirectCodeRe.MatchString(code) {
		metrics.RedirectClick("unknown")
		http.Error(w, "This link has expired or was never valid.", http.StatusNotFound)
		return
	}
	ctx := r.Context()
	now := time.Now()
	t, ok := s.redirects.get(code, now)
	if !ok {
		var err error
		if t, err = s.lookupRedirect(ctx, code); err != nil {
			metrics.RedirectClick("error")
			s.internal(w, r, err)
			return
		}
		s.redirects.put(code, t, now)
	}
	if !t.found {
		metrics.RedirectClick("unknown")
		http.Error(w, "This link has expired or was never valid.", http.StatusNotFound)
		return
	}
	bot := tracking.LikelyBot(r.UserAgent(), time.Time{}, now)
	key := utm.NewShortCode(clickKeyLen)
	c := bufferedClick{ws: t.ws, brand: t.brand, linkID: t.link.ID, key: key, bot: bot, at: now, detail: clickDetails(r)}
	if ip, err := parseClientAddr(clientIP(r)); err == nil && s.box != nil {
		// A keyed hash per workspace: the same visitor's clicks match, and
		// the address can't be read back from it.
		c.ipHash = s.box.Sign("click_ip", t.ws+"|"+ip.Unmap().String())
	}
	// The traffic source's parameters fill the click's custom fields; a
	// destination with tokens ({click_id}, {field1}) gets this click's
	// values.
	var cost string
	c.fields, c.sourceClickID, cost = t.source.capture(r.URL.Query())
	c.costMicros = costMicros(cost)
	link := t.link
	if strings.ContainsAny(link.URL, "{%") {
		link.URL = tokens.ExpandKnown(link.URL, destinationValues(t, c, cost), trafficsources.DestinationTokens)
	}
	target := tagLink(t.campaign, link)
	// The click is written a moment later (RunClicks); the person goes on
	// now. With the buffer full, the click is lost but the redirect isn't.
	if s.clicks.add(c) {
		if bot {
			metrics.RedirectClick("bot")
		} else {
			metrics.RedirectClick("counted")
		}
		target = withParam(target, clickKeyParam, key)
	} else {
		metrics.RedirectClick("dropped")
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, target, http.StatusFound)
}

// clickDetail is where a redirect click came from and on what, worked out
// from the request. The address and user agent themselves aren't kept.
type clickDetail struct {
	place               geoip.Place
	device, browser, os string
	detail              deviceDetail
	language            string
}

func clickDetails(r *http.Request) clickDetail {
	var c clickDetail
	if ip, err := parseClientAddr(clientIP(r)); err == nil {
		c.place = geoip.Lookup(ip)
	}
	if !countryRe.MatchString(c.place.Country) {
		c.place = geoip.Place{ASN: c.place.ASN, Org: c.place.Org}
	}
	ua := r.UserAgent()
	c.device, c.browser, c.os = parseUA(ua)
	c.detail = parseDevice(ua, c.os, c.browser)
	c.language = visitLanguage(r.Header.Get("Accept-Language"))
	return c
}

// withParam adds one query parameter last, keeping the order of the others
// (the campaign's tags) and the fragment.
func withParam(raw, k, v string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	p := url.QueryEscape(k) + "=" + url.QueryEscape(v)
	if u.RawQuery == "" {
		u.RawQuery = p
	} else {
		u.RawQuery += "&" + p
	}
	return u.String()
}

// claimRedirectClick ties a redirect click to the visit its landing page
// started, once: site.js sends the ofclk it found, and only a click of the
// same workspace that no visit has claimed yet is taken.
func claimRedirectClick(ctx context.Context, tx pgx.Tx, ws, key, session string, now time.Time) error {
	tag, err := tx.Exec(ctx, `UPDATE tracking_link_clicks SET session_id = $3, landed_at = $4
		WHERE workspace_id = $1 AND click_key = $2 AND landed_at IS NULL`, ws, key, session, now)
	if err != nil {
		return err
	}
	if tag.RowsAffected() > 0 {
		metrics.RedirectLanding("claimed")
		return nil
	}
	if !clickKeyRe.MatchString(key) {
		metrics.RedirectLanding("unmatched")
		return nil
	}
	// The click may still be waiting to be written (redirect_clicks.go):
	// the landing waits for it, once. A click already landed stays as it is.
	tag, err = tx.Exec(ctx, `INSERT INTO tracking_click_claims (click_key, workspace_id, brand_id, session_id, landed_at)
		SELECT $2, $1, s.brand_id, s.id, $4 FROM site_sessions s
		WHERE s.id = $3 AND NOT EXISTS (SELECT 1 FROM tracking_link_clicks k WHERE k.click_key = $2)
		ON CONFLICT (click_key) DO NOTHING`, ws, key, session, now)
	if err != nil {
		return err
	}
	if tag.RowsAffected() > 0 {
		metrics.RedirectLanding("pending")
	} else {
		metrics.RedirectLanding("unmatched")
	}
	return nil
}
