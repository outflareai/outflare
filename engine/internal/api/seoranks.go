package api

import (
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/ranks"
	"github.com/outflareai/outflare/engine/internal/seointel"
)

// Rank tracking: the keywords a workspace follows for an SEO site and
// where the site ranks for each, from the workspace's own SERP connection
// (DataForSEO for Google). Checks are jobs (ranks), daily or weekly per
// keyword or when someone asks, within the connection's monthly budget.

func (s *Server) routesSEORanks(mux *http.ServeMux) {
	read, manage := auth.ScopeSEORead, auth.ScopeSEOManage
	mux.HandleFunc("GET /v1/workspaces/{ws}/seo-sites/{site}/rankings", s.inBrand(read, s.listRankings))
	mux.HandleFunc("POST /v1/workspaces/{ws}/seo-sites/{site}/rankings", s.inBrand(manage, s.trackKeywords))
	mux.HandleFunc("POST /v1/workspaces/{ws}/seo-sites/{site}/rankings/check", s.inBrand(manage, s.checkRankings))
	mux.HandleFunc("GET /v1/workspaces/{ws}/seo-sites/{site}/rankings/{tracked}/history", s.inBrand(read, s.rankHistory))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/seo-sites/{site}/rankings/{tracked}", s.inBrand(manage, s.updateTracked))
	mux.HandleFunc("DELETE /v1/workspaces/{ws}/seo-sites/{site}/rankings/{tracked}", s.inBrand(manage, s.deleteTracked))
}

type trackedView struct {
	ID            string  `json:"id"`
	Keyword       string  `json:"keyword"`
	Country       string  `json:"country"`
	Device        string  `json:"device"`
	Engine        string  `json:"engine"`
	Frequency     string  `json:"frequency"`
	Status        string  `json:"status"`
	IntegrationID *string `json:"integration_id"`
	// The latest check: the site's position (nil when not in the top 100)
	// and page, and the position at the check before.
	Position      *int       `json:"position"`
	URL           string     `json:"url"`
	Previous      *int       `json:"previous_position"`
	Best          *int       `json:"best_position"`
	LastCheckedAt *time.Time `json:"last_checked_at"`
	LastError     *string    `json:"last_error"`
	// Whether a page targets the keyword (page_targets).
	TargetURL *string   `json:"target_url"`
	CreatedAt time.Time `json:"created_at"`
}

const trackedCols = `t.id::text, t.keyword, t.country, t.device, t.engine, t.frequency, t.status, t.integration_id::text,
	o1.position, COALESCE(o1.url, ''), o2.position, best.position, t.last_checked_at, t.last_error, pt.url, t.created_at`

const trackedFrom = `FROM tracked_keywords t
	LEFT JOIN LATERAL (SELECT position, url FROM rank_observations WHERE tracked_id = t.id ORDER BY day DESC LIMIT 1) o1 ON true
	LEFT JOIN LATERAL (SELECT position FROM rank_observations WHERE tracked_id = t.id ORDER BY day DESC OFFSET 1 LIMIT 1) o2 ON true
	LEFT JOIN LATERAL (SELECT min(position) AS position FROM rank_observations WHERE tracked_id = t.id) best ON true
	LEFT JOIN LATERAL (SELECT url FROM page_targets WHERE site_id = t.site_id AND keyword = t.keyword AND status = 'set' ORDER BY url LIMIT 1) pt ON true`

func scanTracked(row pgx.Row) (trackedView, error) {
	var v trackedView
	err := row.Scan(&v.ID, &v.Keyword, &v.Country, &v.Device, &v.Engine, &v.Frequency, &v.Status, &v.IntegrationID,
		&v.Position, &v.URL, &v.Previous, &v.Best, &v.LastCheckedAt, &v.LastError, &v.TargetURL, &v.CreatedAt)
	return v, err
}

// maxTracked is how many keywords a site can follow.
const maxTracked = 1000

type rankConnection struct {
	IntegrationID string `json:"integration_id"`
	Name          string `json:"name"`
	Connector     string `json:"connector"`
}

// rankConnection is the workspace's default SERP connection, or nil.
func (s *Server) rankConnection(r *http.Request) (*rankConnection, error) {
	ctx := r.Context()
	if s.integrations == nil {
		return nil, nil
	}
	id, err := ranks.DefaultConnection(ctx, s.pool, s.integrations.Registry, accessFrom(ctx).WorkspaceID)
	if err != nil || id == "" {
		return nil, err
	}
	c := rankConnection{IntegrationID: id}
	err = s.pool.QueryRow(ctx, `SELECT name, connector FROM integrations WHERE id = $1`, id).Scan(&c.Name, &c.Connector)
	return &c, err
}

func (s *Server) listRankings(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	site, ok := s.seoSiteFor(w, r, a)
	if !ok {
		return
	}
	ctx := r.Context()
	rows, err := s.pool.Query(ctx, `SELECT `+trackedCols+` `+trackedFrom+`
		WHERE t.workspace_id = $1 AND t.site_id = $2
		ORDER BY o1.position NULLS LAST, t.keyword, t.country, t.device LIMIT $3`, a.WorkspaceID, site.ID, maxTracked)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (trackedView, error) { return scanTracked(row) })
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if list == nil {
		list = []trackedView{}
	}
	conn, err := s.rankConnection(r)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "connection": conn})
}

var countryCode = regexp.MustCompile(`^[A-Z]{2}$`)

type trackInput struct {
	Keywords      []string `json:"keywords"`
	FromTargets   bool     `json:"from_targets"`
	Country       *string  `json:"country"`
	Device        *string  `json:"device"`
	Frequency     *string  `json:"frequency"`
	IntegrationID *string  `json:"integration_id"`
	Status        *string  `json:"status"`
}

// check reads what a track or update asks for.
func (in *trackInput) check() error {
	if in.Country != nil {
		c := strings.ToUpper(strings.TrimSpace(*in.Country))
		if !countryCode.MatchString(c) {
			return errInvalid("country is a two-letter code, like US")
		}
		in.Country = &c
	}
	if in.Device != nil && *in.Device != "desktop" && *in.Device != "mobile" {
		return errInvalid("device is desktop or mobile")
	}
	if in.Frequency != nil && *in.Frequency != "daily" && *in.Frequency != "weekly" {
		return errInvalid("frequency is daily or weekly")
	}
	if in.Status != nil && *in.Status != "active" && *in.Status != "paused" {
		return errInvalid("status is active or paused")
	}
	return nil
}

// serpConnection checks that an integration is one of the workspace's SERP
// connections.
func (s *Server) serpConnection(r *http.Request, id string) error {
	var connector string
	err := s.pool.QueryRow(r.Context(), `SELECT connector FROM integrations WHERE id = $1 AND workspace_id = $2`, id, accessFrom(r.Context()).WorkspaceID).Scan(&connector)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !slices.Contains(ranks.SERPConnectors(s.integrations.Registry), connector)) {
		return errInvalid("integration_id is a connection that can check rankings, such as DataForSEO")
	}
	return err
}

func (s *Server) trackKeywords(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in trackInput
	if !decode(w, r, &in) {
		return
	}
	site, ok := s.targetSite(w, r)
	if !ok {
		return
	}
	if err := in.check(); s.allocError(w, r, err) {
		return
	}
	if in.Status != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "a keyword starts active; pause it with PATCH")
		return
	}
	ctx := r.Context()
	var kws []string
	add := func(k string) {
		if k = seointel.Keyword(k); k != "" && !slices.Contains(kws, k) {
			kws = append(kws, k)
		}
	}
	for _, k := range in.Keywords {
		if len(k) > seointel.MaxKeyword {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "a keyword is at most 200 characters")
			return
		}
		add(k)
	}
	if in.FromTargets {
		rows, err := s.pool.Query(ctx, `SELECT keyword FROM page_targets WHERE workspace_id = $1 AND site_id = $2 AND status = 'set' AND keyword <> '' ORDER BY keyword`, a.WorkspaceID, site.ID)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		targets, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			s.internal(w, r, err)
			return
		}
		for _, k := range targets {
			add(k)
		}
	}
	if len(kws) == 0 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "keywords lists 1 to 100 keywords, or from_targets adds the pages' target keywords")
		return
	}
	if len(in.Keywords) > 100 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "keywords lists at most 100 keywords at once")
		return
	}
	if in.IntegrationID != nil {
		if err := s.serpConnection(r, *in.IntegrationID); s.allocError(w, r, err) {
			return
		}
	}
	country, device, freq := "US", "desktop", "weekly"
	if in.Country != nil {
		country = *in.Country
	}
	if in.Device != nil {
		device = *in.Device
	}
	if in.Frequency != nil {
		freq = *in.Frequency
	}
	var ids []string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM tracked_keywords WHERE workspace_id = $1 AND site_id = $2`, a.WorkspaceID, site.ID).Scan(&n); err != nil {
			return err
		}
		if n+len(kws) > maxTracked {
			return errInvalid("a site follows at most 1000 keywords")
		}
		var createdBy *string
		if a.ActorID != "" {
			createdBy = &a.ActorID
		}
		rows, err := tx.Query(ctx, `
			INSERT INTO tracked_keywords (workspace_id, site_id, keyword, country, device, frequency, integration_id, created_by)
			SELECT $1, $2, k, $4, $5, $6, $7, $8 FROM unnest($3::text[]) AS k
			ON CONFLICT (site_id, keyword, country, device, engine) DO NOTHING
			RETURNING id::text`, a.WorkspaceID, site.ID, kws, country, device, freq, in.IntegrationID, createdBy)
		if err != nil {
			return err
		}
		if ids, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		if err := record(ctx, tx, &a.principal, a.WorkspaceID, "tracked_keyword.added", "seo_site", site.ID, map[string]any{
			"site_id": site.ID, "count": len(ids), "country": country, "device": device, "frequency": freq, "from_targets": in.FromTargets}); err != nil {
			return err
		}
		// The first check runs now, not at the next schedule.
		_, err = ranks.Enqueue(ctx, tx, a.WorkspaceID, site.ID, true, "ranks:"+site.ID+":"+RequestID(ctx))
		return err
	})
	if s.allocError(w, r, err) {
		return
	}
	list := []trackedView{}
	if len(ids) > 0 {
		rows, err := s.pool.Query(ctx, `SELECT `+trackedCols+` `+trackedFrom+` WHERE t.workspace_id = $1 AND t.id = ANY($2::uuid[]) ORDER BY t.keyword`, a.WorkspaceID, ids)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		if list, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (trackedView, error) { return scanTracked(row) }); err != nil {
			s.internal(w, r, err)
			return
		}
	}
	writeJSON(w, http.StatusCreated, map[string]any{"data": list, "added": len(list), "skipped": len(kws) - len(list)})
}

// trackedFor loads one of the site's tracked keywords.
func (s *Server) trackedFor(w http.ResponseWriter, r *http.Request, site *seoSite) (*trackedView, bool) {
	id := r.PathValue("tracked")
	if validUUID(id) {
		v, err := scanTracked(s.pool.QueryRow(r.Context(), `SELECT `+trackedCols+` `+trackedFrom+`
			WHERE t.id = $1 AND t.workspace_id = $2 AND t.site_id = $3`, id, accessFrom(r.Context()).WorkspaceID, site.ID))
		if err == nil {
			return &v, true
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			s.internal(w, r, err)
			return nil, false
		}
	}
	writeError(w, r, http.StatusNotFound, "not_found", "tracked keyword not found")
	return nil, false
}

func (s *Server) updateTracked(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in trackInput
	if !decode(w, r, &in) {
		return
	}
	site, ok := s.targetSite(w, r)
	if !ok {
		return
	}
	t, ok := s.trackedFor(w, r, site)
	if !ok {
		return
	}
	if len(in.Keywords) > 0 || in.FromTargets || in.Country != nil || in.Device != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "keyword, country and device cannot change; track the keyword again with the new ones")
		return
	}
	if err := in.check(); s.allocError(w, r, err) {
		return
	}
	if in.IntegrationID != nil {
		if err := s.serpConnection(r, *in.IntegrationID); s.allocError(w, r, err) {
			return
		}
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE tracked_keywords SET frequency = COALESCE($3, frequency), status = COALESCE($4, status),
			integration_id = COALESCE($5::uuid, integration_id), updated_at = now() WHERE id = $1 AND workspace_id = $2`,
			t.ID, a.WorkspaceID, in.Frequency, in.Status, in.IntegrationID); err != nil {
			return err
		}
		var fields []string
		for name, set := range map[string]bool{"frequency": in.Frequency != nil, "status": in.Status != nil, "integration_id": in.IntegrationID != nil} {
			if set {
				fields = append(fields, name)
			}
		}
		slices.Sort(fields)
		return record(ctx, tx, &a.principal, a.WorkspaceID, "tracked_keyword.updated", "tracked_keyword", t.ID, map[string]any{"site_id": site.ID, "fields": fields})
	})
	if s.allocError(w, r, err) {
		return
	}
	if v, ok := s.trackedFor(w, r, site); ok {
		writeJSON(w, http.StatusOK, v)
	}
}

func (s *Server) deleteTracked(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	site, ok := s.targetSite(w, r)
	if !ok {
		return
	}
	t, ok := s.trackedFor(w, r, site)
	if !ok {
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM tracked_keywords WHERE id = $1 AND workspace_id = $2`, t.ID, a.WorkspaceID); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "tracked_keyword.removed", "tracked_keyword", t.ID, map[string]any{"site_id": site.ID})
	})
	if s.allocError(w, r, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// checkRankings checks every active keyword of the site not checked today,
// in a job.
func (s *Server) checkRankings(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	site, ok := s.targetSite(w, r)
	if !ok {
		return
	}
	conn, err := s.rankConnection(r)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if conn == nil {
		writeError(w, r, http.StatusConflict, "connector_needed", "rank tracking needs a SERP connection: connect DataForSEO on the Connectors page")
		return
	}
	ctx := r.Context()
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := ranks.Enqueue(ctx, tx, a.WorkspaceID, site.ID, true, "ranks:"+site.ID+":"+RequestID(ctx)); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "rank.check_requested", "seo_site", site.ID, map[string]any{"site_id": site.ID})
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"queued": true})
}

type rankDay struct {
	Day      string                `json:"day"`
	Position *int                  `json:"position"`
	URL      string                `json:"url"`
	Engine   string                `json:"engine"`
	Top      []connectors.SERPItem `json:"top"`
}

func (s *Server) rankHistory(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	site, ok := s.seoSiteFor(w, r, a)
	if !ok {
		return
	}
	t, ok := s.trackedFor(w, r, site)
	if !ok {
		return
	}
	rows, err := s.pool.Query(r.Context(), `SELECT day::text, position, url, engine, top FROM rank_observations
		WHERE workspace_id = $1 AND tracked_id = $2 ORDER BY day DESC LIMIT 400`, a.WorkspaceID, t.ID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (rankDay, error) {
		var d rankDay
		err := row.Scan(&d.Day, &d.Position, &d.URL, &d.Engine, &d.Top)
		if d.Top == nil {
			d.Top = []connectors.SERPItem{}
		}
		return d, err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if list == nil {
		list = []rankDay{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"tracked": t, "data": list})
}
