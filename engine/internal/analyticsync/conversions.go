package analyticsync

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/seointel"
	"github.com/outflareai/outflare/engine/internal/track"
)

// ConversionResult is what one site's conversions sync stored.
type ConversionResult struct {
	Days int `json:"days"`
	Rows int `json:"rows"`
}

// syncConversions stores one site's visits and conversions by landing
// page: the last 90 days on the first run, or when the site's conversion
// events change, then the last few days again on every run.
func (s *Syncer) syncConversions(ctx context.Context, o *connectors.Opened, r connectors.PageConversionReader, p SitePlan, res *Result) (ConversionResult, error) {
	ws, id, site := o.WorkspaceID, o.ID, p.ID
	var out ConversionResult
	var stored []string
	var backfilledAt *time.Time
	err := s.Pool.QueryRow(ctx, `SELECT events, backfilled_at FROM page_conversion_syncs WHERE integration_id = $1 AND site = $2`, id, site).Scan(&stored, &backfilledAt)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return out, dbError{err}
	}
	events := slices.Clone(p.ConversionEvents)
	slices.Sort(events)
	slices.Sort(stored)
	backfill := backfilledAt == nil || !slices.Equal(events, stored)
	yesterday := s.now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -1)
	from := yesterday.AddDate(0, 0, -recentDays)
	if backfill {
		from = yesterday.AddDate(0, 0, -(backfillDays - 1))
	}
	res.Calls++
	cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	rows, runErr := r.PageConversions(cctx, o.Client, o.Creds, connectors.PageConversionQuery{Site: site, From: day(from), To: day(yesterday), Events: events})
	cancel()
	if runErr == nil {
		n, err := s.storeConversions(ctx, ws, id, site, from, yesterday, backfill, rows)
		if err != nil {
			return out, err
		}
		out.Rows, out.Days = n, int(yesterday.Sub(from).Hours()/24)+1
		res.Rows += n
	}
	msg, code := failed(runErr)
	if events == nil {
		events = []string{}
	}
	_, err = s.Pool.Exec(ctx, `
		INSERT INTO page_conversion_syncs (integration_id, site, workspace_id, events, oldest_day, newest_day, backfilled_at, last_run_at, last_error,
			failures, last_error_code)
		SELECT $1, $2, $3, $4, d.oldest, d.newest, CASE WHEN $5 THEN now() END, now(), $6, CASE WHEN $6::text IS NULL THEN 0 ELSE 1 END, $7
		FROM (SELECT min(day) AS oldest, max(day) AS newest FROM page_conversions WHERE integration_id = $1 AND site = $2) d
		ON CONFLICT (integration_id, site) DO UPDATE SET
			events = CASE WHEN $5 THEN EXCLUDED.events ELSE page_conversion_syncs.events END,
			oldest_day = EXCLUDED.oldest_day, newest_day = EXCLUDED.newest_day,
			backfilled_at = CASE WHEN $5 THEN now() ELSE page_conversion_syncs.backfilled_at END,
			last_run_at = now(), last_error = EXCLUDED.last_error, last_error_code = EXCLUDED.last_error_code,
			failures = CASE WHEN EXCLUDED.last_error IS NULL THEN 0 ELSE page_conversion_syncs.failures + 1 END`,
		id, site, ws, events, runErr == nil && backfill, msg, code)
	syncRun("page_conversions", code)
	if err != nil {
		return out, dbError{err}
	}
	ev := track.Event{Name: "page_conversions.synced", WorkspaceID: ws, ActorType: "system", Surface: "job",
		Props: map[string]any{"integration_id": id, "days": out.Days, "rows": out.Rows, "events": len(events), "backfilled": backfill}}
	if runErr == nil && out.Rows > 0 {
		// Fresh days can show a drop in a page's conversions.
		hosts := map[string]bool{}
		var list []string
		for _, r := range rows {
			if h := strings.ToLower(r.Host); h != "" && !hosts[h] {
				hosts[h] = true
				list = append(list, h)
			}
		}
		if sites, err := seointel.SitesOnHosts(ctx, s.Pool, ws, list); err != nil {
			track.Fail(ctx, s.Log, track.Event{Name: "seo_regressions.failed", WorkspaceID: ws, ActorType: "system", Surface: "job",
				Props: map[string]any{"trigger": "conversions"}}, "internal", fmt.Errorf("seo regressions after conversions %s: %w", id, err))
		} else {
			seointel.CheckRegressions(ctx, s.Pool, s.Log, ws, "conversions", sites)
		}
	}
	if runErr != nil {
		ev.Outcome = track.Failure
		if connectors.KindOf(runErr) == connectors.ErrUnavailable {
			ev.Outcome = track.Error
		}
		track.Fail(ctx, s.Log, ev, "connector_"+connectors.KindOf(runErr), fmt.Errorf("page conversions %s: %w", id, runErr))
	} else {
		track.Emit(ctx, ev)
	}
	return out, runErr
}

// storeConversions replaces the site's rows for the days read. A backfill
// also drops older days, which counted other events; rows past 16 months
// go either way.
func (s *Syncer) storeConversions(ctx context.Context, ws, id, site string, from, to time.Time, backfill bool, rows []connectors.PageConversionRow) (int, error) {
	type key struct{ day, host, path string }
	seen := make(map[key]bool, len(rows))
	copyRows := make([][]any, 0, len(rows))
	for _, x := range rows {
		d, err := time.Parse(time.DateOnly, x.Day)
		if err != nil || d.Before(from) || d.After(to) {
			continue
		}
		k := key{x.Day, truncate(x.Host, 255), truncate(x.Path, 1000)}
		if k.path == "" {
			k.path = "/"
		}
		if k.host == "" || seen[k] {
			continue
		}
		seen[k] = true
		copyRows = append(copyRows, []any{ws, id, site, d, k.host, k.path, x.Sessions, x.Visitors, x.Conversions, x.SearchSessions, x.SearchConversions})
	}
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		oldest := to.AddDate(0, -16, 0)
		if backfill {
			oldest = from
		}
		if _, err := tx.Exec(ctx, `DELETE FROM page_conversions WHERE integration_id = $1 AND site = $2 AND (day BETWEEN $3 AND $4 OR day < $5)`, id, site, from, to, oldest); err != nil {
			return err
		}
		_, err := tx.CopyFrom(ctx, pgx.Identifier{"page_conversions"},
			[]string{"workspace_id", "integration_id", "site", "day", "host", "path", "sessions", "visitors", "conversions", "search_sessions", "search_conversions"},
			pgx.CopyFromRows(copyRows))
		return err
	})
	if err != nil {
		return 0, dbError{err}
	}
	return len(copyRows), nil
}
