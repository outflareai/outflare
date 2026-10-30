package api

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/outflareai/outflare/engine/internal/auth"
)

// Link assets: pages of the workspace's own worth promoting (a guide, a
// study, a tool). Marking one published records asset.published, which can
// start the "promote an asset" playbook (playbook automations). A CMS
// connector could mark them later; today a person or an agent does.

func (s *Server) routesLinkAssets(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/workspaces/{ws}/link-building/assets", s.inBrand(auth.ScopeSEORead, s.listLinkAssets))
	mux.HandleFunc("POST /v1/workspaces/{ws}/link-building/assets", s.inBrand(auth.ScopeSEOManage, s.publishLinkAsset))
}

type linkAssetView struct {
	ID          string    `json:"id"`
	URL         string    `json:"url"`
	Title       string    `json:"title"`
	Topic       string    `json:"topic"`
	SiteID      *string   `json:"site_id"`
	BrandID     *string   `json:"brand_id"`
	PublishedAt time.Time `json:"published_at"`
}

const linkAssetColumns = `id, url, title, topic, site_id::text, brand_id::text, published_at`

func scanLinkAsset(row pgx.Row) (linkAssetView, error) {
	var v linkAssetView
	err := row.Scan(&v.ID, &v.URL, &v.Title, &v.Topic, &v.SiteID, &v.BrandID, &v.PublishedAt)
	return v, err
}

func (s *Server) listLinkAssets(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	brand, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	args := []any{a.WorkspaceID}
	rows, err := s.pool.Query(r.Context(), `SELECT `+linkAssetColumns+` FROM link_assets x WHERE workspace_id = $1`+brand.sql("x.brand_id", &args)+
		` ORDER BY published_at DESC LIMIT 200`, args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (linkAssetView, error) { return scanLinkAsset(row) })
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if out == nil {
		out = []linkAssetView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out})
}

// publishLinkAsset marks one of the workspace's pages published. Its SEO
// site, and so its brand, is the site whose host the URL is on.
func (s *Server) publishLinkAsset(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	var in struct {
		URL   string `json:"url"`
		Title string `json:"title"`
		Topic string `json:"topic"`
	}
	if !decode(w, r, &in) {
		return
	}
	in.URL, in.Title, in.Topic = strings.TrimSpace(in.URL), strings.TrimSpace(in.Title), strings.TrimSpace(in.Topic)
	u, err := url.Parse(in.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || len(in.URL) > 2048 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "url is the page's full address, such as https://northpeak.example/guides/cold-email")
		return
	}
	if len(in.Title) > 300 || len(in.Topic) > 300 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "title and topic are up to 300 characters")
		return
	}
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	var v linkAssetView
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var site, brand *string
		err := tx.QueryRow(ctx, `SELECT id::text, brand_id::text FROM seo_sites
			WHERE workspace_id = $1 AND (regexp_replace(lower(host), '^www\.', '') = $2 OR $2 LIKE '%.' || regexp_replace(lower(host), '^www\.', ''))
			ORDER BY length(host) DESC LIMIT 1`, a.WorkspaceID, host).Scan(&site, &brand)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if !a.writes(brand) {
			return errDenied("this page isn't on one of your brands' sites")
		}
		v, err = scanLinkAsset(tx.QueryRow(ctx, `
			INSERT INTO link_assets (workspace_id, site_id, brand_id, url, title, topic, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING `+linkAssetColumns,
			a.WorkspaceID, site, brand, in.URL, in.Title, in.Topic, nullIfEmpty(a.ActorID)))
		if err != nil {
			return err
		}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "asset.published", "link_asset", v.ID,
			map[string]any{"asset_id": v.ID, "url": v.URL, "title": v.Title, "topic": v.Topic, "site_id": v.SiteID, "brand_id": v.BrandID},
			map[string]any{"site_id": v.SiteID, "has_topic": v.Topic != ""})
	})
	var pe *pgconn.PgError
	switch {
	case errors.As(err, &pe) && pe.Code == "23505":
		writeError(w, r, http.StatusConflict, "conflict", "this page is already marked published")
		return
	case errors.As(err, new(errDenied)):
		writeError(w, r, http.StatusForbidden, "forbidden", err.Error())
		return
	case err != nil:
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, v)
}
