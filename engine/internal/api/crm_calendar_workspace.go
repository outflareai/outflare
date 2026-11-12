package api

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/audit"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/connectors/calendars"
	"github.com/outflareai/outflare/engine/internal/connectors/workspace"
	"github.com/outflareai/outflare/engine/internal/crmmail"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/outcomes"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Meetings from the team's calendars: for each mailbox the CRM reads
// through the company's Google Workspace connection, the person's own
// calendar is read too (read-only, the last 90 days and everything ahead,
// then only what changed), and a meeting is kept when everyone from
// outside the company on it belongs to one of the brand's contacts or
// companies, under the same rules as their email. It goes on that record,
// so its timeline and relationship check show it. The rest of a calendar
// is never copied.

const jobCRMCalendarWorkspace = "crm_calendar.workspace_sync"

// calendarEvery is how often a calendar is read again.
const calendarEvery = 15 * time.Minute

func (s *Server) enqueueWorkspaceCalendar(ctx context.Context, q jobs.Querier, ws, id string, at time.Time, key string) error {
	_, err := jobs.Enqueue(ctx, q, jobs.NewJob{Kind: jobCRMCalendarWorkspace, WorkspaceID: &ws, Payload: map[string]string{"connection_id": id}, RunAt: at, MaxAttempts: 3,
		IdempotencyKey: "crm_calendar:" + id + ":" + key})
	return err
}

func (s *Server) crmCalendarWorkspaceJob(ctx context.Context, job jobs.Job) (runErr error) {
	var payload struct {
		ID string `json:"connection_id"`
	}
	if json.Unmarshal(job.Payload, &payload) != nil || job.WorkspaceID == nil || !validUUID(payload.ID) {
		return jobs.ErrPermanent
	}
	ws := *job.WorkspaceID
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	var brand, owner, status string
	var integration, cursor *string
	err := s.pool.QueryRow(ctx, `SELECT brand_id::text, owner_id::text, status, workspace_integration_id::text, calendar_cursor FROM crm_google_connections WHERE workspace_id=$1 AND id=$2`,
		ws, payload.ID).Scan(&brand, &owner, &status, &integration, &cursor)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (integration == nil || status != "connected")) {
		return nil
	}
	if err != nil {
		return err
	}
	if ok, err := eligibleCRMMail(ctx, s.pool, ws, brand, owner); err != nil || !ok {
		return err
	}
	more := false
	defer func() {
		// One following read: straight away while the calendar has more,
		// otherwise in a quarter of an hour.
		if runErr != nil {
			return
		}
		at, key := time.Now().Add(calendarEvery), "at:"+strconv.FormatInt(time.Now().Add(calendarEvery).Unix()/int64(calendarEvery.Seconds()), 10)
		if more {
			at, key = time.Now(), "more:"+strconv.FormatInt(job.ID, 10)
		}
		next, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		runErr = s.enqueueWorkspaceCalendar(next, s.pool, ws, payload.ID, at, key)
	}()
	var email string
	if err := s.pool.QueryRow(ctx, `SELECT lower(email) FROM users WHERE id=$1`, owner).Scan(&email); err != nil {
		return err
	}
	o, err := s.integrations.Open(ctx, s.pool, ws, *integration)
	if err != nil {
		return err
	}
	token, err := workspace.CalendarToken(ctx, o.Client, o.Creds, email)
	if err != nil {
		track.Fail(ctx, s.log, track.Event{Name: "crm_calendar.sync_failed", WorkspaceID: ws, ActorType: "system", Surface: "job",
			Props: map[string]any{"brand_id": brand}}, "workspace_access_denied", err)
		if k := connectors.KindOf(err); k == connectors.ErrAuth || k == connectors.ErrInvalid {
			return nil
		}
		return err
	}
	page, err := calendars.Google{}.SyncMeetings(ctx, o.Client, connectors.Creds{"access_token": token}, outcomes.SourceConfig{}, connectors.Account{}, deref(cursor))
	if err != nil {
		return err
	}
	if page.Reset {
		more = true
		_, err := s.pool.Exec(ctx, `UPDATE crm_google_connections SET calendar_cursor=NULL WHERE workspace_id=$1 AND id=$2`, ws, payload.ID)
		return err
	}
	a := &access{WorkspaceID: ws, Brand: brand}
	a.UserID = owner
	base, err := s.crmMailContext(ctx, a, owner, "")
	if err != nil {
		return err
	}
	saved := 0
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		for _, m := range page.Meetings {
			ok, err := upsertWorkspaceMeeting(ctx, tx, ws, brand, *integration, email, base, m)
			if err != nil {
				return err
			}
			if ok {
				saved++
			}
		}
		next := page.Cursor
		if next == "" {
			next = deref(cursor)
		}
		if _, err := tx.Exec(ctx, `UPDATE crm_google_connections SET calendar_cursor=NULLIF($3,''), calendar_synced_at=now() WHERE workspace_id=$1 AND id=$2`, ws, payload.ID, next); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Event{WorkspaceID: ws, ActorType: "system", Action: "crm_calendar.synced", TargetType: "crm_mail_connection", TargetID: payload.ID,
			Data: map[string]any{"brand_id": brand, "read": len(page.Meetings), "saved": saved}})
	})
	more = err == nil && page.More
	return err
}

// upsertWorkspaceMeeting keeps a meeting from a member's calendar on the
// one contact or company its outside guests belong to. One event on
// several members' calendars is one meeting.
func upsertWorkspaceMeeting(ctx context.Context, tx pgx.Tx, ws, brand, integration, owner string, base mailContext, m outcomes.SyncedMeeting) (bool, error) {
	external := "workspace:" + m.ExternalID
	if m.StartsAt.IsZero() {
		// Deleted: a meeting already kept is canceled.
		tag, err := tx.Exec(ctx, `UPDATE meetings SET status='canceled', updated_at=now() WHERE workspace_id=$1 AND source='google_calendar' AND external_id=$2 AND status<>'canceled'`, ws, external)
		return tag.RowsAffected() > 0, err
	}
	msg := crmmail.Message{OwnerEmail: owner, Participants: []string{owner}}
	add := func(e string) {
		if e = strings.ToLower(strings.TrimSpace(e)); strings.Contains(e, "@") && !slices.Contains(msg.Participants, e) {
			msg.Participants = append(msg.Participants, e)
		}
	}
	add(m.OrganizerEmail)
	for _, at := range m.Attendees {
		add(at.Email)
	}
	rec, err := matchMailRecord(ctx, tx, ws, brand, base, msg)
	if err != nil || rec == "" {
		return false, err
	}
	guest, name := "", ""
	for _, p := range crmmail.External(msg, base.brand, base.members) {
		guest = p
		break
	}
	for _, at := range m.Attendees {
		if strings.EqualFold(at.Email, guest) {
			name = at.Name
		}
	}
	status := outcomes.MeetingBooked
	if m.Canceled {
		status = outcomes.MeetingCanceled
	}
	booked := time.Now()
	if m.BookedAt != nil {
		booked = *m.BookedAt
	}
	_, err = tx.Exec(ctx, `INSERT INTO meetings (workspace_id, brand_id, record_id, source, integration_id, external_id, title, starts_at, ends_at, booked_at, status,
			attendee_email, attendee_name, organizer_email, attribution)
		VALUES ($1, $2, $3, 'google_calendar', $4, $5, left($6, 500), $7, $8, $9, $10, $11, left($12, 200), lower($13), 'none')
		ON CONFLICT (workspace_id, source, external_id) WHERE external_id IS NOT NULL DO UPDATE SET
			title = EXCLUDED.title, starts_at = EXCLUDED.starts_at, ends_at = EXCLUDED.ends_at,
			status = CASE WHEN EXCLUDED.status = 'canceled' THEN 'canceled' WHEN meetings.status = 'canceled' THEN 'booked' ELSE meetings.status END,
			record_id = COALESCE(meetings.record_id, EXCLUDED.record_id), updated_at = now()`,
		ws, brand, rec, integration, external, strings.TrimSpace(m.Title), m.StartsAt, m.EndsAt, booked, status, guest, name, m.OrganizerEmail)
	return err == nil, err
}
