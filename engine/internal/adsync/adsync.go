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
func (s *Syncer) Register(q *jobs.Queue) { q.Handle(JobSync, s.job) }

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
		Props: map[string]any{"connector": res.Connector, "accounts": res.Accounts, "campaigns": res.Campaigns, "rows": res.Rows}}
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
	Rows      int    `json:"rows"`
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
				INSERT INTO ad_accounts (workspace_id, integration_id, connector, external_id, name, currency, time_zone, meta, sync_on)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8,
					(SELECT count(*) FROM ad_accounts WHERE integration_id = $2 AND sync_on) < $9)
				ON CONFLICT (integration_id, external_id) DO UPDATE SET name = EXCLUDED.name, currency = EXCLUDED.currency,
					time_zone = EXCLUDED.time_zone, meta = EXCLUDED.meta, status = 'active', updated_at = now()`,
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
				INSERT INTO ad_entities (workspace_id, ad_account_id, level, external_id, name, status, provider_status, objective, daily_budget_micros)
				VALUES ($1, $2, 'campaign', $3, $4, $5, $6, $7, $8)
				ON CONFLICT (ad_account_id, level, external_id) DO UPDATE SET
					name = COALESCE(NULLIF(EXCLUDED.name, ''), ad_entities.name),
					status = CASE WHEN EXCLUDED.provider_status = '' THEN ad_entities.status ELSE EXCLUDED.status END,
					provider_status = COALESCE(NULLIF(EXCLUDED.provider_status, ''), ad_entities.provider_status),
					objective = COALESCE(NULLIF(EXCLUDED.objective, ''), ad_entities.objective),
					daily_budget_micros = COALESCE(EXCLUDED.daily_budget_micros, ad_entities.daily_budget_micros),
					updated_at = now()
				RETURNING id, brand_id`,
				o.WorkspaceID, a.id, c.ID, trim(c.Name, 300), statusOr(c.Status), trim(c.ProviderStatus, 60), trim(c.Objective, 60), c.DailyBudgetMicros).Scan(&id, &b)
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
			var convValue *int64
			if d.ConversionValueMicros != nil {
				convValue = d.ConversionValueMicros
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO ad_metrics_daily (ad_entity_id, day, workspace_id, currency, spend_micros, impressions, clicks, conversions, conversion_value_micros)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
				ON CONFLICT (ad_entity_id, day) DO UPDATE SET currency = EXCLUDED.currency, spend_micros = EXCLUDED.spend_micros,
					impressions = EXCLUDED.impressions, clicks = EXCLUDED.clicks, conversions = EXCLUDED.conversions,
					conversion_value_micros = EXCLUDED.conversion_value_micros, updated_at = now()`,
				id, d.Date, o.WorkspaceID, a.currency, max(d.SpendMicros, 0), max(d.Impressions, 0), max(d.Clicks, 0), d.Conversions, convValue); err != nil {
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
	return nil
}

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
