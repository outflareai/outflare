package outcomes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/audit"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Every deal is a CRM record of the workspace's Deal type with the same id
// (migration 0228). A deal made in the CRM, by hand, a reply rule or an
// import, gets its deals row from the database; this job then ties it to
// the outreach that led to it, as a synced deal is tied when it arrives.

// JobAttributeDeal attributes one deal made in the CRM.
const JobAttributeDeal = "deals.attribute"

func (s *Syncer) attributeDealJob(ctx context.Context, job jobs.Job) error {
	var p struct {
		DealID string `json:"deal_id"`
	}
	if err := json.Unmarshal(job.Payload, &p); err != nil || job.WorkspaceID == nil || p.DealID == "" {
		return fmt.Errorf("%w: bad deal payload", jobs.ErrPermanent)
	}
	ws := *job.WorkspaceID
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		return AttributeDeal(ctx, tx, ws, p.DealID)
	})
	if err != nil {
		track.Fail(ctx, s.Log, track.Event{Name: "deal.attributed", WorkspaceID: ws, ActorType: "system", Surface: "job"}, "internal", err)
	}
	return err
}

// AttributeDeal ties a deal nobody has attributed to the outreach that
// reached its person: their lead, else a lead at their company. A deal
// someone attributed or pinned by hand is left as it is.
func AttributeDeal(ctx context.Context, tx pgx.Tx, ws, id string) error {
	var attribution string
	var pinned bool
	var person *string
	var opened time.Time
	err := tx.QueryRow(ctx, `SELECT attribution, attribution_pinned, record_id::text, opened_at FROM deals
		WHERE workspace_id = $1 AND id = $2 FOR UPDATE`, ws, id).Scan(&attribution, &pinned, &person, &opened)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil || pinned || attribution != AttrNone || person == nil {
		return err
	}
	at := opened.Add(time.Hour)
	var m Match
	var lead string
	err = tx.QueryRow(ctx, `SELECT id FROM leads WHERE workspace_id = $1 AND record_id = $2 ORDER BY created_at DESC LIMIT 1`, ws, *person).Scan(&lead)
	switch {
	case err == nil:
		m, err = AttributeLead(ctx, tx, ws, lead, at)
	case errors.Is(err, pgx.ErrNoRows):
		var emails []string
		if err = tx.QueryRow(ctx, `SELECT COALESCE(array_agg(value ORDER BY verified DESC, valid_from), '{}') FROM record_identities
			WHERE workspace_id = $1 AND record_id = $2 AND namespace = 'email' AND valid_to IS NULL`, ws, *person).Scan(&emails); err == nil {
			m, err = Attribute(ctx, tx, ws, emails, at)
		}
	}
	if err != nil || m.Attribution == AttrNone {
		return err
	}
	var meeting *string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM meetings WHERE workspace_id = $1 AND lead_id = $2 AND booked_at <= $3 AND status <> 'canceled'
		ORDER BY booked_at DESC LIMIT 1`, ws, *m.LeadID, at).Scan(&meeting); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE deals SET lead_id = $3, sequence_id = $4, project_id = $5, meeting_id = COALESCE(meeting_id, $6),
		attribution = $7, updated_at = now() WHERE workspace_id = $1 AND id = $2`,
		ws, id, m.LeadID, m.SequenceID, m.ProjectID, meeting, m.Attribution); err != nil {
		return err
	}
	return audit.Record(ctx, tx, audit.Event{WorkspaceID: ws, ActorType: "system", Action: "deal.attributed", TargetType: "deal", TargetID: id,
		Data:  map[string]any{"attribution": m.Attribution, "sequence_id": m.SequenceID, "project_id": m.ProjectID, "meeting_id": meeting},
		Props: map[string]any{"attribution": m.Attribution, "meeting": meeting != nil}})
}
