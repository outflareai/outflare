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
	// Where the events came from: postgres (the events it keeps) or
	// clickhouse (the event store, which keeps them all).
	Source string `json:"source"`
}

// ArchivedEvent is one event read from the event store.
type ArchivedEvent struct {
	RecordID   string
	Properties map[string]any
	OccurredAt time.Time
}

// EventArchive reads events kept beyond Postgres's retention: the
// ClickHouse event store (engine/internal/eventstore), when one is set up.
type EventArchive interface {
	RuleEvents(ctx context.Context, ws, name string, since time.Time, limit int) ([]ArchivedEvent, error)
}

const (
	// MaxSimulateDays is the furthest back a simulation looks in Postgres,
	// and MaxArchiveDays in the event store (event rules only).
	MaxSimulateDays = 90
	MaxArchiveDays  = 365
	simulateLimit   = 50000
	simulateTimeout = "15s"
	sampleSize      = 20
)

// Simulate runs a rule over the last days of events (or over records and
// list members as they are now) without firing it. An event rule reads its
// events from archive when there is one (every event, up to
// MaxArchiveDays); otherwise everything comes from Postgres, which only
// keeps what the event retention keeps.
func Simulate(ctx context.Context, pool *pgxpool.Pool, r Rule, days int, archive EventArchive) (Simulation, error) {
	if (r.Trigger.Kind == KindEvent || r.Trigger.Kind == KindAbsence) && archive != nil {
		return simulateArchived(ctx, pool, r, days, archive)
	}
	sim := Simulation{WindowDays: days, Sample: []string{}, Source: "postgres"}
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
				FROM (SELECT record_id, entered_at, workspace_id, list_id FROM smart_list_members UNION ALL
					SELECT record_id, entered_at, workspace_id, list_id FROM static_list_members) m JOIN records r ON r.id = m.record_id AND r.merged_into IS NULL AND r.archived_at IS NULL
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

// tally counts firings as the engine would: once per record for a rule
// that fires once, and not again within the cooldown.
type tally struct {
	r     Rule
	sim   *Simulation
	fired map[string]int
	last  map[string]time.Time
}

func (t *tally) hit(id string, at time.Time) {
	if t.r.Fire == "once" && t.fired[id] > 0 {
		return
	}
	if l, ok := t.last[id]; ok && t.r.Cooldown > 0 && at.Sub(l) < time.Duration(t.r.Cooldown)*time.Second {
		return
	}
	if t.fired[id] == 0 && len(t.sim.Sample) < sampleSize {
		t.sim.Sample = append(t.sim.Sample, id)
	}
	t.fired[id]++
	t.last[id] = at
	t.sim.Firings++
}

// simulateArchived replays an event or absence rule over the event store's
// events, testing the records' fields as they are now.
func simulateArchived(ctx context.Context, pool *pgxpool.Pool, r Rule, days int, archive EventArchive) (Simulation, error) {
	sim := Simulation{WindowDays: days, Sample: []string{}, Source: "clickhouse",
		Note: "Each matching event in the window from the event store, as if the rule had been active. Fields are tested as they are now."}
	since := time.Now().AddDate(0, 0, -days)
	var events []ArchivedEvent
	var err error
	if r.Trigger.Kind == KindAbsence {
		sim.Note = "Each start event in the window from the event store whose time has run out with no " + r.Trigger.Event + " after it. Fields are tested as they are now."
		events, err = absentStarts(ctx, archive, r, since, &sim)
	} else {
		events, err = archive.RuleEvents(ctx, r.WorkspaceID, r.Trigger.Event, since, simulateLimit+1)
	}
	if err != nil {
		return sim, err
	}
	if len(events) > simulateLimit {
		events, sim.Truncated = events[:simulateLimit], true
	}
	ids := make([]string, 0, len(events))
	seen := map[string]bool{}
	for _, e := range events {
		if !seen[e.RecordID] {
			seen[e.RecordID] = true
			ids = append(ids, e.RecordID)
		}
	}
	subjects := map[string]*subject{}
	rows, err := pool.Query(ctx, `SELECT r.id::text, r.brand_id::text, r.attributes FROM records r
		WHERE r.workspace_id = $1 AND r.id = ANY ($2::uuid[]) AND r.record_type = $3 AND r.merged_into IS NULL AND r.archived_at IS NULL
		  AND r.deleted_at IS NULL`, r.WorkspaceID, ids, r.RecordType)
	if err != nil {
		return sim, err
	}
	for rows.Next() {
		sub := &subject{}
		var attrs []byte
		if err := rows.Scan(&sub.id, &sub.brandID, &attrs); err != nil {
			rows.Close()
			return sim, err
		}
		sub.attributes = map[string]any{}
		_ = json.Unmarshal(attrs, &sub.attributes)
		subjects[sub.id] = sub
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return sim, err
	}
	t := &tally{r: r, sim: &sim, fired: map[string]int{}, last: map[string]time.Time{}}
	for _, e := range events {
		sim.Checked++
		sub := subjects[e.RecordID]
		if sub == nil || !r.inBrand(sub) {
			continue
		}
		props := e.Properties
		if props == nil {
			props = map[string]any{}
		}
		if journeys.Holds(sub.attributes, props, r.Conditions, r.Match) {
			t.hit(sub.id, e.OccurredAt)
		}
	}
	sim.Records = len(t.fired)
	return sim, nil
}

// absentStarts reads an absence rule's start events since, from the event
// store, and keeps those whose time has run out with no follow-up event
// (the rule's event) after them within the window: what the rule's timers
// would have fired on. The follow-ups come from the store too, up to the
// same limit; past it the numbers are a lower bound.
func absentStarts(ctx context.Context, archive EventArchive, r Rule, since time.Time, sim *Simulation) ([]ArchivedEvent, error) {
	within := time.Duration(r.Trigger.WithinSeconds) * time.Second
	starts, err := archive.RuleEvents(ctx, r.WorkspaceID, r.Trigger.Start(), since, simulateLimit+1)
	if err != nil {
		return nil, err
	}
	follows := starts
	if r.Trigger.Event != r.Trigger.Start() {
		if follows, err = archive.RuleEvents(ctx, r.WorkspaceID, r.Trigger.Event, since, simulateLimit+1); err != nil {
			return nil, err
		}
	}
	if len(follows) > simulateLimit {
		follows, sim.Truncated = follows[:simulateLimit], true
	}
	// Each record's follow-up times, oldest first (the store returns them
	// in time order).
	after := map[string][]time.Time{}
	for _, f := range follows {
		after[f.RecordID] = append(after[f.RecordID], f.OccurredAt)
	}
	cutoff := time.Now().Add(-within)
	out := make([]ArchivedEvent, 0, len(starts))
	for _, s := range starts {
		if s.OccurredAt.After(cutoff) {
			continue // its time hasn't run out yet
		}
		followed := false
		for _, at := range after[s.RecordID] {
			if at.After(s.OccurredAt) && !at.After(s.OccurredAt.Add(within)) {
				followed = true
				break
			}
		}
		if !followed {
			out = append(out, s)
		}
	}
	return out, nil
}
