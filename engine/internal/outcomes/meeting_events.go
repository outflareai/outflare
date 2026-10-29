package outcomes

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Meetings on people's timelines. A meeting changes status in many places
// (sync from calendars and CRMs, a person in the app, the API), so rather
// than record an event at each, MeetingEvents compares every meeting tied
// to a person with the last status it recorded (event_status) and records
// what is new: meeting.booked first, then meeting.held, meeting.no_show or
// meeting.canceled. Each event has a message id of its own, so recording
// it twice stores it once, and a booked or held call can count as a
// conversion like any other customer event.

const meetingEventBatch = 500

// MeetingEventName is the customer event for a meeting status.
func MeetingEventName(status string) string { return "meeting." + status }

// RunMeetingEvents records new meeting events every interval until ctx ends.
func RunMeetingEvents(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		for {
			n, err := MeetingEvents(ctx, pool, meetingEventBatch)
			if err != nil {
				if ctx.Err() == nil {
					track.Fail(ctx, log, track.Event{Name: "meeting_events.run_failed", ActorType: "system", Surface: "system"}, "internal", err)
				}
				break
			}
			if n < meetingEventBatch {
				break
			}
		}
		var pending int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM meetings WHERE record_id IS NOT NULL AND event_status IS DISTINCT FROM status`).Scan(&pending); err == nil {
			metrics.SetMeetingEventsPending(pending)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// MeetingEvents records the events of up to limit meetings whose status
// changed since their last event, and returns how many meetings it looked
// at. A meeting is recorded once it is tied to a person.
func MeetingEvents(ctx context.Context, pool *pgxpool.Pool, limit int) (int, error) {
	n := 0
	recorded := map[string]map[string]int{}
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id::text, workspace_id::text, record_id::text, status, event_status, source, attribution, booked_at, starts_at, ends_at,
				updated_at, sequence_id::text, mission_id::text
			FROM meetings WHERE record_id IS NOT NULL AND event_status IS DISTINCT FROM status
			ORDER BY id LIMIT $1 FOR UPDATE SKIP LOCKED`, limit)
		if err != nil {
			return err
		}
		type meeting struct {
			id, ws, record, status, source, attribution string
			last                                        *string
			booked, starts, updated                     time.Time
			ends                                        *time.Time
			sequence, mission                           *string
		}
		var list []meeting
		for rows.Next() {
			var m meeting
			if err := rows.Scan(&m.id, &m.ws, &m.record, &m.status, &m.last, &m.source, &m.attribution, &m.booked, &m.starts, &m.ends,
				&m.updated, &m.sequence, &m.mission); err != nil {
				rows.Close()
				return err
			}
			list = append(list, m)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		now := time.Now()
		for _, m := range list {
			props := map[string]any{"meeting_id": m.id, "source": m.source, "attribution": m.attribution,
				"starts_at": m.starts.UTC().Format(time.RFC3339)}
			for k, v := range map[string]*string{"sequence_id": m.sequence, "mission_id": m.mission} {
				if v != nil {
					props[k] = *v
				}
			}
			record := func(status string, at time.Time) error {
				if at.After(now) {
					at = now
				}
				ok, err := customers.Record(ctx, tx, m.ws, customers.Event{RecordID: m.record, Name: MeetingEventName(status), Properties: props,
					Source: "meetings", OccurredAt: at, MessageID: "meeting:" + m.id + ":" + status})
				if err == nil && ok {
					if recorded[m.ws] == nil {
						recorded[m.ws] = map[string]int{}
					}
					recorded[m.ws][status]++
				}
				return err
			}
			// Every meeting was booked first. A meeting rebooked after a
			// cancellation has its booking already.
			if m.last == nil || *m.last == "" {
				if err := record(MeetingBooked, m.booked); err != nil {
					return err
				}
			}
			switch m.status {
			case MeetingHeld, MeetingNoShow:
				at := m.starts
				if m.ends != nil {
					at = *m.ends
				}
				if err := record(m.status, at); err != nil {
					return err
				}
			case MeetingCanceled:
				if err := record(m.status, m.updated); err != nil {
					return err
				}
			}
			if _, err := tx.Exec(ctx, `UPDATE meetings SET event_status = status WHERE id = $1`, m.id); err != nil {
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
		for status, c := range by {
			metrics.MeetingEventsRecorded(status, c)
			total += c
		}
		track.Emit(ctx, track.Event{Name: "meeting_events.recorded", WorkspaceID: ws, ActorType: "system", Surface: "job",
			Props: map[string]any{"events": total, "booked": by[MeetingBooked], "held": by[MeetingHeld], "no_show": by[MeetingNoShow], "canceled": by[MeetingCanceled]}})
	}
	return n, nil
}
