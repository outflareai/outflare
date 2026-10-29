package linkbuild

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/seosync"
	"github.com/outflareai/outflare/engine/internal/track"
)

// The live check: an agreed link's page is checked every day until the
// link is there (live), and a live one every week, so a link that goes
// away shows as removed. Each is a page check (JobCheck), free. An agreed
// link not live after OverdueAfter is announced once (link_prospect.overdue)
// so someone can nudge the site.

// Live check timing.
const (
	agreedEvery  = 20 * time.Hour
	liveEvery    = 7 * 24 * time.Hour
	OverdueAfter = 14 * 24 * time.Hour
	// maxLiveChecks is how many checks one round queues.
	maxLiveChecks = 500
)

// Run schedules live checks and overdue notices every hour until ctx ends.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		if err := s.Schedule(ctx, time.Now()); err != nil && ctx.Err() == nil {
			track.Fail(ctx, s.Log, track.Event{Name: "link_live.schedule_failed", Surface: "job"}, "internal", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Schedule queues the live checks that are due and announces overdue
// links, once; Run calls it every hour.
func (s *Service) Schedule(ctx context.Context, now time.Time) error {
	rows, err := s.Pool.Query(ctx, `
		SELECT x.workspace_id::text, x.id::text FROM link_prospects x
		LEFT JOIN link_page_checks c ON c.prospect_id = x.id
		WHERE (x.stage = 'agreed' AND (c.checked_at IS NULL OR c.checked_at < $1))
		   OR (x.stage = 'live' AND (c.checked_at IS NULL OR c.checked_at < $2))
		ORDER BY c.checked_at NULLS FIRST LIMIT $3`, now.Add(-agreedEvery), now.Add(-liveEvery), maxLiveChecks)
	if err != nil {
		return err
	}
	type due struct{ ws, id string }
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (due, error) {
		var d due
		return d, r.Scan(&d.ws, &d.id)
	})
	if err != nil {
		return err
	}
	for _, d := range list {
		if _, err := StartCheck(ctx, s.Pool, d.ws, d.id, now); err != nil {
			return err
		}
	}
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `UPDATE link_prospects SET overdue_at = $1
			WHERE stage = 'agreed' AND (overdue_at IS NULL OR overdue_at < agreed_at) AND agreed_at < $2
			RETURNING workspace_id::text, id::text, site_id::text, record_id::text, method, agreed_at`, now, now.Add(-OverdueAfter))
		if err != nil {
			return err
		}
		type over struct {
			ws, id, site, record, method string
			agreed                       time.Time
		}
		list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (over, error) {
			var o over
			return o, r.Scan(&o.ws, &o.id, &o.site, &o.record, &o.method, &o.agreed)
		})
		if err != nil {
			return err
		}
		for _, o := range list {
			days := int(now.Sub(o.agreed).Hours() / 24)
			if err := seosync.SystemRecorder(o.ws)(ctx, tx, "link_prospect.overdue", o.id, map[string]any{"site_id": o.site, "record_id": o.record,
				"method": o.method, "days_since_agreed": days}, map[string]any{"site_id": o.site, "method": o.method, "days_since_agreed": days}); err != nil {
				return err
			}
		}
		return nil
	})
}
