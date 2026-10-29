package outcomes_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/outcomes"
)

// Meetings tied to a person become meeting.* events on their timeline,
// each once, with the times they happened; a meeting waits until it is
// tied to a person.
func TestMeetingEvents(t *testing.T) {
	pool := testPool(t)
	f := setup(t, pool)
	ctx := context.Background()
	q := func(sql string, args ...any) string {
		t.Helper()
		var id string
		if err := pool.QueryRow(ctx, sql, args...).Scan(&id); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return id
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	rec := q(`INSERT INTO records (workspace_id, record_type, name) VALUES ($1, 'person', 'Sam') RETURNING id::text`, f.ws)
	booked := f.at.Add(-2 * time.Hour)
	// A new meeting, from a calendar.
	m1 := q(`INSERT INTO meetings (workspace_id, record_id, sequence_id, source, external_id, starts_at, ends_at, booked_at, attendee_email)
		VALUES ($1, $2, $3, 'calendly', 'ev-1', $4, $5, $6, 'sam@acme.test') RETURNING id::text`,
		f.ws, rec, f.seqA, f.at, f.at.Add(30*time.Minute), booked)
	// One from before meeting events, already held.
	m2 := q(`INSERT INTO meetings (workspace_id, record_id, source, starts_at, booked_at, status, event_status)
		VALUES ($1, $2, 'manual', $3, $4, 'held', NULL) RETURNING id::text`, f.ws, rec, f.at.Add(-48*time.Hour), f.at.Add(-72*time.Hour))
	// One no one is tied to yet.
	m3 := q(`INSERT INTO meetings (workspace_id, source, starts_at, attendee_email) VALUES ($1, 'manual', $2, 'kim@other.test') RETURNING id::text`, f.ws, f.at)

	drain := func() {
		t.Helper()
		for {
			n, err := outcomes.MeetingEvents(ctx, pool, 50)
			if err != nil {
				t.Fatal(err)
			}
			if n < 50 {
				return
			}
		}
	}
	type event struct {
		name, meeting string
		at            time.Time
	}
	events := func() []event {
		t.Helper()
		rows, err := pool.Query(ctx, `SELECT name, properties->>'meeting_id', occurred_at FROM customer_events
			WHERE workspace_id = $1 AND source = 'meetings' ORDER BY occurred_at, name`, f.ws)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []event
		for rows.Next() {
			var e event
			if err := rows.Scan(&e.name, &e.meeting, &e.at); err != nil {
				t.Fatal(err)
			}
			out = append(out, e)
		}
		return out
	}

	drain()
	got := events()
	want := []event{
		{"meeting.booked", m2, f.at.Add(-72 * time.Hour)},
		{"meeting.held", m2, f.at.Add(-48 * time.Hour)},
		{"meeting.booked", m1, booked},
	}
	if len(got) != len(want) {
		t.Fatalf("events = %+v", got)
	}
	for i := range want {
		if got[i].name != want[i].name || got[i].meeting != want[i].meeting || !got[i].at.Equal(want[i].at) {
			t.Errorf("event %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	var seq string
	if err := pool.QueryRow(ctx, `SELECT properties->>'sequence_id' FROM customer_events WHERE workspace_id = $1 AND message_id = $2`,
		f.ws, "meeting:"+m1+":booked").Scan(&seq); err != nil || seq != f.seqA {
		t.Errorf("sequence_id = %q, %v", seq, err)
	}

	// It's held; running again records nothing more.
	exec(`UPDATE meetings SET status = 'held' WHERE id = $1`, m1)
	drain()
	drain()
	if got = events(); len(got) != 4 || got[3].name != "meeting.held" || got[3].meeting != m1 || !got[3].at.Equal(f.at.Add(30*time.Minute)) {
		t.Fatalf("after held: %+v", got)
	}

	// Canceled, then booked again: one canceled, no second booking.
	exec(`UPDATE meetings SET status = 'canceled' WHERE id = $1`, m2)
	drain()
	exec(`UPDATE meetings SET status = 'booked' WHERE id = $1`, m2)
	drain()
	if got = events(); len(got) != 5 || got[4].name != "meeting.canceled" {
		t.Fatalf("after cancel and rebook: %+v", got)
	}

	// The third meeting is recorded once it is tied to the person.
	exec(`UPDATE meetings SET record_id = $2 WHERE id = $1`, m3, rec)
	drain()
	n := 0
	for _, e := range events() {
		if e.meeting == m3 {
			n++
		}
	}
	if n != 1 {
		t.Errorf("tied meeting: %d events", n)
	}
	pending(t, pool, f.ws, 0)
}

func pending(t *testing.T, pool *pgxpool.Pool, ws string, want int) {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM meetings WHERE workspace_id = $1 AND record_id IS NOT NULL
		AND event_status IS DISTINCT FROM status`, ws).Scan(&n); err != nil || n != want {
		t.Errorf("pending = %d, %v; want %d", n, err, want)
	}
}
