package searchinsights

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"time"
)

type ScheduleRequest struct {
	WorkspaceID string    `json:"workspace_id"`
	BrandID     string    `json:"brand_id"`
	SiteID      string    `json:"site_id"`
	ScheduleID  string    `json:"schedule_id"`
	Revision    int64     `json:"revision"`
	At          time.Time `json:"at"`
}

func (r ScheduleRequest) Key() string {
	return fmt.Sprintf("%s:%d:%s", r.ScheduleID, r.Revision, r.At.UTC().Format(time.RFC3339Nano))
}
func NextCollection(frequency string, weekday, hour int, zone string, now time.Time) (*time.Time, error) {
	if weekday < 0 || weekday > 6 || hour < 0 || hour > 23 {
		return nil, fmt.Errorf("Choose a valid weekday and hour")
	}
	loc, e := time.LoadLocation(zone)
	if e != nil {
		return nil, fmt.Errorf("Choose a valid timezone")
	}
	if frequency == "off" {
		return nil, nil
	}
	if frequency != "daily" && frequency != "weekly" {
		return nil, fmt.Errorf("Frequency is daily, weekly or off")
	}
	local := now.In(loc)
	for i := 0; i < 9; i++ {
		day := local.AddDate(0, 0, i)
		if frequency == "weekly" && int(day.Weekday()) != weekday {
			continue
		}
		candidate := time.Date(day.Year(), day.Month(), day.Day(), hour, 0, 0, 0, loc)
		if candidate.After(now) {
			utc := candidate.UTC()
			return &utc, nil
		}
	}
	return nil, fmt.Errorf("Could not calculate the next collection")
}

// The tick and data fetch are separate jobs: a provider failure never silently
// stops future collections. Revision checks make paused/edited jobs harmless.
func (s *Service) Schedule(ctx context.Context, j jobs.Job) error {
	var p ScheduleRequest
	if e := json.Unmarshal(j.Payload, &p); e != nil {
		return fmt.Errorf("%w: invalid schedule", jobs.ErrPermanent)
	}
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		var frequency, zone, conn, page, device string
		var weekday, hour int
		var next *time.Time
		var revision int64
		e := tx.QueryRow(ctx, `SELECT frequency,timezone,COALESCE(integration_id::text,''),page,device,weekday,hour,next_run_at,revision FROM search_insight_schedules WHERE workspace_id=$1 AND brand_id=$2 AND site_id=$3 AND id=$4 FOR UPDATE`, p.WorkspaceID, p.BrandID, p.SiteID, p.ScheduleID).Scan(&frequency, &zone, &conn, &page, &device, &weekday, &hour, &next, &revision)
		if e == pgx.ErrNoRows {
			return nil
		}
		if e != nil {
			return e
		}
		if frequency == "off" || revision != p.Revision || next == nil || !next.Equal(p.At) {
			return nil
		}
		data := Request{WorkspaceID: p.WorkspaceID, BrandID: p.BrandID, SiteID: p.SiteID, IntegrationID: conn, Page: page, Device: device, ScheduleID: p.ScheduleID, Revision: revision}
		if _, e = jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: "search_insights.refresh", WorkspaceID: &p.WorkspaceID, Payload: data, IdempotencyKey: p.Key()}); e != nil {
			return e
		}
		following, e := NextCollection(frequency, weekday, hour, zone, time.Now())
		if e != nil {
			return e
		}
		if following == nil {
			return nil
		}
		if _, e = tx.Exec(ctx, `UPDATE search_insight_schedules SET next_run_at=$5 WHERE workspace_id=$1 AND brand_id=$2 AND site_id=$3 AND id=$4`, p.WorkspaceID, p.BrandID, p.SiteID, p.ScheduleID, *following); e != nil {
			return e
		}
		p.At = *following
		_, e = jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: "search_insights.schedule", WorkspaceID: &p.WorkspaceID, Payload: p, RunAt: *following, IdempotencyKey: p.Key()})
		return e
	})
}
