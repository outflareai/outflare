package support

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/audit"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/track"
)

// SLAs: the targets a team sets for answering and resolving, and the
// clock that says when each conversation's next reply and resolution are
// due. Clock recomputes them from the conversation as it stands, so every
// change (a customer writing, a reply, a close, a new policy) calls it.

// Week is a team's working hours: per weekday (0 Sunday), [start, end)
// minutes after midnight.
type Week [7][][2]int

// Policy is an SLA: targets in minutes (nil for none), counted in the
// working week in its time zone, or around the clock when Hours is nil.
type Policy struct {
	FirstReply, NextReply, Resolution *int
	Location                          *time.Location
	Hours                             *Week
}

// ParseWeek checks working hours: each span inside the day, starting
// before it ends, in order and not overlapping.
func ParseWeek(raw []byte) (*Week, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var w Week
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, errors.New("hours must be 7 lists of [start, end] minutes, Sunday first")
	}
	open := false
	for d, spans := range w {
		end := 0
		for _, s := range spans {
			if s[0] < end || s[0] >= s[1] || s[1] > 24*60 {
				return nil, fmt.Errorf("hours on day %d must be in order, inside the day, and not overlap", d)
			}
			end = s[1]
			open = true
		}
		if len(spans) > 4 {
			return nil, fmt.Errorf("at most 4 spans a day")
		}
	}
	if !open {
		return nil, errors.New("the working week needs at least one open span")
	}
	return &w, nil
}

// Due is when a target of mins working minutes from start runs out.
func (p Policy) Due(start time.Time, mins int) time.Time {
	if p.Hours == nil {
		return start.Add(time.Duration(mins) * time.Minute)
	}
	loc := p.Location
	if loc == nil {
		loc = time.UTC
	}
	left := time.Duration(mins) * time.Minute
	t := start.In(loc)
	// At most two years of days: a week with any open span uses far fewer.
	for range 750 {
		day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
		for _, s := range p.Hours[day.Weekday()] {
			// Wall-clock times, so a day the clocks change keeps its hours.
			from := time.Date(day.Year(), day.Month(), day.Day(), s[0]/60, s[0]%60, 0, 0, loc)
			to := time.Date(day.Year(), day.Month(), day.Day(), s[1]/60, s[1]%60, 0, 0, loc)
			if !to.After(t) {
				continue
			}
			if from.Before(t) {
				from = t
			}
			if span := to.Sub(from); span >= left {
				return from.Add(left)
			} else {
				left -= span
			}
		}
		t = day.AddDate(0, 0, 1)
	}
	return t
}

// PolicyFor is the SLA that covers a brand's conversations: the brand's
// own when it has one, else the workspace's. Nil when none is on.
func PolicyFor(ctx context.Context, q jobs.Querier, ws string, brand *string) (*Policy, error) {
	var p Policy
	var tz string
	var hours []byte
	var on bool
	err := q.QueryRow(ctx, `
		SELECT enabled, first_reply_minutes, next_reply_minutes, resolution_minutes, timezone, hours FROM support_sla_policies
		WHERE workspace_id = $1 AND brand_id IS NOT DISTINCT FROM $2::uuid
		LIMIT 1`, ws, brand).Scan(&on, &p.FirstReply, &p.NextReply, &p.Resolution, &tz, &hours)
	// A brand's own policy that is off turns SLAs off for that brand.
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !on {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if p.Location, err = time.LoadLocation(tz); err != nil {
		p.Location = time.UTC
	}
	if p.Hours, err = ParseWeek(hours); err != nil {
		p.Hours = nil
	}
	return &p, nil
}

// Clock sets when the conversation's next reply and resolution are due,
// from its state and its SLA. A due time that changes clears its miss; one
// that stays keeps it.
func Clock(ctx context.Context, tx pgx.Tx, conv string) error {
	var ws, status string
	var brand *string
	var firstMessage time.Time
	var opened *time.Time
	err := tx.QueryRow(ctx, `SELECT workspace_id::text, brand_id::text, status, first_message_at, opened_at
		FROM support_conversations WHERE id = $1`, conv).Scan(&ws, &brand, &status, &firstMessage, &opened)
	if err != nil {
		return err
	}
	// Waiting since: the customer's first message the team hasn't answered.
	var waiting *time.Time
	var answered bool
	if err := tx.QueryRow(ctx, `
		WITH last_out AS (SELECT max(created_at) AS at FROM support_messages WHERE conversation_id = $1 AND kind = 'out' AND status <> 'failed')
		SELECT (SELECT min(m.created_at) FROM support_messages m, last_out
		        WHERE m.conversation_id = $1 AND m.kind = 'in' AND NOT m.auto_reply AND m.created_at > COALESCE(last_out.at, '-infinity')),
		       (SELECT at IS NOT NULL FROM last_out)`, conv).Scan(&waiting, &answered); err != nil {
		return err
	}
	switch {
	case status == StatusClosed:
		opened = nil
	case opened == nil && waiting != nil:
		opened = waiting
	case opened == nil:
		opened = &firstMessage
	}
	p, err := PolicyFor(ctx, tx, ws, brand)
	if err != nil {
		return err
	}
	var reply, resolve *time.Time
	if p != nil && status == StatusOpen && waiting != nil {
		target := p.FirstReply
		if answered {
			target = p.NextReply
		}
		if target != nil {
			t := p.Due(*waiting, *target)
			reply = &t
		}
	}
	if p != nil && status != StatusClosed && p.Resolution != nil {
		t := p.Due(*opened, *p.Resolution)
		resolve = &t
	}
	_, err = tx.Exec(ctx, `
		UPDATE support_conversations SET opened_at = $2,
			reply_breached_at = CASE WHEN reply_due_at IS NOT DISTINCT FROM $3 THEN reply_breached_at END, reply_due_at = $3,
			resolve_breached_at = CASE WHEN resolve_due_at IS NOT DISTINCT FROM $4 THEN resolve_breached_at END, resolve_due_at = $4
		WHERE id = $1`, conv, opened, reply, resolve)
	return err
}

// ClockAll recomputes every conversation not closed in a workspace, after
// its SLAs change: at most limit of them, newest first.
func ClockAll(ctx context.Context, tx pgx.Tx, ws string, limit int) (int, error) {
	rows, err := tx.Query(ctx, `SELECT id::text FROM support_conversations WHERE workspace_id = $1 AND status <> 'closed'
		ORDER BY last_message_at DESC LIMIT $2`, ws, limit)
	if err != nil {
		return 0, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return 0, err
	}
	for _, id := range ids {
		if err := Clock(ctx, tx, id); err != nil {
			return 0, err
		}
	}
	return len(ids), nil
}

// SLAWatch marks the replies and resolutions that just ran past their due
// time, records each as support.sla_breached, and reports how many are
// overdue.
type SLAWatch struct {
	Pool *pgxpool.Pool
	Log  *slog.Logger
}

// Run checks every interval until ctx ends.
func (w *SLAWatch) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if err := w.Check(ctx); err != nil && ctx.Err() == nil {
			track.Fail(ctx, w.Log, track.Event{Name: "support.sla_check_failed", ActorType: "system", Surface: "system"}, "internal", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Check runs once.
func (w *SLAWatch) Check(ctx context.Context) error {
	start := time.Now()
	err := pgx.BeginFunc(ctx, w.Pool, func(tx pgx.Tx) error {
		for _, target := range []string{"reply", "resolve"} {
			rows, err := tx.Query(ctx, fmt.Sprintf(`
				UPDATE support_conversations SET %[1]s_breached_at = now(), sla_breaches = sla_breaches + 1
				WHERE id IN (SELECT id FROM support_conversations WHERE %[1]s_due_at <= now() AND %[1]s_breached_at IS NULL
				             ORDER BY %[1]s_due_at LIMIT 500 FOR UPDATE SKIP LOCKED)
				RETURNING id::text, workspace_id::text, %[1]s_due_at`, target))
			if err != nil {
				return err
			}
			type miss struct {
				id, ws string
				due    time.Time
			}
			list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (miss, error) {
				var m miss
				return m, r.Scan(&m.id, &m.ws, &m.due)
			})
			if err != nil {
				return err
			}
			for _, m := range list {
				metrics.SupportSLABreached(target)
				if err := audit.Record(ctx, tx, audit.Event{WorkspaceID: m.ws, ActorType: "system", Action: "support.sla_breached",
					TargetType: "support_conversation", TargetID: m.id,
					Data:  map[string]any{"target": target, "due_at": m.due},
					Props: map[string]any{"target": target}}); err != nil {
					return err
				}
			}
		}
		var reply, resolve int
		if err := tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE reply_breached_at IS NOT NULL), count(*) FILTER (WHERE resolve_breached_at IS NOT NULL)
			FROM support_conversations WHERE status <> 'closed' AND (reply_breached_at IS NOT NULL OR resolve_breached_at IS NOT NULL)`).Scan(&reply, &resolve); err != nil {
			return err
		}
		metrics.SupportOverdue("reply", reply)
		metrics.SupportOverdue("resolve", resolve)
		return nil
	})
	metrics.SupportSLACheck(err == nil, time.Since(start))
	return err
}
