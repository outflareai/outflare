package rules

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/journeys"
)

// Simulation is what a rule would have done: a dry run that writes
// nothing and sends nothing.
type Simulation struct {
	WindowDays int `json:"window_days"`
	// Records it would have fired for, and how many times in all.
	Records int `json:"records"`
	Firings int `json:"firings"`
	// Up to 20 of those records.
	Sample []string `json:"sample"`
	// Events or records looked at, and whether there were more than the
	// limit (the numbers are then a lower bound).
	Checked   int    `json:"checked"`
	Truncated bool   `json:"truncated"`
	Note      string `json:"note"`
}

const (
	// MaxSimulateDays is the furthest back a simulation looks.
	MaxSimulateDays = 90
	simulateLimit   = 50000
	simulateTimeout = "15s"
	sampleSize      = 20
)

// Simulate runs a rule over the last days of events (or over records and
// list members as they are now) without firing it. It reads Postgres, so
// it only sees events the event retention keeps there.
func Simulate(ctx context.Context, pool *pgxpool.Pool, r Rule, days int) (Simulation, error) {
	sim := Simulation{WindowDays: days, Sample: []string{}}
	err := pgx.BeginTxFunc(ctx, pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SET LOCAL statement_timeout = '`+simulateTimeout+`'`); err != nil {
			return err
		}
		window := fmt.Sprintf("%d days", days)
		fired := map[string]int{}
		last := map[string]time.Time{}
		hit := func(id string, at time.Time) {
			if r.Fire == "once" && fired[id] > 0 {
				return
			}
			if t, ok := last[id]; ok && r.Cooldown > 0 && at.Sub(t) < time.Duration(r.Cooldown)*time.Second {
				return
			}
			if fired[id] == 0 && len(sim.Sample) < sampleSize {
				sim.Sample = append(sim.Sample, id)
			}
			fired[id]++
			last[id] = at
			sim.Firings++
		}
		var rows pgx.Rows
		var err error
		switch r.Trigger.Kind {
		case KindEvent:
			sim.Note = "Each matching event in the window, as if the rule had been active."
			rows, err = tx.Query(ctx, `SELECT e.record_id::text, r.brand_id::text, r.attributes, e.properties, e.occurred_at
				FROM customer_events e JOIN records r ON r.id = e.record_id AND r.merged_into IS NULL AND r.archived_at IS NULL AND r.record_type = $3
				WHERE e.workspace_id = $1 AND e.name = $2 AND e.occurred_at >= now() - $4::interval AND e.source <> 'browser'
				ORDER BY e.occurred_at LIMIT $5`, r.WorkspaceID, r.Trigger.Event, r.RecordType, window, simulateLimit+1)
		case KindAbsence:
			sim.Note = "Each start event in the window whose time has run out with no " + r.Trigger.Event + " after it. Fields are tested as they are now."
			rows, err = tx.Query(ctx, `SELECT s.record_id::text, r.brand_id::text, r.attributes, s.properties, s.occurred_at
				FROM customer_events s JOIN records r ON r.id = s.record_id AND r.merged_into IS NULL AND r.archived_at IS NULL AND r.record_type = $3
				WHERE s.workspace_id = $1 AND s.name = $2 AND s.occurred_at >= now() - $4::interval
				  AND s.occurred_at <= now() - make_interval(secs => $6) AND s.source <> 'browser'
				  AND NOT EXISTS (SELECT 1 FROM customer_events t WHERE t.workspace_id = $1 AND t.record_id = s.record_id AND t.name = $7
					AND t.occurred_at > s.occurred_at AND t.occurred_at <= s.occurred_at + make_interval(secs => $6))
				ORDER BY s.occurred_at LIMIT $5`, r.WorkspaceID, r.Trigger.Start(), r.RecordType, window, simulateLimit+1, r.Trigger.WithinSeconds, r.Trigger.Event)
		case KindChange:
			sim.Note = "Records whose fields meet the conditions now. The rule fires when a change makes them true, so these are who it would have fired for had each come to match while it was active."
			rows, err = tx.Query(ctx, `SELECT r.id::text, r.brand_id::text, r.attributes, '{}'::jsonb, r.updated_at FROM records r
				WHERE r.workspace_id = $1 AND r.record_type = $2 AND r.merged_into IS NULL AND r.archived_at IS NULL
				ORDER BY r.updated_at DESC LIMIT $3`, r.WorkspaceID, r.RecordType, simulateLimit+1)
		case KindAudience:
			if r.Trigger.On == "leave" {
				sim.Note = "A leave rule fires for people who leave the list from now on; the list's past leavers are not kept."
				return nil
			}
			sim.Note = "People in the list now. The rule fires for people who join from when it is active."
			rows, err = tx.Query(ctx, `SELECT m.record_id::text, r.brand_id::text, r.attributes, '{}'::jsonb, m.entered_at
				FROM smart_list_members m JOIN records r ON r.id = m.record_id AND r.merged_into IS NULL AND r.archived_at IS NULL
				WHERE m.workspace_id = $1 AND m.list_id = $2 ORDER BY m.entered_at LIMIT $3`, r.WorkspaceID, r.Trigger.ListID, simulateLimit+1)
		default:
			return fmt.Errorf("rules: unknown trigger kind %q", r.Trigger.Kind)
		}
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var sub subject
			var attrs, props []byte
			var at time.Time
			if err := rows.Scan(&sub.id, &sub.brandID, &attrs, &props, &at); err != nil {
				return err
			}
			sim.Checked++
			if sim.Checked > simulateLimit {
				sim.Checked, sim.Truncated = simulateLimit, true
				break
			}
			sub.attributes = map[string]any{}
			_ = json.Unmarshal(attrs, &sub.attributes)
			event := map[string]any{}
			_ = json.Unmarshal(props, &event)
			if !r.inBrand(&sub) || !journeys.Holds(sub.attributes, event, r.Conditions, r.Match) {
				continue
			}
			hit(sub.id, at)
		}
		sim.Records = len(fired)
		return rows.Err()
	})
	return sim, err
}
