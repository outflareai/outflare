package api

import (
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/seointel"
)

// What got worse for an SEO site: search clicks, tracked rankings,
// indexing, conversions and health, as found after each sync. Open ones
// are fixes too; resolved ones stay for the history.

func (s *Server) routesSEORegressions(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/workspaces/{ws}/seo-sites/{site}/regressions", s.inBrand(auth.ScopeSEORead, s.siteRegressions))
}

type regressionView struct {
	ID         string     `json:"id"`
	Kind       string     `json:"kind"`
	Subject    string     `json:"subject"`
	Before     *float64   `json:"before"`
	After      *float64   `json:"after"`
	Detail     string     `json:"detail"`
	Status     string     `json:"status"`
	FixID      *string    `json:"fix_id"`
	DetectedAt time.Time  `json:"detected_at"`
	LastSeenAt time.Time  `json:"last_seen_at"`
	ResolvedAt *time.Time `json:"resolved_at"`
}

var regressionKinds = []string{seointel.RegressionClicks, seointel.RegressionRank, seointel.RegressionDeindexed, seointel.RegressionConversions, seointel.RegressionHealth}

func (s *Server) siteRegressions(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	site, ok := s.seoSiteFor(w, r, a)
	if !ok {
		return
	}
	q := r.URL.Query()
	status := q.Get("status")
	if status == "" {
		status = "open"
	}
	if !slices.Contains([]string{"open", "resolved", "all"}, status) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status is open, resolved or all")
		return
	}
	kind := q.Get("kind")
	if kind != "" && !slices.Contains(regressionKinds, kind) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "kind is search_clicks_dropped, rank_dropped, page_deindexed, conversions_dropped or health_dropped")
		return
	}
	limit, err := limitParam(r, 100, 500)
	if s.allocError(w, r, err) {
		return
	}
	rows, err := s.pool.Query(r.Context(), `
		SELECT g.id::text, g.kind, g.subject, g.before, g.after, g.detail, g.status, f.id::text, g.detected_at, g.last_seen_at, g.resolved_at
		FROM seo_regressions g
		LEFT JOIN fixes f ON g.status = 'open' AND f.workspace_id = g.workspace_id AND f.source = $4 AND f.subject_id = g.site_id AND f.rule = g.kind
			AND f.group_key = CASE WHEN g.subject = '' THEN $5 ELSE g.subject END
		WHERE g.workspace_id = $1 AND g.site_id = $2 AND ($3 = 'all' OR g.status = $3) AND ($6 = '' OR g.kind = $6)
		ORDER BY g.status, COALESCE(g.resolved_at, g.detected_at) DESC, g.id LIMIT `+strconv.Itoa(limit),
		a.WorkspaceID, site.ID, status, seointel.FixSource, site.Host, kind)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (regressionView, error) {
		var v regressionView
		return v, row.Scan(&v.ID, &v.Kind, &v.Subject, &v.Before, &v.After, &v.Detail, &v.Status, &v.FixID, &v.DetectedAt, &v.LastSeenAt, &v.ResolvedAt)
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if list == nil {
		list = []regressionView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}
