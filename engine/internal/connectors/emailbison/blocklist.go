package emailbison

import (
	"context"
	"net/http"
	"strings"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// The blocklist: GET /api/blacklisted-emails and /api/blacklisted-domains
// list it, page by page; POST .../bulk adds many at once.

var _ c.BlocklistReader = EmailBison{}

type blocked struct {
	Email  string `json:"email"`
	Domain string `json:"domain"`
	Value  string `json:"value"`
}

func (EmailBison) ListBlocked(ctx context.Context, cl *c.Client, creds c.Creds) ([]c.Blocked, error) {
	a, err := open(cl, creds)
	if err != nil {
		return nil, err
	}
	var out []c.Blocked
	for _, kind := range []string{"emails", "domains"} {
		list, err := all[blocked](ctx, a, "/api/blacklisted-"+kind)
		if err != nil {
			return nil, err
		}
		for _, b := range list {
			v := strings.ToLower(strings.TrimSpace(b.Email + b.Domain))
			if v == "" {
				v = strings.ToLower(strings.TrimSpace(b.Value))
			}
			if v != "" {
				out = append(out, c.Blocked{Value: v, Domain: kind == "domains"})
			}
		}
	}
	return out, nil
}

func (EmailBison) Block(ctx context.Context, cl *c.Client, creds c.Creds, entries []c.Blocked) error {
	a, err := open(cl, creds)
	if err != nil {
		return err
	}
	var emails, domains []string
	for _, e := range entries {
		if e.Domain {
			domains = append(domains, e.Value)
		} else {
			emails = append(emails, e.Value)
		}
	}
	if len(emails) > 0 {
		if err := a.do(ctx, http.MethodPost, "/api/blacklisted-emails/bulk", map[string]any{"emails": emails}, nil); err != nil {
			return err
		}
	}
	if len(domains) > 0 {
		return a.do(ctx, http.MethodPost, "/api/blacklisted-domains/bulk", map[string]any{"domains": domains}, nil)
	}
	return nil
}
