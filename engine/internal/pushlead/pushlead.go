// Package pushlead adds a person to the platform campaign a sequence is
// linked to (Smartlead, Instantly, EmailBison), through the workspace's
// own connection. The sequencer then sends. Journeys' push_to_sequencer
// steps and the shared people steps' enroll both use it, so the same
// checks hold wherever a person is handed to a sequencer.
package pushlead

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/metrics"
)

// Link is the platform campaign a sequence sends through.
type Link struct {
	IntegrationID, Connector, CampaignID, Name string
}

// Where is the campaign's name for messages, or its id.
func (l Link) Where() string {
	if l.Name != "" {
		return l.Name
	}
	return l.CampaignID
}

// Querier runs queries: a pool or a transaction.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// LinkOf is the first campaign linked to a sequence whose sequencer can
// take people (connectors.CapPushLeads). linked says whether the sequence
// has any campaign link at all; with links but none that can take people,
// the link is nil.
func LinkOf(ctx context.Context, q Querier, reg *connectors.Registry, ws, sequenceID string) (*Link, bool, error) {
	rows, err := q.Query(ctx, `SELECT l.integration_id::text, i.connector, l.external_id, l.external_name FROM campaign_links l
		JOIN integrations i ON i.id = l.integration_id AND i.workspace_id = l.workspace_id
		WHERE l.workspace_id = $1 AND l.sequence_id = $2 ORDER BY l.created_at, l.id`, ws, sequenceID)
	if err != nil {
		return nil, false, err
	}
	links, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Link, error) {
		var l Link
		return l, row.Scan(&l.IntegrationID, &l.Connector, &l.CampaignID, &l.Name)
	})
	if err != nil {
		return nil, false, err
	}
	for i := range links {
		if reg == nil {
			break
		}
		if d, ok := reg.Definition(links[i].Connector); ok && d.Has(connectors.CapPushLeads) {
			return &links[i], true, nil
		}
	}
	return nil, len(links) > 0, nil
}

// ErrGone is a link whose connection was removed.
var ErrGone = errors.New("pushlead: the sequencer connection is gone")

// ErrCannotPush is a connection that can't add people to campaigns.
var ErrCannotPush = errors.New("pushlead: that connection can't add people to campaigns")

// Push adds lead to the link's campaign on the workspace's own connection,
// and counts the outcome. A refused sign-in or bad request comes back as
// the connector's error (connectors.KindOf says which); ErrGone and
// ErrCannotPush when the connection can't be used. The person being left
// out by the platform is not an error: the result says so.
func Push(ctx context.Context, store *connectors.Store, ws string, l Link, lead connectors.PushedLead) (connectors.LeadPushed, error) {
	if store == nil {
		return connectors.LeadPushed{}, ErrCannotPush
	}
	o, err := store.Open(ctx, store.Pool, ws, l.IntegrationID)
	if errors.Is(err, connectors.ErrNotFound) {
		return connectors.LeadPushed{}, ErrGone
	}
	if err != nil {
		return connectors.LeadPushed{}, err
	}
	p, ok := o.Connector.(connectors.LeadPusher)
	if !ok {
		return connectors.LeadPushed{}, ErrCannotPush
	}
	res, err := p.PushLead(ctx, o.Client, o.Creds, l.CampaignID, lead)
	_ = store.Checked(ctx, store.Pool, ws, o.ID, nil, err)
	if err != nil {
		metrics.SequencerPushed(l.Connector, "error")
		return res, fmt.Errorf("pushlead: %s: %w", l.Connector, err)
	}
	metrics.SequencerPushed(l.Connector, res.Outcome)
	return res, nil
}
