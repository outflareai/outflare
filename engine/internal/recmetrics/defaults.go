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
	default: // Last
		return `to_jsonb(to_char(max(b.last_at) FILTER (WHERE ` + inWindow + `) AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'))`
	}
}
