// Package searchinsights collects real-user vitals and public update context.
// Every private provider request uses the workspace's own connection.
package searchinsights

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/seointel"
	"github.com/outflareai/outflare/engine/internal/track"
	"log/slog"
	"net/http"
	"time"
)

type Request struct {
	WorkspaceID   string `json:"workspace_id"`
	BrandID       string `json:"brand_id"`
	SiteID        string `json:"site_id"`
	IntegrationID string `json:"integration_id"`
	Page          string `json:"page"`
	Device        string `json:"device"`
	ScheduleID    string `json:"schedule_id,omitempty"`
	Revision      int64  `json:"revision,omitempty"`
}
type Service struct {
	Pool  *pgxpool.Pool
	Store *connectors.Store
	Log   *slog.Logger
}

func (s *Service) Register(q *jobs.Queue) {
	q.Handle("search_insights.refresh", s.Refresh)
	q.Handle("search_insights.schedule", s.Schedule)
}
func (s *Service) Refresh(ctx context.Context, j jobs.Job) error {
	var p Request
	if e := json.Unmarshal(j.Payload, &p); e != nil {
		return fmt.Errorf("%w: invalid insight request", jobs.ErrPermanent)
	}
	if p.ScheduleID != "" {
		var active bool
		if e := s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM search_insight_schedules WHERE workspace_id=$1 AND brand_id=$2 AND site_id=$3 AND id=$4 AND revision=$5 AND frequency<>'off')`, p.WorkspaceID, p.BrandID, p.SiteID, p.ScheduleID, p.Revision).Scan(&active); e != nil {
			return e
		}
		if !active {
			_, err := s.Pool.Exec(ctx, `UPDATE jobs SET payload=jsonb_set(payload,'{skipped}','true'::jsonb) WHERE id=$1 AND workspace_id=$2 AND kind='search_insights.refresh'`, j.ID, p.WorkspaceID)
			return err
		}
	}
	var exists bool
	if e := s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM seo_sites WHERE workspace_id=$1 AND brand_id=$2 AND id=$3 AND status='active')`, p.WorkspaceID, p.BrandID, p.SiteID).Scan(&exists); e != nil {
		return e
	}
	if !exists {
		return nil
	}
	if p.IntegrationID == "" {
		e := s.updates(ctx, s.Store.Client, p)
		if e != nil {
			track.Fail(ctx, s.Log, track.Event{Name: "search_updates.sync_failed", WorkspaceID: p.WorkspaceID, ActorType: "system", Surface: "job"}, "connector_unavailable", e)
			return e
		}
		track.Emit(ctx, track.Event{Name: "search_updates.synced", WorkspaceID: p.WorkspaceID, ActorType: "system", Surface: "job"})
		return nil
	}
	o, e := s.Store.Open(ctx, s.Pool, p.WorkspaceID, p.IntegrationID)
	if e != nil {
		return e
	}
	all, e := s.Store.List(ctx, s.Pool, p.WorkspaceID, "")
	if e != nil {
		return e
	}
	if !o.Serves(p.BrandID, all) {
		return fmt.Errorf("%w: connection does not serve brand", jobs.ErrPermanent)
	}
	reader, ok := o.Connector.(connectors.WebVitalsReader)
	if !ok {
		return fmt.Errorf("%w: connect Chrome UX Report", jobs.ErrPermanent)
	}
	data, e := reader.WebVitals(ctx, o.Client, o.Creds, p.Page, p.Device)
	if e != nil {
		track.Fail(ctx, s.Log, track.Event{Name: "search_vitals.sync_failed", WorkspaceID: p.WorkspaceID, ActorType: "system", Surface: "job", Props: map[string]any{"site_id": p.SiteID}}, "connector_"+connectors.KindOf(e), e)
		return e
	}
	e = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		for _, x := range data {
			if _, e := tx.Exec(ctx, `INSERT INTO search_vitals(workspace_id,brand_id,site_id,page,device,day,metric,p75,good_share) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT(site_id,page,device,day,metric) DO UPDATE SET p75=EXCLUDED.p75,good_share=EXCLUDED.good_share`, p.WorkspaceID, p.BrandID, p.SiteID, x.Page, x.Device, x.Day, x.Metric, x.P75, x.GoodShare); e != nil {
				return e
			}
		}
		if len(data) > 0 {
			return seointel.RefreshVitals(ctx, tx, p.WorkspaceID, p.BrandID, p.SiteID)
		}
		return nil
	})
	if e != nil {
		return e
	}
	// Fixed official public feed: no workspace credentials are sent to it.
	if e = s.updates(ctx, o.Client, p); e != nil {
		track.Fail(ctx, s.Log, track.Event{Name: "search_updates.sync_failed", WorkspaceID: p.WorkspaceID, ActorType: "system", Surface: "job"}, "connector_unavailable", e)
		return e
	}
	track.Emit(ctx, track.Event{Name: "search_vitals.synced", WorkspaceID: p.WorkspaceID, ActorType: "system", Surface: "job", Props: map[string]any{"site_id": p.SiteID, "rows": len(data)}})
	return nil
}
func (s *Service) updates(ctx context.Context, cl *connectors.Client, p Request) error {
	endpoint := cl.Base("google_search_updates", "https://status.search.google.com") + "/incidents.json"
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if e != nil {
		return e
	}
	var incidents []struct {
		ID       string `json:"id"`
		Begin    string `json:"begin"`
		End      string `json:"end"`
		Name     string `json:"external_desc"`
		Products []struct {
			Title string `json:"title"`
		} `json:"affected_products"`
	}
	if _, e = connectors.DoJSON(ctx, cl, "Google Search Status", req, &incidents); e != nil {
		return e
	}
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		for _, x := range incidents {
			ranking := false
			for _, product := range x.Products {
				if product.Title == "Ranking" {
					ranking = true
				}
			}
			if !ranking {
				continue
			}
			start, e := time.Parse(time.RFC3339, x.Begin)
			if e != nil || x.ID == "" {
				continue
			}
			var end *time.Time
			if t, e := time.Parse(time.RFC3339, x.End); e == nil {
				end = &t
			}
			if _, e = tx.Exec(ctx, `INSERT INTO search_algorithm_updates(workspace_id,brand_id,site_id,id,name,started_at,ended_at) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(site_id,id) DO UPDATE SET name=EXCLUDED.name,ended_at=EXCLUDED.ended_at`, p.WorkspaceID, p.BrandID, p.SiteID, x.ID, x.Name, start, end); e != nil {
				return e
			}
		}
		return nil
	})
}
