package adsync

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/metrics"
)

// Click harvest (plans/paid-ads/plan.md, sync and alerts): a network that
// reports its clicks one by one (Google's click_view) has each click's id
// and the campaign, ad group, ad, keyword and device it came from. Once a
// day the sync reads yesterday's clicks for every account into ad_clicks,
// and on a connection's first sync the last 90 days, one day per query,
// so a landing page that carried only a gclid can be tied to its ad group,
// ad and keyword later (the ledger). Google keeps 90 days, so the harvest
// never asks for more; ad_syncs.last_clicks_day is the watermark, and a
// refused read (quota, an outage) stops the run where it is and the next
// run carries on from there.

const (
	// clickBackfillDays is how far back a first harvest reads.
	clickBackfillDays = 90
	// maxClickDays caps the days one run reads, so a long gap is caught up
	// over a few runs instead of one long one.
	maxClickDays = 95
)

// harvestClicks reads the days after last (or the last 90 days) up to
// yesterday for every syncing account, and returns the last day every
// account has, how many clicks it stored and the first error. The day
// returned is nil when nothing new was read.
func (s *Syncer) harvestClicks(ctx context.Context, o *connectors.Opened, r connectors.AdClickReader, accounts []account, last *time.Time) (*time.Time, int, error) {
	var on []account
	for _, a := range accounts {
		if a.syncOn {
			on = append(on, a)
		}
	}
	if len(on) == 0 {
		return nil, 0, nil
	}
	// Yesterday in the account that is furthest behind the clock, so a
	// day is read only once every account has finished it.
	now := s.now()
	yesterday := today(now, on[0].timeZone)
	for _, a := range on[1:] {
		if d := today(now, a.timeZone); d.Before(yesterday) {
			yesterday = d
		}
	}
	yesterday = yesterday.AddDate(0, 0, -1)
	oldest := yesterday.AddDate(0, 0, -(clickBackfillDays - 1))
	start := oldest
	if last != nil {
		start = time.Date(last.Year(), last.Month(), last.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, 1)
		if start.Before(oldest) {
			start = oldest
		}
	}
	if yesterday.Sub(start) >= maxClickDays*24*time.Hour {
		yesterday = start.AddDate(0, 0, maxClickDays-1)
	}
	connector := o.Connector.Definition().ID
	var done *time.Time
	stored := 0
	for day := start; !day.After(yesterday); day = day.AddDate(0, 0, 1) {
		for _, a := range on {
			clicks, err := r.AdClicks(ctx, o.Client, o.Creds, a.ad(), day.Format(time.DateOnly))
			if err != nil {
				metrics.AdClickHarvestFailed(connector, "connector_"+connectors.KindOf(err))
				return done, stored, err
			}
			n, err := s.saveClicks(ctx, o.WorkspaceID, a, day, clicks)
			if err != nil {
				return done, stored, dbError{err}
			}
			stored += n
		}
		d := day
		done = &d
	}
	metrics.AdClicksHarvested(connector, stored)
	return done, stored, nil
}

// saveClicks stores one account's clicks for a day; a click already stored
// takes the newer ids.
func (s *Syncer) saveClicks(ctx context.Context, ws string, a account, day time.Time, clicks []connectors.AdClick) (int, error) {
	if len(clicks) == 0 {
		return 0, nil
	}
	n := 0
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		b := &pgx.Batch{}
		for _, k := range clicks {
			if k.ClickID == "" {
				continue
			}
			kind := k.Kind
			if kind == "" {
				kind = "gclid"
			}
			b.Queue(`INSERT INTO ad_clicks (workspace_id, brand_id, ad_account_id, click_id, kind, day, campaign_external_id, ad_group_external_id, ad_external_id, keyword, match_type, device)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
				ON CONFLICT (ad_account_id, click_id) DO UPDATE SET day = EXCLUDED.day, campaign_external_id = EXCLUDED.campaign_external_id,
					ad_group_external_id = EXCLUDED.ad_group_external_id, ad_external_id = EXCLUDED.ad_external_id, keyword = EXCLUDED.keyword,
					match_type = EXCLUDED.match_type, device = EXCLUDED.device, brand_id = COALESCE(ad_clicks.brand_id, EXCLUDED.brand_id)`,
				ws, a.brandID, a.id, trim(k.ClickID, 200), trim(kind, 20), day, trim(k.CampaignID, 100), trim(k.AdGroupID, 100), trim(k.AdID, 100),
				trim(k.Keyword, 300), trim(k.MatchType, 30), trim(k.Device, 30))
			n++
		}
		res := tx.SendBatch(ctx, b)
		defer res.Close()
		for i := 0; i < n; i++ {
			if _, err := res.Exec(); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return n, nil
}
