package adconv

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/audit"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/jobs"
)

// Excluded audiences: an audience with exclude on is one whose people no
// campaign in its ad accounts should show to, such as people who were
// refunded on a network that takes no conversion back. Once the list is
// on the network, each sync queues the exclusion on every active campaign
// of the account that hasn't had one (an ad_changes row, exclusion_add,
// carried out by the findings job like any exclusion), so a new campaign
// is covered within a day. A campaign a person took the exclusion off
// stays without it.

const (
	// The findings job's change job and its kinds (adsuggest.JobApply,
	// ChangeExclusionAdd, ChangeExclusionRemove): adsuggest imports this
	// package, so they are written out here, and TestAdAudienceExclusion (the API's
	// tests) holds them equal.
	jobExclusion          = "ads.finding_change"
	changeExclusionAdd    = "exclusion_add"
	changeExclusionRemove = "exclusion_remove"
	// ExclusionFromSync marks an exclusion the audience sync queued, or
	// took out when the audience stopped excluding (AdExclusion.Detail),
	// apart from one a person changed on a campaign.
	ExclusionFromSync = "list_sync"
)

// CanExclude reports whether the network takes a customer list as an
// exclusion on a campaign from here.
func CanExclude(conn connectors.Connector) bool {
	m, ok := conn.(connectors.AdExclusionManager)
	if !ok {
		return false
	}
	for _, k := range m.ExclusionKinds() {
		if k == connectors.ExcludeAudience {
			return true
		}
	}
	return false
}

// QueuedExclusion is one exclusion change QueueAudienceExclusions made.
type QueuedExclusion struct {
	ChangeID   string
	CampaignID string
	ExternalID string
	Connector  string
	Kind       string
	Exclusion  connectors.AdExclusion
}

// QueueAudienceExclusions queues, in tx, the exclusion of a destination's
// list on the campaigns of its ad account, or its removal.
//
// Adding covers each active campaign of the audience's brand whose last
// change to this exclusion isn't an add, and isn't a removal a person made
// on the campaign: that one stays off. Removing covers each campaign whose
// last change to it is an add that wasn't refused. The caller records the
// audit event of each change it gets back.
func QueueAudienceExclusions(ctx context.Context, tx pgx.Tx, ws, destinationID string, add bool, actorID *string) ([]QueuedExclusion, error) {
	var brand, name, listID, accountID, connector string
	err := tx.QueryRow(ctx, `SELECT a.brand_id, a.name, d.external_id, c.id, c.connector
		FROM ad_audience_destinations d
		JOIN ad_audiences a ON a.id = d.audience_id
		JOIN ad_accounts c ON c.id = d.ad_account_id AND c.workspace_id = d.workspace_id
		WHERE d.workspace_id = $1 AND d.id = $2 AND d.external_id <> ''`, ws, destinationID).Scan(&brand, &name, &listID, &accountID, &connector)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	// The last change to this exclusion on each campaign, if any.
	const last = `SELECT DISTINCT ON (ch.ad_entity_id) ch.ad_entity_id, ch.kind, ch.status, coalesce(ch.after->'exclusion'->>'detail', '') AS detail
		FROM ad_changes ch
		WHERE ch.workspace_id = $1 AND ch.kind IN ('exclusion_add', 'exclusion_remove')
		  AND ch.after->'exclusion'->>'kind' = 'audience' AND ch.after->'exclusion'->>'value' = $3
		ORDER BY ch.ad_entity_id, ch.created_at DESC, ch.id DESC`
	pick := `SELECT e.id, e.external_id FROM ad_entities e LEFT JOIN (` + last + `) x ON x.ad_entity_id = e.id
		WHERE e.workspace_id = $1 AND e.ad_account_id = $2 AND e.level = 'campaign'
		  AND coalesce(e.brand_id, (SELECT brand_id FROM ad_accounts WHERE id = e.ad_account_id), $4::uuid) = $4::uuid AND `
	if add {
		pick += `e.status = 'active' AND (x.kind IS NULL OR (x.kind = 'exclusion_remove' AND x.detail = '` + ExclusionFromSync + `'))`
	} else {
		pick += `e.status <> 'removed' AND x.kind = 'exclusion_add' AND x.status <> 'failed'`
	}
	rows, err := tx.Query(ctx, pick+` ORDER BY e.id LIMIT 500`, ws, accountID, listID, brand)
	if err != nil {
		return nil, err
	}
	type campaign struct{ id, external string }
	campaigns, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (campaign, error) {
		var c campaign
		return c, r.Scan(&c.id, &c.external)
	})
	if err != nil {
		return nil, err
	}
	kind := changeExclusionRemove
	if add {
		kind = changeExclusionAdd
	}
	x := connectors.AdExclusion{Kind: connectors.ExcludeAudience, Value: listID, Detail: ExclusionFromSync, Label: AudienceListName(name)}
	after, _ := json.Marshal(map[string]any{"exclusion": x})
	out := make([]QueuedExclusion, 0, len(campaigns))
	for _, c := range campaigns {
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO ad_changes (workspace_id, brand_id, ad_entity_id, kind, before, after, actor_id) VALUES ($1, $2, $3, $4, '{}', $5, $6) RETURNING id`,
			ws, brand, c.id, kind, after, actorID).Scan(&id); err != nil {
			return nil, err
		}
		if _, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: jobExclusion, WorkspaceID: &ws, Payload: map[string]string{"change_id": id},
			IdempotencyKey: jobExclusion + ":" + id, MaxAttempts: 6}); err != nil {
			return nil, err
		}
		out = append(out, QueuedExclusion{ChangeID: id, CampaignID: c.id, ExternalID: c.external, Connector: connector, Kind: kind, Exclusion: x})
	}
	return out, nil
}

// excludeEverywhere queues the exclusion of a destination's list on the
// campaigns that don't have it, as the system, with each change's audit
// event. It returns how many it queued.
func (s *Sender) excludeEverywhere(ctx context.Context, ws, brand, destinationID string) (int, error) {
	n := 0
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		queued, err := QueueAudienceExclusions(ctx, tx, ws, destinationID, true, nil)
		if err != nil {
			return err
		}
		for _, q := range queued {
			if err := audit.Record(ctx, tx, audit.Event{WorkspaceID: ws, ActorType: "system", Action: "ad_campaign.exclusion_added", TargetType: "ad_campaign", TargetID: q.CampaignID,
				Data: map[string]any{"change_id": q.ChangeID, "campaign_id": q.CampaignID, "external_id": q.ExternalID, "connector": q.Connector, "brand_id": brand,
					"after": map[string]any{"exclusion": q.Exclusion}, "audience_destination_id": destinationID},
				Props: map[string]any{"connector": q.Connector, "kind": q.Kind, "exclusion_kind": q.Exclusion.Kind, "auto": true, "rule": "audience_exclude"}}); err != nil {
				return err
			}
		}
		n = len(queued)
		return nil
	})
	return n, err
}
