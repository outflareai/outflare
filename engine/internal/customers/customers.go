// Package customers is how other parts of the engine read and write the
// customer data platform without knowing its tables: who an identity
// belongs to, what a person agreed to be sent, and recording an event on
// their timeline. The API handlers in engine/internal/api own everything
// else (records, identities, the catalog).
package customers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/outflareai/outflare/engine/internal/audit"
	"github.com/outflareai/outflare/engine/internal/jobs"
)

// Querier is a pool, a connection or a transaction.
type Querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// OwnerOf returns the record holding an identity verified, such as the
// person who proved they own an email address. An unverified identity
// never makes anyone its owner, so ok is false for an address someone only
// typed in.
func OwnerOf(ctx context.Context, q Querier, workspaceID, namespace, value string) (recordID string, ok bool, err error) {
	value = strings.TrimSpace(value)
	if namespace == "email" || namespace == "domain" {
		value = strings.ToLower(value)
	}
	err = q.QueryRow(ctx, `SELECT i.record_id::text FROM record_identities i JOIN records r ON r.id = i.record_id
		WHERE i.workspace_id = $1 AND i.namespace = $2 AND i.value = $3 AND i.valid_to IS NULL AND i.verified
		AND r.merged_into IS NULL`, workspaceID, namespace, value).Scan(&recordID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	return recordID, err == nil, err
}

// EnsurePerson returns the person an email, phone or other identity belongs
// to, making one (origin first_party) when nobody holds it. verified says
// the person just proved they own it, such as by clicking a link sent to
// that inbox: the identity's verified owner is theirs, an unverified copy on
// exactly one person is verified there, and with several unverified copies
// (someone typed another person's address) the inbox's owner gets a new
// record of their own. Run it in the same transaction as what it is for.
func EnsurePerson(ctx context.Context, q Querier, workspaceID, namespace, value string, verified bool, requestID string) (string, error) {
	value = strings.TrimSpace(value)
	if namespace == "email" || namespace == "domain" {
		value = strings.ToLower(value)
	}
	if id, ok, err := OwnerOf(ctx, q, workspaceID, namespace, value); err != nil || ok {
		return id, err
	}
	var holders []string
	var oldest string
	err := q.QueryRow(ctx, `SELECT COALESCE(array_agg(i.record_id::text ORDER BY i.valid_from), '{}') FROM record_identities i JOIN records r ON r.id = i.record_id
		WHERE i.workspace_id = $1 AND i.namespace = $2 AND i.value = $3 AND i.valid_to IS NULL AND r.merged_into IS NULL AND r.record_type = 'person'`,
		workspaceID, namespace, value).Scan(&holders)
	if err != nil {
		return "", err
	}
	if len(holders) > 0 {
		oldest = holders[0]
	}
	switch {
	case !verified && oldest != "":
		return oldest, nil
	case verified && len(holders) == 1:
		if _, err := q.Exec(ctx, `UPDATE record_identities SET verified = true, verified_at = now()
			WHERE record_id = $1 AND namespace = $2 AND value = $3 AND valid_to IS NULL`, oldest, namespace, value); err != nil {
			return "", err
		}
		if _, err := q.Exec(ctx, `UPDATE records SET version = version + 1, updated_at = now() WHERE id = $1`, oldest); err != nil {
			return "", err
		}
		return oldest, audit.Record(ctx, q, audit.Event{WorkspaceID: workspaceID, Action: "identity.verified", TargetType: "record", TargetID: oldest,
			RequestID: requestID, ActorType: "system", Data: map[string]any{"record_id": oldest, "namespace": namespace, "verified": true}})
	}
	var id string
	if err := q.QueryRow(ctx, `INSERT INTO records (workspace_id, record_type, brand_id, origin) VALUES ($1, 'person', outflare_first_brand($1), 'first_party') RETURNING id::text`, workspaceID).Scan(&id); err != nil {
		return "", err
	}
	if _, err := q.Exec(ctx, `INSERT INTO record_identities (workspace_id, record_id, namespace, value, verified, verified_at, source)
		VALUES ($1, $2, $3, $4, $5, CASE WHEN $5 THEN now() END, 'system')`, workspaceID, id, namespace, value, verified); err != nil {
		return "", err
	}
	return id, audit.Record(ctx, q, audit.Event{WorkspaceID: workspaceID, Action: "record.created", TargetType: "record", TargetID: id,
		RequestID: requestID, ActorType: "system", Data: map[string]any{"type": "person", "origin": "first_party", "identities": 1}})
}

// ConsentChange is a grant or withdrawal another feature records, such as
// lifecycle email's unsubscribe link.
type ConsentChange struct {
	RecordID string
	Channel  string // email, sms, push, whatsapp or in_app
	Topic    string // a topic's key, or "" for the whole channel
	Purpose  string // default marketing
	Status   string // Granted or Withdrawn
	Source   string // such as unsubscribe_link or preference_center
	Evidence map[string]any
	// Default now.
	OccurredAt time.Time
	RequestID  string
}

// RecordConsent adds a consent change to the person's log. The latest one
// for a channel, topic and purpose is what Consent returns.
func RecordConsent(ctx context.Context, q Querier, workspaceID string, c ConsentChange) error {
	if c.Purpose == "" {
		c.Purpose = "marketing"
	}
	if c.Source == "" {
		c.Source = "system"
	}
	if c.Evidence == nil {
		c.Evidence = map[string]any{}
	}
	if c.OccurredAt.IsZero() {
		c.OccurredAt = time.Now()
	}
	if c.Status != Granted && c.Status != Withdrawn {
		return errors.New("customers: consent status is granted or withdrawn")
	}
	evidence, err := json.Marshal(c.Evidence)
	if err != nil {
		return err
	}
	tag, err := q.Exec(ctx, `INSERT INTO consents (workspace_id, record_id, channel, topic, purpose, status, source, evidence, occurred_at, request_id)
		SELECT $1, r.id, $3, $4, $5, $6, $7, $8, $9, $10 FROM records r WHERE r.id = $2 AND r.workspace_id = $1`,
		workspaceID, c.RecordID, c.Channel, c.Topic, c.Purpose, c.Status, c.Source, string(evidence), c.OccurredAt, c.RequestID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errors.New("customers: no such record in this workspace")
	}
	if err := audit.Record(ctx, q, audit.Event{WorkspaceID: workspaceID, Action: "consent." + c.Status, TargetType: "record", TargetID: c.RecordID,
		RequestID: c.RequestID, ActorType: "system", Data: map[string]any{"channel": c.Channel, "purpose": c.Purpose, "topic": c.Topic != "", "source": c.Source}}); err != nil {
		return err
	}
	_, err = OptedIn(ctx, q, workspaceID, c.RecordID, c.Channel, c.Topic, c.Purpose, c.Status, c.Source, c.RequestID)
	return err
}

// Consent statuses.
const (
	Granted   = "granted"
	Withdrawn = "withdrawn"
)

// Consent returns a record's current consent for a channel (email, sms,
// push, whatsapp, in_app), topic and purpose: Granted, Withdrawn or "" when
// it never said. A row for the topic wins over one for the whole channel
// (topic ""), and a withdrawal from the whole channel wins over any topic's
// grant made before it.
func Consent(ctx context.Context, q Querier, workspaceID, recordID, channel, topic, purpose string) (status string, at time.Time, err error) {
	if purpose == "" {
		purpose = "marketing"
	}
	var all, one *string
	var allAt, oneAt *time.Time
	err = q.QueryRow(ctx, `
		SELECT
		  (SELECT status FROM consents WHERE workspace_id = $1 AND record_id = $2 AND channel = $3 AND purpose = $5 AND topic = ''
		     ORDER BY occurred_at DESC, created_at DESC LIMIT 1),
		  (SELECT occurred_at FROM consents WHERE workspace_id = $1 AND record_id = $2 AND channel = $3 AND purpose = $5 AND topic = ''
		     ORDER BY occurred_at DESC, created_at DESC LIMIT 1),
		  (SELECT status FROM consents WHERE workspace_id = $1 AND record_id = $2 AND channel = $3 AND purpose = $5 AND topic = $4 AND $4 <> ''
		     ORDER BY occurred_at DESC, created_at DESC LIMIT 1),
		  (SELECT occurred_at FROM consents WHERE workspace_id = $1 AND record_id = $2 AND channel = $3 AND purpose = $5 AND topic = $4 AND $4 <> ''
		     ORDER BY occurred_at DESC, created_at DESC LIMIT 1)`,
		workspaceID, recordID, channel, topic, purpose).Scan(&all, &allAt, &one, &oneAt)
	if err != nil {
		return "", time.Time{}, err
	}
	switch {
	case one != nil && all != nil && *all == Withdrawn && allAt.After(*oneAt):
		return Withdrawn, *allAt, nil
	case one != nil:
		return *one, *oneAt, nil
	case all != nil:
		return *all, *allAt, nil
	}
	return "", time.Time{}, nil
}

// Event is a product event another feature records on a person's timeline,
// such as lifecycle email's email.delivered.
type Event struct {
	RecordID   string
	Name       string // a catalog name: lowercase words joined by dots or underscores
	Properties map[string]any
	Source     string // the feature that recorded it, such as lifecycle
	OccurredAt time.Time
	// The feature's own id for it, so recording it twice stores it once.
	MessageID string
	RequestID string
}

// Record stores an event on a record's timeline. It reports false when an
// event with the same MessageID is already stored, or the workspace has no
// such record.
func Record(ctx context.Context, q Querier, workspaceID string, ev Event) (bool, error) {
	id, err := RecordEvent(ctx, q, workspaceID, ev)
	return id != "", err
}

// RecordEvent is Record returning the stored event's id, or "" when it
// stored nothing.
func RecordEvent(ctx context.Context, q Querier, workspaceID string, ev Event) (string, error) {
	if ev.Properties == nil {
		ev.Properties = map[string]any{}
	}
	props, err := json.Marshal(ev.Properties)
	if err != nil {
		return "", err
	}
	if ev.OccurredAt.IsZero() {
		ev.OccurredAt = time.Now()
	}
	if ev.Source == "" {
		ev.Source = "system"
	}
	var msg *string
	if ev.MessageID != "" {
		msg = &ev.MessageID
	}
	var id string
	err = q.QueryRow(ctx, `INSERT INTO customer_events (workspace_id, record_id, brand_id, kind, name, raw_name, properties, source, message_id, occurred_at, request_id)
		SELECT $1, r.id, COALESCE(r.brand_id, outflare_first_brand($1)), 'track', $3, $3, $4, $5, $6, $7, $8 FROM records r WHERE r.id = $2 AND r.workspace_id = $1
		ON CONFLICT (workspace_id, message_id) DO NOTHING RETURNING id::text`,
		workspaceID, ev.RecordID, ev.Name, string(props), ev.Source, msg, ev.OccurredAt, ev.RequestID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	// The catalog entry goes in before journeys hear of the event: outside a
	// transaction the job could otherwise run before the name is approved.
	if _, err := q.Exec(ctx, `INSERT INTO event_definitions (workspace_id, name, status, first_seen_at, last_seen_at, seen_count)
		VALUES ($1, $2, 'approved', $3, $3, 1)
		ON CONFLICT (workspace_id, name) DO UPDATE SET seen_count = event_definitions.seen_count + 1,
			last_seen_at = GREATEST(event_definitions.last_seen_at, EXCLUDED.last_seen_at),
			first_seen_at = LEAST(COALESCE(event_definitions.first_seen_at, EXCLUDED.first_seen_at), EXCLUDED.first_seen_at)`,
		workspaceID, ev.Name, ev.OccurredAt); err != nil {
		return "", err
	}
	if err := EventAdded(ctx, q, workspaceID, id, ev.Name); err != nil {
		return "", err
	}
	return id, nil
}

// JobJourneyEvent is the job that hands a new event to journeys.
const JobJourneyEvent = "journey.event"

// JobJourneyRun is the job that moves a journey run along
// (journeys.JobRun).
const JobJourneyRun = "journey.run"

// The jobs that hand changes to the rules engine (engine/internal/rules).
const (
	JobRuleEvent    = "rule.event"
	JobRuleChange   = "rule.change"
	JobRuleAudience = "rule.audience"
)

// EventAdded tells journeys and rules about a new event on a person's
// timeline when an active journey starts or ends on its name, or an active
// rule listens to it. Call it in the transaction that stored the event.
func EventAdded(ctx context.Context, q Querier, workspaceID, eventID, name string) error {
	var journey, rule bool
	if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM journeys WHERE workspace_id = $1
		AND ((status = 'active' AND trigger_event = $2) OR (status IN ('active', 'paused') AND (goal_event = $2 OR $2 = ANY (exit_events)))))
		OR EXISTS (SELECT 1 FROM journey_runs WHERE workspace_id = $1 AND status = 'active' AND wait_events @> ARRAY[$2]::text[]),
		EXISTS (SELECT 1 FROM rules WHERE workspace_id = $1 AND status = 'active' AND $2 = ANY (watch_events))`,
		workspaceID, name).Scan(&journey, &rule); err != nil {
		return err
	}
	if journey {
		if _, err := jobs.Enqueue(ctx, q, jobs.NewJob{Kind: JobJourneyEvent, WorkspaceID: &workspaceID, Payload: map[string]string{"event_id": eventID},
			IdempotencyKey: "journey-event:" + eventID}); err != nil {
			return err
		}
	}
	if rule {
		if _, err := jobs.Enqueue(ctx, q, jobs.NewJob{Kind: JobRuleEvent, WorkspaceID: &workspaceID, Payload: map[string]string{"event_id": eventID},
			IdempotencyKey: "rule-event:" + eventID}); err != nil {
			return err
		}
	}
	return nil
}

// FieldChange is one field of a record that changed, for rules that test
// it.
type FieldChange struct {
	RecordID string `json:"record_id"`
	Field    string `json:"field"`
	// The value before, and whether there was one.
	Old    any    `json:"old,omitempty"`
	HadOld bool   `json:"had_old"`
	Source string `json:"source"`
}

// FieldChanged tells rules that a record's field changed when an active
// rule tests that field, and has journey runs waiting for the record's
// conditions to come true look again. Call it in the transaction that
// changed it.
func FieldChanged(ctx context.Context, q Querier, workspaceID string, c FieldChange) error {
	// One statement: the waiting runs look again now, each in a job.
	tag, err := q.Exec(ctx, `WITH w AS (
			UPDATE journey_runs SET next_at = now(), updated_at = now()
			WHERE workspace_id = $1 AND record_id = $2 AND status = 'active' AND wait_fields RETURNING id, workspace_id)
		INSERT INTO jobs (kind, payload, workspace_id, run_at, idempotency_key)
		SELECT $3, jsonb_build_object('run_id', w.id::text), w.workspace_id, now(), 'journey-run:' || w.id::text || ':field:' || $4::text FROM w`,
		workspaceID, c.RecordID, JobJourneyRun, fmt.Sprint(time.Now().UnixNano()))
	if err != nil {
		return err
	}
	if tag.RowsAffected() > 0 {
		if _, err := q.Exec(ctx, `SELECT pg_notify('outflare_jobs', '')`); err != nil {
			return err
		}
	}
	var hit bool
	if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM rules WHERE workspace_id = $1 AND status = 'active' AND trigger_kind = 'change'
		AND $2 = ANY (watch_fields))`, workspaceID, c.Field).Scan(&hit); err != nil || !hit {
		return err
	}
	_, err = jobs.Enqueue(ctx, q, jobs.NewJob{Kind: JobRuleChange, WorkspaceID: &workspaceID, Payload: c})
	return err
}

// ListWatched reports whether an active rule acts on people joining or
// leaving a smart list, so its refresh says who did.
func ListWatched(ctx context.Context, q Querier, workspaceID, listID string) (bool, error) {
	var hit bool
	err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM rules WHERE workspace_id = $1 AND status = 'active' AND list_id = $2)`,
		workspaceID, listID).Scan(&hit)
	return hit, err
}

// ListChange is people who joined or left a smart list in one refresh.
type ListChange struct {
	ListID    string    `json:"list_id"`
	On        string    `json:"on"` // enter or leave
	RecordIDs []string  `json:"record_ids"`
	At        time.Time `json:"at"`
}

// listBatch is the most people one rule.audience job takes.
const listBatch = 500

// ListChanged hands who joined or left a smart list to rules, in jobs of
// up to 500 people. Call it in the refresh's transaction.
func ListChanged(ctx context.Context, q Querier, workspaceID string, c ListChange) error {
	for start := 0; start < len(c.RecordIDs); start += listBatch {
		part := c
		part.RecordIDs = c.RecordIDs[start:min(start+listBatch, len(c.RecordIDs))]
		if _, err := jobs.Enqueue(ctx, q, jobs.NewJob{Kind: JobRuleAudience, WorkspaceID: &workspaceID, Payload: part,
			IdempotencyKey: fmt.Sprintf("rule-audience:%s:%s:%d:%d", c.ListID, c.On, c.At.UnixNano(), start)}); err != nil {
			return err
		}
	}
	return nil
}
