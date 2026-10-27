package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
)

// Analytics sites by brand: each site a tool reads (a GA4 property, a
// Search Console site, a PostHog project) reports for the brand whose
// domains include its domain, unless someone picked another brand or
// marked it shared.

func (s *Server) routesAnalyticsBrands(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/workspaces/{ws}/analytics/site-brands", s.inBrand(auth.ScopeAnalyticsRead, s.listSiteBrands))
	mux.HandleFunc("PUT /v1/workspaces/{ws}/analytics/{id}/site-brand", s.inWorkspace(auth.ScopeIntegrationsManage, s.setSiteBrand))
}

type siteBrand struct {
	Integration string  `json:"integration_id"`
	Site        string  `json:"site"`
	Host        string  `json:"host"`
	BrandID     *string `json:"brand_id"`
	// Source says why: set (someone picked it), domain (the brand's
	// domains include the site's) or none (no brand has it).
	Source    string     `json:"source"`
	UpdatedAt *time.Time `json:"updated_at"`
}

// siteBrandSQL lists sites with stored history and sites someone mapped,
// one row each, under the host its latest sync stored, so days stored
// before its domain was known don't list it again as having no brand.
const siteBrandSQL = `
	WITH daily AS (
		SELECT integration_id, site, max(host) AS host FROM analytics_daily WHERE workspace_id = $1 GROUP BY 1, 2
	), sites AS (
		SELECT k.integration_id, k.site, COALESCE(NULLIF(s.host, ''), d.host, '') AS host
		FROM (SELECT integration_id, site FROM daily
			UNION SELECT integration_id, site FROM analytics_site_brands WHERE workspace_id = $1) k
		LEFT JOIN daily d ON d.integration_id = k.integration_id AND d.site = k.site
		LEFT JOIN analytics_syncs s ON s.integration_id = k.integration_id AND s.site = k.site
	)
	SELECT x.integration_id::text, x.site, x.host, analytics_site_brand($1, x.integration_id, x.site, x.host)::text,
		o.integration_id IS NOT NULL, o.updated_at
	FROM sites x
	LEFT JOIN analytics_site_brands o ON o.integration_id = x.integration_id AND o.site = x.site`

func (s *Server) loadSiteBrands(ctx context.Context, ws, where string, args ...any) ([]siteBrand, error) {
	rows, err := s.pool.Query(ctx, siteBrandSQL+where+` ORDER BY x.host = '', x.host, x.site`, append([]any{ws}, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []siteBrand{}
	for rows.Next() {
		var b siteBrand
		var set bool
		if err := rows.Scan(&b.Integration, &b.Site, &b.Host, &b.BrandID, &set, &b.UpdatedAt); err != nil {
			return nil, err
		}
		switch {
		case set:
			b.Source = "set"
		case b.BrandID != nil:
			b.Source = "domain"
		default:
			b.Source = "none"
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s *Server) listSiteBrands(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	brand, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	where, args := "", []any{}
	if id := r.URL.Query().Get("integration"); id != "" {
		if !isUUID(id) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "integration is an analytics connection id")
			return
		}
		where, args = ` WHERE x.integration_id = $2`, append(args, id)
	}
	list, err := s.loadSiteBrands(r.Context(), a.WorkspaceID, where, args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if !brand.all() {
		list = filterSiteBrands(list, brand)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

func filterSiteBrands(list []siteBrand, brand brandSel) []siteBrand {
	out := []siteBrand{}
	for _, b := range list {
		if brand.matches(b.BrandID) {
			out = append(out, b)
		}
	}
	return out
}

func (s *Server) setSiteBrand(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	var req struct {
		Site  string `json:"site"`
		Brand string `json:"brand"`
	}
	if !decode(w, r, &req) {
		return
	}
	req.Site = strings.TrimSpace(req.Site)
	ref := strings.ToLower(strings.TrimSpace(req.Brand))
	if req.Site == "" || len(req.Site) > 500 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "site is the site's id, from GET .../analytics/{id}/sites")
		return
	}
	if !isUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "analytics connection not found")
		return
	}
	var exists bool
	if err := s.pool.QueryRow(r.Context(), `SELECT EXISTS (SELECT 1 FROM integrations WHERE workspace_id = $1 AND id = $2)`, a.WorkspaceID, id).Scan(&exists); err != nil {
		s.internal(w, r, err)
		return
	}
	if !exists {
		writeError(w, r, http.StatusNotFound, "not_found", "analytics connection not found")
		return
	}
	var brandID *string
	switch ref {
	case "", "auto", "none":
	default:
		bid, err := s.brandIDFor(r.Context(), a.WorkspaceID, ref)
		if errors.Is(err, errNotFound) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "brand not found: brand is a brand's id or handle, none for shared, or auto to follow the domain")
			return
		}
		if err != nil {
			s.internal(w, r, err)
			return
		}
		brandID = &bid
	}
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		var err error
		if ref == "" || ref == "auto" {
			_, err = tx.Exec(r.Context(), `DELETE FROM analytics_site_brands WHERE workspace_id = $1 AND integration_id = $2 AND site = $3`, a.WorkspaceID, id, req.Site)
		} else {
			_, err = tx.Exec(r.Context(), `
				INSERT INTO analytics_site_brands (workspace_id, integration_id, site, brand_id) VALUES ($1, $2, $3, $4)
				ON CONFLICT (integration_id, site) DO UPDATE SET brand_id = EXCLUDED.brand_id, updated_at = now()`,
				a.WorkspaceID, id, req.Site, brandID)
		}
		if err != nil {
			return err
		}
		mode := ref
		if mode == "" {
			mode = "auto"
		} else if brandID != nil {
			mode = "brand"
		}
		return record(r.Context(), tx, &a.principal, a.WorkspaceID, "analytics.site_brand_set", "integration", id,
			map[string]any{"brand_id": brandID, "mode": mode})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := s.loadSiteBrands(r.Context(), a.WorkspaceID, ` WHERE x.integration_id = $2 AND x.site = $3`, id, req.Site)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out := siteBrand{Integration: id, Site: req.Site, Source: "none"}
	if len(list) == 1 {
		out = list[0]
	}
	writeJSON(w, http.StatusOK, out)
}

// hostBrandFilter says whether a bare domain (with no tool site behind
// it, such as an SEO-only domain) passes a brandFilter value.
func (s *Server) hostBrandFilter(ctx context.Context, ws string, brand brandSel) (func(string) bool, error) {
	if brand.all() {
		return func(string) bool { return true }, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT id::text, domains FROM brands WHERE workspace_id = $1 AND status = 'active'`, ws)
	if err != nil {
		return nil, err
	}
	byBrand, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) ([2]any, error) {
		var id string
		var domains []string
		err := row.Scan(&id, &domains)
		return [2]any{id, domains}, err
	})
	if err != nil {
		return nil, err
	}
	return func(host string) bool {
		// The longest matching domain wins, as in analytics_site_brand.
		owner, best := "", 0
		for _, b := range byBrand {
			for _, d := range b[1].([]string) {
				if len(d) > best && (host == d || strings.HasSuffix(host, "."+d)) {
					owner, best = b[0].(string), len(d)
				}
			}
		}
		if owner == "" {
			return brand.matches(nil)
		}
		return brand.matches(&owner)
	}, nil
}
