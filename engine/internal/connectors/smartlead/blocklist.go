package smartlead

import (
	"context"
	"net/http"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// The global block list: POST /leads/add-domain-block-list adds emails
// and domains alike. Smartlead has no way to read it back, so only
// Outflare's opt-out list is written there; its unsubscribed leads come in
// with campaign imports.

var _ c.Blocklist = Smartlead{}

func (Smartlead) Block(ctx context.Context, cl *c.Client, cr c.Creds, entries []c.Blocked) error {
	if len(entries) == 0 {
		return nil
	}
	values := make([]string, len(entries))
	for i, e := range entries {
		values[i] = e.Value
	}
	return send(ctx, cl, cr, http.MethodPost, "/leads/add-domain-block-list", map[string]any{"domain_block_list": values})
}
