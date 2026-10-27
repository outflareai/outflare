package instantly

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// The blocklist: GET /block-lists-entries lists it by cursor;
// POST /block-lists-entries/bulk-create adds up to 1,000 values at once,
// emails and domains alike.

var _ c.BlocklistReader = Instantly{}

type blockEntry struct {
	Value    string `json:"bl_value"`
	IsDomain bool   `json:"is_domain"`
}

func (Instantly) ListBlocked(ctx context.Context, cl *c.Client, cr c.Creds) ([]c.Blocked, error) {
	var out []c.Blocked
	err := page(ctx, func(after string) ([]blockEntry, string, error) {
		q := url.Values{"limit": {"100"}}
		if after != "" {
			q.Set("starting_after", after)
		}
		var p struct {
			Items []blockEntry `json:"items"`
			Next  string       `json:"next_starting_after"`
		}
		err := call(ctx, cl, cr, http.MethodGet, "/block-lists-entries", q, nil, &p)
		return p.Items, p.Next, err
	}, func(e blockEntry) error {
		if v := strings.ToLower(strings.TrimSpace(e.Value)); v != "" {
			out = append(out, c.Blocked{Value: v, Domain: e.IsDomain || !strings.Contains(v, "@")})
		}
		return nil
	})
	return out, err
}

func (Instantly) Block(ctx context.Context, cl *c.Client, cr c.Creds, entries []c.Blocked) error {
	if len(entries) == 0 {
		return nil
	}
	values := make([]string, len(entries))
	for i, e := range entries {
		values[i] = e.Value
	}
	return call(ctx, cl, cr, http.MethodPost, "/block-lists-entries/bulk-create", nil, map[string]any{"bl_values": values}, nil)
}
