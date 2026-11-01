package api_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/api"
	"github.com/outflareai/outflare/engine/outflare"
)

// An extension lists the workspace's SEO sites with what their brand is
// about, and adds pages to a site's link prospect list as the caller.
func TestExtensionLinkBuilding(t *testing.T) {
	var e *env
	h := &outflare.Host{}
	e = freshEnvWith(t, func(o *api.Options) {
		o.WorkspaceRoutes = []api.WorkspaceRoute{{
			Pattern: "GET /v1/workspaces/{ws}/x/sites", Scope: "seo.read",
			Handler: func(w http.ResponseWriter, r *http.Request, c *api.Caller) {
				sites, err := h.LinkSitesFor(r.Context(), c, nil)
				if err != nil {
					c.Internal(w, r, err)
					return
				}
				api.WriteJSON(w, http.StatusOK, map[string]any{"data": sites})
			},
		}, {
			Pattern: "POST /v1/workspaces/{ws}/x/linkers", Scope: "seo.manage",
			Handler: func(w http.ResponseWriter, r *http.Request, c *api.Caller) {
				var in struct {
					SiteID string   `json:"site_id"`
					Pages  []string `json:"pages"`
					Method string   `json:"method"`
				}
				if !c.Decode(w, r, &in) {
					return
				}
				list := make([]outflare.LinkProspect, len(in.Pages))
				for i, p := range in.Pages {
					list[i] = outflare.LinkProspect{PageURL: p, Method: in.Method, Evidence: map[string]any{"expired_domain": "lapsed.example"}}
				}
				var res outflare.LinkProspectsAdded
				err := pgx.BeginFunc(r.Context(), e.pool, func(tx pgx.Tx) error {
					var err error
					res, err = h.AddLinkProspects(r.Context(), tx, c, in.SiteID, "expired_domains", list)
					return err
				})
				if errors.Is(err, outflare.ErrBadProspect) {
					api.WriteError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
					return
				}
				if err != nil {
					c.Internal(w, r, err)
					return
				}
				api.WriteJSON(w, http.StatusOK, map[string]any{"added": res.Added, "existing": res.Existing, "excluded": res.Excluded})
			},
		}}
	})
	h.Pool = e.pool
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()

	siteID := owner.must(201, "POST", base+"/seo-sites", map[string]any{"url": "https://www.example.com/"})["id"].(string)
	owner.must(201, "POST", base+"/seo-competitors", map[string]any{"site_id": siteID, "host": "rival.example"})
	if _, err := e.pool.Exec(ctx, `INSERT INTO tracked_keywords (workspace_id, site_id, keyword) VALUES ($1, $2, 'trail shoes')`, ws, siteID); err != nil {
		t.Fatal(err)
	}

	sites := owner.must(200, "GET", base+"/x/sites", nil)["data"].([]any)
	if len(sites) != 1 {
		t.Fatalf("sites: %v", sites)
	}
	s := sites[0].(map[string]any)
	if s["ID"] != siteID || s["Host"] != "www.example.com" || len(s["Competitors"].([]any)) != 1 || s["Keywords"].([]any)[0] != "trail shoes" {
		t.Fatalf("site: %v", s)
	}

	add := func(want int, method string, pages ...string) map[string]any {
		t.Helper()
		return owner.must(want, "POST", base+"/x/linkers", map[string]any{"site_id": siteID, "method": method, "pages": pages})
	}
	res := add(200, "expired_domain", "https://blog.example.org/resources", "https://rival.example/links")
	if res["added"].(float64) != 2 || res["excluded"].(float64) != 1 {
		t.Fatalf("added: %v", res)
	}
	if again := add(200, "expired_domain", "https://blog.example.org/resources"); again["existing"].(float64) != 1 {
		t.Fatalf("again: %v", again)
	}
	var method, source, actor string
	if err := e.pool.QueryRow(ctx, `SELECT p.method, p.source, a.actor_type FROM link_prospects p
		JOIN audit_events a ON a.workspace_id = p.workspace_id AND a.action = 'link_prospect.found' AND a.target_id = p.id::text
		WHERE p.workspace_id = $1 AND p.page_url = 'https://blog.example.org/resources'`, ws).Scan(&method, &source, &actor); err != nil {
		t.Fatal(err)
	}
	if method != "expired_domain" || source != "expired_domains" || actor != "human" {
		t.Fatalf("prospect: %s %s %s", method, source, actor)
	}

	// Pages on the site itself, unknown methods and other workspaces' sites
	// are refused.
	add(400, "expired_domain", "https://example.com/blog")
	add(400, "bought_link", "https://blog.example.org/x")
	owner.must(400, "POST", base+"/x/linkers", map[string]any{"site_id": "00000000-0000-0000-0000-000000000000", "method": "expired_domain",
		"pages": []string{"https://blog.example.org/y"}})
}
