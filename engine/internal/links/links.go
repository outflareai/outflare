// Package links ties campaigns on outside platforms (EmailBison, Smartlead,
// Instantly, Apollo) to Outflare sequences.
//
// The sequence is the logical campaign: it carries the team, priority,
// daily target and brand that mailbox allocation works from. A link says
// which platform campaign it runs as. Importers and the platform sync both
// call Upsert, so a campaign is linked once however it arrives.
package links

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// Control levels.
const (
	Full     = "full"      // Outflare attaches and detaches the campaign's mailboxes
	ReadOnly = "read_only" // the platform picks mailboxes itself; Outflare only watches
)

// Link is a platform campaign linked to a sequence.
type Link struct {
	ID            string
	SequenceID    string
	IntegrationID string
	ExternalID    string
	Name          string
	// Status is the platform's own word: active, paused, completed, draft.
	Status  string
	Control string
	// SequenceDays is how many days a lead spends from first email to last
	// step; nil when unknown.
	SequenceDays *int
	// DailyLimit is the campaign's own daily cap on the platform, if any.
	DailyLimit *int
}

// Querier is a transaction or pool.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// SequenceStatus maps a platform campaign's status to a sequence status.
func SequenceStatus(platform string) string {
	switch platform {
	case "active", "running", "started", "in_progress":
		return "active"
	case "paused", "stopped":
		return "paused"
	case "completed", "finished", "archived", "deleted":
		return "archived"
	}
	return "draft"
}

// Upsert links l.ExternalID on l.IntegrationID to a sequence and returns
// the link's id and sequence. When the platform campaign is already linked,
// its name, status, length and cap are refreshed and the existing sequence
// is kept. Otherwise, with l.SequenceID empty, a sequence is created for
// it: named after the campaign, in the platform campaign's status, with no
// steps (the platform sends them) and no daily target, so allocation does
// not touch it until a person sets one. The mailboxes already attached on
// the platform come in as they are.
func Upsert(ctx context.Context, q Querier, workspaceID string, l Link) (id, sequenceID string, err error) {
	if l.Control == "" {
		l.Control = Full
	}
	err = q.QueryRow(ctx, `
		UPDATE campaign_links SET external_name = $3, external_status = $4,
		       sequence_days = COALESCE($5, sequence_days), daily_limit = COALESCE($6, daily_limit), last_synced_at = now()
		WHERE integration_id = $1 AND external_id = $2
		RETURNING id::text, sequence_id::text`,
		l.IntegrationID, l.ExternalID, l.Name, l.Status, l.SequenceDays, l.DailyLimit).Scan(&id, &sequenceID)
	if err == nil || !errors.Is(err, pgx.ErrNoRows) {
		return id, sequenceID, err
	}
	sequenceID = l.SequenceID
	if sequenceID == "" {
		name := l.Name
		if name == "" {
			name = "Campaign " + l.ExternalID
		}
		if err := q.QueryRow(ctx, `
			INSERT INTO sequences (workspace_id, name, status)
			VALUES ($1, $2, $3)
			RETURNING id::text`, workspaceID, name, SequenceStatus(l.Status)).Scan(&sequenceID); err != nil {
			return "", "", err
		}
	}
	err = q.QueryRow(ctx, `
		INSERT INTO campaign_links (workspace_id, sequence_id, integration_id, external_id, external_name, external_status,
		                            control, sequence_days, daily_limit, last_synced_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, now())
		RETURNING id::text`,
		workspaceID, sequenceID, l.IntegrationID, l.ExternalID, l.Name, l.Status, l.Control, l.SequenceDays, l.DailyLimit).Scan(&id)
	return id, sequenceID, err
}

// ForSequence returns a sequence's links, oldest first.
func ForSequence(ctx context.Context, q Querier, sequenceID string) ([]Link, error) {
	rows, err := q.Query(ctx, `
		SELECT id::text, sequence_id::text, integration_id::text, external_id, external_name, external_status, control, sequence_days, daily_limit
		FROM campaign_links WHERE sequence_id = $1 ORDER BY created_at, id`, sequenceID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Link, error) {
		var l Link
		err := r.Scan(&l.ID, &l.SequenceID, &l.IntegrationID, &l.ExternalID, &l.Name, &l.Status, &l.Control, &l.SequenceDays, &l.DailyLimit)
		return l, err
	})
}

// Find returns the link for a platform campaign, or false.
func Find(ctx context.Context, q Querier, integrationID, externalID string) (Link, bool, error) {
	var l Link
	err := q.QueryRow(ctx, `
		SELECT id::text, sequence_id::text, integration_id::text, external_id, external_name, external_status, control, sequence_days, daily_limit
		FROM campaign_links WHERE integration_id = $1 AND external_id = $2`, integrationID, externalID).
		Scan(&l.ID, &l.SequenceID, &l.IntegrationID, &l.ExternalID, &l.Name, &l.Status, &l.Control, &l.SequenceDays, &l.DailyLimit)
	if errors.Is(err, pgx.ErrNoRows) {
		return l, false, nil
	}
	return l, err == nil, err
}
