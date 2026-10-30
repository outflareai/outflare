package emails

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/metrics"
)

// Guardrails keep marketing email from piling up on one person: a
// frequency cap (at most PerDay emails in any 24 hours and PerWeek in any
// 7 days) and quiet hours (nothing from QuietStart to QuietEnd o'clock in
// the person's time zone, or the workspace's when theirs is unknown;
// emails are held until QuietEnd). Both are on by default and set per
// workspace. They apply to lifecycle email that journeys and broadcasts
// send to a customer record; transactional email is never capped or held.
type Guardrails struct {
	CapEnabled   bool   `json:"cap_enabled"`
	PerDay       int    `json:"per_day"`
	PerWeek      int    `json:"per_week"`
	QuietEnabled bool   `json:"quiet_hours_enabled"`
	QuietStart   int    `json:"quiet_start"`
	QuietEnd     int    `json:"quiet_end"`
	TimeZone     string `json:"time_zone"`
}

// Why a guardrail stopped or held an email: step outcomes, broadcast skip
// reasons and the reason on email.capped.
const (
	FrequencyCap = "frequency_cap"
	QuietHours   = "quiet_hours"
)

// LoadGuardrails reads a workspace's guardrails.
func LoadGuardrails(ctx context.Context, q jobs.Querier, ws string) (Guardrails, error) {
	var g Guardrails
	err := q.QueryRow(ctx, `SELECT email_cap_enabled, email_cap_per_day, email_cap_per_week, email_quiet_enabled,
		email_quiet_start, email_quiet_end, email_time_zone FROM workspaces WHERE id = $1`, ws).
		Scan(&g.CapEnabled, &g.PerDay, &g.PerWeek, &g.QuietEnabled, &g.QuietStart, &g.QuietEnd, &g.TimeZone)
	return g, err
}

// Check is what's wrong with guardrails someone wants to save, or "".
func (g Guardrails) Check() string {
	switch {
	case g.PerDay < 0 || g.PerDay > 100:
		return "per_day is 0 (no daily limit) to 100"
	case g.PerWeek < 0 || g.PerWeek > 500:
		return "per_week is 0 (no weekly limit) to 500"
	case g.QuietStart < 0 || g.QuietStart > 23 || g.QuietEnd < 0 || g.QuietEnd > 23:
		return "quiet_start and quiet_end are hours, 0 to 23"
	case g.QuietStart == g.QuietEnd:
		return "quiet_start and quiet_end must differ"
	case g.TimeZone == "" || len(g.TimeZone) > 100:
		return "time_zone is an IANA time zone such as Europe/Paris"
	}
	if _, err := time.LoadLocation(g.TimeZone); err != nil {
		return "time_zone is an IANA time zone such as Europe/Paris"
	}
	return ""
}

// Guarded is what the guardrails did to one email: Capped says why it must
// not go (the frequency cap), Skipped why it doesn't go (quiet hours, on a
// step that skips rather than holds), or HeldUntil is when quiet hours end
// and the email was scheduled for.
type Guarded struct {
	Capped    string
	Skipped   string
	HeldUntil *time.Time
}

// GuardOptions are a sender's own choices about the guardrails. Dry only
// checks, for a journey test: nothing is recorded. IgnoreCap sends over the
// frequency cap (for email a person must get); SkipQuiet doesn't send in
// quiet hours rather than holding the email until they end.
type GuardOptions struct {
	Dry, IgnoreCap, SkipQuiet bool
}

// QuietHoursSkipped is the step outcome and email.capped reason for an
// email quiet hours stopped on a step that skips.
const QuietHoursSkipped = "quiet_hours_skipped"

// Guard applies the workspace's guardrails to a lifecycle email for the
// person on recordID, before it is queued. A capped email is recorded as
// email.capped, on the person's timeline and in the log; a held one has
// its SendAt moved to the end of quiet hours. Call it after the person's
// own choices (SkipReason) let the email through.
func (s *Service) Guard(ctx context.Context, tx pgx.Tx, recordID string, e *NewEmail, o GuardOptions) (Guarded, error) {
	if e.Purpose != "lifecycle" || e.Test || recordID == "" {
		return Guarded{}, nil
	}
	g, err := LoadGuardrails(ctx, tx, e.WorkspaceID)
	if err != nil {
		return Guarded{}, err
	}
	now := time.Now()
	if g.CapEnabled && (g.PerDay > 0 || g.PerWeek > 0) && !o.IgnoreCap {
		// One person at a time, so two sends at once can't both slip under.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('email-cap:' || $1, 0))`, recordID); err != nil {
			return Guarded{}, err
		}
		var day, week int
		err := tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE created_at > $3::timestamptz - interval '1 day'), count(*)
			FROM emails WHERE workspace_id = $1 AND record_id = $2 AND purpose = 'lifecycle' AND NOT test
			AND status NOT IN ('cancelled', 'failed', 'suppressed') AND created_at > $3::timestamptz - interval '7 days'`,
			e.WorkspaceID, recordID, now).Scan(&day, &week)
		if err != nil {
			return Guarded{}, err
		}
		why := ""
		switch {
		case g.PerDay > 0 && day >= g.PerDay:
			why = fmt.Sprintf("frequency cap: already got %d marketing %s in the last day (limit %d)", day, plural(day), g.PerDay)
		case g.PerWeek > 0 && week >= g.PerWeek:
			why = fmt.Sprintf("frequency cap: already got %d marketing %s in the last 7 days (limit %d)", week, plural(week), g.PerWeek)
		}
		if why != "" {
			if o.Dry {
				return Guarded{Capped: why}, nil
			}
			return Guarded{Capped: why}, s.capped(ctx, tx, recordID, e, FrequencyCap, why, map[string]any{"sent_last_day": day, "sent_last_week": week}, now)
		}
	}
	if !g.QuietEnabled {
		return Guarded{}, nil
	}
	tz, err := personZone(ctx, tx, e.WorkspaceID, recordID, g.TimeZone)
	if err != nil {
		return Guarded{}, err
	}
	at := now
	if e.SendAt != nil && e.SendAt.After(now) {
		at = *e.SendAt
	}
	until := QuietUntil(at, tz, g.QuietStart, g.QuietEnd)
	if until == nil {
		return Guarded{}, nil
	}
	if o.SkipQuiet {
		why := "quiet hours: not sent, as the step skips rather than holds (they end " + until.Format("15:04 MST") + ")"
		if o.Dry {
			return Guarded{Skipped: why}, nil
		}
		return Guarded{Skipped: why}, s.capped(ctx, tx, recordID, e, QuietHoursSkipped, why, nil, now)
	}
	e.SendAt = until
	if !o.Dry {
		metrics.EmailGuarded(e.Trigger, QuietHours)
	}
	return Guarded{HeldUntil: until}, nil
}

// capped records an email a guardrail stopped, with its reason
// (FrequencyCap, QuietHoursSkipped): on the person's timeline, as
// email.capped and in the log.
func (s *Service) capped(ctx context.Context, tx pgx.Tx, recordID string, e *NewEmail, reason, why string, extra map[string]any, now time.Time) error {
	metrics.EmailGuarded(e.Trigger, reason)
	props := map[string]any{"reason": reason, "purpose": e.Purpose, "trigger": e.Trigger}
	for k, v := range extra {
		props[k] = v
	}
	if e.TemplateID != nil {
		props["template_id"] = *e.TemplateID
	}
	if e.TopicID != nil {
		props["topic_id"] = *e.TopicID
	}
	if e.TriggerRef != "" {
		props[e.Trigger+"_id"] = e.TriggerRef
	}
	if _, err := customers.Record(ctx, tx, e.WorkspaceID, customers.Event{RecordID: recordID, Name: "email.capped", Properties: props,
		Source: "lifecycle", OccurredAt: now, MessageID: "email.capped:" + newID()}); err != nil {
		return err
	}
	if s.Log != nil {
		s.Log.InfoContext(ctx, "email capped", "workspace_id", e.WorkspaceID, "record_id", recordID, "trigger", e.Trigger,
			"trigger_ref", e.TriggerRef, "reason", why)
	}
	return s.record(ctx, tx, System, e.WorkspaceID, "email.capped", "record", recordID, props)
}

func plural(n int) string {
	if n == 1 {
		return "email"
	}
	return "emails"
}

// personZone is the person's time zone (their timezone attribute) when it
// is a real one, else fallback's, else UTC.
func personZone(ctx context.Context, q jobs.Querier, ws, recordID, fallback string) (*time.Location, error) {
	var tz string
	if err := q.QueryRow(ctx, `SELECT COALESCE(attributes->>'timezone', '') FROM records WHERE workspace_id = $1 AND id = $2`, ws, recordID).Scan(&tz); err != nil {
		return nil, err
	}
	for _, name := range []string{tz, fallback} {
		if name == "" {
			continue
		}
		if loc, err := time.LoadLocation(name); err == nil {
			return loc, nil
		}
	}
	return time.UTC, nil
}

// QuietUntil is when quiet hours (start to end o'clock, wrapping past
// midnight when start > end) end, if at falls inside them in loc, or nil.
func QuietUntil(at time.Time, loc *time.Location, start, end int) *time.Time {
	local := at.In(loc)
	h := local.Hour()
	inside := (start < end && h >= start && h < end) || (start > end && (h >= start || h < end))
	if !inside {
		return nil
	}
	day := local
	if start > end && h >= start {
		day = local.AddDate(0, 0, 1)
	}
	until := time.Date(day.Year(), day.Month(), day.Day(), end, 0, 0, 0, loc)
	return &until
}

// HeldDetail says in words until when an email was held.
func HeldDetail(until time.Time) string {
	return "quiet hours: held until " + until.Format("15:04 MST, Jan 2")
}
