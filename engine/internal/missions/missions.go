// Package missions holds what other packages need to know about agent
// missions: which mission a request belongs to, its progress, and whether
// it may still send or spend. The API for missions lives in package api.
package missions

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/outcomes"
)

// Header is the request header an agent sends to act inside a mission.
const Header = "Outflare-Mission"

// ReasonHeader carries why the agent made a call, kept in the call log.
const ReasonHeader = "Outflare-Reason"

// Statuses.
const (
	StatusDraft           = "draft"
	StatusPendingApproval = "pending_approval"
	StatusRunning         = "running"
	StatusPaused          = "paused"
	StatusCompleted       = "completed"
	StatusStopped         = "stopped"
	StatusFailed          = "failed"
)

// Modes.
const (
	ModeDryRun        = "dry_run"
	ModeApproveWrites = "approve_writes"
	ModeAutonomous    = "autonomous"
)

// Ended reports whether a mission in status s is over.
func Ended(s string) bool {
	return s == StatusCompleted || s == StatusStopped || s == StatusFailed
}

type ctxKey struct{}

// WithID returns ctx carrying the mission a request acts in.
func WithID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxKey{}, id)
}

// ID returns the mission the request acts in, or "" when there is none.
// Code that records spend, outcomes or new campaigns tags them with it.
func ID(ctx context.Context) string {
	id, _ := ctx.Value(ctxKey{}).(string)
	return id
}

// Progress is what a mission has done so far.
type Progress struct {
	Sends    int `json:"sends"`
	Replies  int `json:"replies"`
	Leads    int `json:"leads"`
	Meetings int `json:"meetings"`
	// Money recorded in the spend ledger against the mission, in US cents,
	// with units priced by the workspace's rates.
	SpendCents int64 `json:"spend_cents"`
	// Revenue in US cents: payments credited to the campaigns of the
	// mission's sequences under the workspace's default attribution model,
	// net of refunds, and won deals tagged with the mission.
	RevenueAttributedCents int64 `json:"revenue_attributed_cents"`
	RevenueDealsCents      int64 `json:"revenue_deals_cents"`
}

// Value returns the progress on a goal metric. Revenue goals are in whole
// US dollars.
func (p Progress) Value(metric string) int {
	switch metric {
	case "meetings":
		return p.Meetings
	case "replies":
		return p.Replies
	case "sends":
		return p.Sends
	case "leads":
		return p.Leads
	case "revenue_attributed":
		return int(p.RevenueAttributedCents / 100)
	case "revenue_deals":
		return int(p.RevenueDealsCents / 100)
	}
	return 0
}

// GetProgress counts the mission's results: sends, replies and leads from
// the campaigns started in it; meetings and spend from the outcomes ledger.
func GetProgress(ctx context.Context, q outcomes.Querier, ws, missionID string) (Progress, error) {
	var p Progress
	err := q.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM messages g JOIN sequences s ON s.id = g.sequence_id
			 WHERE s.mission_id = $1 AND g.status IN ('sending', 'sent', 'unknown')),
			(SELECT count(*) FROM inbound_messages i JOIN sequences s ON s.id = i.sequence_id
			 WHERE s.mission_id = $1 AND i.kind = 'reply'),
			(SELECT count(*) FROM enrollments e JOIN sequences s ON s.id = e.sequence_id WHERE s.mission_id = $1)`,
		missionID).Scan(&p.Sends, &p.Replies, &p.Leads)
	if err != nil {
		return p, err
	}
	rep, err := missionReport(ctx, q, ws, missionID)
	if err != nil {
		return p, err
	}
	p.Meetings = rep.Totals.Meetings
	p.SpendCents = rep.Totals.Spend.TotalCents
	p.RevenueDealsCents = rep.Totals.WonCents
	// Payments in other currencies are left out; nothing is converted.
	var attributed float64
	if err := q.QueryRow(ctx, `
		SELECT coalesce(sum(cr.weight * c.value_minor), 0)::float8
		FROM attribution_credits cr
		JOIN attribution_conversions c ON c.event_id = cr.event_id
		JOIN tracking_campaigns tc ON tc.id = cr.tracking_campaign_id AND tc.workspace_id = $1 AND tc.object_kind = 'sequence'
		JOIN sequences s ON s.id::text = tc.object_id AND s.workspace_id = $1
		WHERE cr.workspace_id = $1 AND s.mission_id = $2 AND c.currency = 'USD' AND c.value_minor IS NOT NULL
		  AND cr.model = coalesce((SELECT model FROM attribution_settings WHERE workspace_id = $1), 'last_non_direct')`,
		ws, missionID).Scan(&attributed); err != nil {
		return p, err
	}
	p.RevenueAttributedCents = int64(math.Round(attributed))
	return p, nil
}

// missionReport is the outcomes summary of everything tagged with the
// mission. Reports cover at most three years, which no mission outlives.
func missionReport(ctx context.Context, q outcomes.Querier, ws, missionID string) (outcomes.Report, error) {
	to := time.Now().Add(time.Minute)
	return outcomes.Summary(ctx, q, ws, outcomes.ReportFilter{
		From: to.AddDate(-3, 0, 0), To: to, MissionID: missionID, Currency: "USD",
	})
}

// ErrSpendLimit is returned by CheckSpend when a charge would take the
// mission past its spend limit.
var ErrSpendLimit = errors.New("this would take the mission past its spend limit")

// ErrNotRunning is returned by CheckSpend when the mission cannot spend now.
var ErrNotRunning = errors.New("the mission is not running")

// CheckSpend reports whether a mission may spend cents more. Connectors
// that cost money call it before the call; with no mission it allows.
func CheckSpend(ctx context.Context, q outcomes.Querier, missionID string, cents int64) error {
	if missionID == "" {
		return nil
	}
	var status, ws string
	var limit *int64
	err := q.QueryRow(ctx, `SELECT status, max_spend_cents, workspace_id FROM missions WHERE id = $1`, missionID).Scan(&status, &limit, &ws)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if status != StatusRunning {
		return ErrNotRunning
	}
	if limit == nil {
		return nil
	}
	rep, err := missionReport(ctx, q, ws, missionID)
	if err != nil {
		return err
	}
	if rep.Totals.Spend.TotalCents+cents > *limit {
		return ErrSpendLimit
	}
	return nil
}
