package api

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/fraud"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/quota"
)

// Direct mode (plans/ad-tracker.md, T8). An ad can link straight to the
// brand's page, with the tracker campaign's ofc code and the traffic
// source's parameters, instead of through a redirect link. There is no
// redirect to count the click, so the visit does: the site script names a
// new click id (ofclk) for a page with ofc and none, sends it as direct,
// and fills it into the page's links; the new visit records the tracker
// click with that id, claimed by itself at once, and the traffic source's
// click id, cost and custom fields from the page's URL. Postbacks, landers
// and the click's conversions then work as for a redirect click.
//
// Only a tracker campaign (one with a traffic source) of the visit's brand
// gets direct clicks, so the plain UTM tool's links don't start counting
// clicks; and only a new visit does, so a reload isn't a second click.

// directClick records a new visit's direct-mode click, when it has one.
func (s *Server) directClick(ctx context.Context, tx pgx.Tx, a *access, v siteVisitor, m eventMessage, now time.Time) error {
	if m.text("type") != "page" {
		return nil
	}
	props, err := m.object(maxSiteProps, "properties")
	if err != nil {
		return nil
	}
	ctxObj, err := m.object(maxEventContext, "context")
	if err != nil {
		return nil
	}
	t := touchOf(props, ctxObj)
	session, _ := ctxObj["sessionId"].(string)
	if !t.direct || t.ofc == "" || !clickKeyRe.MatchString(t.ofclk) || !validUUID(session) {
		return nil
	}
	pageObj, _ := ctxObj["page"].(map[string]any)
	pageURL, _ := pageObj["url"].(string)
	u, err := url.Parse(pageURL)
	if err != nil {
		metrics.RedirectLanding("unmatched")
		return nil
	}

	// The tracker campaign the ofc code names, of the visit's brand, with
	// its traffic source.
	var campaign, brand string
	var sourceID *string
	var clickParam, clickToken, costParam, costToken string
	var params []byte
	err = tx.QueryRow(ctx, `SELECT c.id::text, COALESCE(c.brand_id, outflare_first_brand(c.workspace_id))::text, ts.id::text,
			ts.click_id_param, ts.click_id_token, ts.cost_param, ts.cost_token, ts.parameters
		FROM tracking_campaigns c JOIN tracker_traffic_sources ts ON ts.id = c.traffic_source_id AND ts.workspace_id = c.workspace_id
		WHERE c.workspace_id = $1 AND c.code = $2 AND ($3::uuid IS NULL OR COALESCE(c.brand_id, outflare_first_brand(c.workspace_id)) = $3::uuid)
		LIMIT 1`, a.WorkspaceID, t.ofc, v.brandID).
		Scan(&campaign, &brand, &sourceID, &clickParam, &clickToken, &costParam, &costToken, &params)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // not a tracker campaign: the visit keeps its ofc as before
	}
	if err != nil {
		return err
	}
	// Its link for the page: the one whose page has the same path, else
	// its first.
	rows, err := tx.Query(ctx, `SELECT id::text, url FROM tracking_links WHERE workspace_id = $1 AND campaign_id = $2 ORDER BY created_at LIMIT 100`,
		a.WorkspaceID, campaign)
	if err != nil {
		return err
	}
	type linkRow struct{ id, url string }
	links, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (linkRow, error) {
		var l linkRow
		return l, row.Scan(&l.id, &l.url)
	})
	if err != nil {
		return err
	}
	if len(links) == 0 {
		return nil
	}
	link := links[0].id
	for _, l := range links {
		if lu, err := url.Parse(l.url); err == nil && strings.TrimSuffix(lu.Path, "/") == strings.TrimSuffix(u.Path, "/") {
			link = l.id
			break
		}
	}
	// A direct click counts against the workspace's tracker clicks, as a
	// redirect click does; over the quota it isn't recorded.
	if _, refused := quota.Refusal(quota.Check(ctx, s.quota, s.log, a.WorkspaceID, quota.TrackerClicks, 1)); refused {
		metrics.RedirectClickWrites("refused_quota", 1)
		return nil
	}
	src := readCampaignSource(sourceID, clickParam, clickToken, costParam, costToken, params)
	fields, sourceClickID, cost := src.capture(u.Query())
	f, err := json.Marshal(fields)
	if err != nil || fields == nil {
		f = []byte("{}")
	}
	locale, _ := ctxObj["locale"].(string)
	// The hosting-network check (B7a) applies here too; repeat clicks are
	// counted on redirects only, where the click's address is at hand.
	var reasons []string
	if v.asn > 0 && v.asn <= math.MaxUint32 {
		reasons = fraud.ClickReasons(uint32(v.asn), 0)
	}
	tag, err := tx.Exec(ctx, `INSERT INTO tracking_link_clicks (workspace_id, brand_id, link_id, click_key, likely_bot, clicked_at,
			country, region, city, asn, isp, device, device_brand, device_model, os, os_version, browser, browser_version, language,
			fields, source_click_id, cost_micros, session_id, landed_at, direct, fraud_reasons)
		VALUES ($1, $2, $3, $4, false, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $5, true, $23)
		ON CONFLICT (click_key) DO NOTHING`,
		a.WorkspaceID, brand, link, t.ofclk, now, v.country, cutUTF8(v.region, 200), cutUTF8(v.city, 200), v.asn, cutUTF8(v.isp, 200),
		v.device, v.detail.brand, v.detail.model, v.os, cutUTF8(v.detail.osVersion, 20), v.browser, cutUTF8(v.detail.browserVersion, 20),
		visitLanguage(locale), string(f), cutUTF8(sourceClickID, maxSourceClickIDLen), costMicros(cost), session, append([]string{}, reasons...))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		// A click with that id exists: the visit is someone else's id.
		metrics.RedirectLanding("unmatched")
		return nil
	}
	metrics.RedirectLanding("direct")
	for _, reason := range reasons {
		metrics.TrackerFraudFlag("click", reason)
	}
	return nil
}
