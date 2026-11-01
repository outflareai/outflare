package analyticsync

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/jobs"
)

func (s *Syncer) storeSearchDetails(ctx context.Context, ws, id, site, host string, day time.Time, visible []connectors.SearchRow, d connectors.SearchDetails) error {
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		// Connections can serve several brands; resolve the property's brand using
		// the same explicit site assignment the analytics sync already uses.
		var brand *string
		if err := tx.QueryRow(ctx, `SELECT analytics_site_brand($1,$2,$3,$4)`, ws, id, site, host).Scan(&brand); err != nil && err != pgx.ErrNoRows {
			return err
		}
		var clicks, impressions float64
		for _, x := range visible {
			clicks += x.Clicks
			impressions += x.Impressions
		}
		_, err := tx.Exec(ctx, `INSERT INTO search_completeness(workspace_id,brand_id,integration_id,site,day,clicks,impressions,visible_clicks,visible_impressions,rows_read,row_limit,capped)
   VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) ON CONFLICT(integration_id,site,day) DO UPDATE SET
   brand_id=EXCLUDED.brand_id,clicks=EXCLUDED.clicks,impressions=EXCLUDED.impressions,visible_clicks=EXCLUDED.visible_clicks,
   visible_impressions=EXCLUDED.visible_impressions,rows_read=EXCLUDED.rows_read,row_limit=EXCLUDED.row_limit,capped=EXCLUDED.capped`, ws, brand, id, site, day, d.Clicks, d.Impressions, clicks, impressions, len(visible), searchRowsPerDay, len(visible) >= searchRowsPerDay)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `DELETE FROM search_segments WHERE workspace_id=$1 AND integration_id=$2 AND site=$3 AND day=$4`, ws, id, site, day); err != nil {
			return err
		}
		rows := [][]any{}
		seen := map[string]bool{}
		for _, x := range d.Segments {
			key := x.Page + "\x00" + x.Country + "\x00" + x.Device
			if seen[key] {
				continue
			}
			seen[key] = true
			rows = append(rows, []any{ws, brand, id, site, host, day, x.Page, x.Country, x.Device, int(x.Clicks), int(x.Impressions), x.Position})
		}
		if _, err = tx.CopyFrom(ctx, pgx.Identifier{"search_segments"}, []string{"workspace_id", "brand_id", "integration_id", "site", "host", "day", "page", "country", "device", "clicks", "impressions", "position"}, pgx.CopyFromRows(rows)); err != nil {
			return err
		}
		for _, table := range []string{"search_segments", "search_completeness"} {
			if _, err = tx.Exec(ctx, `DELETE FROM `+table+` WHERE workspace_id=$1 AND integration_id=$2 AND site=$3 AND day<$4::date-486`, ws, id, site, day); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return dbError{err}
	}
	return nil
}

func (s *Syncer) queueSearchUpdateContext(ctx context.Context, ws, host string, day time.Time) error {
	rows, e := s.Pool.Query(ctx, `SELECT id::text,brand_id::text FROM seo_sites WHERE workspace_id=$1 AND brand_id IS NOT NULL AND status='active' AND (host=$2 OR host LIKE '%.'||$2)`, ws, host)
	if e != nil {
		return e
	}
	type target struct{ id, brand string }
	list, e := pgx.CollectRows(rows, func(r pgx.CollectableRow) (target, error) { var x target; e := r.Scan(&x.id, &x.brand); return x, e })
	if e != nil {
		return e
	}
	for _, x := range list {
		if _, e = jobs.Enqueue(ctx, s.Pool, jobs.NewJob{Kind: "search_insights.refresh", WorkspaceID: &ws, Payload: map[string]string{"workspace_id": ws, "brand_id": x.brand, "site_id": x.id}, IdempotencyKey: x.id + ":updates:" + day.Format(time.DateOnly)}); e != nil {
			return e
		}
	}
	return nil
}
