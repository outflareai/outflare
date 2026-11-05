// Package adsync copies what each connected ad platform reports into
// Postgres: the ad accounts a sign-in can see, their campaigns, and each
// campaign's spend, impressions, clicks and platform-reported conversions
// per day. Every campaign-day with spend is also a row in the spend ledger
// (spend_entries, category ads, channel paid_ads), so costs and outcomes
// include ads without anyone typing them in.
//
// An account's first run stores the last 90 days. Later runs re-read the
// last 7 days every few hours and the last 30 once a day, because the
// platforms keep revising recent days (late conversions, invalid clicks).
package adsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/adsuggest"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/retry"
	"github.com/outflareai/outflare/engine/internal/track"
)

const (
	JobSync      = "ads.sync"
	interval     = 6 * time.Hour
	fullInterval = 24 * time.Hour
	backfillDays = 90
	fullDays     = 30
	recentDays   = 7
	// MaxAccounts is how many of a sign-in's ad accounts sync without being
	// switched on one by one; the rest are listed but off.
	MaxAccounts = 20
	// Channel is the spend ledger's channel for ad spend.
	Channel = "paid_ads"
)

// Syncer runs ad syncs as jobs.
type Syncer struct {
	Pool  *pgxpool.Pool
	Store *connectors.Store
	Log   *slog.Logger
	// Now is the clock; tests set it.
	Now func() time.Time
}

type payload struct {
	Integration string `json:"integration_id"`
}

// Register adds the job handler.
func (s *Syncer) Register(q *jobs.Queue) {
	q.Handle(JobSync, s.job)
	q.Handle(JobChange, s.changeJob)
	q.Handle(JobDraft, s.draftJob)
	q.Handle(JobTracking, s.trackingJob)
	q.Handle(JobTest, s.testJob)
}

func (s *Syncer) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Connectors lists the ad connectors this build has.
func Connectors(r *connectors.Registry) []string {
	var out []string
	for _, d := range r.All() {
		if c, ok := r.Get(d.ID); ok && d.Status == connectors.Available {
			if _, ok := c.(connectors.AdsReader); ok {
				out = append(out, d.ID)
			}
		}
	}
	return out
}

// Run schedules syncs until ctx ends.
func (s *Syncer) Run(ctx context.Context) {
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for {
		if err := s.QueueDue(ctx); err != nil && ctx.Err() == nil {
			s.Log.Error("ads: schedule syncs", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// QueueDue queues a sync of each connection that is due, or whose last
// run failed and is due a retry (retry.After).
func (s *Syncer) QueueDue(ctx context.Context) error {
	// A connection is due after the interval, or sooner while its last
	// runs failed (retry.After), so a failed sync is tried again soon.
	rows, err := s.Pool.Query(ctx, `
		SELECT i.id, i.workspace_id, COALESCE(a.failures, 0), COALESCE(extract(epoch FROM a.last_run_at)::bigint, 0) FROM integrations i
		LEFT JOIN ad_syncs a ON a.integration_id = i.id
		WHERE i.connector = ANY($1) AND i.status = 'connected'
		  AND NOT COALESCE((i.config->>'sync_paused')::boolean, false)
		  AND (COALESCE(a.last_run_at, '-infinity') < now() - $2::interval
		   OR (a.failures > 0 AND a.last_run_at < now() - `+retry.SQL("a.failures")+`))`,
		Connectors(s.Store.Registry), fmt.Sprintf("%d seconds", int(interval.Seconds())))
	if err != nil {
		return err
	}
	type due struct {
		id, ws   string
		failures int
		lastRun  int64
	}
	var list []due
	for rows.Next() {
		var d due
		if err := rows.Scan(&d.id, &d.ws, &d.failures, &d.lastRun); err != nil {
			rows.Close()
			return err
		}
		list = append(list, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	slot := s.now().Truncate(interval).Unix()
	for _, d := range list {
		key := fmt.Sprintf("%s:%d", d.id, slot)
		if d.failures > 0 {
			// Each retry after a failed run is a job of its own.
			key = fmt.Sprintf("%s:retry:%d", d.id, d.lastRun)
		}
		if _, err := Enqueue(ctx, s.Pool, d.ws, d.id, key); err != nil {
			return err
		}
	}
	return nil
}

// Enqueue asks for a sync of one connection. Key de-duplicates; "" is a
// sync someone asked for now.
func Enqueue(ctx context.Context, q jobs.Querier, ws, integrationID, key string) (int64, error) {
	if key == "" {
		key = fmt.Sprintf("%s:now:%d", integrationID, time.Now().Unix()/30)
	}
	return jobs.Enqueue(ctx, q, jobs.NewJob{Kind: JobSync, WorkspaceID: &ws, Payload: payload{Integration: integrationID},
		IdempotencyKey: "ads:" + key, MaxAttempts: 3})
}

func (s *Syncer) job(ctx context.Context, job jobs.Job) error {
	var p payload
	if err := json.Unmarshal(job.Payload, &p); err != nil || job.WorkspaceID == nil {
		return nil
	}
	res, err := s.SyncOnce(ctx, *job.WorkspaceID, p.Integration)
	if errors.Is(err, connectors.ErrNotFound) {
		return nil
	}
	ev := track.Event{Name: "ads.synced", WorkspaceID: *job.WorkspaceID, ActorType: "system", Surface: "job",
		Props: map[string]any{"connector": res.Connector, "accounts": res.Accounts, "campaigns": res.Campaigns, "ad_groups": res.AdGroups, "ads": res.Ads, "keywords": res.Keywords, "search_terms": res.SearchTerms, "rows": res.Rows}}
	var db dbError
	switch {
	case err == nil:
		track.Emit(ctx, ev)
	case errors.As(err, &db):
		track.Fail(ctx, s.Log, ev, "internal", err)
		return err
	default:
		kind := connectors.KindOf(err)
		ev.Outcome = track.Failure
		if kind == connectors.ErrUnavailable {
			ev.Outcome = track.Error
		}
		track.Fail(ctx, s.Log, ev, "connector_"+kind, err)
		if kind == connectors.ErrUnavailable {
			return err // worth retrying
		}
	}
	// A refused token or a missing developer token won't fix itself; the
	// error shows on the connection's sync status.
	return nil
}

// Result is what one run stored.
type Result struct {
	Connector string `json:"connector"`
	Accounts  int    `json:"accounts"`
	Campaigns int    `json:"campaigns"`
	// AdGroups and Ads are how many the platforms listed, where they
	// read below campaigns (Meta and Google).
	AdGroups int `json:"ad_groups"`
	Ads      int `json:"ads"`
	// Keywords and SearchTerms are keyword-days and search-days stored
	// (Google).
	Keywords    int `json:"keywords"`
	SearchTerms int `json:"search_terms"`
	// Rows are campaign-days stored.
	Rows int `json:"rows"`
}

// dbError marks a database failure, which is retried, from a platform's.
type dbError struct{ error }

func (e dbError) Unwrap() error { return e.error }

// SyncOnce syncs one connection now.
func (s *Syncer) SyncOnce(ctx context.Context, ws, integrationID string) (Result, error) {
	var res Result
	o, err := s.Store.Open(ctx, s.Pool, ws, integrationID)
	if err != nil {
		if connectors.KindOf(err) == connectors.ErrAuth {
			s.finish(ctx, ws, integrationID, false, res, err)
		}
		return res, err
	}
	res.Connector = o.Connector.Definition().ID
	reader, ok := o.Connector.(connectors.AdsReader)
	if !ok {
		return res, connectors.Errf(connectors.ErrInvalid, "%s is not an ad platform connection", o.Name)
	}
	if missing := connectors.MissingSetup(o.Connector, o.Creds); len(missing) > 0 {
		err := connectors.Errf(connectors.ErrAuth, "%s needs your %s before it can sync: add it on the connection",
			o.Connector.Definition().Name, strings.ToLower(missing[0].Label))
		s.finish(ctx, ws, integrationID, false, res, err)
		return res, err
	}
	found, err := reader.AdAccounts(ctx, o.Client, o.Creds)
	if err != nil {
		s.finish(ctx, ws, integrationID, false, res, err)
		return res, err
	}
	if err := s.saveAccounts(ctx, ws, integrationID, res.Connector, found); err != nil {
		return res, dbError{err}
	}
	accounts, err := s.loadAccounts(ctx, ws, integrationID)
	if err != nil {
		return res, dbError{err}
	}
	var last struct{ full *time.Time }
	_ = s.Pool.QueryRow(ctx, `SELECT last_full_at FROM ad_syncs WHERE integration_id = $1`, integrationID).Scan(&last.full)
	full := last.full == nil || s.now().Sub(*last.full) >= fullInterval
	var firstErr error
	for _, a := range accounts {
		if !a.syncOn {
			continue
		}
		res.Accounts++
		err := s.syncAccount(ctx, o, reader, a, full, &res)
		if err == nil {
			continue
		}
		var db dbError
		if errors.As(err, &db) {
			return res, err
		}
		if firstErr == nil {
			firstErr = err
		}
		if connectors.KindOf(err) == connectors.ErrAuth {
			_ = s.Store.Checked(ctx, s.Pool, ws, integrationID, nil, err)
			break
		}
	}
	s.finish(ctx, ws, integrationID, full && firstErr == nil, res, firstErr)
	return res, firstErr
}

func (s *Syncer) finish(ctx context.Context, ws, integrationID string, full bool, res Result, runErr error) {
	var msg, code *string
	outcome := "success"
	if runErr != nil {
		m, c := runErr.Error(), "connector_"+connectors.KindOf(runErr)
		var db dbError
		if errors.As(runErr, &db) {
			c = "internal"
		}
		msg, code, outcome = &m, &c, c
	}
	metrics.SyncRun("ads", outcome)
	// Fresh numbers: judge measured changes, suggest new ones and read
	// running tests.
	if runErr == nil {
		if err := adsuggest.Enqueue(ctx, s.Pool, ws, s.now()); err != nil {
			track.Fail(ctx, s.Log, track.Event{Name: "ad_suggestions.failed", WorkspaceID: ws, ActorType: "system", Surface: "job", Outcome: track.Error}, "internal", err)
		}
		if err := s.queueTestChecks(ctx, ws, integrationID); err != nil {
			track.Fail(ctx, s.Log, track.Event{Name: "ad_test.failed", WorkspaceID: ws, ActorType: "system", Surface: "job", Outcome: track.Error,
				Props: map[string]any{"step": TestCheck}}, "internal", err)
		}
	}
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO ad_syncs (integration_id, workspace_id, last_run_at, last_ok_at, last_full_at, last_error, last_rows, failures, last_error_code)
		VALUES ($1, $2, $3::timestamptz, CASE WHEN $4::text IS NULL THEN $3::timestamptz END, CASE WHEN $5::boolean THEN $3::timestamptz END, $4::text, $6::integer,
			CASE WHEN $4::text IS NULL THEN 0 ELSE 1 END, $7::text)
		ON CONFLICT (integration_id) DO UPDATE SET last_run_at = EXCLUDED.last_run_at,
			last_ok_at = COALESCE(EXCLUDED.last_ok_at, ad_syncs.last_ok_at),
			last_full_at = COALESCE(EXCLUDED.last_full_at, ad_syncs.last_full_at),
			last_error = EXCLUDED.last_error, last_rows = EXCLUDED.last_rows, last_error_code = EXCLUDED.last_error_code,
			failures = CASE WHEN EXCLUDED.last_error IS NULL THEN 0 ELSE ad_syncs.failures + 1 END`,
		integrationID, ws, s.now(), msg, full, res.Rows, code)
	if err != nil && s.Log != nil {
		s.Log.Error("ads: record sync", "integration", integrationID, "err", err)
	}
}

// connectionBrand is the one brand a connection serves, which its ad
// accounts take until someone sets theirs: the brand's own connection, or
// a workspace connection shared with only one brand. Otherwise null, and
// the accounts stay workspace-wide.
const connectionBrand = `COALESCE(i.brand_id, CASE WHEN cardinality(i.brand_ids) = 1 THEN i.brand_ids[1] END)`

// saveAccounts records the accounts a sign-in sees. New accounts sync
// until MaxAccounts are on; accounts it no longer sees are marked gone.
func (s *Syncer) saveAccounts(ctx context.Context, ws, integrationID, connector string, found []connectors.AdAccount) error {
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		ids := make([]string, 0, len(found))
		for _, a := range found {
			if a.ID == "" {
				continue
			}
			ids = append(ids, a.ID)
			meta, _ := json.Marshal(a.Meta)
			if a.Meta == nil {
				meta = []byte("{}")
			}
			cur := strings.ToUpper(strings.TrimSpace(a.Currency))
			if len(cur) != 3 {
				cur = "USD"
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO ad_accounts (workspace_id, integration_id, connector, external_id, name, currency, time_zone, meta, sync_on, brand_id)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8,
					(SELECT count(*) FROM ad_accounts WHERE integration_id = $2 AND sync_on) < $9,
					(SELECT `+connectionBrand+` FROM integrations i WHERE i.id = $2))
				ON CONFLICT (integration_id, external_id) DO UPDATE SET name = EXCLUDED.name, currency = EXCLUDED.currency,
					time_zone = EXCLUDED.time_zone, meta = EXCLUDED.meta, status = 'active', updated_at = now(),
					brand_id = COALESCE(ad_accounts.brand_id, EXCLUDED.brand_id)`,
				ws, integrationID, connector, a.ID, trim(a.Name, 300), cur, a.TimeZone, meta, MaxAccounts); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `UPDATE ad_accounts SET status = 'gone', updated_at = now()
			WHERE integration_id = $1 AND status = 'active' AND NOT (external_id = ANY($2))`, integrationID, ids)
		return err
	})
}

type account struct {
	id, externalID, name, currency, timeZone string
	meta                                     map[string]string
	brandID                                  *string
	syncOn                                   bool
	firstDay                                 *time.Time
}

func (a account) ad() connectors.AdAccount {
	return connectors.AdAccount{ID: a.externalID, Name: a.name, Currency: a.currency, TimeZone: a.timeZone, Meta: a.meta}
}

func (s *Syncer) loadAccounts(ctx context.Context, ws, integrationID string) ([]account, error) {
	rows, err := s.Pool.Query(ctx, `SELECT id, external_id, name, currency, time_zone, meta, brand_id, sync_on, first_day
		FROM ad_accounts WHERE workspace_id = $1 AND integration_id = $2 AND status = 'active' ORDER BY created_at, id`, ws, integrationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []account
	for rows.Next() {
		var a account
		var meta []byte
		if err := rows.Scan(&a.id, &a.externalID, &a.name, &a.currency, &a.timeZone, &meta, &a.brandID, &a.syncOn, &a.firstDay); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(meta, &a.meta)
		out = append(out, a)
	}
	return out, rows.Err()
}

// today is the current date in the account's reporting time zone.
func today(now time.Time, tz string) time.Time {
	loc, err := time.LoadLocation(tz)
	if err != nil || tz == "" {
		loc = time.UTC
	}
	y, m, d := now.In(loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// Window is the dates a run reads for an account.
func Window(now time.Time, tz string, first, full bool) (from, to string) {
	end := today(now, tz)
	n := recentDays
	switch {
	case first:
		n = backfillDays
	case full:
		n = fullDays
	}
	return end.AddDate(0, 0, -(n - 1)).Format(time.DateOnly), end.Format(time.DateOnly)
}

func (s *Syncer) syncAccount(ctx context.Context, o *connectors.Opened, r connectors.AdsReader, a account, full bool, res *Result) error {
	from, to := Window(s.now(), a.timeZone, a.firstDay == nil, full)
	camps, err := r.AdCampaigns(ctx, o.Client, o.Creds, a.ad())
	if err != nil {
		s.accountError(ctx, a.id, err)
		return err
	}
	rows, err := r.AdDays(ctx, o.Client, o.Creds, a.ad(), from, to)
	if err != nil {
		s.accountError(ctx, a.id, err)
		return err
	}
	connector := o.Connector.Definition().ID
	err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		entity := map[string]string{}
		brand := map[string]*string{}
		save := func(c connectors.AdCampaign) error {
			var id string
			var b *string
			err := tx.QueryRow(ctx, `
				INSERT INTO ad_entities (workspace_id, ad_account_id, level, external_id, name, status, provider_status, objective, daily_budget_micros,
					bidding_strategy, platform_target_cpa_micros, platform_target_roas)
				VALUES ($1, $2, 'campaign', $3, $4, $5, $6, $7, $8, $9, $10, $11)
				ON CONFLICT (ad_account_id, level, external_id) DO UPDATE SET
					name = COALESCE(NULLIF(EXCLUDED.name, ''), ad_entities.name),
					-- A platform that says how it bids replaces the targets with
					-- what it says now; one that doesn't leaves them.
					platform_target_cpa_micros = CASE WHEN EXCLUDED.bidding_strategy = '' THEN ad_entities.platform_target_cpa_micros ELSE EXCLUDED.platform_target_cpa_micros END,
					platform_target_roas = CASE WHEN EXCLUDED.bidding_strategy = '' THEN ad_entities.platform_target_roas ELSE EXCLUDED.platform_target_roas END,
					bidding_strategy = COALESCE(NULLIF(EXCLUDED.bidding_strategy, ''), ad_entities.bidding_strategy),
					status = CASE WHEN EXCLUDED.provider_status = '' THEN ad_entities.status ELSE EXCLUDED.status END,
					provider_status = COALESCE(NULLIF(EXCLUDED.provider_status, ''), ad_entities.provider_status),
					objective = COALESCE(NULLIF(EXCLUDED.objective, ''), ad_entities.objective),
					daily_budget_micros = COALESCE(EXCLUDED.daily_budget_micros, ad_entities.daily_budget_micros),
					updated_at = now()
				RETURNING id, brand_id`,
				o.WorkspaceID, a.id, c.ID, trim(c.Name, 300), statusOr(c.Status), trim(c.ProviderStatus, 60), trim(c.Objective, 60), c.DailyBudgetMicros,
				trim(c.BiddingStrategy, 60), c.TargetCPAMicros, c.TargetROAS).Scan(&id, &b)
			if err != nil {
				return err
			}
			entity[c.ID] = id
			if b == nil {
				b = a.brandID
			}
			brand[c.ID] = b
			return nil
		}
		for _, c := range camps {
			if c.ID == "" {
				continue
			}
			if err := save(c); err != nil {
				return err
			}
		}
		res.Campaigns += len(entity)
		names := map[string]string{}
		for _, d := range rows {
			if _, ok := entity[d.CampaignID]; !ok && d.CampaignID != "" {
				// In the report but no longer listed: a deleted campaign.
				if err := save(connectors.AdCampaign{ID: d.CampaignID, Name: d.CampaignName, Status: connectors.AdRemoved}); err != nil {
					return err
				}
			}
			if d.CampaignName != "" {
				names[d.CampaignID] = d.CampaignName
			}
		}
		var first, last string
		for _, d := range rows {
			id, ok := entity[d.CampaignID]
			if !ok {
				continue
			}
			if _, err := time.Parse(time.DateOnly, d.Date); err != nil {
				continue
			}
			if err := saveDay(ctx, tx, id, o.WorkspaceID, a.currency, d.Date, d.SpendMicros, d.Impressions, d.Clicks, d.Conversions, d.ConversionValueMicros, d.AdExtra); err != nil {
				return err
			}
			if err := ledger(ctx, tx, o.WorkspaceID, o.ID, connector, id, brand[d.CampaignID], a.currency, d.Date, d.SpendMicros, names[d.CampaignID]); err != nil {
				return err
			}
			res.Rows++
			if first == "" || d.Date < first {
				first = d.Date
			}
			if d.Date > last {
				last = d.Date
			}
		}
		// The window was read in full, so the account has history from its
		// start even when the first days had no spend.
		_, err := tx.Exec(ctx, `UPDATE ad_accounts SET first_day = LEAST(COALESCE(first_day, $2::date), $2::date),
				last_day = GREATEST(COALESCE(last_day, $3::date), $3::date), last_synced_at = now(), last_error = NULL, updated_at = now()
			WHERE id = $1`, a.id, from, to)
		return err
	})
	if err != nil {
		return dbError{err}
	}
	if ir, ok := r.(connectors.AdItemsReader); ok {
		for _, level := range []string{connectors.AdLevelGroup, connectors.AdLevelAd} {
			if err := s.syncItems(ctx, o, ir, a, level, from, to, res); err != nil {
				return err
			}
		}
	}
	if kr, ok := r.(connectors.AdKeywordsReader); ok {
		if err := s.syncKeywords(ctx, o, kr, a, from, to, res); err != nil {
			return err
		}
	}
	return nil
}

// syncKeywords stores an account's search keywords with their numbers per
// day, and the searches they matched, under their ad groups. The window's
// days are replaced whole, so a search the platform no longer reports
// for a day goes.
func (s *Syncer) syncKeywords(ctx context.Context, o *connectors.Opened, r connectors.AdKeywordsReader, a account, from, to string, res *Result) error {
	kws, err := r.AdKeywordDays(ctx, o.Client, o.Creds, a.ad(), from, to)
	if err != nil {
		s.accountError(ctx, a.id, err)
		return err
	}
	terms, err := r.AdSearchTermDays(ctx, o.Client, o.Creds, a.ad(), from, to)
	if err != nil {
		s.accountError(ctx, a.id, err)
		return err
	}
	var kwDays, termDays int
	err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		groups, brands := map[string]string{}, map[string]*string{}
		rows, err := tx.Query(ctx, `SELECT e.external_id, e.id, `+entityBrand+` FROM ad_entities e JOIN ad_accounts a ON a.id = e.ad_account_id
			WHERE e.ad_account_id = $1 AND e.level = 'ad_group'`, a.id)
		if err != nil {
			return err
		}
		for rows.Next() {
			var ext, id string
			var brand *string
			if err := rows.Scan(&ext, &id, &brand); err != nil {
				rows.Close()
				return err
			}
			groups[ext], brands[id] = id, brand
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM ad_keyword_metrics_daily m USING ad_keywords k
			WHERE k.id = m.keyword_id AND k.ad_account_id = $1 AND m.day BETWEEN $2 AND $3`, a.id, from, to); err != nil {
			return err
		}
		keyword := map[string]string{}
		for _, d := range kws {
			group, ok := groups[d.AdGroupID]
			if !ok || d.ID == "" {
				continue
			}
			if _, err := time.Parse(time.DateOnly, d.Date); err != nil {
				continue
			}
			k := d.AdGroupID + "|" + d.ID
			id, ok := keyword[k]
			if !ok {
				if err := tx.QueryRow(ctx, `
					INSERT INTO ad_keywords (workspace_id, ad_account_id, ad_group_id, external_id, text, match_type, status, brand_id)
					VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
					ON CONFLICT (ad_group_id, external_id) DO UPDATE SET text = EXCLUDED.text, match_type = EXCLUDED.match_type,
						status = EXCLUDED.status, brand_id = EXCLUDED.brand_id, updated_at = now()
					RETURNING id`, o.WorkspaceID, a.id, group, d.ID, trim(d.Text, 300), trim(d.MatchType, 30), statusOr(d.Status), brands[group]).Scan(&id); err != nil {
					return err
				}
				keyword[k] = id
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO ad_keyword_metrics_daily (keyword_id, day, workspace_id, currency, spend_micros, impressions, clicks, conversions, conversion_value_micros, brand_id)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
				ON CONFLICT (keyword_id, day) DO UPDATE SET spend_micros = EXCLUDED.spend_micros, impressions = EXCLUDED.impressions,
					clicks = EXCLUDED.clicks, conversions = EXCLUDED.conversions, conversion_value_micros = EXCLUDED.conversion_value_micros,
					brand_id = EXCLUDED.brand_id`,
				id, d.Date, o.WorkspaceID, a.currency, max(d.SpendMicros, 0), max(d.Impressions, 0), max(d.Clicks, 0), d.Conversions, d.ConversionValueMicros, brands[group]); err != nil {
				return err
			}
			kwDays++
		}
		if _, err := tx.Exec(ctx, `DELETE FROM ad_search_terms_daily WHERE ad_account_id = $1 AND day BETWEEN $2 AND $3`, a.id, from, to); err != nil {
			return err
		}
		// One row per ad group, day, search and keyword, however the
		// platform splits them.
		type key struct{ group, day, term, kw, match string }
		type sums struct {
			spend, impressions, clicks int64
			conv                       *float64
			value                      *int64
		}
		agg := map[key]*sums{}
		var order []key
		for _, d := range terms {
			group, ok := groups[d.AdGroupID]
			term := trim(d.Term, 300)
			if !ok || term == "" {
				continue
			}
			if _, err := time.Parse(time.DateOnly, d.Date); err != nil {
				continue
			}
			k := key{group, d.Date, term, trim(d.KeywordText, 300), trim(d.KeywordMatchType, 30)}
			x := agg[k]
			if x == nil {
				x = &sums{}
				agg[k] = x
				order = append(order, k)
			}
			x.spend += max(d.SpendMicros, 0)
			x.impressions += max(d.Impressions, 0)
			x.clicks += max(d.Clicks, 0)
			if d.Conversions != nil {
				x.conv = ptr(deref(x.conv) + *d.Conversions)
			}
			if d.ConversionValueMicros != nil {
				x.value = ptr(deref(x.value) + *d.ConversionValueMicros)
			}
		}
		copyRows := make([][]any, 0, len(order))
		for _, k := range order {
			x := agg[k]
			day, _ := time.Parse(time.DateOnly, k.day)
			copyRows = append(copyRows, []any{o.WorkspaceID, brands[k.group], a.id, k.group, day, k.term, k.kw, k.match, a.currency, x.spend, x.impressions, x.clicks, x.conv, x.value})
		}
		n, err := tx.CopyFrom(ctx, pgx.Identifier{"ad_search_terms_daily"},
			[]string{"workspace_id", "brand_id", "ad_account_id", "ad_group_id", "day", "term", "keyword_text", "keyword_match_type", "currency",
				"spend_micros", "impressions", "clicks", "conversions", "conversion_value_micros"}, pgx.CopyFromRows(copyRows))
		termDays = int(n)
		return err
	})
	if err != nil {
		return dbError{err}
	}
	metrics.AdItemRows("keyword", kwDays)
	metrics.AdItemRows("search_term", termDays)
	res.Keywords += len(kws)
	res.SearchTerms += termDays
	return nil
}

func ptr[T any](v T) *T { return &v }

func deref[T int64 | float64](p *T) T {
	if p == nil {
		return 0
	}
	return *p
}

// parentLevel is the level an ad group's or ad's parent is at.
var parentLevel = map[string]string{connectors.AdLevelGroup: "campaign", connectors.AdLevelAd: connectors.AdLevelGroup}

// syncItems stores an account's ad groups or ads under their parents, and
// their numbers per day. Their spend is already in their campaign's, so
// none of it goes to the spend ledger. An item whose parent isn't stored
// (an ad in a removed ad group) is left out.
func (s *Syncer) syncItems(ctx context.Context, o *connectors.Opened, r connectors.AdItemsReader, a account, level, from, to string, res *Result) error {
	items, err := r.AdItems(ctx, o.Client, o.Creds, a.ad(), level)
	if err != nil {
		s.accountError(ctx, a.id, err)
		return err
	}
	days, err := r.AdItemDays(ctx, o.Client, o.Creds, a.ad(), level, from, to)
	if err != nil {
		s.accountError(ctx, a.id, err)
		return err
	}
	stored := 0
	entity := map[string]string{}
	err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		parents := map[string]string{}
		rows, err := tx.Query(ctx, `SELECT external_id, id FROM ad_entities WHERE ad_account_id = $1 AND level = $2`, a.id, parentLevel[level])
		if err != nil {
			return err
		}
		for rows.Next() {
			var ext, id string
			if err := rows.Scan(&ext, &id); err != nil {
				rows.Close()
				return err
			}
			parents[ext] = id
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		save := func(it connectors.AdItem) error {
			parent, ok := parents[it.ParentID]
			if !ok || it.ID == "" {
				return nil
			}
			details := []byte("{}")
			if len(it.Details) > 0 {
				details, _ = json.Marshal(it.Details)
			}
			var id string
			// An ad group or ad belongs to its campaign's brand.
			err := tx.QueryRow(ctx, `
				INSERT INTO ad_entities (workspace_id, ad_account_id, level, parent_id, external_id, name, status, provider_status, daily_budget_micros, details, brand_id)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, (SELECT brand_id FROM ad_entities WHERE id = $4))
				ON CONFLICT (ad_account_id, level, external_id) DO UPDATE SET
					parent_id = EXCLUDED.parent_id,
					name = COALESCE(NULLIF(EXCLUDED.name, ''), ad_entities.name),
					status = CASE WHEN EXCLUDED.provider_status = '' THEN ad_entities.status ELSE EXCLUDED.status END,
					provider_status = COALESCE(NULLIF(EXCLUDED.provider_status, ''), ad_entities.provider_status),
					daily_budget_micros = COALESCE(EXCLUDED.daily_budget_micros, ad_entities.daily_budget_micros),
					details = CASE WHEN EXCLUDED.details = '{}' THEN ad_entities.details ELSE EXCLUDED.details END,
					brand_id = EXCLUDED.brand_id, updated_at = now()
				RETURNING id`,
				o.WorkspaceID, a.id, level, parent, it.ID, trim(it.Name, 300), statusOr(it.Status), trim(it.ProviderStatus, 60), it.DailyBudgetMicros, details).Scan(&id)
			if err != nil {
				return err
			}
			entity[it.ID] = id
			return nil
		}
		for _, it := range items {
			if err := save(it); err != nil {
				return err
			}
		}
		for _, d := range days {
			if _, ok := entity[d.ID]; !ok {
				// In the report but no longer listed: removed.
				if err := save(connectors.AdItem{ID: d.ID, ParentID: d.ParentID, Name: d.Name, Status: connectors.AdRemoved, ProviderStatus: "REMOVED"}); err != nil {
					return err
				}
			}
		}
		for _, d := range days {
			id, ok := entity[d.ID]
			if !ok {
				continue
			}
			if _, err := time.Parse(time.DateOnly, d.Date); err != nil {
				continue
			}
			if err := saveDay(ctx, tx, id, o.WorkspaceID, a.currency, d.Date, d.SpendMicros, d.Impressions, d.Clicks, d.Conversions, d.ConversionValueMicros, d.AdExtra); err != nil {
				return err
			}
			stored++
		}
		return nil
	})
	if err != nil {
		return dbError{err}
	}
	metrics.AdItemRows(level, stored)
	if level == connectors.AdLevelGroup {
		res.AdGroups += len(entity)
	} else {
		res.Ads += len(entity)
	}
	return nil
}

// saveDay stores one campaign's, ad group's or ad's numbers for a day,
// and its conversions by action in place of what the day had before.
func saveDay(ctx context.Context, tx pgx.Tx, entityID, ws, currency, day string, spend, impressions, clicks int64, conv *float64, value *int64, x connectors.AdExtra) error {
	nonNeg := func(v *int64) *int64 {
		if v == nil || *v < 0 {
			return nil
		}
		return v
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO ad_metrics_daily (ad_entity_id, day, workspace_id, currency, spend_micros, impressions, clicks, conversions, conversion_value_micros,
			reach, link_clicks, landing_page_views, video_views)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		ON CONFLICT (ad_entity_id, day) DO UPDATE SET currency = EXCLUDED.currency, spend_micros = EXCLUDED.spend_micros,
			impressions = EXCLUDED.impressions, clicks = EXCLUDED.clicks, conversions = EXCLUDED.conversions,
			conversion_value_micros = EXCLUDED.conversion_value_micros, reach = EXCLUDED.reach, link_clicks = EXCLUDED.link_clicks,
			landing_page_views = EXCLUDED.landing_page_views, video_views = EXCLUDED.video_views, updated_at = now()`,
		entityID, day, ws, currency, max(spend, 0), max(impressions, 0), max(clicks, 0), conv, value,
		nonNeg(x.Reach), nonNeg(x.LinkClicks), nonNeg(x.LandingPageViews), nonNeg(x.VideoViews)); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM ad_conversion_actions_daily WHERE ad_entity_id = $1 AND day = $2`, entityID, day); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, a := range x.Actions {
		name := trim(a.Name, 300)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		if _, err := tx.Exec(ctx, `INSERT INTO ad_conversion_actions_daily (ad_entity_id, day, action, workspace_id, conversions, value_micros, brand_id)
			VALUES ($1, $2, $3, $4, $5, $6, (SELECT `+entityBrand+` FROM ad_entities e JOIN ad_accounts a ON a.id = e.ad_account_id WHERE e.id = $1))`,
			entityID, day, name, ws, max(a.Conversions, 0), a.ValueMicros); err != nil {
			return err
		}
	}
	return nil
}

// entityBrand is an ad entity's brand: its own, or its account's (e and
// a).
const entityBrand = `COALESCE(e.brand_id, a.brand_id)`

// ledger writes one campaign-day's spend to the spend ledger, or removes
// it when the platform now says nothing was spent.
func ledger(ctx context.Context, tx pgx.Tx, ws, integrationID, connector, entityID string, brandID *string, currency, day string, micros int64, name string) error {
	ref := "ads:" + entityID + ":" + day
	if micros <= 0 {
		_, err := tx.Exec(ctx, `DELETE FROM spend_entries WHERE workspace_id = $1 AND ref = $2`, ws, ref)
		return err
	}
	cents := int64(math.Round(float64(micros) / 1e4))
	occurred, _ := time.Parse(time.DateOnly, day)
	_, err := tx.Exec(ctx, `
		INSERT INTO spend_entries (workspace_id, category, provider, integration_id, amount_cents, currency, occurred_at, source, ref, note,
			channel, brand_id, ad_entity_id)
		VALUES ($1, 'ads', $2, $3, $4, $5, $6, 'auto', $7, $8, $9, $10, $11)
		ON CONFLICT (workspace_id, ref) WHERE ref IS NOT NULL DO UPDATE SET amount_cents = EXCLUDED.amount_cents,
			currency = EXCLUDED.currency, note = EXCLUDED.note, brand_id = EXCLUDED.brand_id, channel = EXCLUDED.channel`,
		ws, connector, integrationID, cents, currency, occurred.Add(12*time.Hour), ref, trim(name, 200), Channel, brandID, entityID)
	return err
}

func (s *Syncer) accountError(ctx context.Context, id string, err error) {
	_, _ = s.Pool.Exec(ctx, `UPDATE ad_accounts SET last_error = $2, updated_at = now() WHERE id = $1`, id, err.Error())
}

func statusOr(s string) string {
	switch s {
	case connectors.AdActive, connectors.AdPaused, connectors.AdRemoved:
		return s
	}
	return connectors.AdOther
}

func trim(s string, n int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}
