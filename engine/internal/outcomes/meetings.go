package outcomes

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Meeting statuses.
const (
	MeetingBooked   = "booked"
	MeetingHeld     = "held"
	MeetingNoShow   = "no_show"
	MeetingCanceled = "canceled"
)

// Meeting sources.
const (
	SourceGoogleCalendar    = "google_calendar"
	SourceMicrosoftCalendar = "microsoft_calendar"
	SourceCalendly          = "calendly"
	SourceCalCom            = "cal_com"
	SourceHubSpot           = "hubspot"
	SourceAPI               = "api"
)

// Meeting is a meeting with a lead.
type Meeting struct {
	ID                string     `json:"id"`
	SequenceID        *string    `json:"sequence_id"`
	MissionID         *string    `json:"mission_id"`
	LeadID            *string    `json:"lead_id"`
	InboundID         *string    `json:"inbound_id"`
	Source            string     `json:"source"`
	IntegrationID     *string    `json:"integration_id"`
	ExternalID        *string    `json:"external_id"`
	Title             string     `json:"title"`
	StartsAt          time.Time  `json:"starts_at"`
	EndsAt            *time.Time `json:"ends_at"`
	BookedAt          time.Time  `json:"booked_at"`
	Status            string     `json:"status"`
	AttendeeEmail     string     `json:"attendee_email"`
	AttendeeName      string     `json:"attendee_name"`
	OrganizerEmail    string     `json:"organizer_email"`
	Attribution       string     `json:"attribution"`
	AttributionPinned bool       `json:"attribution_pinned"`
	Qualified         *bool      `json:"qualified"`
	Notes             string     `json:"notes"`
	CreatedBy         *string    `json:"created_by"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
	// Joined for display.
	LeadEmail    *string `json:"lead_email"`
	SequenceName *string `json:"sequence_name"`
}

// ErrNotFound means no such row in the workspace.
var ErrNotFound = errors.New("not found")

const meetingCols = `m.id, m.sequence_id, m.mission_id, m.lead_id, m.inbound_id, m.source, m.integration_id, m.external_id,
	m.title, m.starts_at, m.ends_at, m.booked_at, m.status, m.attendee_email, m.attendee_name, m.organizer_email,
	m.attribution, m.attribution_pinned, m.qualified, m.notes, m.created_by, m.created_at, m.updated_at,
	l.email, s.name`

const meetingFrom = ` FROM meetings m
	LEFT JOIN leads l ON l.id = m.lead_id
	LEFT JOIN sequences s ON s.id = m.sequence_id`

func scanMeeting(row pgx.Row) (Meeting, error) {
	var m Meeting
	err := row.Scan(&m.ID, &m.SequenceID, &m.MissionID, &m.LeadID, &m.InboundID, &m.Source, &m.IntegrationID,
		&m.ExternalID, &m.Title, &m.StartsAt, &m.EndsAt, &m.BookedAt, &m.Status, &m.AttendeeEmail, &m.AttendeeName,
		&m.OrganizerEmail, &m.Attribution, &m.AttributionPinned, &m.Qualified, &m.Notes, &m.CreatedBy, &m.CreatedAt,
		&m.UpdatedAt, &m.LeadEmail, &m.SequenceName)
	return m, err
}

// Querier runs queries on a pool or a transaction.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// MeetingFilter narrows ListMeetings.
type MeetingFilter struct {
	From, To   *time.Time // on starts_at
	SequenceID string
	MissionID  string
	Status     string
	// Attributed: only meetings tied to outreach.
	Attributed bool
	// Brand is a brand's id, "none" for rows no brand owns, or "" for
	// all; a row's brand is its sequence's.
	Brand  string
	Limit  int
	Before *time.Time // cursor: starts_at of the last row seen
}

// ListMeetings returns meetings, newest first.
func ListMeetings(ctx context.Context, q Querier, ws string, f MeetingFilter) ([]Meeting, error) {
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 100
	}
	rows, err := q.Query(ctx, `SELECT `+meetingCols+meetingFrom+`
		WHERE m.workspace_id = $1
		  AND ($2::timestamptz IS NULL OR m.starts_at >= $2)
		  AND ($3::timestamptz IS NULL OR m.starts_at < $3)
		  AND ($4 = '' OR m.sequence_id::text = $4)
		  AND ($5 = '' OR m.mission_id::text = $5)
		  AND ($6 = '' OR m.status = $6)
		  AND (NOT $7 OR m.attribution <> 'none')
		  AND ($8::timestamptz IS NULL OR m.starts_at < $8)
		  AND ($10 = '' OR (SELECT q.brand_id FROM sequences q WHERE q.id = m.sequence_id)::text IS NOT DISTINCT FROM NULLIF($10, 'none'))
		ORDER BY m.starts_at DESC, m.id LIMIT $9`,
		ws, f.From, f.To, f.SequenceID, f.MissionID, f.Status, f.Attributed, f.Before, f.Limit, f.Brand)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Meeting{}
	for rows.Next() {
		m, err := scanMeeting(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// GetMeeting returns one meeting.
func GetMeeting(ctx context.Context, q Querier, ws, id string) (Meeting, error) {
	m, err := scanMeeting(q.QueryRow(ctx, `SELECT `+meetingCols+meetingFrom+` WHERE m.workspace_id = $1 AND m.id = $2`, ws, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return m, ErrNotFound
	}
	return m, err
}

func validMeetingStatus(s string) bool {
	return s == MeetingBooked || s == MeetingHeld || s == MeetingNoShow || s == MeetingCanceled
}

// NewMeeting is a meeting logged by a person or an agent.
type NewMeeting struct {
	LeadID        *string `json:"lead_id"`
	AttendeeEmail string  `json:"attendee_email"`
	AttendeeName  string  `json:"attendee_name"`
	SequenceID    *string `json:"sequence_id"`
	// Ignored: projects were removed. Accepted so older clients still work.
	ProjectID  *string    `json:"project_id"`
	MissionID  *string    `json:"mission_id"`
	Title      string     `json:"title"`
	StartsAt   time.Time  `json:"starts_at"`
	EndsAt     *time.Time `json:"ends_at"`
	BookedAt   *time.Time `json:"booked_at"`
	Status     string     `json:"status"`
	Qualified  *bool      `json:"qualified"`
	Notes      string     `json:"notes"`
	ExternalID *string    `json:"external_id"`
}

// CreateMeeting logs a meeting. Give a lead, or an attendee email that is
// matched to a lead; a sequence given explicitly overrides the match.
func CreateMeeting(ctx context.Context, tx pgx.Tx, ws, source, actorID string, n NewMeeting) (Meeting, error) {
	if n.StartsAt.IsZero() {
		return Meeting{}, invalid("starts_at is required")
	}
	if n.EndsAt != nil && n.EndsAt.Before(n.StartsAt) {
		return Meeting{}, invalid("ends_at is before starts_at")
	}
	if n.Status == "" {
		n.Status = MeetingBooked
		if n.StartsAt.Before(time.Now().Add(-time.Hour)) {
			n.Status = MeetingHeld
		}
	}
	if !validMeetingStatus(n.Status) {
		return Meeting{}, invalid("status must be booked, held, no_show or canceled")
	}
	n.AttendeeEmail = strings.ToLower(strings.TrimSpace(n.AttendeeEmail))
	n.Title, n.Notes, n.AttendeeName = strings.TrimSpace(n.Title), strings.TrimSpace(n.Notes), strings.TrimSpace(n.AttendeeName)
	if len(n.Title) > 500 || len(n.Notes) > 10000 || len(n.AttendeeName) > 200 || len(n.AttendeeEmail) > 320 {
		return Meeting{}, invalid("title, notes or attendee is too long")
	}
	booked := time.Now()
	if n.BookedAt != nil {
		booked = *n.BookedAt
	}
	var m Match
	var err error
	switch {
	case n.LeadID != nil:
		var email string
		if err := tx.QueryRow(ctx, `SELECT email FROM leads WHERE workspace_id = $1 AND id = $2`, ws, *n.LeadID).Scan(&email); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return Meeting{}, invalid("lead_id is not a lead in this workspace")
			}
			return Meeting{}, err
		}
		if n.AttendeeEmail == "" {
			n.AttendeeEmail = strings.ToLower(email)
		}
		m, err = AttributeLead(ctx, tx, ws, *n.LeadID, booked)
	case n.AttendeeEmail != "":
		m, err = Attribute(ctx, tx, ws, []string{n.AttendeeEmail}, booked)
	default:
		return Meeting{}, invalid("give lead_id or attendee_email")
	}
	if err != nil {
		return Meeting{}, err
	}
	pinned := false
	if n.SequenceID != nil {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM sequences WHERE workspace_id = $1 AND id = $2)`, ws, *n.SequenceID).Scan(&ok); err != nil {
			return Meeting{}, err
		}
		if !ok {
			return Meeting{}, invalid("sequence_id is not a sequence in this workspace")
		}
		m.SequenceID, m.Attribution, pinned = n.SequenceID, AttrManual, true
	}
	var actor *string
	if actorID != "" {
		actor = &actorID
	}
	var id string
	err = tx.QueryRow(ctx, `
		INSERT INTO meetings (workspace_id, sequence_id, mission_id, lead_id, inbound_id, source, external_id, title,
			starts_at, ends_at, booked_at, status, attendee_email, attendee_name, attribution, attribution_pinned, qualified, notes, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19)
		RETURNING id`,
		ws, m.SequenceID, n.MissionID, m.LeadID, m.InboundID, source, n.ExternalID, n.Title,
		n.StartsAt, n.EndsAt, booked, n.Status, n.AttendeeEmail, n.AttendeeName, m.Attribution, pinned, n.Qualified, n.Notes, actor).Scan(&id)
	if err != nil {
		if isUniqueViolation(err) {
			return Meeting{}, invalid("a meeting with this external_id already exists")
		}
		return Meeting{}, err
	}
	return GetMeeting(ctx, tx, ws, id)
}

// MeetingPatch changes a meeting. Nil fields are left alone. Setting
// SequenceID pins the attribution so sync never moves it.
type MeetingPatch struct {
	Status     *string    `json:"status"`
	Qualified  *bool      `json:"qualified"`
	Notes      *string    `json:"notes"`
	Title      *string    `json:"title"`
	StartsAt   *time.Time `json:"starts_at"`
	SequenceID *string    `json:"sequence_id"`
	// Ignored: projects were removed. Accepted so older clients still work.
	ProjectID *string `json:"project_id"`
	MissionID *string `json:"mission_id"`
	// Unattributed true clears the sequence and pins that.
	Unattributed bool `json:"unattributed"`
}

// UpdateMeeting applies a patch.
func UpdateMeeting(ctx context.Context, tx pgx.Tx, ws, id string, p MeetingPatch) (Meeting, error) {
	cur, err := GetMeeting(ctx, tx, ws, id)
	if err != nil {
		return cur, err
	}
	if p.Status != nil && !validMeetingStatus(*p.Status) {
		return cur, invalid("status must be booked, held, no_show or canceled")
	}
	if p.Status != nil {
		cur.Status = *p.Status
	}
	if p.Qualified != nil {
		cur.Qualified = p.Qualified
	}
	if p.Notes != nil {
		cur.Notes = strings.TrimSpace(*p.Notes)
	}
	if p.Title != nil {
		cur.Title = strings.TrimSpace(*p.Title)
	}
	if p.StartsAt != nil {
		cur.StartsAt = *p.StartsAt
	}
	if p.MissionID != nil {
		cur.MissionID = p.MissionID
		if *p.MissionID == "" {
			cur.MissionID = nil
		}
	}
	if p.Unattributed {
		cur.SequenceID, cur.Attribution, cur.AttributionPinned = nil, AttrNone, true
	}
	if p.SequenceID != nil {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM sequences WHERE workspace_id = $1 AND id = $2)`, ws, *p.SequenceID).Scan(&ok); err != nil {
			return cur, err
		}
		if !ok {
			return cur, invalid("sequence_id is not a sequence in this workspace")
		}
		cur.SequenceID, cur.Attribution, cur.AttributionPinned = p.SequenceID, AttrManual, true
	}
	_, err = tx.Exec(ctx, `UPDATE meetings SET status = $3, qualified = $4, notes = $5, title = $6, starts_at = $7, mission_id = $8,
		sequence_id = $9, attribution = $10, attribution_pinned = $11, updated_at = now()
		WHERE workspace_id = $1 AND id = $2`,
		ws, id, cur.Status, cur.Qualified, cur.Notes, cur.Title, cur.StartsAt, cur.MissionID,
		cur.SequenceID, cur.Attribution, cur.AttributionPinned)
	if err != nil {
		return cur, err
	}
	return GetMeeting(ctx, tx, ws, id)
}

// DeleteMeeting removes a meeting. A synced meeting comes back on the next
// sync unless it is removed at the source; mark it canceled instead.
func DeleteMeeting(ctx context.Context, tx pgx.Tx, ws, id string) error {
	tag, err := tx.Exec(ctx, `DELETE FROM meetings WHERE workspace_id = $1 AND id = $2`, ws, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SyncedMeeting is a meeting as a calendar, booking tool or CRM reports it.
type SyncedMeeting struct {
	ExternalID string
	Title      string
	StartsAt   time.Time
	EndsAt     *time.Time
	BookedAt   *time.Time
	Canceled   bool
	// Status reported by the source when it knows more than booked or
	// canceled, e.g. a CRM meeting outcome: held or no_show.
	Status         string
	OrganizerEmail string
	// Every attendee, including the workspace's own people.
	Attendees []Attendee
	// LeadID when the source already resolved a lead (e.g. a CRM contact).
	LeadID *string
}

// Attendee is one invitee.
type Attendee struct {
	Email string
	Name  string
}

// UpsertSynced records a synced meeting. It returns false without storing
// anything when no attendee is tied to outreach: the rest of a calendar is
// never copied. A meeting already stored keeps its pinned attribution and
// any status a person set (held, no_show), unless the source canceled it.
func UpsertSynced(ctx context.Context, tx pgx.Tx, ws, source, integrationID string, sm SyncedMeeting, ownDomains []string) (bool, error) {
	emails := make([]string, 0, len(sm.Attendees))
	names := map[string]string{}
	for _, a := range sm.Attendees {
		emails = append(emails, a.Email)
		names[strings.ToLower(strings.TrimSpace(a.Email))] = a.Name
	}
	ext, err := External(ctx, tx, ws, emails, ownDomains)
	if err != nil {
		return false, err
	}
	booked := time.Now()
	if sm.BookedAt != nil {
		booked = *sm.BookedAt
	}
	var m Match
	if sm.LeadID != nil {
		m, err = AttributeLead(ctx, tx, ws, *sm.LeadID, booked)
	} else {
		m, err = Attribute(ctx, tx, ws, ext, booked)
	}
	if err != nil {
		return false, err
	}
	var existing string
	err = tx.QueryRow(ctx, `SELECT id FROM meetings WHERE workspace_id = $1 AND source = $2 AND external_id = $3`,
		ws, source, sm.ExternalID).Scan(&existing)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, err
	}
	if sm.StartsAt.IsZero() {
		// A deletion notice without the event: cancel a stored meeting.
		if existing == "" || !sm.Canceled {
			return false, nil
		}
		_, err = tx.Exec(ctx, `UPDATE meetings SET status = 'canceled', updated_at = now() WHERE id = $1`, existing)
		return err == nil, err
	}
	if m.Attribution == AttrNone && existing == "" {
		return false, nil
	}
	attendee := ""
	if m.LeadID != nil {
		_ = tx.QueryRow(ctx, `SELECT lower(email) FROM leads WHERE id = $1`, *m.LeadID).Scan(&attendee)
	}
	if attendee == "" && len(ext) > 0 {
		attendee = ext[0]
	}
	status := MeetingBooked
	if sm.Status == MeetingHeld || sm.Status == MeetingNoShow {
		status = sm.Status
	}
	if sm.Canceled {
		status = MeetingCanceled
	}
	if existing == "" && m.LeadID != nil {
		// The same meeting seen through another source (the calendar and
		// the CRM both log it): keep one row, and take a held or no-show
		// outcome from whichever source knows it.
		var dup string
		err := tx.QueryRow(ctx, `SELECT id FROM meetings WHERE workspace_id = $1 AND lead_id = $2 AND source <> $3
			AND starts_at BETWEEN $4::timestamptz - interval '15 minutes' AND $4::timestamptz + interval '15 minutes'
			AND status <> 'canceled' LIMIT 1`, ws, *m.LeadID, source, sm.StartsAt).Scan(&dup)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return false, err
		}
		if dup != "" {
			if status == MeetingHeld || status == MeetingNoShow {
				_, err = tx.Exec(ctx, `UPDATE meetings SET status = $2, updated_at = now() WHERE id = $1 AND status = 'booked'`, dup, status)
			}
			return false, err
		}
	}
	if existing == "" {
		_, err = tx.Exec(ctx, `
			INSERT INTO meetings (workspace_id, sequence_id, lead_id, inbound_id, source, integration_id, external_id,
				title, starts_at, ends_at, booked_at, status, attendee_email, attendee_name, organizer_email, attribution)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)`,
			ws, m.SequenceID, m.LeadID, m.InboundID, source, ptr(integrationID), sm.ExternalID,
			truncate(sm.Title, 500), sm.StartsAt, sm.EndsAt, booked, status, attendee, truncate(names[attendee], 200),
			strings.ToLower(sm.OrganizerEmail), m.Attribution)
		return err == nil, err
	}
	reported := "" // a status the source knows better than booked
	if status != MeetingBooked {
		reported = status
	}
	// Times and titles follow the source; a reschedule back to booked
	// clears a canceled status, but held and no-show stay as a person set them.
	_, err = tx.Exec(ctx, `
		UPDATE meetings SET title = $3, starts_at = $4, ends_at = $5,
			status = CASE WHEN $6 THEN 'canceled' WHEN $12 <> '' THEN $12 WHEN status = 'canceled' THEN 'booked' ELSE status END,
			organizer_email = $7,
			lead_id = CASE WHEN attribution_pinned THEN lead_id ELSE COALESCE($8, lead_id) END,
			sequence_id = CASE WHEN attribution_pinned THEN sequence_id ELSE COALESCE($9, sequence_id) END,
			inbound_id = COALESCE(inbound_id, $10),
			attribution = CASE WHEN attribution_pinned OR $11 = 'none' THEN attribution ELSE $11 END,
			updated_at = now()
		WHERE workspace_id = $1 AND id = $2`,
		ws, existing, truncate(sm.Title, 500), sm.StartsAt, sm.EndsAt, sm.Canceled, strings.ToLower(sm.OrganizerEmail),
		m.LeadID, m.SequenceID, m.InboundID, m.Attribution, reported)
	return err == nil, err
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	for n > 0 && (s[n]&0xC0) == 0x80 {
		n--
	}
	return s[:n]
}
