package adsuggest

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/paidads"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Loading what the rules read: every campaign of the workspace with its
// numbers by day (the network's from ad_metrics_daily, the ledger's from
// paidads.Daily), the brand's economics, lag and judgement window, which
// campaigns are its winners, and, per campaign, the search terms,
// keywords, ads and segments the keyword and exclusion rules look at.

// brandCtx is what the rules know about a brand.
type brandCtx struct {
	id, name   string
	econ       paidads.Economics
	lag        paidads.Lag
	windowDays int
	lagDays    int
	cycleDays  int
	// alertOpen: a tracking_broken or sends_failing alert is open, so the
	// numbers can't be trusted and no change-class finding is made.
	alertOpen bool
}

// Alert kinds that stop change-class rules while open (the alerts job
// writes them: adsync/alerts.go).
var blockingAlerts = []string{"alert.tracking_broken", "alert.sends_failing"}

func brandKey(b *string) string {
	if b == nil {
		return ""
	}
	return *b
}

// historyDays is how far back the campaigns' days are read: the longest
// lookback plus the longest lag.
const historyDays = kwLookback3 + maxLagDays

// loadCampaigns reads the workspace's campaigns on syncing accounts, with
// their numbers by day and their brand's context.
func (r *Runner) loadCampaigns(ctx context.Context, ws string) ([]campaign, map[string]brandCtx, error) {
	rows, err := r.Pool.Query(ctx, `
		SELECT e.id, e.external_id, e.name, a.id, a.external_id, a.connector, a.currency, e.status, e.objective, COALESCE(e.brand_id, a.brand_id)::text, COALESCE(b.name, ''),
			e.daily_budget_micros, e.bidding_strategy, e.platform_target_cpa_micros, e.platform_target_roas,
			COALESCE(e.target_cost_per_conversion_micros, a.target_cost_per_conversion_micros, e.platform_target_cpa_micros),
			(SELECT max(c.created_at) FROM ad_changes c WHERE c.ad_entity_id = e.id AND c.status <> 'failed'),
			(SELECT max(c.created_at) FROM ad_changes c WHERE c.ad_entity_id = e.id AND c.status <> 'failed' AND c.kind IN ('bidding', 'target')),
			(SELECT max(c.created_at) FROM ad_changes c WHERE c.ad_entity_id = e.id AND c.status <> 'failed' AND c.kind = 'pause'),
			EXISTS (SELECT 1 FROM ad_suggestions s WHERE s.ad_entity_id = e.id AND s.status IN ('measuring', 'testing'))
				OR EXISTS (SELECT 1 FROM ad_tests t WHERE t.ad_entity_id = e.id AND t.status IN ('starting', 'running', 'judged', 'ending')),
			a.integration_id, a.name, a.time_zone, a.meta
		FROM ad_entities e JOIN ad_accounts a ON a.id = e.ad_account_id LEFT JOIN brands b ON b.id = COALESCE(e.brand_id, a.brand_id)
		WHERE e.workspace_id = $1 AND e.level = 'campaign' AND e.status IN ('active', 'paused') AND a.status = 'active' AND a.sync_on
		ORDER BY a.id, e.name, e.id`, ws)
	if err != nil {
		return nil, nil, err
	}
	camps, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (campaign, error) {
		var c campaign
		var meta []byte
		err := row.Scan(&c.id, &c.externalID, &c.name, &c.accountID, &c.acctExternalID, &c.connector, &c.currency, &c.status, &c.objective, &c.brand, &c.brandName,
			&c.budget, &c.bidding, &c.platformTCPA, &c.platformTROAS, &c.platformTarget, &c.lastChange, &c.lastTargetChange, &c.pausedSince, &c.busy,
			&c.integrationID, &c.acctName, &c.acctTZ, &meta)
		_ = json.Unmarshal(meta, &c.acctMeta)
		c.workspaceID = ws
		c.caps = paidads.Caps(c.connector)
		c.days = map[string]dayNum{}
		return c, err
	})
	if err != nil {
		return nil, nil, err
	}
	brands := map[string]brandCtx{}
	byBrand := map[string][]int{}
	ids := make([]string, len(camps))
	for i := range camps {
		ids[i] = camps[i].id
		byBrand[brandKey(camps[i].brand)] = append(byBrand[brandKey(camps[i].brand)], i)
	}
	for key, idx := range byBrand {
		b := brandCtx{id: key, windowDays: int(paidads.MinJudgementWindow / (24 * time.Hour)), cycleDays: 1}
		if key != "" {
			b.name = camps[idx[0]].brandName
			econ, err := paidads.Load(ctx, r.Pool, ws, key)
			if err != nil && !errors.Is(err, paidads.ErrNoBrand) {
				return nil, nil, err
			}
			b.econ = econ
			if b.lag, err = paidads.ClickToPaymentLag(ctx, r.Pool, key); err != nil {
				return nil, nil, err
			}
			b.windowDays, b.lagDays = windowOf(b.lag)
			b.cycleDays = max(1, b.lagDays)
			if err := r.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM ad_suggestions WHERE workspace_id = $1 AND brand_id = $2 AND status = 'open' AND kind = ANY($3))`, ws, key, blockingAlerts).Scan(&b.alertOpen); err != nil {
				return nil, nil, err
			}
		}
		brands[key] = b
	}
	last := r.lastDay()
	// The network's numbers by day.
	mrows, err := r.Pool.Query(ctx, `SELECT ad_entity_id, to_char(day, 'YYYY-MM-DD'), spend_micros, clicks, impressions, COALESCE(conversions, 0)::float8
		FROM ad_metrics_daily WHERE workspace_id = $1 AND ad_entity_id = ANY($2::uuid[]) AND day > $3::date - $4::int AND day <= $3::date`, ws, ids, last, historyDays)
	if err != nil {
		return nil, nil, err
	}
	pos := map[string]int{}
	for i := range camps {
		pos[camps[i].id] = i
	}
	for mrows.Next() {
		var id, day string
		var d dayNum
		if err := mrows.Scan(&id, &day, &d.spend, &d.clicks, &d.impressions, &d.conv); err != nil {
			mrows.Close()
			return nil, nil, err
		}
		camps[pos[id]].days[day] = d
	}
	mrows.Close()
	if err := mrows.Err(); err != nil {
		return nil, nil, err
	}
	// The ledger's numbers by day, per brand.
	for key, idx := range byBrand {
		if key == "" {
			continue
		}
		bids := make([]string, len(idx))
		for i, j := range idx {
			bids[i] = camps[j].id
		}
		daily, err := paidads.Daily(ctx, r.Pool, key, bids, last.AddDate(0, 0, -historyDays), last)
		if err != nil {
			return nil, nil, err
		}
		for id, days := range daily {
			c := &camps[pos[id]]
			for day, s := range days {
				d := c.days[day]
				d.payers, d.signups, d.revenue = s.NewPayers, s.Signups, s.NewRevenue
				c.days[day] = d
			}
		}
	}
	// Each campaign's unit, target, window and the brand's totals.
	for key, idx := range byBrand {
		b := brands[key]
		var brandSpend int64
		var brandConv float64
		for _, j := range idx {
			c := &camps[j]
			c.windowDays, c.lagDays, c.cycleDays = b.windowDays, b.lagDays, b.cycleDays
			c.last = last.AddDate(0, 0, -b.lagDays)
			c.unit, c.target = UnitPlatform, c.platformTarget
			if b.econ.HasTarget() {
				c.unit, c.target = UnitPayers, b.econ.TargetCostPerPayerMicros
			}
			for i := 0; i < 7; i++ {
				if d := c.days[c.last.AddDate(0, 0, -i).Format(time.DateOnly)]; c.budget != nil && *c.budget > 0 && d.spend >= *c.budget*9/10 {
					c.capped++
				}
			}
			w := c.window()
			brandSpend += w.Spend
			brandConv += w.Conv
		}
		// Winners: the top share of the brand's campaigns by payers over
		// 30 days, among those with any.
		type ranked struct {
			j    int
			conv float64
		}
		var list []ranked
		for _, j := range idx {
			if n := camps[j].over(tightenDays); n.Conv > 0 {
				list = append(list, ranked{j, n.Conv})
			}
		}
		sort.Slice(list, func(i, k int) bool { return list[i].conv > list[k].conv })
		top := int(math.Ceil(winnerShare * float64(len(list))))
		for i := 0; i < top && i < len(list); i++ {
			camps[list[i].j].top = true
		}
		for _, j := range idx {
			camps[j].brandPayers = int(brandConv)
			if brandConv > 0 {
				camps[j].brandCost = float64(brandSpend) / brandConv
			}
		}
	}
	return camps, brands, nil
}

// readSignals reads the campaign's own report (impression share lost,
// networks, segments) once a day per campaign, where the network has it.
func (r *Runner) readSignals(ctx context.Context, c *campaign) {
	if r.Store == nil {
		return
	}
	day := r.now().UTC().Format(time.DateOnly)
	r.mu.Lock()
	if r.signalsRead == nil {
		r.signalsRead, r.signalsMemo = map[string]string{}, map[string]connectors.AdCampaignSignals{}
	}
	if r.signalsRead[c.id] == day {
		if s, ok := r.signalsMemo[c.id]; ok {
			c.signals = &s
		}
		r.mu.Unlock()
		return
	}
	r.mu.Unlock()
	o, err := r.Store.Open(ctx, r.Pool, c.ws(), c.integrationID)
	if err != nil {
		return
	}
	rd, ok := o.Connector.(connectors.AdSignalsReader)
	if !ok {
		return
	}
	from, to := c.last.AddDate(0, 0, -(exclDays-1)).Format(time.DateOnly), c.last.Format(time.DateOnly)
	s, err := rd.AdCampaignSignals(ctx, o.Client, o.Creds, c.ad(), c.externalID, from, to)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.signalsRead[c.id] = day
	if err != nil {
		track.Fail(ctx, r.log(), track.Event{Name: "ad_suggestions.read_failed", ActorType: "system", Surface: "job", Outcome: track.Error,
			Props: map[string]any{"connector": c.connector, "campaign_id": c.id, "check": "signals"}}, "connector_"+connectors.KindOf(err), err)
		delete(r.signalsMemo, c.id)
		return
	}
	r.signalsMemo[c.id] = s
	c.signals = &s
}

// ad is the campaign's account as the network calls take it.
func (c campaign) ad() connectors.AdAccount {
	return connectors.AdAccount{ID: c.acctExternalID, Name: c.acctName, Currency: c.currency, TimeZone: c.acctTZ, Meta: c.acctMeta}
}

// payersByKeyword is the ledger's payers and sign-ups over the last days
// for a campaign, by the keyword text its clicks carried (lowercased).
// Search terms and keywords are matched on it: the ledger knows the
// keyword a click matched, not the search typed, so a search converting
// through a broad keyword is never counted as waste on this alone; the
// rules ask the network's conversions too.
func (r *Runner) payersByKeyword(ctx context.Context, c campaign, days int) (map[string][2]int, error) {
	out := map[string][2]int{}
	if c.brand == nil {
		return out, nil
	}
	rows, err := r.Pool.Query(ctx, `SELECT lower(keyword), count(*) FILTER (WHERE first_paid_at > $3::date - $4::int AND first_paid_at < $3::date + 1)::int,
			count(*) FILTER (WHERE signed_up_at > $3::date - $4::int AND signed_up_at < $3::date + 1)::int
		FROM ad_acquisitions WHERE brand_id = $1 AND campaign_entity_id = $2 AND keyword <> '' GROUP BY 1`, *c.brand, c.id, c.last, days)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		var p, s int
		if err := rows.Scan(&k, &p, &s); err != nil {
			return nil, err
		}
		out[k] = [2]int{p, s}
	}
	return out, rows.Err()
}

// loadTerms reads the campaign's search terms over the last 30 days (60
// for the payment-quality rule, which reads sign-ups), with the ledger's
// payers where a keyword matches the search.
func (r *Runner) loadTerms(ctx context.Context, ws string, c campaign) ([]term, error) {
	rows, err := r.Pool.Query(ctx, `
		SELECT g.id, g.external_id, g.name, t.term, COALESCE(sum(t.spend_micros), 0)::bigint, COALESCE(sum(t.clicks), 0)::bigint, COALESCE(sum(t.conversions), 0)::float8,
			EXISTS (SELECT 1 FROM ad_keywords k WHERE k.ad_group_id = g.id AND lower(k.text) = lower(t.term) AND k.status <> 'removed')
		FROM ad_search_terms_daily t JOIN ad_entities g ON g.id = t.ad_group_id
		WHERE t.workspace_id = $1 AND g.parent_id = $2 AND g.status = 'active' AND t.day > $3::date - $4::int AND t.day <= $3::date
		GROUP BY g.id, t.term`, ws, c.id, c.last, termDays)
	if err != nil {
		return nil, err
	}
	terms, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (term, error) {
		var t term
		err := row.Scan(&t.groupID, &t.groupExternal, &t.groupName, &t.text, &t.spend, &t.clicks, &t.conv, &t.exists)
		return t, err
	})
	if err != nil {
		return nil, err
	}
	payers, err := r.payersByKeyword(ctx, c, negQualityDays)
	if err != nil {
		return nil, err
	}
	for i := range terms {
		p := payers[strings.ToLower(terms[i].text)]
		terms[i].payers, terms[i].signups = p[0], p[1]
	}
	return terms, nil
}

// loadKeywords reads the campaign's active keywords over 30, 60 and 90
// days, and its ad groups with their keywords and payers over 30 days.
func (r *Runner) loadKeywords(ctx context.Context, ws string, c campaign) (map[int][]keyword, []group, error) {
	rows, err := r.Pool.Query(ctx, `
		SELECT k.id, k.external_id, g.id, g.external_id, g.name, k.text, k.match_type,
			COALESCE(sum(m.spend_micros) FILTER (WHERE m.day > $3::date - 30), 0)::bigint, COALESCE(sum(m.clicks) FILTER (WHERE m.day > $3::date - 30), 0)::bigint, COALESCE(sum(m.conversions) FILTER (WHERE m.day > $3::date - 30), 0)::float8,
			COALESCE(sum(m.spend_micros) FILTER (WHERE m.day > $3::date - 60), 0)::bigint, COALESCE(sum(m.clicks) FILTER (WHERE m.day > $3::date - 60), 0)::bigint, COALESCE(sum(m.conversions) FILTER (WHERE m.day > $3::date - 60), 0)::float8,
			COALESCE(sum(m.spend_micros), 0)::bigint, COALESCE(sum(m.clicks), 0)::bigint, COALESCE(sum(m.conversions), 0)::float8
		FROM ad_keywords k JOIN ad_entities g ON g.id = k.ad_group_id
		LEFT JOIN ad_keyword_metrics_daily m ON m.keyword_id = k.id AND m.day > $3::date - 90 AND m.day <= $3::date
		WHERE k.workspace_id = $1 AND g.parent_id = $2 AND k.status = 'active' AND g.status = 'active'
		GROUP BY k.id, g.id`, ws, c.id, c.last)
	if err != nil {
		return nil, nil, err
	}
	type kwRow struct {
		keyword
		groupID string
		n       [3]numbers
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (kwRow, error) {
		var k kwRow
		err := row.Scan(&k.id, &k.externalID, &k.groupID, &k.groupExternal, &k.groupName, &k.text, &k.matchType,
			&k.n[0].Spend, &k.n[0].Clicks, &k.n[0].Conv, &k.n[1].Spend, &k.n[1].Clicks, &k.n[1].Conv, &k.n[2].Spend, &k.n[2].Clicks, &k.n[2].Conv)
		return k, err
	})
	if err != nil {
		return nil, nil, err
	}
	payers := map[int]map[string][2]int{}
	for _, days := range []int{kwLookback1, kwLookback2, kwLookback3} {
		if payers[days], err = r.payersByKeyword(ctx, c, days); err != nil {
			return nil, nil, err
		}
	}
	out := map[int][]keyword{}
	groups := map[string]*group{}
	for _, k := range list {
		for i, days := range []int{kwLookback1, kwLookback2, kwLookback3} {
			kw := k.keyword
			kw.spend, kw.clicks, kw.conv = k.n[i].Spend, k.n[i].Clicks, k.n[i].Conv
			kw.payers = payers[days][strings.ToLower(kw.text)][0]
			out[days] = append(out[days], kw)
		}
		g := groups[k.groupID]
		if g == nil {
			g = &group{id: k.groupID, externalID: k.groupExternal, name: k.groupName}
			groups[k.groupID] = g
		}
		kw := k.keyword
		kw.spend, kw.clicks, kw.conv = k.n[0].Spend, k.n[0].Clicks, k.n[0].Conv
		g.keywords = append(g.keywords, kw)
	}
	// Each group's conversions over 30 days, in the unit.
	for id, g := range groups {
		var conv float64
		if c.unit == UnitPayers && c.brand != nil {
			var n int
			if err := r.Pool.QueryRow(ctx, `SELECT count(*) FROM ad_acquisitions WHERE brand_id = $1 AND ad_group_entity_id = $2 AND first_paid_at > $3::date - $4::int AND first_paid_at < $3::date + 1`,
				*c.brand, id, c.last, consolidateDays).Scan(&n); err != nil {
				return nil, nil, err
			}
			conv = float64(n)
		} else if err := r.Pool.QueryRow(ctx, `SELECT COALESCE(sum(conversions), 0)::float8 FROM ad_metrics_daily WHERE ad_entity_id = $1 AND day > $2::date - $3::int AND day <= $2::date`,
			id, c.last, consolidateDays).Scan(&conv); err != nil {
			return nil, nil, err
		}
		g.payers = conv
	}
	var gl []group
	for _, g := range groups {
		gl = append(gl, *g)
	}
	sort.Slice(gl, func(i, j int) bool { return gl[i].name < gl[j].name })
	return out, gl, nil
}

// loadAds reads the campaign's active ads with their impressions and
// conversions over 90 days.
func (r *Runner) loadAds(ctx context.Context, ws string, c campaign) ([]ad, error) {
	rows, err := r.Pool.Query(ctx, `
		SELECT x.id, x.external_id, g.id, g.external_id, g.name, x.name, COALESCE(sum(m.impressions), 0)::bigint, COALESCE(sum(m.conversions), 0)::float8,
			(SELECT count(*) FROM ad_acquisitions q WHERE q.ad_entity_id = x.id AND q.first_paid_at > $3::date - $4::int AND q.first_paid_at < $3::date + 1)::int
		FROM ad_entities x JOIN ad_entities g ON g.id = x.parent_id
		LEFT JOIN ad_metrics_daily m ON m.ad_entity_id = x.id AND m.day > $3::date - $4::int AND m.day <= $3::date
		WHERE x.workspace_id = $1 AND x.level = 'ad' AND x.status = 'active' AND g.status = 'active' AND g.parent_id = $2
		GROUP BY x.id, g.id ORDER BY g.id, x.id`, ws, c.id, c.last, kwLookback3)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (ad, error) {
		var a ad
		err := row.Scan(&a.id, &a.externalID, &a.groupID, &a.groupExternal, &a.groupName, &a.name, &a.impressions, &a.conv, &a.payers)
		return a, err
	})
}

// loadSegments joins the network's segments (read by readSignals) with
// the ledger's payers for each: by device, country and hour from the
// acquiring visit; a placement's or network's payers aren't known, so
// those keep the network's count (payers -1).
func (r *Runner) loadSegments(ctx context.Context, ws string, c campaign) ([]segment, error) {
	if c.signals == nil {
		return nil, nil
	}
	devices, countries, hours := map[string]int{}, map[string]int{}, map[string]int{}
	if c.brand != nil && c.unit == UnitPayers {
		tz := c.acctTZ
		if tz == "" {
			tz = "UTC"
		}
		rows, err := r.Pool.Query(ctx, `SELECT COALESCE(s.device, ''), COALESCE(s.country, ''), to_char(s.started_at AT TIME ZONE $4, 'HH24'), count(*)::int
			FROM ad_acquisitions a JOIN site_sessions s ON s.id = a.session_id
			WHERE a.brand_id = $1 AND a.campaign_entity_id = $2 AND a.first_paid_at > $3::date - $5::int AND a.first_paid_at < $3::date + 1
			GROUP BY 1, 2, 3`, *c.brand, c.id, c.last, tz, exclDays)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var device, country, hour string
			var n int
			if err := rows.Scan(&device, &country, &hour, &n); err != nil {
				rows.Close()
				return nil, err
			}
			devices[strings.ToLower(device)] += n
			countries[strings.ToUpper(country)] += n
			hours[hour] += n
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	var out []segment
	for _, s := range c.signals.Segments {
		seg := segment{AdSegment: s, payers: -1}
		if c.unit == UnitPayers {
			switch s.Kind {
			case connectors.ExcludeDevice:
				seg.payers = devices[strings.ToLower(s.Value)]
			case connectors.ExcludeLocation:
				if s.Label != "" {
					seg.payers = countries[strings.ToUpper(s.Label)]
				}
			case "hour":
				seg.payers = hours[s.Value]
			}
		}
		out = append(out, seg)
	}
	return out, nil
}
