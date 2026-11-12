package api

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/quota"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Redirect links take up to about a million clicks a day per workspace
// (plans/ad-tracker.md, T1). A redirect never waits on the database for
// its click: the link it resolves to is kept in memory, and the click is
// buffered and written in batches a moment later (RunClicks). A landing page
// that reports its click before the batch is written waits in
// tracking_click_claims until it is.

const (
	// A link's destination never changes once it has a redirect code (its
	// campaign's slug, channel and source are fixed), so it is kept a while.
	redirectCacheTTL = 5 * time.Minute
	// An unknown code is remembered for less. New codes are random, so a
	// code someone tried is almost never given to a link later.
	redirectMissTTL  = 30 * time.Second
	redirectCacheMax = 100_000

	// Clicks are written in batches of up to clickBatch, every
	// clickFlushEvery or as soon as a batch is full. Over clickBufferMax
	// waiting (the database down for a while), new clicks are dropped and
	// counted: the redirect still happens.
	clickBatch      = 1000
	clickFlushEvery = 250 * time.Millisecond
	clickBufferMax  = 100_000
	// A landing that no click took within this is dropped.
	clickClaimMaxAge = time.Hour
)

// redirectTarget is what a redirect code resolves to.
type redirectTarget struct {
	found     bool
	ws, brand string
	// loc is the brand's reporting time zone, whose days caps count.
	loc      *time.Location
	link     trackingLinkView
	campaign trackingCampaignView
	// source is the campaign's traffic source, nil without one.
	source *campaignSource
	// dests are the link's weighted destinations, none for the link's own
	// page only.
	dests []linkDestination
	// paths are its rule-based paths, in order.
	paths []linkPath
	until time.Time
}

type redirectCache struct {
	mu sync.RWMutex
	m  map[string]redirectTarget
}

func (c *redirectCache) get(code string, now time.Time) (redirectTarget, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	t, ok := c.m[code]
	if !ok || now.After(t.until) {
		return redirectTarget{}, false
	}
	return t, true
}

func (c *redirectCache) put(code string, t redirectTarget, now time.Time) {
	t.until = now.Add(redirectCacheTTL)
	if !t.found {
		t.until = now.Add(redirectMissTTL)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil || len(c.m) >= redirectCacheMax {
		c.m = map[string]redirectTarget{}
	}
	c.m[code] = t
}

// clear forgets every code, so a change to a traffic source shows at
// once on this server (other servers within redirectCacheTTL).
func (c *redirectCache) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m = nil
}

// lookupRedirect reads what code resolves to from the database.
func (s *Server) lookupRedirect(ctx context.Context, code string) (redirectTarget, error) {
	t := redirectTarget{found: true}
	var brand, sourceID, flow *string
	var clickParam, clickToken, costParam, costToken, tz string
	var params []byte
	err := s.pool.QueryRow(ctx, `SELECT l.workspace_id::text, l.brand_id::text, l.id::text, l.url, l.content, l.term,
			c.id::text, c.code, c.channel, c.source, c.medium, c.slug, ts.id::text,
			COALESCE(ts.click_id_param, ''), COALESCE(ts.click_id_token, ''), COALESCE(ts.cost_param, ''), COALESCE(ts.cost_token, ''),
			COALESCE(ts.parameters, '[]'), c.redirect_mode, f.id::text,
			COALESCE((SELECT b.timezone FROM brands b WHERE b.id = COALESCE(l.brand_id, c.brand_id, outflare_first_brand(l.workspace_id))), 'UTC')
		FROM tracking_links l JOIN tracking_campaigns c ON c.id = l.campaign_id
		LEFT JOIN tracker_flows f ON f.id = l.flow_id AND f.workspace_id = l.workspace_id
		LEFT JOIN tracker_traffic_sources ts ON ts.id = c.traffic_source_id AND ts.workspace_id = c.workspace_id
		WHERE l.redirect_code = $1`, code).
		Scan(&t.ws, &brand, &t.link.ID, &t.link.URL, &t.link.Content, &t.link.Term,
			&t.campaign.ID, &t.campaign.Code, &t.campaign.Channel, &t.campaign.Source, &t.campaign.Medium, &t.campaign.Slug,
			&sourceID, &clickParam, &clickToken, &costParam, &costToken, &params, &t.campaign.RedirectMode, &flow, &tz)
	if errors.Is(err, pgx.ErrNoRows) {
		return redirectTarget{}, nil
	}
	t.source = readCampaignSource(sourceID, clickParam, clickToken, costParam, costToken, params)
	// A link with a flow routes by the flow's paths and destinations.
	owner := linkOwner(t.link.ID)
	if flow != nil {
		owner = flowOwner(*flow)
	}
	t.loc = zone(tz)
	day := capDay(time.Now(), t.loc)
	if err == nil {
		t.dests, err = s.loadDestinations(ctx, owner, day)
	}
	if err == nil {
		t.paths, err = s.loadPaths(ctx, owner, day)
	}
	if brand != nil {
		t.brand = *brand
	}
	return t, err
}

// bufferedClick is one redirect click waiting to be written.
type bufferedClick struct {
	ws, brand, linkID, key, ipHash string
	// A partner short-link click names its membership instead of a link,
	// with the partner's sub id and the referring site.
	membershipID, subID, referrerHost string
	// The click's custom fields (by number, "1" to "20"), the traffic
	// source's click id for it and the cost it reported, in millionths.
	fields        map[string]string
	sourceClickID string
	costMicros    int64
	// destinationID is the destination the click went to, "" for the
	// link's own page, and pathID the rule-based path it took.
	destinationID, pathID string
	// fraud are why it is suspect (engine/internal/fraud), none when it
	// isn't.
	fraud []string
	// offerID is the offer it went to, and landerID the lander.
	offerID, landerID string
	bot               bool
	at                time.Time
	// day is the day its caps count it on: the brand's (capDay).
	day    string
	detail clickDetail
}

// uncounted says a click goes where a person's would but counts against
// no cap: a likely bot, or a click a fraud signal flagged.
func (c bufferedClick) uncounted() bool { return c.bot || len(c.fraud) > 0 }

type clickBuffer struct {
	mu      sync.Mutex
	pending []bufferedClick
	wake    chan struct{}
	// One flush at a time, so a batch is never written twice.
	flushing sync.Mutex
}

// add queues a click. It reports false, dropping it, when the buffer is
// full.
func (b *clickBuffer) add(c bufferedClick) bool {
	b.mu.Lock()
	if len(b.pending) >= clickBufferMax {
		b.mu.Unlock()
		return false
	}
	b.pending = append(b.pending, c)
	n := len(b.pending)
	b.mu.Unlock()
	metrics.RedirectClickBuffer(n)
	if n >= clickBatch && b.wake != nil {
		select {
		case b.wake <- struct{}{}:
		default:
		}
	}
	return true
}

// take removes up to n clicks from the front of the buffer.
func (b *clickBuffer) take(n int) []bufferedClick {
	b.mu.Lock()
	defer b.mu.Unlock()
	if n > len(b.pending) {
		n = len(b.pending)
	}
	out := make([]bufferedClick, n)
	copy(out, b.pending[:n])
	b.pending = b.pending[n:]
	if len(b.pending) == 0 {
		b.pending = nil // let the old array go
	}
	return out
}

// putBack returns a batch that couldn't be written to the front of the
// buffer, as far as there is room.
func (b *clickBuffer) putBack(batch []bufferedClick) (dropped int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	room := clickBufferMax - len(b.pending)
	if room < len(batch) {
		dropped = len(batch) - max(room, 0)
		batch = batch[:max(room, 0)]
	}
	b.pending = append(batch, b.pending...)
	return dropped
}

func (b *clickBuffer) len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.pending)
}

// RunClicks writes buffered redirect clicks until ctx ends, then writes
// what is left. Every process that serves redirect links runs it.
func (s *Server) RunClicks(ctx context.Context) {
	t := time.NewTicker(clickFlushEvery)
	defer t.Stop()
	sweep := time.NewTicker(time.Minute)
	defer sweep.Stop()
	for {
		select {
		case <-ctx.Done():
			// What is still waiting is written before the process stops.
			end, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			s.FlushClicks(end)
			cancel()
			return
		case <-t.C:
		case <-s.clicks.wake:
		case <-sweep.C:
			s.sweepClickClaims(ctx)
			s.refreshOfferCaps(ctx)
			continue
		}
		s.FlushClicks(ctx)
	}
}

// FlushClicks writes every buffered click now. A batch that fails goes
// back to wait for the next flush.
func (s *Server) FlushClicks(ctx context.Context) error {
	s.clicks.flushing.Lock()
	defer s.clicks.flushing.Unlock()
	defer func() { metrics.RedirectClickBuffer(s.clicks.len()) }()
	for {
		batch := s.clicks.take(clickBatch)
		if len(batch) == 0 {
			return nil
		}
		start := time.Now()
		err := s.writeClicks(ctx, batch)
		metrics.RedirectClickFlush(time.Since(start))
		if err != nil {
			metrics.RedirectClickWrites("failed", len(batch))
			if n := s.clicks.putBack(batch); n > 0 {
				metrics.RedirectClickWrites("dropped_full", n)
			}
			if ctx.Err() == nil {
				track.Fail(ctx, s.log, track.Event{Name: "tracking_link.click_failed", Surface: "system",
					Props: map[string]any{"clicks": len(batch)}}, "internal", err)
			}
			return err
		}
	}
}

// writeClicks writes one batch: each workspace's clicks once its quota
// allows them, and the landings that were waiting for them.
func (s *Server) writeClicks(ctx context.Context, batch []bufferedClick) error {
	byWS := map[string]int64{}
	for _, c := range batch {
		if !c.bot {
			byWS[c.ws]++
		}
	}
	refused := map[string]bool{}
	for ws, n := range byWS {
		if _, ok := quota.Refusal(quota.Check(ctx, s.quota, s.log, ws, quota.TrackerClicks, n)); ok {
			refused[ws] = true
		}
	}
	// Clicks over the quota aren't stored, so they don't count against a
	// destination's cap either: release what this server held for them.
	var kept []bufferedClick
	for _, c := range batch {
		if !refused[c.ws] {
			kept = append(kept, c)
		} else if !c.uncounted() {
			for _, id := range []string{c.destinationID, c.offerID} {
				if id != "" {
					s.caps.settle(id, c.day, 1, 0)
				}
			}
		}
	}
	var (
		membership, subID, referrer, fields, sourceClickID, destination, path, offer, lander, fr   []string
		cost                                                                                       []int64
		ws, brand, link, key, ipHash, country, region, city, isp, device, deviceBrand, deviceModel []string
		os, osVersion, browser, browserVersion, language                                           []string
		bot                                                                                        []bool
		at                                                                                         []time.Time
		asn                                                                                        []int64
	)
	for _, c := range batch {
		if refused[c.ws] {
			continue
		}
		d := c.detail
		ws, brand, link, key, ipHash = append(ws, c.ws), append(brand, c.brand), append(link, c.linkID), append(key, c.key), append(ipHash, c.ipHash)
		bot, at = append(bot, c.bot), append(at, c.at)
		country, region, city = append(country, d.place.Country), append(region, cutUTF8(d.place.Region, 200)), append(city, cutUTF8(d.place.City, 200))
		asn, isp = append(asn, int64(d.place.ASN)), append(isp, cutUTF8(d.place.Org, 200))
		device, deviceBrand, deviceModel = append(device, d.device), append(deviceBrand, d.detail.brand), append(deviceModel, d.detail.model)
		os, osVersion = append(os, d.os), append(osVersion, cutUTF8(d.detail.osVersion, 20))
		browser, browserVersion = append(browser, d.browser), append(browserVersion, cutUTF8(d.detail.browserVersion, 20))
		language = append(language, d.language)
		membership, subID, referrer = append(membership, c.membershipID), append(subID, c.subID), append(referrer, c.referrerHost)
		f, err := json.Marshal(c.fields)
		if err != nil || c.fields == nil {
			f = []byte("{}")
		}
		fields, sourceClickID, cost = append(fields, string(f)), append(sourceClickID, c.sourceClickID), append(cost, c.costMicros)
		destination, path, offer = append(destination, c.destinationID), append(path, c.pathID), append(offer, c.offerID)
		lander, fr = append(lander, c.landerID), append(fr, strings.Join(c.fraud, ","))
	}
	if n := len(batch) - len(key); n > 0 {
		metrics.RedirectClickWrites("refused_quota", n)
	}
	if len(key) == 0 {
		return nil
	}
	var written, claimed int64
	var settles []destinationSettle
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// A click whose link or partner is gone (its workspace deleted
		// meanwhile) is left out rather than failing the batch; a retried
		// batch writes each click once.
		tag, err := tx.Exec(ctx, `INSERT INTO tracking_link_clicks (workspace_id, brand_id, link_id, membership_id, sub_id, referrer_host,
				click_key, likely_bot, clicked_at, ip_hash,
				country, region, city, asn, isp, device, device_brand, device_model, os, os_version, browser, browser_version, language,
				fields, source_click_id, cost_micros, destination_id, path_id, offer_id, lander_id, fraud_reasons)
			SELECT t.ws::uuid, COALESCE(NULLIF(t.brand, '')::uuid, l.brand_id, m.brand_id), l.id, m.id, t.sub_id, t.referrer,
				t.key, t.bot, t.at, t.ip_hash,
				t.country, t.region, t.city, t.asn, t.isp, t.device, t.device_brand, t.device_model, t.os, t.os_version, t.browser, t.browser_version, t.language,
				t.fields::jsonb, t.source_click_id, t.cost, NULLIF(t.destination, '')::uuid, NULLIF(t.path, '')::uuid, NULLIF(t.offer, '')::uuid, NULLIF(t.lander, '')::uuid,
				COALESCE(string_to_array(NULLIF(t.fraud, ''), ','), '{}')
			FROM unnest($1::text[], $2::text[], $3::text[], $4::text[], $5::bool[], $6::timestamptz[], $7::text[],
				$8::text[], $9::text[], $10::text[], $11::bigint[], $12::text[], $13::text[], $14::text[], $15::text[], $16::text[], $17::text[],
				$18::text[], $19::text[], $20::text[], $21::text[], $22::text[], $23::text[], $24::text[], $25::text[], $26::bigint[], $27::text[], $28::text[], $29::text[], $30::text[], $31::text[])
				AS t(ws, brand, link, key, bot, at, ip_hash, country, region, city, asn, isp, device, device_brand, device_model, os, os_version,
					browser, browser_version, language, membership, sub_id, referrer, fields, source_click_id, cost, destination, path, offer, lander, fraud)
			LEFT JOIN tracking_links l ON t.link <> '' AND l.id = NULLIF(t.link, '')::uuid AND l.workspace_id = t.ws::uuid
			LEFT JOIN partner_memberships m ON t.membership <> '' AND m.id = NULLIF(t.membership, '')::uuid AND m.workspace_id = t.ws::uuid
			WHERE l.id IS NOT NULL OR m.id IS NOT NULL
			ON CONFLICT (click_key) DO NOTHING`,
			ws, brand, link, key, bot, at, ipHash, country, region, city, asn, isp, device, deviceBrand, deviceModel, os, osVersion,
			browser, browserVersion, language, membership, subID, referrer, fields, sourceClickID, cost, destination, path, offer, lander, fr)
		if err != nil {
			return err
		}
		written = tag.RowsAffected()
		if settles, err = countDestinationClicks(ctx, tx, kept); err != nil {
			return err
		}
		offers, err := countOfferClicks(ctx, tx, kept)
		if err != nil {
			return err
		}
		settles = append(settles, offers...)
		claimed, err = applyClickClaims(ctx, tx, key)
		return err
	})
	if err != nil {
		return err
	}
	for _, d := range settles {
		s.caps.settle(d.id, d.day, d.n, d.total)
	}
	metrics.RedirectClickWrites("written", int(written))
	if gone := int64(len(key)) - written; gone > 0 {
		metrics.RedirectClickWrites("link_gone", int(gone))
	}
	for range claimed {
		metrics.RedirectLanding("claimed")
	}
	return nil
}

// applyClickClaims gives clicks the landings that were waiting for them:
// the clicks in keys, or every waiting landing whose click exists when keys
// is nil.
func applyClickClaims(ctx context.Context, tx pgx.Tx, keys []string) (int64, error) {
	tag, err := tx.Exec(ctx, `WITH c AS (
			DELETE FROM tracking_click_claims p USING tracking_link_clicks k
			WHERE k.click_key = p.click_key AND k.workspace_id = p.workspace_id AND ($1::text[] IS NULL OR p.click_key = ANY ($1))
			RETURNING p.click_key, p.workspace_id, p.session_id, p.landed_at)
		UPDATE tracking_link_clicks k SET session_id = c.session_id, landed_at = c.landed_at
		FROM c WHERE k.click_key = c.click_key AND k.workspace_id = c.workspace_id AND k.landed_at IS NULL`, keys)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// sweepClickClaims gives clicks any landing that missed its batch (both
// were written at the same moment), then drops landings no click took
// within clickClaimMaxAge.
func (s *Server) sweepClickClaims(ctx context.Context) {
	var claimed, dropped int64
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		if claimed, err = applyClickClaims(ctx, tx, nil); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `DELETE FROM tracking_click_claims WHERE created_at < $1`, time.Now().Add(-clickClaimMaxAge))
		dropped = tag.RowsAffected()
		return err
	})
	if err != nil {
		if ctx.Err() == nil {
			track.Fail(ctx, s.log, track.Event{Name: "tracking_link.claims_sweep_failed", Surface: "system"}, "internal", err)
		}
		return
	}
	for range claimed {
		metrics.RedirectLanding("claimed")
	}
	for range dropped {
		metrics.RedirectLanding("unmatched")
	}
}
