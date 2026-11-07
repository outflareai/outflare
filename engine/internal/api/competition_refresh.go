package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/crawl"
	"github.com/outflareai/outflare/engine/internal/seosync"
)

// Refresh all competitors: instead of waiting for the weekly snapshot,
// read every tracked competitor of the brand now. Their SEO numbers come
// from one SEO connection (Ahrefs first, so the overview's scale holds),
// for the ones it hasn't read today, within its budget; OutflareBot looks
// at each home page (at most once an hour each); and the followed social
// profiles and review pages are read through the page reader.

func (s *Server) routesCompetitionRefresh(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/brands/{brand}/competition/refresh", s.onBrand(auth.ScopeSEOManage, s.refreshCompetition))
}

type competitionRefreshView struct {
	Queued bool `json:"queued"`
	// Competitors is how many tracked competitors are being read.
	Competitors int `json:"competitors"`
	// SEOConnection is the SEO connection reading their numbers; null
	// when the workspace has none, so only their sites and profiles are.
	SEOConnection *string `json:"seo_integration_id"`
	// PageReader says a page reader reads their social profiles and
	// review pages.
	PageReader bool `json:"page_reader"`
}

// seoReaderFor is the SEO connection that reads a brand's competitors:
// Ahrefs when connected, else the oldest other one that serves the brand.
// One marked broken is still tried when there's no other: the mark can
// be stale (one refused call), and the read says so if it really is.
func (s *Server) seoReaderFor(ctx context.Context, q pgx.Tx, ws, brand string) (*string, error) {
	if s.integrations == nil {
		return nil, nil
	}
	var ids []string
	for _, d := range s.integrations.Registry.All() {
		if c, ok := s.integrations.Registry.Get(d.ID); ok {
			if _, ok := c.(connectors.SEOReader); ok {
				ids = append(ids, d.ID)
			}
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	var id string
	err := q.QueryRow(ctx, `SELECT i.id::text FROM integrations i
		WHERE i.workspace_id = $1 AND i.connector = ANY($2) AND integration_serves(i, $3::uuid)
		ORDER BY i.status <> 'connected', i.connector <> 'ahrefs', i.created_at LIMIT 1`, ws, ids, brand).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &id, nil
}

func (s *Server) refreshCompetition(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	now := time.Now()
	var out competitionRefreshView
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text FROM seo_competitors WHERE workspace_id = $1 AND brand_id = $2 AND status = 'tracked'`, a.WorkspaceID, a.Brand)
		if err != nil {
			return err
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		out.Competitors = len(ids)
		if len(ids) == 0 {
			return nil
		}
		for _, id := range ids {
			if err := crawl.StartGlance(ctx, tx, a.WorkspaceID, id, now); err != nil {
				return err
			}
		}
		if out.SEOConnection, err = s.seoReaderFor(ctx, tx, a.WorkspaceID, a.Brand); err != nil {
			return err
		}
		if out.SEOConnection != nil {
			if _, err := seosync.EnqueueRefresh(ctx, tx, a.WorkspaceID, *out.SEOConnection, a.Brand); err != nil {
				return err
			}
		}
		readers, err := s.pageReaders(ctx, tx, a.WorkspaceID)
		if err != nil {
			return err
		}
		out.PageReader = len(readers) > 0
		if err := s.enqueuePublicSocialSync(ctx, tx, a.WorkspaceID, "competition:"+a.Brand+":"+strconv.FormatInt(now.Unix()/60, 10)); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "competition.refresh_requested", "brand", a.Brand, map[string]any{
			"brand_id": a.Brand, "competitors": out.Competitors, "seo": out.SEOConnection != nil, "page_reader": out.PageReader,
		})
	})
	if s.allocError(w, r, err) {
		return
	}
	if out.Competitors == 0 {
		writeError(w, r, http.StatusConflict, "conflict", "track a competitor first: only tracked competitors are read")
		return
	}
	out.Queued = true
	writeJSON(w, http.StatusAccepted, out)
}
