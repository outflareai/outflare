// Package deploys connects code hosts and deploy platforms (GitHub,
// Vercel), so Outflare knows when a website changed: each production
// deploy of a watched repository or project. They only read.
package deploys

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// All returns the deploy connectors.
func All() []c.Connector { return []c.Connector{GitHub{}, Vercel{}} }

// get calls a JSON API with a bearer token. name is the service's name for
// messages; id picks the base URL override in tests.
func get(ctx context.Context, cl *c.Client, name, id, base, path, token string, out any, headers map[string]string) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, cl.Base(id, base)+path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := cl.Do(req)
	if err != nil {
		return c.Errf(c.ErrUnavailable, "could not reach %s: %v", name, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode == http.StatusNotFound {
		return c.Errf(c.ErrInvalid, "%s can't find that, or this connection can't see it. Check the connection's access.", name)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return c.HTTPError(name, resp.StatusCode, string(body))
	}
	if out != nil {
		if err := json.Unmarshal(body, out); err != nil {
			return c.Errf(c.ErrUnavailable, "%s returned something that is not JSON", name)
		}
	}
	return nil
}

// firstLine is a commit message's subject, kept short.
func firstLine(s string) string {
	s, _, _ = strings.Cut(s, "\n")
	s = strings.TrimSpace(s)
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

// hostOf is a website's host name, for matching a target to a site.
func hostOf(raw string) string {
	raw = strings.TrimSpace(strings.ToLower(raw))
	if raw == "" {
		return ""
	}
	if i := strings.Index(raw, "://"); i >= 0 {
		raw = raw[i+3:]
	}
	raw, _, _ = strings.Cut(raw, "/")
	raw, _, _ = strings.Cut(raw, ":")
	return strings.TrimPrefix(raw, "www.")
}
