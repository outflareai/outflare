// Package recmetrics keeps live metrics per company and person: totals
// over a rolling window, such as spend in the last 30 days, optionally per
// product (docs/metrics.md). Values live in per-day buckets, are updated
// as events arrive, and are written to the record's attributes under
// "metrics", so rules, journeys, smart lists and Query read them like any
// field. A backfill from stored events fires nothing: values it writes are
// the starting point, so a record already past a rule's threshold counts
// as having crossed it.
package recmetrics

import (
	"context"
	"regexp"
	"slices"
	"strconv"

	"github.com/jackc/pgx/v5"
)

// Aggregates.
const (
	Count = "count" // matching events
	Sum   = "sum"   // of property
	Min   = "min"   // of property
	Max   = "max"   // of property
	Days  = "days"  // days with a matching event
	Last  = "last"  // when the latest matching event happened
	// Latest is the value of property in the latest matching event: a
	// balance, a limit or a seat count the product reports.
	Latest = "latest"
)

// Levels.
const (
	LevelCompany = "company"
	LevelPerson  = "person"
)

// NoProduct is the product id of a metric's overall value.
const NoProduct = "00000000-0000-0000-0000-000000000000"

// Definition is one metric.
type Definition struct {
	ID          string   `json:"id"`
	Key         string   `json:"key"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Levels      []string `json:"levels"`
	Aggregate   string   `json:"aggregate"`
	Events      []string `json:"events"`
	Usage       bool     `json:"usage"`
	Property    string   `json:"property,omitempty"`
	WindowDays  int      `json:"window_days"`
	OffsetDays  int      `json:"offset_days"`
	PerProduct  bool     `json:"per_product"`
	Version     int      `json:"version"`
}

var both = []string{LevelCompany, LevelPerson}

// Defaults are the metrics a brand starts with when it turns metrics on.
// Each can be changed or archived.
var Defaults = []Definition{
	{Key: "spend_30d", Name: "Spend, last 30 days", Description: "Payments in the last 30 days, in minor units (cents).",
		Levels: both, Aggregate: Sum, Events: []string{"payment.succeeded"}, Property: "amount_minor", WindowDays: 30, PerProduct: true},
	{Key: "payments_90d", Name: "Payments, last 90 days", Description: "How many payments in the last 90 days.",
		Levels: both, Aggregate: Count, Events: []string{"payment.succeeded"}, WindowDays: 90, PerProduct: true},
	{Key: "refunds_90d", Name: "Refunds, last 90 days", Description: "Refunds in the last 90 days, in minor units (cents).",
		Levels: both, Aggregate: Sum, Events: []string{"payment.refunded"}, Property: "amount_minor", WindowDays: 90, PerProduct: true},
	{Key: "usage_7d", Name: "Usage, last 7 days", Description: "Events the product sent in the last 7 days.",
		Levels: both, Aggregate: Count, Usage: true, WindowDays: 7, PerProduct: true},
	{Key: "usage_prev_7d", Name: "Usage, the 7 days before", Description: "Events the product sent in the 7 days before the last 7: compare with usage_7d.",
		Levels: both, Aggregate: Count, Usage: true, WindowDays: 7, OffsetDays: 7, PerProduct: true},
	{Key: "active_days_30d", Name: "Active days, last 30 days", Description: "Days in the last 30 with any usage.",
		Levels: []string{LevelPerson}, Aggregate: Days, Usage: true, WindowDays: 30, PerProduct: true},
	{Key: "last_seen", Name: "Last seen", Description: "When the product last sent an event about them.",
		Levels: both, Aggregate: Last, Usage: true, WindowDays: 365, PerProduct: true},
}

// KeyRE is what a metric's key looks like.
var KeyRE = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)

// Seed writes the default metrics of a brand that has none, and returns
// their ids.
func Seed(ctx context.Context, tx pgx.Tx, ws, brand string) ([]string, error) {
	var has bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM metric_definitions WHERE brand_id = $1)`, brand).Scan(&has); err != nil || has {
		return nil, err
	}
	var ids []string
	for _, d := range Defaults {
		id, err := Insert(ctx, tx, ws, brand, d)
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// Aggregates are the ways a metric counts.
var Aggregates = []string{Count, Sum, Min, Max, Days, Last, Latest}

var eventNameRE = regexp.MustCompile(`^[a-z][a-z0-9_]*([._][a-z0-9_]+)*$`)

// Check reports what is wrong with a metric, or "".
func Check(d Definition) string {
	reads := d.Aggregate == Sum || d.Aggregate == Min || d.Aggregate == Max || d.Aggregate == Latest
	switch {
	case d.Name == "" || len(d.Name) > 100:
		return "name is 1 to 100 characters"
	case len(d.Description) > 500:
		return "description is at most 500 characters"
	case !KeyRE.MatchString(d.Key) || d.Key == "by_product":
		return "key is lowercase letters, digits and underscores, such as spend_30d (not by_product)"
	case len(d.Levels) == 0 || len(d.Levels) > 2:
		return "levels are company, person or both"
	case !slices.Contains(Aggregates, d.Aggregate):
		return "aggregate is count, sum, min, max, days, last or latest"
	case len(d.Events) == 0 && !d.Usage:
		return "a metric counts events (their names) or usage, or both"
	case len(d.Events) > 20:
		return "a metric names at most 20 events"
	case reads && !KeyRE.MatchString(d.Property):
		return "sum, min, max and latest read a number property of the events, such as amount_minor"
	case !reads && d.Property != "":
		return "only sum, min, max and latest read a property"
	case d.WindowDays < 1 || d.WindowDays > 365:
		return "window_days is 1 to 365"
	case d.OffsetDays < 0 || d.OffsetDays > 365:
		return "offset_days is 0 to 365"
	}
	for _, l := range d.Levels {
		if l != LevelCompany && l != LevelPerson {
			return "levels are company, person or both"
		}
	}
	for _, e := range d.Events {
		if len(e) > 100 || !eventNameRE.MatchString(e) {
			return "events are event names, such as payment.succeeded"
		}
	}
	return ""
}

// Insert stores a metric and returns its id.
func Insert(ctx context.Context, tx pgx.Tx, ws, brand string, d Definition) (string, error) {
	var id string
	events := d.Events
	if events == nil {
		events = []string{}
	}
	err := tx.QueryRow(ctx, `INSERT INTO metric_definitions (workspace_id, brand_id, key, name, description, levels, aggregate, events, usage, property,
			window_days, offset_days, per_product)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, NULLIF($10, ''), $11, $12, $13) RETURNING id::text`,
		ws, brand, d.Key, d.Name, d.Description, d.Levels, d.Aggregate, events, d.Usage, d.Property, d.WindowDays, d.OffsetDays, d.PerProduct).Scan(&id)
	return id, err
}

// Columns is what Load selects, in the order scan reads.
const Columns = `id::text, key, name, description, levels, aggregate, events, usage, COALESCE(property, ''), window_days, offset_days, per_product, version`

// Scan reads a definition selected with Columns.
func Scan(row pgx.Row) (Definition, error) {
	var d Definition
	err := row.Scan(&d.ID, &d.Key, &d.Name, &d.Description, &d.Levels, &d.Aggregate, &d.Events, &d.Usage, &d.Property, &d.WindowDays, &d.OffsetDays,
		&d.PerProduct, &d.Version)
	return d, err
}

// span is how many days of buckets a metric needs: its window and offset.
func (d Definition) span() int { return d.WindowDays + d.OffsetDays }

// valueSQL is SQL for a metric's value from its buckets aliased b (one
// record and product), within the window: a JSON value, or NULL.
func (d Definition) valueSQL() string {
	inWindow := `b.day BETWEEN (now() AT TIME ZONE 'UTC')::date - ` + strconv.Itoa(d.span()-1) + ` AND (now() AT TIME ZONE 'UTC')::date - ` + strconv.Itoa(d.OffsetDays)
	switch d.Aggregate {
	case Count:
		return `to_jsonb(COALESCE(sum(b.n) FILTER (WHERE ` + inWindow + `), 0))`
	case Sum:
		return `to_jsonb(COALESCE(sum(b.total) FILTER (WHERE ` + inWindow + `), 0))`
	case Min:
		return `to_jsonb(min(b.lo) FILTER (WHERE ` + inWindow + `))`
	case Max:
		return `to_jsonb(max(b.hi) FILTER (WHERE ` + inWindow + `))`
	case Days:
		return `to_jsonb(count(*) FILTER (WHERE ` + inWindow + ` AND b.n > 0))`
	case Latest:
		return `to_jsonb((array_agg(b.total ORDER BY b.last_at DESC) FILTER (WHERE ` + inWindow + `))[1])`
	default: // Last
		return `to_jsonb(to_char(max(b.last_at) FILTER (WHERE ` + inWindow + `) AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'))`
	}
}
