package outcomes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/jobs"
)

// Connector capabilities for outcomes.
const (
	CapMeetings  = "meetings.sync" // implements MeetingSource
	CapDeals     = "deals.sync"    // implements DealSource
	CapPushCRM   = "crm.push"      // implements CRMWriter
	streamMeets  = "meetings"
	streamDeals  = "deals"
	streamPush   = "push"
	JobSync      = "outcomes.sync"
	syncInterval = 15 * time.Minute
)

// SourceConfig is what outcome connectors read from an integration's
// config. Every field is optional.
type SourceConfig struct {
	// The company's own domains, so colleagues on an invite are never
	// taken for leads. The connected account's domain is always included.
	OwnDomains []string `json:"own_domains,omitempty"`
	// Calendar to read, for calendar connectors. Default: the primary one.
	CalendarID string `json:"calendar_id,omitempty"`
	// Write meetings booked from outreach into the CRM. Off by default:
	// many CRMs already log meetings from their own calendar sync.
	PushMeetings bool `json:"push_meetings,omitempty"`
	// Pause syncing without disconnecting.
	Paused bool `json:"paused,omitempty"`
	// Reply labels and groups (such as "hot") whose replies are written
	// into the CRM: the contact, a note and, with ReplyTasks, a task to
	// follow up. Empty writes none.
	PushReplies []string `json:"push_replies,omitempty"`
	// ReplyTasks adds a follow-up task with each reply written.
	ReplyTasks bool `json:"reply_tasks,omitempty"`
}

// MeetingPage is one run's worth of meetings from a source.
type MeetingPage struct {
	Meetings []SyncedMeeting
	// Cursor to pass next time. Empty keeps the previous one.
	Cursor string
	// More is true when the source has more right now; the sync runs again
	// straight away.
	More bool
	// Reset asks to drop the cursor and start over (an expired sync token).
	Reset bool
}

// MeetingSource reads meetings from a calendar, booking tool or CRM. It
// only reads.
type MeetingSource interface {
	connectors.Connector
	SyncMeetings(ctx context.Context, c *connectors.Client, creds connectors.Creds, cfg SourceConfig, account connectors.Account, cursor string) (MeetingPage, error)
}

// DealPage is one run's worth of deals.
type DealPage struct {
	Deals  []SyncedDeal
	Cursor string
	More   bool
}

// DealSource reads deals from a CRM. It only reads.
type DealSource interface {
	connectors.Connector
	SyncDeals(ctx context.Context, c *connectors.Client, creds connectors.Creds, cursor string) (DealPage, error)
}

// PushMeeting is a meeting to write into a CRM.
type PushMeeting struct {
	Meeting
	LeadFirstName, LeadLastName, LeadCompany string
}

// CRMWriter writes outcomes into a CRM. It returns the CRM's id for the
// meeting so the meeting is written once.
type CRMWriter interface {
	connectors.Connector
	PushMeeting(ctx context.Context, c *connectors.Client, creds connectors.Creds, m PushMeeting) (string, error)
}

// Syncer runs outcome syncs as jobs.
type Syncer struct {
	Pool  *pgxpool.Pool
	Store *connectors.Store
	Log   *slog.Logger
	// PublicURL is where links written into the CRM point.
	PublicURL string
}

type syncPayload struct {
	Integration string `json:"integration_id"`
}

// Register adds the sync, CRM push and deal attribution job handlers.
func (s *Syncer) Register(q *jobs.Queue) {
	q.Handle(JobSync, s.syncJob)
	q.Handle(JobPushReply, s.HandlePushReply)
	q.Handle(JobAttributeDeal, s.attributeDealJob)
}

// Run enqueues a sync for every outcome integration every interval.
func (s *Syncer) Run(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		if err := s.tick(ctx); err != nil && ctx.Err() == nil {
			s.Log.Error("outcomes: schedule syncs", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// outcomeConnectors are the registry's connectors that sync outcomes.
func (s *Syncer) outcomeConnectors() []string {
	var ids []string
	for _, d := range s.Store.Registry.All() {
		if d.Status == connectors.Available && (d.Has(CapMeetings) || d.Has(CapDeals)) {
			ids = append(ids, d.ID)
		}
	}
	return ids
}

func (s *Syncer) tick(ctx context.Context) error {
	ids := s.outcomeConnectors()
	if len(ids) == 0 {
		return nil
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT i.id, i.workspace_id FROM integrations i
		WHERE i.connector = ANY($1) AND i.status = 'connected' AND NOT COALESCE((i.config->>'paused')::boolean, false)
		  AND NOT EXISTS (SELECT 1 FROM outcome_syncs o WHERE o.integration_id = i.id AND o.last_run_at > now() - $2::interval)`,
		ids, fmt.Sprintf("%d seconds", int(syncInterval.Seconds())))
	if err != nil {
		return err
	}
	type due struct{ id, ws string }
	var list []due
	for rows.Next() {
		var d due
		if err := rows.Scan(&d.id, &d.ws); err != nil {
			rows.Close()
			return err
		}
		list = append(list, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	slot := time.Now().Truncate(syncInterval).Unix()
	for _, d := range list {
		if _, err := Enqueue(ctx, s.Pool, d.ws, d.id, fmt.Sprintf("%s:%d", d.id, slot)); err != nil {
			return err
		}
	}
	return nil
}

// Enqueue asks for a sync of one integration. Key de-duplicates; pass ""
// for a sync someone asked for now.
func Enqueue(ctx context.Context, q jobs.Querier, ws, integrationID, key string) (int64, error) {
	if key == "" {
		key = fmt.Sprintf("%s:now:%d", integrationID, time.Now().Unix()/10)
	}
	return jobs.Enqueue(ctx, q, jobs.NewJob{Kind: JobSync, WorkspaceID: &ws, Payload: syncPayload{Integration: integrationID},
		IdempotencyKey: key, MaxAttempts: 3})
}

func (s *Syncer) syncJob(ctx context.Context, job jobs.Job) error {
	var p syncPayload
	if err := json.Unmarshal(job.Payload, &p); err != nil || job.WorkspaceID == nil {
		return nil // malformed: nothing to retry
	}
	more, err := s.SyncOnce(ctx, *job.WorkspaceID, p.Integration)
	if errors.Is(err, connectors.ErrNotFound) {
		return nil // disconnected meanwhile
	}
	if err != nil {
		return err
	}
	if more {
		_, err = Enqueue(ctx, s.Pool, *job.WorkspaceID, p.Integration, fmt.Sprintf("%s:more:%d", p.Integration, job.ID))
	}
	return err
}

// SyncOnce runs every stream of one integration once. more reports that a
// source has more waiting.
func (s *Syncer) SyncOnce(ctx context.Context, ws, integrationID string) (more bool, err error) {
	o, err := s.Store.Open(ctx, s.Pool, ws, integrationID)
	if err != nil {
		return false, err
	}
	var cfg SourceConfig
	_ = json.Unmarshal(o.Config, &cfg)
	if cfg.Paused {
		return false, nil
	}
	var firstErr error
	note := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if src, ok := o.Connector.(MeetingSource); ok && o.Connector.Definition().Has(CapMeetings) {
		m, err := s.syncMeetings(ctx, o, src, cfg)
		more = more || m
		note(err)
	}
	if src, ok := o.Connector.(DealSource); ok && o.Connector.Definition().Has(CapDeals) {
		m, err := s.syncDeals(ctx, o, src)
		more = more || m
		note(err)
	}
	if w, ok := o.Connector.(CRMWriter); ok && cfg.PushMeetings && o.Connector.Definition().Has(CapPushCRM) {
		note(s.push(ctx, o, w))
	}
	// Only credential problems mark the integration broken.
	if firstErr != nil && connectors.KindOf(firstErr) == connectors.ErrAuth {
		_ = s.Store.Checked(ctx, s.Pool, ws, integrationID, nil, firstErr)
	}
	if firstErr != nil && connectors.KindOf(firstErr) != connectors.ErrUnavailable {
		// Retrying a refused key or a bad request will not help; the error
		// is shown on the sync status instead.
		s.Log.Warn("outcomes: sync failed", "integration", integrationID, "err", firstErr)
		return more, nil
	}
	return more, firstErr
}

func (s *Syncer) cursor(ctx context.Context, ws, id, stream string) (string, error) {
	var c string
	err := s.Pool.QueryRow(ctx, `SELECT cursor FROM outcome_syncs WHERE integration_id = $1 AND stream = $2`, id, stream).Scan(&c)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return c, err
}

func (s *Syncer) finish(ctx context.Context, q jobs.Querier, ws, id, stream, cursor string, changes int, runErr error) error {
	var msg *string
	if runErr != nil {
		m := runErr.Error()
		msg = &m
	}
	_, err := q.Exec(ctx, `
		INSERT INTO outcome_syncs (integration_id, stream, workspace_id, cursor, last_run_at, last_ok_at, last_error, last_changes)
		VALUES ($1, $2, $3, $4, now(), CASE WHEN $5::text IS NULL THEN now() END, $5, $6)
		ON CONFLICT (integration_id, stream) DO UPDATE SET
			cursor = CASE WHEN $5::text IS NULL THEN $4 ELSE outcome_syncs.cursor END,
			last_run_at = now(),
			last_ok_at = CASE WHEN $5::text IS NULL THEN now() ELSE outcome_syncs.last_ok_at END,
			last_error = $5,
			last_changes = CASE WHEN $5::text IS NULL THEN $6 ELSE outcome_syncs.last_changes END`,
		id, stream, ws, cursor, msg, changes)
	return err
}

func (s *Syncer) syncMeetings(ctx context.Context, o *connectors.Opened, src MeetingSource, cfg SourceConfig) (bool, error) {
	cur, err := s.cursor(ctx, o.WorkspaceID, o.ID, streamMeets)
	if err != nil {
		return false, err
	}
	page, err := src.SyncMeetings(ctx, o.Client, o.Creds, cfg, o.Account, cur)
	if err != nil {
		return false, errors.Join(err, s.finish(ctx, s.Pool, o.WorkspaceID, o.ID, streamMeets, cur, 0, err))
	}
	if page.Reset {
		return true, s.finish(ctx, s.Pool, o.WorkspaceID, o.ID, streamMeets, "", 0, nil)
	}
	own := append([]string{}, cfg.OwnDomains...)
	if d := domainOf(o.Account.Email); d != "" && !IsFreeMail(d) {
		own = append(own, d)
	}
	source := sourceFor(o.Connector.Definition().ID)
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	changes := 0
	for _, m := range page.Meetings {
		ok, err := UpsertSynced(ctx, tx, o.WorkspaceID, source, o.ID, m, own)
		if err != nil {
			return false, err
		}
		if ok {
			changes++
		}
	}
	next := page.Cursor
	if next == "" {
		next = cur
	}
	if err := s.finish(ctx, tx, o.WorkspaceID, o.ID, streamMeets, next, changes, nil); err != nil {
		return false, err
	}
	return page.More, tx.Commit(ctx)
}

func (s *Syncer) syncDeals(ctx context.Context, o *connectors.Opened, src DealSource) (bool, error) {
	cur, err := s.cursor(ctx, o.WorkspaceID, o.ID, streamDeals)
	if err != nil {
		return false, err
	}
	page, err := src.SyncDeals(ctx, o.Client, o.Creds, cur)
	if err != nil {
		return false, errors.Join(err, s.finish(ctx, s.Pool, o.WorkspaceID, o.ID, streamDeals, cur, 0, err))
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	changes := 0
	for _, d := range page.Deals {
		ok, err := UpsertSyncedDeal(ctx, tx, o.WorkspaceID, sourceFor(o.Connector.Definition().ID), o.ID, d)
		if err != nil {
			return false, err
		}
		if ok {
			changes++
		}
	}
	next := page.Cursor
	if next == "" {
		next = cur
	}
	if err := s.finish(ctx, tx, o.WorkspaceID, o.ID, streamDeals, next, changes, nil); err != nil {
		return false, err
	}
	return page.More, tx.Commit(ctx)
}

// push writes meetings booked from outreach into the CRM, oldest first,
// each once. Meetings that came from the same CRM are never written back.
func (s *Syncer) push(ctx context.Context, o *connectors.Opened, w CRMWriter) error {
	source := sourceFor(o.Connector.Definition().ID)
	rows, err := s.Pool.Query(ctx, `
		SELECT `+meetingCols+`, COALESCE(l.first_name, ''), COALESCE(l.last_name, ''), COALESCE(l.company, '')`+meetingFrom+`
		WHERE m.workspace_id = $1 AND m.source <> $2 AND m.attribution <> 'none' AND m.status <> 'canceled'
		  AND m.created_at >= $4
		  AND NOT EXISTS (SELECT 1 FROM outcome_links k WHERE k.integration_id = $3 AND k.kind = 'meeting' AND k.local_id = m.id)
		ORDER BY m.created_at LIMIT 50`, o.WorkspaceID, source, o.ID, o.CreatedAt)
	if err != nil {
		return err
	}
	var list []PushMeeting
	for rows.Next() {
		var p PushMeeting
		err := rows.Scan(&p.ID, &p.ProjectID, &p.SequenceID, &p.MissionID, &p.LeadID, &p.InboundID, &p.Source, &p.IntegrationID,
			&p.ExternalID, &p.Title, &p.StartsAt, &p.EndsAt, &p.BookedAt, &p.Status, &p.AttendeeEmail, &p.AttendeeName,
			&p.OrganizerEmail, &p.Attribution, &p.AttributionPinned, &p.Qualified, &p.Notes, &p.CreatedBy, &p.CreatedAt,
			&p.UpdatedAt, &p.LeadEmail, &p.SequenceName, &p.ProjectName, &p.LeadFirstName, &p.LeadLastName, &p.LeadCompany)
		if err != nil {
			rows.Close()
			return err
		}
		list = append(list, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	pushed := 0
	var runErr error
	for _, p := range list {
		ext, err := w.PushMeeting(ctx, o.Client, o.Creds, p)
		if err != nil {
			runErr = err
			break
		}
		if _, err := s.Pool.Exec(ctx, `INSERT INTO outcome_links (integration_id, workspace_id, kind, local_id, external_id)
			VALUES ($1, $2, 'meeting', $3, $4) ON CONFLICT DO NOTHING`, o.ID, o.WorkspaceID, p.ID, ext); err != nil {
			return err
		}
		pushed++
	}
	return errors.Join(runErr, s.finish(ctx, s.Pool, o.WorkspaceID, o.ID, streamPush, "", pushed, runErr))
}

// sourceFor maps a connector id to a meeting or deal source.
func sourceFor(connector string) string {
	switch connector {
	case "google_calendar":
		return SourceGoogleCalendar
	case "outlook_calendar":
		return SourceMicrosoftCalendar
	case "calendly":
		return SourceCalendly
	case "cal_com":
		return SourceCalCom
	case "hubspot":
		return SourceHubSpot
	}
	return SourceAPI
}

// SyncStatus is one stream of one integration.
type SyncStatus struct {
	IntegrationID string     `json:"integration_id"`
	Connector     string     `json:"connector"`
	Name          string     `json:"name"`
	Stream        string     `json:"stream"`
	LastRunAt     *time.Time `json:"last_run_at"`
	LastOKAt      *time.Time `json:"last_ok_at"`
	LastError     *string    `json:"last_error"`
	LastChanges   int        `json:"last_changes"`
	Paused        bool       `json:"paused"`
}

// ListSyncs returns every outcome integration with the state of its
// streams; an integration that never ran has one row with no times.
func ListSyncs(ctx context.Context, q Querier, ws string, connectorIDs []string) ([]SyncStatus, error) {
	rows, err := q.Query(ctx, `
		SELECT i.id, i.connector, i.name, COALESCE(o.stream, ''), o.last_run_at, o.last_ok_at, o.last_error, COALESCE(o.last_changes, 0),
			COALESCE((i.config->>'paused')::boolean, false)
		FROM integrations i LEFT JOIN outcome_syncs o ON o.integration_id = i.id
		WHERE i.workspace_id = $1 AND i.connector = ANY($2)
		ORDER BY i.created_at, o.stream`, ws, connectorIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SyncStatus{}
	for rows.Next() {
		var st SyncStatus
		if err := rows.Scan(&st.IntegrationID, &st.Connector, &st.Name, &st.Stream, &st.LastRunAt, &st.LastOKAt, &st.LastError,
			&st.LastChanges, &st.Paused); err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

// ConnectorIDs returns the registry's outcome connectors.
func ConnectorIDs(r *connectors.Registry) []string {
	var ids []string
	for _, d := range r.All() {
		if d.Has(CapMeetings) || d.Has(CapDeals) || d.Has(CapPushCRM) {
			ids = append(ids, d.ID)
		}
	}
	return ids
}
