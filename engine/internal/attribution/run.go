package attribution

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/conversions"
	"github.com/outflareai/outflare/engine/internal/track"
	"github.com/outflareai/outflare/engine/internal/utm"
)

// Runner classifies new visits and credits new conversions, a batch per
// tick. Each conversion is credited once, a few minutes after it happens,
// so the visit and the identify call that tie it to a person have landed.
type Runner struct {
	Pool *pgxpool.Pool
	Log  *slog.Logger
	// Settle is how long after a conversion it is credited; zero is five
	// minutes.
	Settle time.Duration
}

const (
	visitBatch      = 1000
	conversionBatch = 500
	// The newest conversions are looked at again on every tick; older ones
	// only on a backfill.
	recentWindow = 3 * 24 * time.Hour
)

// Run ticks every interval until ctx ends.
func (r *Runner) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if _, _, err := r.Tick(ctx); err != nil && ctx.Err() == nil {
			track.Fail(ctx, r.Log, track.Event{Name: "attribution.run_failed", ActorType: "system", Surface: "system"}, "internal", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Tick classifies a batch of visits and credits each workspace's new
// conversions. It returns how many of each it did.
func (r *Runner) Tick(ctx context.Context) (visits, conversions int, err error) {
	if visits, err = r.classifyBatch(ctx); err != nil {
		return visits, 0, err
	}
	if _, err := r.Pool.Exec(ctx, `INSERT INTO attribution_settings (workspace_id, lookback_days) SELECT id, $1 FROM workspaces ON CONFLICT DO NOTHING`,
		DefaultLookbackDays); err != nil {
		return visits, 0, err
	}
	rows, err := r.Pool.Query(ctx, `SELECT workspace_id::text FROM attribution_settings`)
	if err != nil {
		return visits, 0, err
	}
	wss, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return visits, 0, err
	}
	for _, ws := range wss {
		n, err := r.creditWorkspace(ctx, ws)
		conversions += n
		if err != nil && ctx.Err() == nil {
			track.Fail(ctx, r.Log, track.Event{Name: "attribution.run_failed", WorkspaceID: ws, ActorType: "system", Surface: "system"}, "internal", err)
		}
	}
	return visits, conversions, ctx.Err()
}

// visitRow is a site_sessions row as the classifier needs it.
type visitRow struct {
	id, ws                                 string
	utmSource, utmMedium, utmCampaign, ref string
	ofc, referrerHost, landingURL          string
	clickIDs                               []byte
	campID, campSlug, campChannel, campSrc *string
}

const visitCols = `s.id::text, s.workspace_id::text, s.utm_source, s.utm_medium, s.utm_campaign, s.ref, s.ofc, s.referrer_host, s.landing_url, s.click_ids,
	tc.id::text, tc.slug, tc.channel, tc.source`
const visitJoin = `LEFT JOIN tracking_campaigns tc ON tc.workspace_id = s.workspace_id AND s.ofc <> '' AND tc.code = lower(s.ofc)`

func scanVisit(row pgx.Row, v *visitRow) error {
	return row.Scan(&v.id, &v.ws, &v.utmSource, &v.utmMedium, &v.utmCampaign, &v.ref, &v.ofc, &v.referrerHost, &v.landingURL, &v.clickIDs,
		&v.campID, &v.campSlug, &v.campChannel, &v.campSrc)
}

func (v visitRow) touch(own []string) Touch {
	in := Visit{UTMSource: v.utmSource, UTMMedium: v.utmMedium, UTMCampaign: v.utmCampaign, Ref: v.ref, ReferrerHost: v.referrerHost}
	_ = json.Unmarshal(v.clickIDs, &in.ClickIDs)
	if v.campID != nil {
		in.Campaign = &utm.Campaign{ID: *v.campID, Slug: *v.campSlug, Channel: *v.campChannel, Source: *v.campSrc}
	}
	// The page they landed on is the workspace's own, whatever its brands
	// say.
	if u, err := url.Parse(v.landingURL); err == nil && u.Hostname() != "" {
		own = append(own[:len(own):len(own)], strings.TrimPrefix(strings.ToLower(u.Hostname()), "www."))
	}
	return Classify(in, own)
}

// ownHosts are the hosts of a workspace's brands.
func ownHosts(ctx context.Context, q pgx.Tx, ws string) ([]string, error) {
	rows, err := q.Query(ctx, `SELECT website, domains FROM brands WHERE workspace_id = $1`, ws)
	if err != nil {
		return nil, err
	}
	sites := []string{}
	for rows.Next() {
		var w string
		var d []string
		if err := rows.Scan(&w, &d); err != nil {
			rows.Close()
			return nil, err
		}
		sites = append(append(sites, w), d...)
	}
	rows.Close()
	return utm.Hosts(sites...), rows.Err()
}

func saveTouch(ctx context.Context, tx pgx.Tx, id string, t Touch) error {
	_, err := tx.Exec(ctx, `UPDATE site_sessions SET channel = $2, source = $3, campaign = $4, tracking_campaign_id = $5, tag_method = $6 WHERE id = $1`,
		id, t.Channel, cut(t.Source, 80), cut(t.Campaign, 80), t.CampaignID, t.Method)
	return err
}

// classifyBatch works out the channel of the oldest visits not done yet.
func (r *Runner) classifyBatch(ctx context.Context) (int, error) {
	n := 0
	err := pgx.BeginFunc(ctx, r.Pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+visitCols+` FROM site_sessions s `+visitJoin+`
			WHERE s.channel = '' ORDER BY s.started_at LIMIT $1 FOR UPDATE OF s SKIP LOCKED`, visitBatch)
		if err != nil {
			return err
		}
		var list []visitRow
		for rows.Next() {
			var v visitRow
			if err := scanVisit(rows, &v); err != nil {
				rows.Close()
				return err
			}
			list = append(list, v)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		hosts := map[string][]string{}
		for _, v := range list {
			own, ok := hosts[v.ws]
			if !ok {
				if own, err = ownHosts(ctx, tx, v.ws); err != nil {
					return err
				}
				hosts[v.ws] = own
			}
			if err := saveTouch(ctx, tx, v.id, v.touch(own)); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	return n, err
}

// conversion is a customer event to credit.
type conversion struct {
	id                  string
	recordID, anonymous *string
	brandID             *string
	name                string
	at                  time.Time
	props               []byte
}

// value is a conversion's money in minor units, and its currency: a
// payment's amount_minor, or Segment-style revenue or value in major units.
func (c conversion) value() (*int64, string) {
	var p map[string]any
	_ = json.Unmarshal(c.props, &p)
	cur, _ := p["currency"].(string)
	cur = strings.ToUpper(strings.TrimSpace(cur))
	if len(cur) != 3 {
		cur = ""
	}
	num := func(k string) (float64, bool) {
		switch x := p[k].(type) {
		case float64:
			return x, true
		case string:
			var f float64
			if err := json.Unmarshal([]byte(x), &f); err == nil {
				return f, true
			}
		}
		return 0, false
	}
	var minor int64
	if f, ok := num("amount_minor"); ok {
		minor = int64(math.Round(f))
	} else if f, ok := num("revenue"); ok {
		minor = int64(math.Round(f * 100))
	} else if f, ok := num("value"); ok {
		minor = int64(math.Round(f * 100))
	} else {
		return nil, cur
	}
	if c.name == "payment.refunded" && minor > 0 {
		minor = -minor
	}
	return &minor, cur
}

// creditWorkspace credits one workspace's conversions that have none yet:
// those of the last few days on every tick, and all of them once after its
// settings change.
func (r *Runner) creditWorkspace(ctx context.Context, ws string) (int, error) {
	settle := r.Settle
	if settle == 0 {
		settle = 5 * time.Minute
	}
	var lookback int
	var events []string
	var backfilled *time.Time
	var changed time.Time
	if err := r.Pool.QueryRow(ctx, `SELECT lookback_days, conversion_events, backfilled_at, updated_at FROM attribution_settings WHERE workspace_id = $1`, ws).
		Scan(&lookback, &events, &backfilled, &changed); err != nil {
		return 0, err
	}
	// The events that count: the settings' conversion_events, each as it
	// is, and the workspace's conversion definitions that credit channels,
	// each when its filters match.
	defs, defNames, err := conversions.ForAttribution(ctx, r.Pool, ws)
	if err != nil {
		return 0, err
	}
	names := append([]string{}, events...)
	for _, e := range defNames {
		if !slices.Contains(names, e) {
			names = append(names, e)
		}
	}
	if slices.Contains(names, "payment.succeeded") {
		names = append(names, "payment.refunded")
	}
	since := time.Now().Add(-recentWindow)
	if backfilled == nil {
		since = time.Time{} // everything, after the settings change
		// What was credited before the change is credited again.
		if _, err := r.Pool.Exec(ctx, `DELETE FROM attribution_conversions WHERE workspace_id = $1 AND credited_at < $2`, ws, changed); err != nil {
			return 0, err
		}
	}
	// Events are read in order from a cursor, so ones that don't count
	// (a definition's filters didn't match, a refund waiting for its
	// payment) never hold up the ones after them.
	total, refunds := 0, 0
	at := cursor{at: since, id: "00000000-0000-0000-0000-000000000000"}
	for {
		n, rf, fetched, next, err := r.creditBatch(ctx, ws, names, at, settle, lookback, events, defs)
		total, refunds = total+n, refunds+rf
		if err != nil {
			return total, err
		}
		if fetched < conversionBatch {
			break
		}
		at = next
	}
	if backfilled == nil {
		if _, err := r.Pool.Exec(ctx, `UPDATE attribution_settings SET backfilled_at = now() WHERE workspace_id = $1`, ws); err != nil {
			return total, err
		}
	}
	if total > 0 {
		track.Emit(ctx, track.Event{Name: "attribution.credited", WorkspaceID: ws, ActorType: "system", Surface: "job",
			Props: map[string]any{"conversions": total, "refunds": refunds, "backfill": backfilled == nil}})
	}
	return total, nil
}

// cursor is where creditBatch carries on from: the last event it read.
type cursor struct {
	at time.Time
	id string
}

// creditBatch credits up to conversionBatch conversions in one
// transaction, from the events after at. An event counts when it is one of
// plain (the settings' conversion_events) or a definition matches it. A
// refund whose payment isn't credited yet waits.
func (r *Runner) creditBatch(ctx context.Context, ws string, names []string, at cursor, settle time.Duration, lookback int,
	plain []string, defs []conversions.Definition) (n, refunds, fetched int, next cursor, err error) {
	next = at
	err = pgx.BeginFunc(ctx, r.Pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT e.id::text, e.record_id::text, e.anonymous_id, e.brand_id::text, e.name, e.occurred_at, e.properties
			FROM customer_events e
			WHERE e.workspace_id = $1 AND e.name = ANY($2) AND e.kind = 'track' AND (e.occurred_at, e.id) > ($3, $6::uuid) AND e.occurred_at <= $4
			  AND NOT EXISTS (SELECT 1 FROM attribution_conversions c WHERE c.event_id = e.id)
			ORDER BY e.occurred_at, e.id LIMIT $5`, ws, names, at.at, time.Now().Add(-settle), conversionBatch, at.id)
		if err != nil {
			return err
		}
		var list []conversion
		for rows.Next() {
			var c conversion
			if err := rows.Scan(&c.id, &c.recordID, &c.anonymous, &c.brandID, &c.name, &c.at, &c.props); err != nil {
				rows.Close()
				return err
			}
			list = append(list, c)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		fetched = len(list)
		if fetched > 0 {
			next = cursor{at: list[fetched-1].at, id: list[fetched-1].id}
		}
		var own []string
		var models []CreditModel
		if len(list) > 0 {
			if own, err = ownHosts(ctx, tx, ws); err != nil {
				return err
			}
			if models, err = creditModels(ctx, tx, ws, lookback); err != nil {
				return err
			}
		}
		for _, c := range list {
			if c.name == "payment.refunded" {
				done, err := creditRefund(ctx, tx, ws, c)
				if err != nil {
					return err
				}
				if done {
					n++
					refunds++
					continue
				}
				// Its payment is waiting to be credited: try again later.
				var waiting bool
				if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM customer_events WHERE workspace_id = $1 AND name = 'payment.succeeded'
					AND properties->>'payment_id' = $2::jsonb->>'payment_id')`, ws, c.props).Scan(&waiting); err != nil {
					return err
				}
				if waiting {
					continue
				}
			}
			var def *conversions.Definition
			if c.name != "payment.refunded" {
				var props map[string]any
				_ = json.Unmarshal(c.props, &props)
				if def = conversions.For(defs, c.name, props); def == nil && !slices.Contains(plain, c.name) {
					continue // a definition's event that its filters leave out
				}
			}
			if err := credit(ctx, tx, ws, c, own, models, def); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	return n, refunds, fetched, next, err
}

// creditRefund gives a refund the same credits as the payment it refunds,
// so revenue per channel is net of refunds. It reports false when that
// payment has no credits yet.
func creditRefund(ctx context.Context, tx pgx.Tx, ws string, c conversion) (bool, error) {
	var orig string
	err := tx.QueryRow(ctx, `SELECT a.event_id::text FROM attribution_conversions a JOIN customer_events o ON o.id = a.event_id
		WHERE a.workspace_id = $1 AND o.name = 'payment.succeeded' AND o.properties->>'payment_id' = $2::jsonb->>'payment_id'
		  AND $2::jsonb->>'payment_id' IS NOT NULL LIMIT 1`, ws, c.props).Scan(&orig)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	value, cur := c.value()
	if _, err := tx.Exec(ctx, `INSERT INTO attribution_conversions (event_id, workspace_id, record_id, brand_id, name, occurred_at, value_minor, currency, touches, matched_by)
		SELECT $1, $2, $3, $4, $5, $6, $7, CASE WHEN $8 = '' THEN a.currency ELSE $8 END, a.touches, a.matched_by
		FROM attribution_conversions a WHERE a.event_id = $9`,
		c.id, ws, c.recordID, c.brandID, c.name, c.at, value, cur, orig); err != nil {
		return false, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO attribution_credits (event_id, workspace_id, model, position, session_id, touch_at, channel, source, campaign,
			tracking_campaign_id, tag_method, weight)
		SELECT $1, workspace_id, model, position, session_id, touch_at, channel, source, campaign, tracking_campaign_id, tag_method, weight
		FROM attribution_credits WHERE event_id = $2`, c.id, orig)
	return err == nil, err
}

// credit matches a conversion to the visits before it and stores its
// credits under every model.
func credit(ctx context.Context, tx pgx.Tx, ws string, c conversion, own []string, models []CreditModel, def *conversions.Definition) error {
	// Visits as far back as the widest model looks; each model then keeps
	// the ones in its own windows.
	lookback := 0
	for _, m := range models {
		lookback = max(lookback, m.Spec.window(0))
	}
	rows, err := tx.Query(ctx, `SELECT `+visitCols+`, s.started_at, s.channel, s.source, s.campaign, s.tracking_campaign_id::text, s.tag_method,
			COALESCE(s.record_id = $2, false)
		FROM site_sessions s `+visitJoin+`
		WHERE s.workspace_id = $1 AND (s.record_id = $2 OR ($3::text IS NOT NULL AND s.anonymous_id = $3))
		  AND s.started_at <= $4 AND s.started_at > $4 - make_interval(days => $5)
		ORDER BY s.started_at DESC LIMIT $6`, ws, c.recordID, c.anonymous, c.at, lookback, MaxTouches)
	if err != nil {
		return err
	}
	type visit struct {
		row   visitRow
		at    time.Time
		touch Touch
		byRec bool
	}
	var visits []visit
	for rows.Next() {
		var v visit
		var campaignID *string
		err := rows.Scan(&v.row.id, &v.row.ws, &v.row.utmSource, &v.row.utmMedium, &v.row.utmCampaign, &v.row.ref, &v.row.ofc,
			&v.row.referrerHost, &v.row.landingURL, &v.row.clickIDs, &v.row.campID, &v.row.campSlug, &v.row.campChannel, &v.row.campSrc,
			&v.at, &v.touch.Channel, &v.touch.Source, &v.touch.Campaign, &campaignID, &v.touch.Method, &v.byRec)
		if err != nil {
			rows.Close()
			return err
		}
		v.touch.CampaignID = campaignID
		visits = append(visits, v)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	matched, touches := "none", 0
	points := make([]Point, len(visits))
	for i := range visits {
		v := &visits[i]
		if v.touch.Channel == "" {
			v.touch = v.row.touch(own)
			if err := saveTouch(ctx, tx, v.row.id, v.touch); err != nil {
				return err
			}
		}
		points[i] = Point{At: v.at, Direct: v.touch.Channel == Direct, Channel: v.touch.Channel}
		// The conversion's touches are those in the workspace's own
		// window (the first model's), whatever wider ones others look at.
		if !models[0].Spec.counts(points[i], c.at) {
			continue
		}
		touches++
		if v.byRec {
			matched = "record"
		} else if matched == "none" {
			matched = "anonymous"
		}
	}
	// A definition says what the conversion is worth; an event that only
	// the settings name keeps its own value.
	value, cur := c.value()
	var defID *string
	if def != nil {
		var props map[string]any
		_ = json.Unmarshal(c.props, &props)
		value, cur = def.Value(props)
		defID = &def.ID
	}
	if _, err := tx.Exec(ctx, `INSERT INTO attribution_conversions (event_id, workspace_id, record_id, brand_id, name, occurred_at, value_minor, currency, touches, matched_by, definition_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`, c.id, ws, c.recordID, c.brandID, c.name, c.at, value, cur, touches, matched, defID); err != nil {
		return err
	}
	batch := &pgx.Batch{}
	const ins = `INSERT INTO attribution_credits (event_id, workspace_id, model, position, session_id, touch_at, channel, source, campaign,
		tracking_campaign_id, tag_method, weight) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`
	for _, model := range models {
		m := model.ID
		w := model.Spec.Weights(points, c.at)
		if w == nil {
			batch.Queue(ins, c.id, ws, m, 0, nil, nil, Direct, "(direct)", "", nil, "none", 1)
			continue
		}
		pos := 0
		for i, x := range w {
			if x <= 0 {
				continue
			}
			x = math.Max(math.Round(x*1e6)/1e6, 0.000001)
			v := visits[i]
			batch.Queue(ins, c.id, ws, m, pos, v.row.id, v.at, v.touch.Channel, cut(v.touch.Source, 80), cut(v.touch.Campaign, 80),
				v.touch.CampaignID, v.touch.Method, x)
			pos++
		}
	}
	return tx.SendBatch(ctx, batch).Close()
}
