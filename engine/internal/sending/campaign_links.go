package sending

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/audit"
	"github.com/outflareai/outflare/engine/internal/tracking"
	"github.com/outflareai/outflare/engine/internal/utm"
)

// tagOwnLinks adds the sequence's campaign tags (channel outbound_email)
// to the links in a step that go to the brand's own sites (any brand's,
// for a sequence with none), the way broadcasts and journeys tag theirs.
// Links to other sites, links tagged by hand, and the unsubscribe and
// click links under base stay as written. The campaign is made with the
// first message that has such a link.
func tagOwnLinks(ctx context.Context, tx pgx.Tx, en enrollment, body, base string) (string, error) {
	hosts, err := utm.BrandHosts(ctx, tx, en.WorkspaceID, en.BrandID)
	if err != nil || len(hosts) == 0 {
		return body, err
	}
	lower := strings.ToLower(body)
	mentioned := false
	for _, h := range hosts {
		if strings.Contains(lower, h) {
			mentioned = true
			break
		}
	}
	if !mentioned {
		return body, nil
	}
	var name string
	if err := tx.QueryRow(ctx, `SELECT name FROM sequences WHERE workspace_id = $1 AND id = $2`, en.WorkspaceID, en.SequenceID).Scan(&name); err != nil {
		return body, err
	}
	c, created, err := utm.Ensure(ctx, tx, en.WorkspaceID, utm.Spec{Channel: "outbound_email", Source: "outflare", Name: name,
		BrandID: en.BrandID, ObjectKind: "sequence", ObjectID: en.SequenceID})
	if err != nil {
		return body, err
	}
	if created {
		if err := audit.Record(ctx, tx, audit.Event{WorkspaceID: en.WorkspaceID, ActorType: "system", Action: "tracking_campaign.created",
			TargetType: "tracking_campaign", TargetID: c.ID,
			Data: map[string]any{"channel": c.Channel, "source": c.Source, "object_kind": "sequence"}}); err != nil {
			return body, err
		}
	}
	return tracking.ReplaceLinks(body, func(link string) string {
		if strings.HasPrefix(link, base+"/") {
			return link
		}
		return c.OwnLink(link, hosts)
	}), nil
}
