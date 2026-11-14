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
// as that member's own, private like their email unless they share the
// contact or team sharing shows it after the AI check. The rest of a
// calendar is never copied.

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
			ok, err := s.upsertWorkspaceMeeting(ctx, tx, ws, brand, owner, email, base, m)
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
		// With team sharing on, new meetings are checked before colleagues see them.
		if saved > 0 {
			if err := queueMailPrivacyIfShared(ctx, tx, ws, brand); err != nil {
				return err
			}
		}
		return audit.Record(ctx, tx, audit.Event{WorkspaceID: ws, ActorType: "system", Action: "crm_calendar.synced", TargetType: "crm_mail_connection", TargetID: payload.ID,
			Data: map[string]any{"brand_id": brand, "read": len(page.Meetings), "saved": saved}})
	})
	more = err == nil && page.More
	return err
}

// calendarEvent is a meeting from a member's calendar, sealed in
// crm_calendar_events.
type calendarEvent struct {
	Title     string     `json:"title"`
	Organizer string     `json:"organizer"`
	Guests    []string   `json:"guests"`
	StartsAt  time.Time  `json:"starts_at"`
	EndsAt    *time.Time `json:"ends_at,omitempty"`
}

func calendarAAD(id string) []byte { return []byte("crm_calendar:" + id) }

// upsertWorkspaceMeeting keeps a meeting from a member's calendar on the
// one contact or company its outside guests belong to, as that member's
// own, like their email: private unless they share the contact, or team
// sharing shows it after the AI check. An event the calendar marks private
// or confidential is never kept.
func (s *Server) upsertWorkspaceMeeting(ctx context.Context, tx pgx.Tx, ws, brand, ownerID, owner string, base mailContext, m outcomes.SyncedMeeting) (bool, error) {
	if m.StartsAt.IsZero() || m.Private {
		// Deleted, or made private: what was kept is marked canceled, or
		// removed when it turned private.
		if m.Private {
			tag, err := tx.Exec(ctx, `DELETE FROM crm_calendar_events WHERE workspace_id=$1 AND brand_id=$2 AND owner_id=$3 AND event_key=$4`, ws, brand, ownerID, m.ExternalID)
			return tag.RowsAffected() > 0, err
		}
		tag, err := tx.Exec(ctx, `UPDATE crm_calendar_events SET canceled=true, updated_at=now() WHERE workspace_id=$1 AND brand_id=$2 AND owner_id=$3 AND event_key=$4 AND NOT canceled`,
			ws, brand, ownerID, m.ExternalID)
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
	id := newID()
	data, _ := json.Marshal(calendarEvent{Title: strings.TrimSpace(m.Title), Organizer: strings.ToLower(m.OrganizerEmail), Guests: msg.Participants, StartsAt: m.StartsAt, EndsAt: m.EndsAt})
	var existing string
	err = tx.QueryRow(ctx, `SELECT id::text FROM crm_calendar_events WHERE workspace_id=$1 AND brand_id=$2 AND owner_id=$3 AND event_key=$4`, ws, brand, ownerID, m.ExternalID).Scan(&existing)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, err
	}
	if existing != "" {
		// A changed title is checked again before colleagues see it.
		_, err = tx.Exec(ctx, `UPDATE crm_calendar_events SET content_sealed=$2, starts_at=$3, ends_at=$4, canceled=$5, record_id=$6, privacy='unchecked', privacy_checked_at=NULL, updated_at=now() WHERE id=$1`,
			existing, s.box.Seal(data, calendarAAD(existing)), m.StartsAt, m.EndsAt, m.Canceled, rec)
		return err == nil, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO crm_calendar_events (id, workspace_id, brand_id, owner_id, record_id, event_key, content_sealed, starts_at, ends_at, canceled)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`, id, ws, brand, ownerID, rec, m.ExternalID, s.box.Seal(data, calendarAAD(id)), m.StartsAt, m.EndsAt, m.Canceled)
	return err == nil, err
}
