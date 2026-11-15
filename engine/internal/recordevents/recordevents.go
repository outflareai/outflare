// Package recordevents puts what happens in other modules on people's and
// companies' timelines as customer events: deals (deal.created, deal.won,
// deal.lost, deal.stage_changed, deal.reopened), support conversations
// (support.conversation_opened, support.conversation_closed,
// support.conversation_reopened) and CRM tasks (task.created,
// task.completed, task.reopened).
//
// A deal, a conversation or a task changes in many places (the app, the
// API, a CRM sync, an import, a journey, a database trigger), so rather
// than record an event at each, a loop compares every row with the last
// state it recorded (event_state) and records what is new. Each event has
// a message id of its own, so recording it twice stores it once. Rules,
// journeys and metrics then act on these like on any other event.
//
// Rows from before the loop existed carry event_state 'history': their
// state is noted and nothing is recorded, so the past sets nothing off.
package recordevents

import (
	"context"
	"encoding/json"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/track"
)

// history is the event_state of a row from before the loop existed.
const history = "history"

const batch = 500

// row is one deal, conversation or task waiting for its events.
type row struct {
	id, ws, record, cur string
	prev                *string
	props               map[string]any
	opened, updated     time.Time
	closed              *time.Time
}

// event is one event to record for a row.
type event struct {
	name string
	at   time.Time
}

// module is one kind of row the loop follows.
type module struct {
	name    string // deals, support or tasks: the events' source
	table   string
	pending string // SQL condition (alias t) for rows waiting for an event
	// query selects, for the waiting rows: id, workspace, the record the
	// events go on, event_state, the state now, properties, when it
	// opened, when it last changed and when it closed.
	query  string
	events func(r row) []event
}

func closedAt(r row) time.Time {
	if r.closed != nil {
		return *r.closed
	}
	return r.updated
}

var modules = []module{
	{
		name: "deals", table: "deals", pending: `t.event_state IS DISTINCT FROM (t.state || ':' || t.stage)`,
		// A deal's events go on its person, else its company, else the
		// deal's own record.
		query: `SELECT t.id::text, t.workspace_id::text,
				COALESCE(t.record_id, (SELECT l.to_id FROM record_relationships l WHERE l.from_id = t.id AND l.kind = 'associated_company'
					AND l.valid_to IS NULL ORDER BY l.valid_from, l.id LIMIT 1), t.id)::text,
				t.event_state, t.state || ':' || t.stage,
				jsonb_strip_nulls(jsonb_build_object('deal_id', t.id, 'state', t.state, 'stage', NULLIF(t.stage, ''), 'amount_minor', t.amount_cents,
					'currency', CASE WHEN t.amount_cents IS NOT NULL THEN t.currency END, 'deal_source', t.source,
					'lost_reason', CASE WHEN t.state = 'lost' THEN (SELECT r.attributes ->> 'lost_reason' FROM records r WHERE r.id = t.id) END)),
				t.opened_at, t.updated_at, t.closed_at
			FROM deals t`,
		events: func(r row) []event {
			state, stage, _ := strings.Cut(r.cur, ":")
			if r.prev == nil {
				out := []event{{"deal.created", r.opened}}
				if state != "open" {
					out = append(out, event{"deal." + state, closedAt(r)})
				}
				return out
			}
			was, wasStage, _ := strings.Cut(*r.prev, ":")
			switch {
			case was != state && state == "open":
				return []event{{"deal.reopened", r.updated}}
			case was != state:
				return []event{{"deal." + state, closedAt(r)}}
			case state == "open" && wasStage != stage:
				r.props["from_stage"] = wasStage
				return []event{{"deal.stage_changed", r.updated}}
			}
			return nil
		},
	},
	{
		name: "support", table: "support_conversations",
		pending: `t.record_id IS NOT NULL AND t.event_state IS DISTINCT FROM (CASE WHEN t.status = 'closed' THEN 'closed' ELSE 'open' END)`,
		query: `SELECT t.id::text, t.workspace_id::text, t.record_id::text, t.event_state, CASE WHEN t.status = 'closed' THEN 'closed' ELSE 'open' END,
				jsonb_strip_nulls(jsonb_build_object('conversation_id', t.id, 'channel', t.channel, 'topic_id', t.topic_id,
					'sla_breaches', t.sla_breaches)),
				t.first_message_at, t.updated_at, t.closed_at
			FROM support_conversations t`,
		events: func(r row) []event {
			switch {
			case r.prev == nil && r.cur == "closed":
				return []event{{"support.conversation_opened", r.opened}, {"support.conversation_closed", closedAt(r)}}
			case r.prev == nil:
				return []event{{"support.conversation_opened", r.opened}}
			case r.cur == "closed":
				return []event{{"support.conversation_closed", closedAt(r)}}
			}
			return []event{{"support.conversation_reopened", r.updated}}
		},
	},
	{
		name: "tasks", table: "crm_activities", pending: `t.kind = 'task' AND t.event_state IS DISTINCT FROM t.status`,
		query: `SELECT t.id::text, t.workspace_id::text, t.record_id::text, t.event_state, t.status,
				jsonb_strip_nulls(jsonb_build_object('task_id', t.id, 'priority', t.priority, 'due_at', t.due_at, 'assigned_to', t.assigned_to)),
				t.created_at, t.updated_at, NULL::timestamptz
			FROM crm_activities t`,
		events: func(r row) []event {
			switch {
			case r.prev == nil && r.cur == "done":
				return []event{{"task.created", r.opened}, {"task.completed", r.updated}}
			case r.prev == nil:
				return []event{{"task.created", r.opened}}
			case r.cur == "done":
				return []event{{"task.completed", r.updated}}
			}
			return []event{{"task.reopened", r.updated}}
		},
	},
}

// Run records new events every interval until ctx ends.
func Run(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		for _, m := range modules {
			for {
				n, err := m.round(ctx, pool, batch)
				if err != nil {
					if ctx.Err() == nil {
						track.Fail(ctx, log, track.Event{Name: "record_events.run_failed", ActorType: "system", Surface: "system",
							Props: map[string]any{"module": m.name}}, "internal", err)
					}
					break
				}
				if n < batch {
					break
				}
			}
			var pending int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM `+m.table+` t WHERE `+m.pending).Scan(&pending); err == nil {
				metrics.SetRecordEventsPending(m.name, pending)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Round records the events of up to limit rows of every module, and
// returns how many rows it looked at.
func Round(ctx context.Context, pool *pgxpool.Pool, limit int) (int, error) {
	total := 0
	for _, m := range modules {
		n, err := m.round(ctx, pool, limit)
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

func (m module) round(ctx context.Context, pool *pgxpool.Pool, limit int) (int, error) {
	n := 0
	recorded := map[string]map[string]int{}
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, m.query+` WHERE `+m.pending+` ORDER BY t.id LIMIT $1 FOR UPDATE OF t SKIP LOCKED`, limit)
		if err != nil {
			return err
		}
		var list []row
		for rows.Next() {
			var r row
			var props []byte
			if err := rows.Scan(&r.id, &r.ws, &r.record, &r.prev, &r.cur, &props, &r.opened, &r.updated, &r.closed); err != nil {
				rows.Close()
				return err
			}
			r.props = map[string]any{}
			_ = json.Unmarshal(props, &r.props)
			list = append(list, r)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		now := time.Now()
		for _, r := range list {
			if r.prev == nil || *r.prev != history {
				for _, ev := range m.events(r) {
					at := ev.at
					if at.After(now) {
						at = now
					}
					ok, err := customers.Record(ctx, tx, r.ws, customers.Event{RecordID: r.record, Name: ev.name, Properties: r.props,
						Source: m.name, OccurredAt: at, MessageID: m.name + ":" + r.id + ":" + ev.name + ":" + strconv.FormatInt(r.updated.UnixMilli(), 10)})
					if err != nil {
						return err
					}
					if ok {
						if recorded[r.ws] == nil {
							recorded[r.ws] = map[string]int{}
						}
						recorded[r.ws][ev.name]++
					}
				}
			}
			if _, err := tx.Exec(ctx, `UPDATE `+m.table+` SET event_state = $2 WHERE id = $1`, r.id, r.cur); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	if err != nil {
		return n, err
	}
	for ws, by := range recorded {
		total := 0
		for name, c := range by {
			metrics.RecordEventsRecorded(m.name, name, c)
			total += c
		}
		track.Emit(ctx, track.Event{Name: "record_events.recorded", WorkspaceID: ws, ActorType: "system", Surface: "job",
			Props: map[string]any{"module": m.name, "events": total}})
	}
	return n, nil
}
