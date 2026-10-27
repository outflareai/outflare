// Package usagelimits caps what Outflare spends through a paid connection:
// a daily and a monthly limit per connection, in the provider's own unit
// (credits, API units, tokens) or in US dollars. Usage is counted from the
// spend ledger (spend_entries), so every paid call that books its cost
// there counts, whichever feature made it.
//
// A feature that calls a paid provider calls Check first and books the
// cost with outcomes.Record after; Record calls Booked, which warns the
// workspace's owners and admins at 80% of a limit and when it is reached.
// The calls run on the workspace's own account with the provider and the
// provider bills the workspace; these limits only stop Outflare from
// making more of them.
package usagelimits

import (
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/audit"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/metrics"
)

// USD is the unit for limits in money: the ledger's US dollar amounts.
const USD = "USD"

// JobAlert emails one owner or admin that a connection reached 80% or all
// of a limit. The API server handles it (it sends account email).
const JobAlert = "connection_limit.alert"

// Max is the largest limit, in any unit.
const Max = 1e12

// Periods a limit covers, in UTC.
const (
	Day   = "day"
	Month = "month"
)

// ErrNotFound means the connection is not in the workspace.
var ErrNotFound = errors.New("usagelimits: connection not found")

var unitRe = regexp.MustCompile(`^[A-Za-z][A-Za-z ]{0,49}$`)

// ValidUnit reports whether u can name a limit's unit.
func ValidUnit(u string) bool { return unitRe.MatchString(u) }

// starting is the monthly limit a connection has until someone sets its
// own: the SEO providers bill per call and a weekly snapshot alone can use
// a lot, so a new connection can't run away.
var starting = map[string]struct {
	unit    string
	monthly float64
}{
	"ahrefs":     {"API unit", 10000},
	"dataforseo": {USD, 10},
}

// Starting is a connector's starting monthly limit, if it has one.
func Starting(connector string) (unit string, monthly float64, ok bool) {
	s, ok := starting[connector]
	return s.unit, s.monthly, ok
}

// Limits are a connection's caps. A nil limit is no limit.
type Limits struct {
	Unit    string   `json:"unit"`
	Daily   *float64 `json:"daily_limit"`
	Monthly *float64 `json:"monthly_limit"`
	// Stop: calls stop at a limit. Off, the limits only warn.
	Stop bool `json:"hard_stop"`
	// Default: nobody set limits, so the connector's starting limit (or
	// none) applies.
	Default bool `json:"default"`

	Name      string `json:"-"` // the connection's name, for messages
	Connector string `json:"-"`
}

// Has reports whether any limit is set.
func (l Limits) Has() bool { return l.Unit != "" && (l.Daily != nil || l.Monthly != nil) }

// Of returns the limit for a period.
func (l Limits) Of(period string) *float64 {
	if period == Day {
		return l.Daily
	}
	return l.Monthly
}

// Load returns a connection's limits, or its starting ones.
func Load(ctx context.Context, q jobs.Querier, ws, id string) (Limits, error) {
	var l Limits
	var unit *string
	var daily, monthly *float64
	var stop *bool
	err := q.QueryRow(ctx, `
		SELECT i.name, i.connector, l.unit, l.daily_limit::float8, l.monthly_limit::float8, l.hard_stop
		FROM integrations i LEFT JOIN connection_limits l ON l.integration_id = i.id AND l.workspace_id = i.workspace_id
		WHERE i.workspace_id = $1 AND i.id = $2`, ws, id).Scan(&l.Name, &l.Connector, &unit, &daily, &monthly, &stop)
	if errors.Is(err, pgx.ErrNoRows) {
		return l, ErrNotFound
	}
	if err != nil {
		return l, err
	}
	if unit == nil {
		l.Stop, l.Default = true, true
		if u, m, ok := Starting(l.Connector); ok {
			l.Unit, l.Monthly = u, &m
		}
		return l, nil
	}
	l.Unit, l.Daily, l.Monthly, l.Stop = *unit, daily, monthly, *stop
	return l, nil
}

// Save sets a connection's limits. A limit left nil is no limit.
func Save(ctx context.Context, q jobs.Querier, ws, id string, l Limits, actorID *string) error {
	if !ValidUnit(l.Unit) {
		return fmt.Errorf("usagelimits: bad unit %q", l.Unit)
	}
	_, err := q.Exec(ctx, `
		INSERT INTO connection_limits (workspace_id, integration_id, unit, daily_limit, monthly_limit, hard_stop, updated_at, updated_by)
		VALUES ($1, $2, $3, $4, $5, $6, now(), $7)
		ON CONFLICT (workspace_id, integration_id) DO UPDATE SET unit = EXCLUDED.unit, daily_limit = EXCLUDED.daily_limit,
			monthly_limit = EXCLUDED.monthly_limit, hard_stop = EXCLUDED.hard_stop, updated_at = now(), updated_by = EXCLUDED.updated_by`,
		ws, id, l.Unit, l.Daily, l.Monthly, l.Stop, actorID)
	return err
}

// Reset removes a connection's own limits, so its starting limit (or none)
// applies again.
func Reset(ctx context.Context, q jobs.Querier, ws, id string) error {
	_, err := q.Exec(ctx, `DELETE FROM connection_limits WHERE workspace_id = $1 AND integration_id = $2`, ws, id)
	return err
}

// Used is what a connection spent today and this month (UTC), in a unit.
type Used struct {
	Unit  string  `json:"unit"`
	Today float64 `json:"today"`
	Month float64 `json:"month"`
}

// Of returns the usage for a period.
func (u Used) Of(period string) float64 {
	if period == Day {
		return u.Today
	}
	return u.Month
}

// Starts are the first instants of t's day and month, in UTC.
func Starts(t time.Time) (day, month time.Time) {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC), time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// amountSQL is one ledger entry's amount in $unit: US dollars count the
// exact dollars an SEO call booked as units, else its cents; any other
// unit counts the entry's units of that name.
const amountSQL = `CASE WHEN $3 = 'USD' THEN
		CASE WHEN unit_name = 'USD' AND units IS NOT NULL THEN units
		     WHEN currency = 'USD' AND amount_cents IS NOT NULL THEN amount_cents / 100.0 ELSE 0 END
	WHEN unit_name = $3 THEN COALESCE(units, 0) ELSE 0 END`

// Spent sums a connection's ledger entries today and this month, in unit.
func Spent(ctx context.Context, q jobs.Querier, ws, id, unit string, now time.Time) (Used, error) {
	day, month := Starts(now)
	u := Used{Unit: unit}
	err := q.QueryRow(ctx, `SELECT COALESCE(sum(`+amountSQL+`) FILTER (WHERE occurred_at >= $5), 0)::float8, COALESCE(sum(`+amountSQL+`), 0)::float8
		FROM spend_entries WHERE workspace_id = $1 AND integration_id = $2 AND occurred_at >= $4`,
		ws, id, unit, month, day).Scan(&u.Today, &u.Month)
	return u, err
}

// DayUsage is one day's usage, for a chart.
type DayUsage struct {
	Date  string  `json:"date"`
	Used  float64 `json:"used"`
	Calls int64   `json:"calls"`
}

// Days is a connection's usage per day from from (UTC) on, in unit.
func Days(ctx context.Context, q interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}, ws, id, unit string, from time.Time) ([]DayUsage, error) {
	rows, err := q.Query(ctx, `SELECT to_char(date_trunc('day', occurred_at AT TIME ZONE 'UTC'), 'YYYY-MM-DD'), COALESCE(sum(`+amountSQL+`), 0)::float8, count(*)
		FROM spend_entries WHERE workspace_id = $1 AND integration_id = $2 AND occurred_at >= $4
		GROUP BY 1 ORDER BY 1`, ws, id, unit, from)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (DayUsage, error) {
		var d DayUsage
		return d, r.Scan(&d.Date, &d.Used, &d.Calls)
	})
}

// Error means a call would take a connection past one of its limits.
type Error struct {
	Name   string
	Period string // day or month
	Unit   string
	Limit  float64
	Used   float64
	Cost   float64 // the call's estimated cost, 0 when unknown
}

func (e *Error) Error() string {
	when, next := "daily", "tomorrow (UTC)"
	if e.Period == Month {
		when, next = "monthly", "next month"
	}
	s := fmt.Sprintf("%s reached its %s limit of %s (%s used", e.Name, when, Amount(e.Unit, e.Limit), Amount(e.Unit, e.Used))
	if e.Cost > 0 {
		s += fmt.Sprintf("; this call would cost about %s", Amount(e.Unit, e.Cost))
	}
	return s + "). Raise the limit on the connection's page under Connectors, or wait until " + next + "."
}

// Code is the error code the API and extensions report.
func (e *Error) Code() string { return "usage_limit_reached" }

// Check reports whether a call costing about cost (in unit; 0 when it
// can't be told) may run on the connection: a *Error when it would take
// the connection past a limit that stops calls. A cost in another unit
// than the limits' counts as 0, so the call stops only once a limit is
// used up.
func Check(ctx context.Context, q jobs.Querier, ws, id, unit string, cost float64) error {
	l, err := Load(ctx, q, ws, id)
	if errors.Is(err, ErrNotFound) {
		return nil // the caller finds out it is gone
	}
	if err != nil || !l.Stop || !l.Has() {
		return err
	}
	if unit != l.Unit || cost < 0 {
		cost = 0
	}
	u, err := Spent(ctx, q, ws, id, l.Unit, time.Now())
	if err != nil {
		return err
	}
	for _, p := range []string{Day, Month} {
		lim := l.Of(p)
		if lim == nil {
			continue
		}
		used := u.Of(p)
		if over(used, cost, *lim) {
			metrics.ConnectionLimitStop(l.Connector, p)
			return &Error{Name: l.Name, Period: p, Unit: l.Unit, Limit: *lim, Used: used, Cost: cost}
		}
	}
	return nil
}

// Allowance is how much more, in unit, the connection may spend today
// before a limit stops it, and whether any limit stops it at all. Limits
// counted in another unit stop it only once used up (left 0 then).
func Allowance(ctx context.Context, q jobs.Querier, ws, id, unit string) (left float64, limited bool, err error) {
	l, err := Load(ctx, q, ws, id)
	if errors.Is(err, ErrNotFound) {
		return 0, false, nil
	}
	if err != nil || !l.Stop || !l.Has() {
		return 0, false, err
	}
	u, err := Spent(ctx, q, ws, id, l.Unit, time.Now())
	if err != nil {
		return 0, false, err
	}
	left = math.Inf(1)
	for _, p := range []string{Day, Month} {
		lim := l.Of(p)
		if lim == nil {
			continue
		}
		rest := math.Max(0, *lim-u.Of(p))
		if l.Unit != unit && rest > 0 {
			continue
		}
		if rest < left {
			left = rest
		}
		if rest <= 1e-9 {
			metrics.ConnectionLimitStop(l.Connector, p)
		}
	}
	if math.IsInf(left, 1) {
		return 0, false, nil
	}
	return left, true, nil
}

// over: with a known cost, the call would pass the limit; without one,
// the limit is used up.
func over(used, cost, limit float64) bool {
	const eps = 1e-9
	if cost > 0 {
		return used+cost > limit+eps
	}
	return used >= limit-eps
}

// Booked checks, after a charge to the connection was booked, whether
// today's or this month's usage now reached 80% or all of a limit, and
// warns the workspace once a period for each: an audit event (and
// webhook), and an email to each owner and admin, sent by a job. Pass the
// transaction that booked the charge.
func Booked(ctx context.Context, q jobs.Querier, ws, id string) error {
	l, err := Load(ctx, q, ws, id)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil || !l.Has() {
		return err
	}
	now := time.Now()
	u, err := Spent(ctx, q, ws, id, l.Unit, now)
	if err != nil {
		return err
	}
	dayStart, monthStart := Starts(now)
	for _, p := range []string{Day, Month} {
		lim := l.Of(p)
		if lim == nil || *lim <= 0 {
			continue
		}
		used := u.Of(p)
		level := 0
		switch {
		case used >= *lim-1e-9:
			level = 100
		case used >= *lim*0.8:
			level = 80
		default:
			continue
		}
		start := monthStart
		if p == Day {
			start = dayStart
		}
		if level == 100 {
			// Reaching the limit in one step also counts as the warning,
			// so the 80% email never follows the "reached" one.
			if _, err := q.Exec(ctx, `INSERT INTO connection_limit_alerts (workspace_id, integration_id, period, period_start, level, unit, limit_value, used)
				VALUES ($1, $2, $3, $4, 80, $5, $6, $7) ON CONFLICT DO NOTHING`, ws, id, p, start, l.Unit, *lim, used); err != nil {
				return err
			}
		}
		var inserted bool
		err := q.QueryRow(ctx, `INSERT INTO connection_limit_alerts (workspace_id, integration_id, period, period_start, level, unit, limit_value, used)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8) ON CONFLICT DO NOTHING RETURNING true`, ws, id, p, start, level, l.Unit, *lim, used).Scan(&inserted)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		action := "connection_limit.warned"
		if level == 100 {
			action = "connection_limit.reached"
		}
		data := map[string]any{"integration_id": id, "connector": l.Connector, "period": p, "period_start": start.Format(time.DateOnly),
			"level": level, "unit": l.Unit, "limit": *lim, "used": round(used), "hard_stop": l.Stop}
		if err := audit.Record(ctx, q, audit.Event{WorkspaceID: ws, ActorType: "system", Action: action, TargetType: "integration", TargetID: id,
			Data: data, Props: map[string]any{"connector": l.Connector, "period": p, "level": level, "hard_stop": l.Stop}}); err != nil {
			return err
		}
		metrics.ConnectionLimitAlert(level, p)
		if err := enqueueEmails(ctx, q, ws, id, p, start, level); err != nil {
			return err
		}
	}
	return nil
}

func round(v float64) float64 { return math.Round(v*10000) / 10000 }

// enqueueEmails queues a warning email to each owner and admin with a
// confirmed address.
func enqueueEmails(ctx context.Context, q jobs.Querier, ws, id, period string, start time.Time, level int) error {
	var users []string
	if err := q.QueryRow(ctx, `
		SELECT COALESCE(array_agg(u.id::text ORDER BY u.id), '{}') FROM memberships m JOIN users u ON u.id = m.user_id
		WHERE m.workspace_id = $1 AND m.role IN ('owner', 'admin') AND u.email_verified_at IS NOT NULL`, ws).Scan(&users); err != nil {
		return err
	}
	for _, u := range users {
		key := fmt.Sprintf("%s:%s:%s:%s:%d:%s", ws, id, period, start.Format(time.DateOnly), level, u)
		if _, err := jobs.Enqueue(ctx, q, jobs.NewJob{Kind: JobAlert, WorkspaceID: &ws, IdempotencyKey: key, MaxAttempts: 5,
			Payload: map[string]any{"user_id": u, "integration_id": id, "period": period, "period_start": start.Format(time.DateOnly), "level": level}}); err != nil {
			return err
		}
	}
	return nil
}

// Amount writes a usage for people: $1.23, 1,500 API units, 1 credit.
func Amount(unit string, v float64) string {
	if unit == USD {
		return "$" + strconv.FormatFloat(math.Round(v*100)/100, 'f', 2, 64)
	}
	r := math.Round(v)
	s := strconv.FormatFloat(r, 'f', 0, 64)
	if math.Abs(v-r) > 1e-9 && math.Abs(v) < 100 {
		s = strconv.FormatFloat(math.Round(v*100)/100, 'f', -1, 64)
	}
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	whole, frac, _ := strings.Cut(s, ".")
	for i := len(whole) - 3; i > 0; i -= 3 {
		whole = whole[:i] + "," + whole[i:]
	}
	if frac != "" {
		whole += "." + frac
	}
	if neg {
		whole = "-" + whole
	}
	name := unit
	if !(r == 1 && math.Abs(v-1) < 1e-9) && !strings.HasSuffix(unit, "s") {
		name += "s"
	}
	return whole + " " + name
}
