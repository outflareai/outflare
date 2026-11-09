package api

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/outflareai/outflare/engine/internal/geoip"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/track"
	"github.com/outflareai/outflare/engine/internal/tracking"
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
	var ws string
	var c trackingCampaignView
	l := trackingLinkView{}
	err := s.pool.QueryRow(ctx, `SELECT l.workspace_id::text, l.id::text, l.url, l.content, l.term, c.id::text, c.code, c.channel, c.source, c.medium, c.slug
		FROM tracking_links l JOIN tracking_campaigns c ON c.id = l.campaign_id WHERE l.redirect_code = $1`, code).
		Scan(&ws, &l.ID, &l.URL, &l.Content, &l.Term, &c.ID, &c.Code, &c.Channel, &c.Source, &c.Medium, &c.Slug)
	if errors.Is(err, pgx.ErrNoRows) {
		metrics.RedirectClick("unknown")
		http.Error(w, "This link has expired or was never valid.", http.StatusNotFound)
		return
	}
	if err != nil {
		metrics.RedirectClick("error")
		s.internal(w, r, err)
		return
	}
	target := tagLink(c, l)
	bot := tracking.LikelyBot(r.UserAgent(), time.Time{}, time.Now())
	key := utm.NewShortCode(clickKeyLen)
	click := clickDetails(r)
	if _, err := s.pool.Exec(ctx, `INSERT INTO tracking_link_clicks (workspace_id, link_id, click_key, likely_bot,
			country, region, city, asn, isp, device, device_brand, device_model, os, os_version, browser, browser_version, language)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)`,
		ws, l.ID, key, bot, click.place.Country, cutUTF8(click.place.Region, 200), cutUTF8(click.place.City, 200), int64(click.place.ASN), cutUTF8(click.place.Org, 200),
		click.device, click.detail.brand, click.detail.model, click.os, cutUTF8(click.detail.osVersion, 20), click.browser, cutUTF8(click.detail.browserVersion, 20),
		click.language); err != nil {
		// The person still goes where the link says; only the count is lost.
		metrics.RedirectClick("error")
		track.Fail(ctx, s.log, track.Event{Name: "tracking_link.click_failed", Surface: "system", WorkspaceID: ws,
			Props: map[string]any{"campaign_id": c.ID}}, "internal", err)
	} else {
		if bot {
			metrics.RedirectClick("bot")
		} else {
			metrics.RedirectClick("counted")
		}
		target = withParam(target, clickKeyParam, key)
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
	} else {
		metrics.RedirectLanding("unmatched")
	}
	return nil
}
