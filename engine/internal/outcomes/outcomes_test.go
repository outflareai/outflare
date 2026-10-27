package outcomes_test

import (
	"context"
	"math"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/db"
	"github.com/outflareai/outflare/engine/internal/outcomes"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := db.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	return pool
}

// fixture is a workspace with two sequences in one project, one mailbox,
// and a lead at acme.test who was emailed by sequence A and replied.
type fixture struct {
	ws, project, seqA, seqB, mailbox, lead, lead2 string
	at                                            time.Time
}

func setup(t *testing.T, pool *pgxpool.Pool) fixture {
	t.Helper()
	ctx := context.Background()
	var f fixture
	f.at = time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	q := func(sql string, args []any, dest ...any) {
		t.Helper()
		if err := pool.QueryRow(ctx, sql, args...).Scan(dest...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	q(`INSERT INTO workspaces (name, slug) VALUES ('Outcomes', 'outcomes-' || gen_random_uuid()) RETURNING id`, nil, &f.ws)
	q(`INSERT INTO projects (workspace_id, name) VALUES ($1, 'Agencies AU') RETURNING id`, []any{f.ws}, &f.project)
	q(`INSERT INTO sequences (workspace_id, name, project_id) VALUES ($1, 'A', $2) RETURNING id`, []any{f.ws, f.project}, &f.seqA)
	q(`INSERT INTO sequences (workspace_id, name, project_id) VALUES ($1, 'B', $2) RETURNING id`, []any{f.ws, f.project}, &f.seqB)
	q(`INSERT INTO mailboxes (workspace_id, email, provider, smtp_host, smtp_port, smtp_username, imap_host, imap_port, imap_username, credentials)
		VALUES ($1, 'jo@send.test', 'other', 'smtp', 587, 'jo', 'imap', 993, 'jo', '\x00') RETURNING id`, []any{f.ws}, &f.mailbox)
	q(`INSERT INTO leads (workspace_id, email) VALUES ($1, 'sam@acme.test') RETURNING id`, []any{f.ws}, &f.lead)
	q(`INSERT INTO leads (workspace_id, email) VALUES ($1, 'kim@other.test') RETURNING id`, []any{f.ws}, &f.lead2)
	send := func(seq, lead, to string, n int) {
		for i := 0; i < n; i++ {
			var id string
			q(`INSERT INTO messages (workspace_id, sequence_id, lead_id, mailbox_id, from_email, to_email, subject, body, message_id, status, sent_at)
				VALUES ($1, $2, $3, $4, 'jo@send.test', $5, 's', 'b', gen_random_uuid()::text, 'sent', $6) RETURNING id`,
				[]any{f.ws, seq, lead, f.mailbox, to, f.at.Add(-time.Duration(48+i) * time.Hour)}, &id)
		}
	}
	send(f.seqA, f.lead, "sam@acme.test", 3)
	send(f.seqB, f.lead2, "kim@other.test", 1)
	var id string
	q(`INSERT INTO inbound_messages (workspace_id, mailbox_id, uidvalidity, uid, from_email, kind, lead_id, sequence_id, received_at)
		VALUES ($1, $2, 1, 1, 'sam@acme.test', 'reply', $3, $4, $5) RETURNING id`, []any{f.ws, f.mailbox, f.lead, f.seqA, f.at.Add(-24 * time.Hour)}, &id)
	return f
}

func tx(t *testing.T, pool *pgxpool.Pool, fn func(pgx.Tx)) {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	fn(tx)
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func i64(v int64) *int64     { return &v }
func f64(v float64) *float64 { return &v }
func str(v string) *string   { return &v }

func TestAttributionAndCostPerMeeting(t *testing.T) {
	pool := testPool(t)
	f := setup(t, pool)
	ctx := context.Background()

	tx(t, pool, func(tx pgx.Tx) {
		// A calendar event with the lead's colleague: tied by company domain.
		ok, err := outcomes.UpsertSynced(ctx, tx, f.ws, outcomes.SourceGoogleCalendar, "", outcomes.SyncedMeeting{
			ExternalID: "evt1", Title: "Intro", StartsAt: f.at.Add(48 * time.Hour), BookedAt: &f.at,
			Attendees: []outcomes.Attendee{{Email: "jo@send.test"}, {Email: "alex@ACME.test", Name: "Alex"}},
		}, nil)
		if err != nil || !ok {
			t.Fatalf("company match: %v %v", ok, err)
		}
		// An internal meeting and one with a stranger: never stored.
		for _, a := range [][]outcomes.Attendee{{{Email: "jo@send.test"}}, {{Email: "x@gmail.com"}}} {
			ok, err := outcomes.UpsertSynced(ctx, tx, f.ws, outcomes.SourceGoogleCalendar, "", outcomes.SyncedMeeting{
				ExternalID: "evt-" + a[0].Email, StartsAt: f.at, BookedAt: &f.at, Attendees: a}, nil)
			if err != nil || ok {
				t.Fatalf("stored an unrelated meeting: %v %v", ok, err)
			}
		}
		// The lead themselves, booked through Calendly.
		if _, err := outcomes.UpsertSynced(ctx, tx, f.ws, outcomes.SourceCalendly, "", outcomes.SyncedMeeting{
			ExternalID: "c1", StartsAt: f.at.Add(72 * time.Hour), BookedAt: &f.at,
			Attendees: []outcomes.Attendee{{Email: "sam@acme.test"}}}, nil); err != nil {
			t.Fatal(err)
		}
	})
	ms, err := outcomes.ListMeetings(ctx, pool, f.ws, outcomes.MeetingFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 2 {
		t.Fatalf("meetings: %d", len(ms))
	}
	for _, m := range ms {
		if m.SequenceID == nil || *m.SequenceID != f.seqA || m.ProjectID == nil || *m.ProjectID != f.project || m.InboundID == nil {
			t.Fatalf("attribution: %+v", m)
		}
	}
	if ms[0].Attribution != outcomes.AttrLead || ms[1].Attribution != outcomes.AttrCompany {
		t.Fatalf("kinds: %s %s", ms[0].Attribution, ms[1].Attribution)
	}

	tx(t, pool, func(tx pgx.Tx) {
		// $40 of data for sequence A, 1000 verification credits at $0.004
		// with no sequence (shared), a $90/month mailbox seat (direct via
		// the mailbox), and a retry of the data charge.
		for _, e := range []outcomes.Entry{
			{WorkspaceID: f.ws, SequenceID: &f.seqA, Category: "data", Provider: "Apollo", AmountCents: i64(4000), OccurredAt: f.at, Ref: str("run:1")},
			{WorkspaceID: f.ws, SequenceID: &f.seqA, Category: "data", Provider: "Apollo", AmountCents: i64(4000), OccurredAt: f.at, Ref: str("run:1")},
			{WorkspaceID: f.ws, Category: "verification", Provider: "ZeroBounce", Units: f64(1000), UnitName: "credit", OccurredAt: f.at},
			{WorkspaceID: f.ws, Category: "data", Provider: "Mystery", Units: f64(5), UnitName: "lookup", OccurredAt: f.at},
		} {
			if _, _, err := outcomes.Record(ctx, tx, e); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := outcomes.SetRate(ctx, tx, f.ws, outcomes.Rate{Provider: "zerobounce", UnitName: "credit", UnitPriceMicros: 4000}); err != nil {
			t.Fatal(err)
		}
		if _, err := outcomes.CreateDeal(ctx, tx, f.ws, "manual", "", outcomes.NewDeal{Name: "Acme pilot", ContactEmail: "sam@acme.test",
			State: "won", AmountCents: i64(500000), OpenedAt: &f.at}); err != nil {
			t.Fatal(err)
		}
	})
	if _, _, err := outcomes.Record(ctx, pool, outcomes.Entry{WorkspaceID: f.ws, Category: "nope", AmountCents: i64(1)}); !outcomes.IsInvalid(err) {
		t.Fatalf("bad category: %v", err)
	}

	from, to := f.at.Add(-30*24*time.Hour), f.at.Add(30*24*time.Hour)
	rep, err := outcomes.Summary(ctx, pool, f.ws, outcomes.ReportFilter{From: from, To: to, GroupBy: outcomes.GroupSequence})
	if err != nil {
		t.Fatal(err)
	}
	tot := rep.Totals
	// 4000 + 1000 * 4000 / 10000 = 4400 cents.
	if tot.Sent != 4 || tot.Replies != 1 || tot.Meetings != 2 || tot.Won != 1 || tot.Spend.TotalCents != 4400 || tot.Spend.EstimatedCents != 400 {
		t.Fatalf("totals: %+v", tot)
	}
	if len(rep.Unpriced) != 1 || rep.Unpriced[0].Provider != "mystery" {
		t.Fatalf("unpriced: %+v", rep.Unpriced)
	}
	var a outcomes.Row
	for _, r := range rep.Rows {
		if r.Key == f.seqA {
			a = r
		}
	}
	// A gets its $40 plus 3/4 of the $4 shared.
	if a.Name != "A" || a.Spend.DirectCents != 4000 || a.Spend.SharedCents != 300 || *a.CostPerMeeting != 2150 || a.WonCents != 500000 {
		t.Fatalf("sequence A: %+v", a)
	}

	// Recurring: $90 a month on the mailbox's project, for the last 30.5 days of a
	// 60-day window.
	tx(t, pool, func(tx pgx.Tx) {
		if _, err := outcomes.CreateRecurring(ctx, tx, f.ws, "", outcomes.Recurring{ProjectID: &f.project, Category: "mailboxes", Name: "Seats",
			AmountCents: 9000, StartsOn: outcomes.Date{Time: f.at}}); err != nil {
			t.Fatal(err)
		}
	})
	rep, err = outcomes.Summary(ctx, pool, f.ws, outcomes.ReportFilter{From: from, To: to, GroupBy: outcomes.GroupProject})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Rows) != 1 || rep.Rows[0].Name != "Agencies AU" {
		t.Fatalf("project rows: %+v", rep.Rows)
	}
	days := 30.5 // from the start of the day the seat began
	want := int64(math.Round(4400 + 9000*12*days/365))
	if got := rep.Totals.Spend.TotalCents; got != want {
		t.Fatalf("with recurring: %d want %d", got, want)
	}
}

func TestPinnedAttributionSurvivesSync(t *testing.T) {
	pool := testPool(t)
	f := setup(t, pool)
	ctx := context.Background()
	sm := outcomes.SyncedMeeting{ExternalID: "e9", StartsAt: f.at, BookedAt: &f.at, Attendees: []outcomes.Attendee{{Email: "sam@acme.test"}}}
	tx(t, pool, func(tx pgx.Tx) {
		if _, err := outcomes.UpsertSynced(ctx, tx, f.ws, outcomes.SourceCalCom, "", sm, nil); err != nil {
			t.Fatal(err)
		}
	})
	ms, _ := outcomes.ListMeetings(ctx, pool, f.ws, outcomes.MeetingFilter{})
	tx(t, pool, func(tx pgx.Tx) {
		held := outcomes.MeetingHeld
		if _, err := outcomes.UpdateMeeting(ctx, tx, f.ws, ms[0].ID, outcomes.MeetingPatch{SequenceID: &f.seqB, Status: &held}); err != nil {
			t.Fatal(err)
		}
		sm.Title = "Renamed"
		if _, err := outcomes.UpsertSynced(ctx, tx, f.ws, outcomes.SourceCalCom, "", sm, nil); err != nil {
			t.Fatal(err)
		}
	})
	m, _ := outcomes.GetMeeting(ctx, pool, f.ws, ms[0].ID)
	if *m.SequenceID != f.seqB || m.Status != outcomes.MeetingHeld || m.Title != "Renamed" || m.Attribution != outcomes.AttrManual {
		t.Fatalf("after sync: %+v", m)
	}
	// The source canceling it wins over held.
	sm.Canceled = true
	tx(t, pool, func(tx pgx.Tx) {
		if _, err := outcomes.UpsertSynced(ctx, tx, f.ws, outcomes.SourceCalCom, "", sm, nil); err != nil {
			t.Fatal(err)
		}
	})
	m, _ = outcomes.GetMeeting(ctx, pool, f.ws, ms[0].ID)
	if m.Status != outcomes.MeetingCanceled {
		t.Fatalf("status %s", m.Status)
	}
}
