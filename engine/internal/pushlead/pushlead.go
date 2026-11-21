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

// PushMany adds several people to the link's campaign on the workspace's
// own connection, in as few calls as the sequencer allows
// (connectors.LeadsPusher), else one by one, and counts each outcome. It
// answers one outcome per lead, in their order, with Push's errors.
func PushMany(ctx context.Context, store *connectors.Store, ws string, l Link, leads []connectors.PushedLead) []connectors.PushOutcome {
	out := make([]connectors.PushOutcome, len(leads))
	failAll := func(err error) []connectors.PushOutcome {
		for i := range out {
			out[i].Err = err
		}
		return out
	}
	if store == nil {
		return failAll(ErrCannotPush)
	}
	o, err := store.Open(ctx, store.Pool, ws, l.IntegrationID)
	if errors.Is(err, connectors.ErrNotFound) {
		return failAll(ErrGone)
	}
	if err != nil {
		return failAll(err)
	}
	p, ok := o.Connector.(connectors.LeadPusher)
	if !ok {
		return failAll(ErrCannotPush)
	}
	if many, ok := p.(connectors.LeadsPusher); ok {
		out = many.PushLeads(ctx, o.Client, o.Creds, l.CampaignID, leads)
	} else {
		for i, lead := range leads {
			out[i].LeadPushed, out[i].Err = p.PushLead(ctx, o.Client, o.Creds, l.CampaignID, lead)
		}
	}
	var last error
	for i := range out {
		if out[i].Err != nil {
			last = out[i].Err
			metrics.SequencerPushed(l.Connector, "error")
			out[i].Err = fmt.Errorf("pushlead: %s: %w", l.Connector, out[i].Err)
			continue
		}
		metrics.SequencerPushed(l.Connector, out[i].Outcome)
	}
	_ = store.Checked(ctx, store.Pool, ws, o.ID, nil, last)
	return out
}

// Ready reads whether the link's campaign can take people now, on the
// workspace's own connection (connectors.CampaignReadiness). Nil, with no
// error, when the sequencer can't say. ErrGone when the connection was
// removed; a refused sign-in is the connector's error.
func Ready(ctx context.Context, store *connectors.Store, ws string, l Link) (*connectors.Readiness, error) {
	if store == nil {
		return nil, nil
	}
	o, err := store.Open(ctx, store.Pool, ws, l.IntegrationID)
	if errors.Is(err, connectors.ErrNotFound) {
		return nil, ErrGone
	}
	if err != nil {
		return nil, err
	}
	r, ok := o.Connector.(connectors.CampaignReadiness)
	if !ok {
		return nil, nil
	}
	got, err := r.CampaignReady(ctx, o.Client, o.Creds, l.CampaignID)
	if err != nil {
		return nil, fmt.Errorf("pushlead: %s readiness: %w", l.Connector, err)
	}
	return &got, nil
}
