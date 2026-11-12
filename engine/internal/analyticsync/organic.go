package analyticsync

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/seointel"
	"github.com/outflareai/outflare/engine/internal/track"
)

// syncOrganicConversions stores one PostHog project's first-touch Organic
// Search signups and payments by landing page: the last 90 days on the
// first run, then the last few days again on every run. It only reads
// from PostHog.
func (s *Syncer) syncOrganicConversions(ctx context.Context, o *connectors.Opened, r connectors.OrganicConversionReader, p SitePlan, res *Result) (ConversionResult, error) {
	ws, id, site := o.WorkspaceID, o.ID, p.ID
	var out ConversionResult
	var backfilledAt *time.Time
	err := s.Pool.QueryRow(ctx, `SELECT backfilled_at FROM organic_conversion_syncs WHERE integration_id = $1 AND site = $2`, id, site).Scan(&backfilledAt)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return out, dbError{err}
	}
	backfill := backfilledAt == nil
	yesterday := s.now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -1)
	from := yesterday.AddDate(0, 0, -recentDays)
	if backfill {
		from = yesterday.AddDate(0, 0, -(backfillDays - 1))
	}
	res.Calls++
	cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	got, runErr := r.OrganicConversions(cctx, o.Client, o.Creds, connectors.OrganicConversionQuery{
		Site: site, From: day(from), To: day(yesterday), TestAccounts: p.FilterTestAccounts,
	})
	cancel()
	if runErr == nil {
		n, err := s.storeOrganicConversions(ctx, ws, id, site, p.Host, from, yesterday, backfill, got)
		if err != nil {
			return out, err
		}
		out.Rows, out.Days = n, int(yesterday.Sub(from).Hours()/24)+1
		res.Rows += n
	}
	msg, code := failed(runErr)
	payFrom, sessSrc := nullIfEmpty(got.PaymentSucceededFrom), nullIfEmpty(got.SessionsSource)
	if runErr != nil {
		payFrom, sessSrc = nil, nil
	}
	_, err = s.Pool.Exec(ctx, `
		INSERT INTO organic_conversion_syncs (integration_id, site, workspace_id, oldest_day, newest_day, payment_succeeded_from, sessions_source, backfilled_at, last_run_at, last_error, failures, last_error_code)
		SELECT $1, $2, $3, d.oldest, d.newest, $7::date, $8, CASE WHEN $4 THEN now() END, now(), $5,
			CASE WHEN $5::text IS NULL THEN 0 ELSE 1 END, $6
		FROM (SELECT min(day) AS oldest, max(day) AS newest FROM organic_conversions WHERE integration_id = $1 AND site = $2) d
		ON CONFLICT (integration_id, site) DO UPDATE SET
			oldest_day = EXCLUDED.oldest_day, newest_day = EXCLUDED.newest_day,
			payment_succeeded_from = COALESCE(EXCLUDED.payment_succeeded_from, organic_conversion_syncs.payment_succeeded_from),
			sessions_source = COALESCE(EXCLUDED.sessions_source, organic_conversion_syncs.sessions_source),
			backfilled_at = CASE WHEN $4 THEN now() ELSE organic_conversion_syncs.backfilled_at END,
			last_run_at = now(), last_error = EXCLUDED.last_error, last_error_code = EXCLUDED.last_error_code,
			failures = CASE WHEN EXCLUDED.last_error IS NULL THEN 0 ELSE organic_conversion_syncs.failures + 1 END`,
		id, site, ws, runErr == nil && backfill, msg, code, payFrom, sessSrc)
	syncRun("organic_conversions", code)
	if err != nil {
		return out, dbError{err}
	}
	ev := track.Event{Name: "organic_conversions.synced", WorkspaceID: ws, ActorType: "system", Surface: "job",
		Props: map[string]any{"integration_id": id, "days": out.Days, "rows": out.Rows, "backfilled": backfill}}
	if runErr != nil {
		ev.Outcome = track.Failure
		if connectors.KindOf(runErr) == connectors.ErrUnavailable {
			ev.Outcome = track.Error
		}
		track.Fail(ctx, s.Log, ev, "connector_"+connectors.KindOf(runErr), fmt.Errorf("organic conversions %s: %w", id, runErr))
	} else {
		track.Emit(ctx, ev)
	}
	return out, runErr
}

func (s *Syncer) storeOrganicConversions(ctx context.Context, ws, id, site, host string, from, to time.Time, backfill bool, got connectors.OrganicConversionResult) (int, error) {
	rows, _ := seointel.RollupOrganicConversions(got, host)
	type siteRef struct{ site, brand *string }
	sites := map[string]siteRef{}
	siteFor := func(h string) siteRef {
		key := seointel.Bare(h)
		if key == "" {
			key = seointel.Bare(host)
		}
		if ref, ok := sites[key]; ok {
			return ref
		}
		var ref siteRef
		lookup := h
		if lookup == "" {
			lookup = host
		}
		if lookup != "" {
			_ = s.Pool.QueryRow(ctx, `SELECT id::text, brand_id::text FROM seo_sites WHERE workspace_id = $1 AND lower(host) = ANY($2) LIMIT 1`,
				ws, seointel.PageHosts(lookup)).Scan(&ref.site, &ref.brand)
		}
		if ref.brand == nil {
			var b *string
			if err := s.Pool.QueryRow(ctx, `SELECT outflare_site_brand($1, $2)`, id, site).Scan(&b); err == nil {
				ref.brand = b
			}
		}
		sites[key] = ref
		return ref
	}
	copyRows := make([][]any, 0, len(rows))
	for _, r := range rows {
		d, err := time.Parse(time.DateOnly, r.Day)
		if err != nil || d.Before(from) || d.After(to) {
			continue
		}
		h := truncate(r.Host, 255)
		if h == "" {
			continue
		}
		ref := siteFor(h)
		copyRows = append(copyRows, []any{ws, ref.brand, ref.site, id, site, d, h, truncate(r.Path, 1000), truncate(r.CanonicalPath, 1000),
			r.OrganicSessions, r.Signups, r.SignupPeople, r.Payments, r.PaymentPeople, r.RevenueUSD})
	}
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		oldest := to.AddDate(0, -16, 0)
		if backfill {
			oldest = from
		}
		if _, err := tx.Exec(ctx, `DELETE FROM organic_conversions WHERE integration_id = $1 AND site = $2 AND (day BETWEEN $3 AND $4 OR day < $5)`, id, site, from, to, oldest); err != nil {
			return err
		}
		_, err := tx.CopyFrom(ctx, pgx.Identifier{"organic_conversions"},
			[]string{"workspace_id", "brand_id", "seo_site_id", "integration_id", "site", "day", "host", "path", "canonical_path",
				"organic_sessions", "signups", "signup_people", "payments", "payment_people", "revenue_usd"},
			pgx.CopyFromRows(copyRows))
		return err
	})
	if err != nil {
		return 0, dbError{err}
	}
	return len(copyRows), nil
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
