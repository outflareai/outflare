package paidads

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Filling in campaigns later. A Google click that landed with only a gclid
// is tied to its campaign through the click harvest (ad_clicks), and Google
// publishes a day's clicks the next day, so a sign-up or payment is usually
// read before its click is known: the acquisition and the revenue row are
// written with the campaign unknown. Each ledger run, once the click
// harvest or the brand's campaigns have changed since the last pass, the
// brand's rows still unknown and young enough for the network to report
// (ClickKeepDays) are looked up again with the same rules, and filled in
// when the campaign is now known.

const (
	// ClickKeepDays is how long a network keeps a click to look up (Google
	// keeps 90 days), so how long an unknown campaign is retried.
	ClickKeepDays = 90
	// fillBatch bounds the rows one pass looks at per table, newest first.
	fillBatch = 500
)

// fillMark is what the last pass for a brand saw, so a pass runs only when
// something that could name a campaign has changed since.
func (l *Ledger) fillMark(brand string) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.filled[brand]
}

func (l *Ledger) setFillMark(brand, mark string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.filled == nil {
		l.filled = map[string]string{}
	}
	l.filled[brand] = mark
}

// fill looks again at the brand's acquisitions and revenue rows whose
// campaign was unknown when they were written.
func (r *run) fill(ctx context.Context) error {
	st := r.brands[r.brand]
	if st == nil {
		return nil
	}
	// The click harvest's watermarks and when the campaigns last synced: a
	// pass with both as they were last time would find nothing new, since
	// every row written since was resolved against the same data.
	var mark string
	if err := r.l.Pool.QueryRow(ctx, `SELECT COALESCE((SELECT string_agg(s.integration_id::text || ':' || COALESCE(s.last_clicks_day::text, ''), ',' ORDER BY s.integration_id)
			FROM ad_syncs s WHERE s.workspace_id = $1), '')
		|| '|' || COALESCE((SELECT max(e.updated_at)::text FROM ad_entities e WHERE e.workspace_id = $1 AND e.level = 'campaign'), '')`, r.ws).Scan(&mark); err != nil {
		return err
	}
	if r.l.fillMark(r.brand) == mark {
		return nil
	}
	acqs, err := r.fillAcquisitions(ctx)
	if err != nil {
		metrics.PaidAdsLedgerEvent("fill_acquisition", "error")
		return err
	}
	revs, err := r.fillRevenue(ctx, st)
	if err != nil {
		metrics.PaidAdsLedgerEvent("fill_revenue", "error")
		return err
	}
	r.l.setFillMark(r.brand, mark)
	r.res.Filled += acqs + revs
	if acqs+revs > 0 {
		track.Emit(ctx, track.Event{Name: "paidads.campaigns_filled", WorkspaceID: r.ws, ActorType: "system", Surface: "job",
			Props: map[string]any{"brand_id": r.brand, "acquisitions": acqs, "revenue": revs}})
	}
	return nil
}

// unknownAcquisition is an acquisition written without its campaign.
type unknownAcquisition struct {
	id, connector, kind, clickID, campaignExt, adGroupExt, adExt, keyword, matchedBy, utmCampaign string
	record                                                                                        string
	anon                                                                                          *string
	clickedAt                                                                                     time.Time
}

// fillAcquisitions looks up the brand's acquisitions with no campaign that
// now have a way to one: the click harvested since, an earlier visit by the
// same person with the same click id that named a campaign the brand has,
// or a campaign, ad group or ad they name (by id, or the campaign by its
// UTM name or id) synced since. Rows with neither are left alone, so they never crowd out the rest.
func (r *run) fillAcquisitions(ctx context.Context) (int, error) {
	since := r.l.now().AddDate(0, 0, -ClickKeepDays)
	rows, err := r.l.Pool.Query(ctx, `
		SELECT q.id::text, q.connector, q.click_id_kind, q.click_id, q.campaign_external_id, q.ad_group_external_id, q.ad_external_id, q.keyword, q.matched_by, COALESCE(s.utm_campaign, ''),
			q.record_id::text, s.anonymous_id, q.clicked_at
		FROM ad_acquisitions q LEFT JOIN site_sessions s ON s.id = q.session_id AND s.workspace_id = q.workspace_id
		WHERE q.workspace_id = $1 AND q.brand_id = $2 AND q.campaign_entity_id IS NULL AND q.clicked_at > $3
		  AND ((q.click_id <> '' AND q.campaign_external_id = '' AND EXISTS (
		          SELECT 1 FROM ad_accounts a JOIN ad_clicks k ON k.ad_account_id = a.id AND k.click_id = q.click_id
		          WHERE a.workspace_id = $1 AND k.workspace_id = $1 AND (a.brand_id IS NULL OR a.brand_id = $2)))
		    OR (q.click_id <> '' AND q.campaign_external_id = '' AND q.ad_group_external_id = '' AND q.ad_external_id = '' AND EXISTS (
		          SELECT 1 FROM site_sessions l JOIN ad_entities e ON e.workspace_id = $1 AND e.level = 'campaign' AND e.external_id = l.ad->>'campaign'
		          JOIN ad_accounts a ON a.id = e.ad_account_id
		          WHERE l.workspace_id = $1 AND (l.record_id = q.record_id OR (s.anonymous_id IS NOT NULL AND l.anonymous_id = s.anonymous_id))
		            AND l.started_at <= q.clicked_at AND l.started_at > q.clicked_at - make_interval(days => $5)
		            AND (l.brand_id IS NULL OR l.brand_id = $2) AND l.click_ids->>q.click_id_kind = q.click_id
		            AND COALESCE(e.brand_id, a.brand_id) = $2))
		    OR EXISTS (
		          SELECT 1 FROM ad_entities e JOIN ad_accounts a ON a.id = e.ad_account_id
		          WHERE e.workspace_id = $1 AND COALESCE(e.brand_id, a.brand_id) = $2
		            AND ((e.external_id <> '' AND e.external_id IN (q.campaign_external_id, q.ad_group_external_id, q.ad_external_id))
		              OR (e.level = 'campaign' AND COALESCE(s.utm_campaign, '') <> '' AND (lower(e.name) = lower(s.utm_campaign) OR e.external_id = s.utm_campaign)))))
		ORDER BY q.clicked_at DESC LIMIT $4`, r.ws, r.brand, since, fillBatch, ClickKeepDays)
	if err != nil {
		return 0, err
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (unknownAcquisition, error) {
		var x unknownAcquisition
		return x, row.Scan(&x.id, &x.connector, &x.kind, &x.clickID, &x.campaignExt, &x.adGroupExt, &x.adExt, &x.keyword, &x.matchedBy, &x.utmCampaign, &x.record, &x.anon, &x.clickedAt)
	})
	if err != nil {
		return 0, err
	}
	n := 0
	for _, x := range list {
		c := click{at: x.clickedAt, connector: x.connector, kind: x.kind, id: x.clickID, campaignExt: x.campaignExt, adGroupExt: x.adGroupExt, adExt: x.adExt, keyword: x.keyword, matchedBy: x.matchedBy}
		if c.id != "" && c.campaignExt == "" {
			if err := harvested(ctx, r.l.Pool, r.ws, r.brand, &c); err != nil {
				return n, err
			}
		}
		if c.id != "" && c.campaignExt == "" && c.adGroupExt == "" && c.adExt == "" {
			if err := landed(ctx, r.l.Pool, r.ws, r.brand, x.record, x.anon, &c); err != nil {
				return n, err
			}
		}
		if err := resolve(ctx, r.l.Pool, r.ws, r.brand, &c, x.utmCampaign); err != nil {
			return n, err
		}
		if c.campaignID == nil {
			continue
		}
		tag, err := r.l.Pool.Exec(ctx, `UPDATE ad_acquisitions SET connector = $4, ad_account_id = COALESCE($5, ad_account_id),
				campaign_entity_id = $6, ad_group_entity_id = COALESCE($7, ad_group_entity_id), ad_entity_id = COALESCE($8, ad_entity_id),
				campaign_external_id = $9, ad_group_external_id = $10, ad_external_id = $11, keyword = $12, matched_by = $13, updated_at = now()
			WHERE id = $1 AND workspace_id = $2 AND brand_id = $3 AND campaign_entity_id IS NULL`,
			x.id, r.ws, r.brand, c.connector, c.accountID, c.campaignID, c.adGroupID, c.adID,
			cut(c.campaignExt, 100), cut(c.adGroupExt, 100), cut(c.adExt, 100), cut(c.keyword, 200), c.matchedBy)
		if err != nil {
			return n, err
		}
		if tag.RowsAffected() > 0 {
			n++
			metrics.PaidAdsLedgerEvent("fill_acquisition", "filled")
		}
	}
	return n, nil
}

// unknownTouch is a payment's revenue row written without the campaign
// whose paid click was last before it.
type unknownTouch struct {
	eventID, record string
	anon            *string
	at              time.Time
}

// fillRevenue recomputes the touch campaign of the brand's payments that
// had none but did have a paid click within the window before them, with
// the rule the payment was read with; then gives each refund of a payment
// filled in the payment's campaign, as a refund always takes its payment's.
func (r *run) fillRevenue(ctx context.Context, st *brandState) (int, error) {
	window := st.econ.AcquisitionWindowDays
	if window <= 0 {
		window = DefaultWindowDays
	}
	since := r.l.now().AddDate(0, 0, -(ClickKeepDays + window))
	rows, err := r.l.Pool.Query(ctx, `
		SELECT r.event_id::text, r.record_id::text, e.anonymous_id, r.occurred_at
		FROM ad_revenue r JOIN customer_events e ON e.id = r.event_id
		WHERE r.workspace_id = $1 AND r.brand_id = $2 AND r.touch_campaign_entity_id IS NULL AND r.occurred_at > $3
		  AND e.workspace_id = $1 AND e.name = 'payment.succeeded'
		  AND EXISTS (SELECT 1 FROM site_sessions s
		      WHERE s.workspace_id = $1
		        AND (s.record_id = r.record_id OR (e.anonymous_id IS NOT NULL AND s.anonymous_id = e.anonymous_id)
		             OR s.anonymous_id IN (SELECT v.anonymous_id FROM site_visitors v WHERE v.workspace_id = $1 AND v.record_id = r.record_id))
		        AND s.started_at <= r.occurred_at AND s.started_at > r.occurred_at - make_interval(days => $4)
		        AND (s.brand_id IS NULL OR s.brand_id = $2)
		        AND (s.channel = 'paid_ads' OR s.ad <> '{}'::jsonb OR EXISTS (SELECT 1 FROM jsonb_object_keys(s.click_ids) k WHERE k = ANY($5::text[]))))
		ORDER BY r.occurred_at DESC LIMIT $6`, r.ws, r.brand, since, window, paidParams, fillBatch)
	if err != nil {
		return 0, err
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (unknownTouch, error) {
		var x unknownTouch
		return x, row.Scan(&x.eventID, &x.record, &x.anon, &x.at)
	})
	if err != nil {
		return 0, err
	}
	n := 0
	for _, x := range list {
		c, err := locate(ctx, r.l.Pool, r.ws, r.brand, x.record, x.anon, x.at, window)
		if err != nil {
			return n, err
		}
		if c == nil || c.campaignID == nil {
			continue
		}
		tag, err := r.l.Pool.Exec(ctx, `UPDATE ad_revenue SET touch_campaign_entity_id = $4, touch_connector = $5
			WHERE event_id = $1 AND workspace_id = $2 AND brand_id = $3 AND touch_campaign_entity_id IS NULL`,
			x.eventID, r.ws, r.brand, c.campaignID, c.connector)
		if err != nil {
			return n, err
		}
		if tag.RowsAffected() > 0 {
			n++
			metrics.PaidAdsLedgerEvent("fill_revenue", "filled")
		}
	}
	tag, err := r.l.Pool.Exec(ctx, `
		UPDATE ad_revenue f SET touch_campaign_entity_id = p.touch_campaign_entity_id, touch_connector = p.touch_connector
		FROM customer_events fe, LATERAL (
			SELECT x.touch_campaign_entity_id, x.touch_connector FROM ad_revenue x JOIN customer_events o ON o.id = x.event_id
			WHERE x.workspace_id = $1 AND x.brand_id = $2 AND o.workspace_id = $1 AND o.name = 'payment.succeeded'
			  AND o.properties->>'payment_id' = fe.properties->>'payment_id'
			ORDER BY o.occurred_at LIMIT 1) p
		WHERE fe.id = f.event_id AND fe.workspace_id = $1 AND fe.name = 'payment.refunded'
		  AND f.workspace_id = $1 AND f.brand_id = $2 AND f.touch_campaign_entity_id IS NULL AND f.occurred_at > $3
		  AND p.touch_campaign_entity_id IS NOT NULL`, r.ws, r.brand, since)
	if err != nil {
		return n, err
	}
	for range tag.RowsAffected() {
		n++
		metrics.PaidAdsLedgerEvent("fill_revenue", "filled")
	}
	return n, nil
}
