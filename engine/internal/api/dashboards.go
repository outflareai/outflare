package api

// Dashboards (docs/insights.md): a brand's saved insights laid out on
// one page, with a date range every tile takes unless it pins its own.
// A dashboard keeps no results; every view runs its tiles again.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/outflareai/outflare/engine/internal/auth"
)

func (s *Server) routesDashboards(mux *http.ServeMux) {
	read, manage := auth.ScopeAnalyticsRead, auth.ScopeAnalyticsManage
	mux.HandleFunc("GET /v1/brands/{brand}/dashboards", s.onBrand(read, s.listDashboards))
	mux.HandleFunc("POST /v1/brands/{brand}/dashboards", s.onBrand(manage, s.createDashboard))
	mux.HandleFunc("GET /v1/brands/{brand}/dashboards/{dashboard}", s.onBrand(read, s.getDashboard))
	mux.HandleFunc("PATCH /v1/brands/{brand}/dashboards/{dashboard}", s.onBrand(manage, s.updateDashboard))
	mux.HandleFunc("DELETE /v1/brands/{brand}/dashboards/{dashboard}", s.onBrand(manage, s.deleteDashboard))
	mux.HandleFunc("POST /v1/brands/{brand}/dashboards/{dashboard}/tiles", s.onBrand(manage, s.addDashboardTile))
	mux.HandleFunc("PATCH /v1/brands/{brand}/dashboards/{dashboard}/tiles/{tile}", s.onBrand(manage, s.updateDashboardTile))
	mux.HandleFunc("DELETE /v1/brands/{brand}/dashboards/{dashboard}/tiles/{tile}", s.onBrand(manage, s.removeDashboardTile))
}

// dashboardSettings is what every tile takes unless it pins its own.
type dashboardSettings struct {
	DateRange *dateRange `json:"date_range,omitempty"`
	// Compare adds the period before to every trend tile.
	Compare bool `json:"compare,omitempty"`
	// Digest puts the dashboard's trends in the workspace's digest.
	Digest bool `json:"digest,omitempty"`
}

func checkDashboardSettings(raw json.RawMessage) ([]byte, error) {
	if len(raw) == 0 {
		return []byte(`{}`), nil
	}
	var st dashboardSettings
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&st); err != nil {
		return nil, errInvalid("settings: " + err.Error())
	}
	if st.DateRange != nil {
		if _, _, err := st.DateRange.resolve(time.Now()); err != nil {
			return nil, errInvalid("settings.date_range: " + err.Error())
		}
	}
	return json.Marshal(st)
}

type dashboardView struct {
	ID          string          `json:"id"`
	BrandID     string          `json:"brand_id"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Settings    json.RawMessage `json:"settings"`
	Tiles       int             `json:"tiles"`
	// Share is the dashboard's link, when it has one (dashboards_share.go).
	Share     *shareView `json:"share"`
	CreatedBy *string    `json:"created_by"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

const dashboardColumns = `d.id::text, d.brand_id::text, d.name, d.description, d.settings,
	(SELECT count(*)::int FROM dashboard_tiles t WHERE t.dashboard_id = d.id), ` + shareColumns + `, d.created_by::text, d.created_at, d.updated_at`

func scanDashboard(row pgx.Row) (dashboardView, error) {
	var v dashboardView
	err := row.Scan(&v.ID, &v.BrandID, &v.Name, &v.Description, &v.Settings, &v.Tiles, &v.Share, &v.CreatedBy, &v.CreatedAt, &v.UpdatedAt)
	return v, err
}

// tileView is one tile: a saved insight (with its spec, so the app can
// run it) or a note.
type tileView struct {
	ID        string       `json:"id"`
	InsightID *string      `json:"insight_id"`
	Insight   *insightView `json:"insight"`
	Note      string       `json:"note"`
	X         int          `json:"x"`
	Y         int          `json:"y"`
	W         int          `json:"w"`
	H         int          `json:"h"`
}

type dashboardDetail struct {
	dashboardView
	TileList []tileView `json:"tile_list"`
}

func (s *Server) dashboardTiles(r *http.Request, q pgxQuerier, dashboard string) ([]tileView, error) {
	rows, err := q.Query(r.Context(), `SELECT t.id::text, t.insight_id::text, t.note, t.x, t.y, t.w, t.h,
			i.id::text, i.brand_id::text, i.kind, i.name, i.description, i.spec, i.created_by::text, i.created_at, i.updated_at
		FROM dashboard_tiles t LEFT JOIN insights i ON i.id = t.insight_id
		WHERE t.dashboard_id = $1 ORDER BY t.y, t.x, t.created_at`, dashboard)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (tileView, error) {
		var t tileView
		var iid, ibrand, ikind, iname, idesc *string
		var ispec []byte
		var icreatedBy *string
		var icreated, iupdated *time.Time
		if err := row.Scan(&t.ID, &t.InsightID, &t.Note, &t.X, &t.Y, &t.W, &t.H,
			&iid, &ibrand, &ikind, &iname, &idesc, &ispec, &icreatedBy, &icreated, &iupdated); err != nil {
			return t, err
		}
		if iid != nil {
			t.Insight = &insightView{ID: *iid, BrandID: *ibrand, Kind: *ikind, Name: *iname, Description: *idesc, Spec: ispec,
				CreatedBy: icreatedBy, CreatedAt: *icreated, UpdatedAt: *iupdated}
		}
		return t, nil
	})
}

// pgxQuerier is a pool or a transaction.
type pgxQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func (s *Server) listDashboards(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	limit, ok := pageLimit(w, r, 100, 500)
	if !ok {
		return
	}
	offset, ok := pageOffsetParam(w, r)
	if !ok {
		return
	}
	rows, err := s.pool.Query(r.Context(), `SELECT `+dashboardColumns+` FROM dashboards d
		WHERE d.workspace_id = $1 AND d.brand_id = $2 ORDER BY d.updated_at DESC, d.id DESC LIMIT $3 OFFSET $4`,
		a.WorkspaceID, a.Brand, limit+1, offset)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (dashboardView, error) { return scanDashboard(row) })
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var next any
	if len(list) > limit {
		list = list[:limit]
		next = offset + limit
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": nonNil(list), "next_cursor": nextCursor(pageOffset, next)})
}

type dashboardInput struct {
	Name        *string         `json:"name"`
	Description *string         `json:"description"`
	Settings    json.RawMessage `json:"settings"`
	// Layout moves tiles: each tile's new place.
	Layout []tileInput `json:"layout"`
}

type tileInput struct {
	ID        string  `json:"id,omitempty"`
	InsightID *string `json:"insight_id"`
	Note      *string `json:"note"`
	X         *int    `json:"x"`
	Y         *int    `json:"y"`
	W         *int    `json:"w"`
	H         *int    `json:"h"`
}

func (t tileInput) place(x, y, w, h *int) error {
	set := func(dst *int, v *int, lo, hi int, name string) error {
		if v == nil {
			return nil
		}
		if *v < lo || *v > hi {
			return errInvalid(name + " is " + strconv.Itoa(lo) + " to " + strconv.Itoa(hi))
		}
		*dst = *v
		return nil
	}
	if err := set(x, t.X, 0, 11, "x"); err != nil {
		return err
	}
	if err := set(y, t.Y, 0, 10000, "y"); err != nil {
		return err
	}
	if err := set(w, t.W, 1, 12, "w"); err != nil {
		return err
	}
	if err := set(h, t.H, 1, 24, "h"); err != nil {
		return err
	}
	if *x+*w > 12 {
		return errInvalid("x + w is at most 12: the grid has 12 columns")
	}
	return nil
}

func (s *Server) createDashboard(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in dashboardInput
	if !decode(w, r, &in) {
		return
	}
	var name, desc string
	if in.Name != nil {
		name = strings.TrimSpace(*in.Name)
	}
	if in.Description != nil {
		desc = strings.TrimSpace(*in.Description)
	}
	switch {
	case name == "" || len(name) > 120:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "name is 1 to 120 characters")
		return
	case len(desc) > 1000:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "description is at most 1000 characters")
		return
	case len(in.Layout) > 0:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "add tiles with POST .../tiles once the dashboard exists")
		return
	}
	settings, err := checkDashboardSettings(in.Settings)
	if s.allocError(w, r, err) {
		return
	}
	var v dashboardView
	err = pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		var id string
		if err := tx.QueryRow(r.Context(), `INSERT INTO dashboards (workspace_id, brand_id, name, description, settings, created_by, updated_by)
			VALUES ($1, $2, $3, $4, $5, $6, $6) RETURNING id::text`, a.WorkspaceID, a.Brand, name, desc, settings, nullIfEmpty(a.ActorID)).Scan(&id); err != nil {
			return err
		}
		if err := record(r.Context(), tx, &a.principal, a.WorkspaceID, "dashboard.created", "dashboard", id, map[string]any{"brand_id": a.Brand}); err != nil {
			return err
		}
		var err error
		v, err = scanDashboard(tx.QueryRow(r.Context(), `SELECT `+dashboardColumns+` FROM dashboards d WHERE d.id = $1`, id))
		return err
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusCreated, dashboardDetail{dashboardView: v, TileList: []tileView{}})
}

func (s *Server) getDashboard(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	v, err := scanDashboard(s.pool.QueryRow(r.Context(), `SELECT `+dashboardColumns+` FROM dashboards d
		WHERE d.workspace_id = $1 AND d.brand_id = $2 AND d.id = $3`, a.WorkspaceID, a.Brand, validOrNil(r.PathValue("dashboard"))))
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, http.StatusNotFound, "not_found", "dashboard not found")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	tiles, err := s.dashboardTiles(r, s.pool, v.ID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, dashboardDetail{dashboardView: v, TileList: nonNil(tiles)})
}

// lockDashboard loads a dashboard of the brand for update.
func lockDashboard(r *http.Request, tx pgx.Tx, ws, brand, id string) (dashboardView, error) {
	v, err := scanDashboard(tx.QueryRow(r.Context(), `SELECT `+dashboardColumns+` FROM dashboards d
		WHERE d.workspace_id = $1 AND d.brand_id = $2 AND d.id = $3 FOR UPDATE OF d`, ws, brand, validOrNil(id)))
	if errors.Is(err, pgx.ErrNoRows) {
		return v, errNotFound
	}
	return v, err
}

func (s *Server) touchDashboard(r *http.Request, tx pgx.Tx, a *access, id string, changed []string) error {
	if _, err := tx.Exec(r.Context(), `UPDATE dashboards SET updated_by = $2, updated_at = now() WHERE id = $1`, id, nullIfEmpty(a.ActorID)); err != nil {
		return err
	}
	return recordWithProps(r.Context(), tx, &a.principal, a.WorkspaceID, "dashboard.updated", "dashboard", id,
		map[string]any{"brand_id": a.Brand, "fields": changed}, map[string]any{"fields": strings.Join(changed, ",")})
}

func (s *Server) updateDashboard(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in dashboardInput
	if !decode(w, r, &in) {
		return
	}
	var out dashboardDetail
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		cur, err := lockDashboard(r, tx, a.WorkspaceID, a.Brand, r.PathValue("dashboard"))
		if err != nil {
			return err
		}
		name, desc, settings := cur.Name, cur.Description, []byte(cur.Settings)
		changed := []string{}
		if in.Name != nil {
			name = strings.TrimSpace(*in.Name)
			if name == "" || len(name) > 120 {
				return errInvalid("name is 1 to 120 characters")
			}
			changed = append(changed, "name")
		}
		if in.Description != nil {
			desc = strings.TrimSpace(*in.Description)
			if len(desc) > 1000 {
				return errInvalid("description is at most 1000 characters")
			}
			changed = append(changed, "description")
		}
		if len(in.Settings) > 0 {
			if settings, err = checkDashboardSettings(in.Settings); err != nil {
				return err
			}
			changed = append(changed, "settings")
		}
		if _, err := tx.Exec(r.Context(), `UPDATE dashboards SET name = $2, description = $3, settings = $4 WHERE id = $1`, cur.ID, name, desc, settings); err != nil {
			return err
		}
		if len(in.Layout) > 0 {
			if len(in.Layout) > 200 {
				return errInvalid("layout has at most 200 tiles")
			}
			for _, t := range in.Layout {
				if t.ID == "" {
					return errInvalid("layout tiles name their id")
				}
				var x, y, wd, h int
				if err := tx.QueryRow(r.Context(), `SELECT x, y, w, h FROM dashboard_tiles WHERE dashboard_id = $1 AND id = $2 FOR UPDATE`,
					cur.ID, validOrNil(t.ID)).Scan(&x, &y, &wd, &h); errors.Is(err, pgx.ErrNoRows) {
					return errInvalid("layout: tile " + t.ID + " isn't on this dashboard")
				} else if err != nil {
					return err
				}
				if err := t.place(&x, &y, &wd, &h); err != nil {
					return err
				}
				if _, err := tx.Exec(r.Context(), `UPDATE dashboard_tiles SET x = $2, y = $3, w = $4, h = $5 WHERE id = $1`, t.ID, x, y, wd, h); err != nil {
					return err
				}
			}
			changed = append(changed, "layout")
		}
		if err := s.touchDashboard(r, tx, a, cur.ID, changed); err != nil {
			return err
		}
		out.dashboardView, err = scanDashboard(tx.QueryRow(r.Context(), `SELECT `+dashboardColumns+` FROM dashboards d WHERE d.id = $1`, cur.ID))
		if err != nil {
			return err
		}
		out.TileList, err = s.dashboardTiles(r, tx, cur.ID)
		return err
	})
	if errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "dashboard not found")
		return
	}
	if s.allocError(w, r, err) {
		return
	}
	out.TileList = nonNil(out.TileList)
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) deleteDashboard(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		var id, name string
		err := tx.QueryRow(r.Context(), `DELETE FROM dashboards WHERE workspace_id = $1 AND brand_id = $2 AND id = $3
			RETURNING id::text, name`, a.WorkspaceID, a.Brand, validOrNil(r.PathValue("dashboard"))).Scan(&id, &name)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		return record(r.Context(), tx, &a.principal, a.WorkspaceID, "dashboard.deleted", "dashboard", id, map[string]any{"brand_id": a.Brand, "name": name})
	})
	if errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "dashboard not found")
		return
	}
	if s.allocError(w, r, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) addDashboardTile(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in tileInput
	if !decode(w, r, &in) {
		return
	}
	var out dashboardDetail
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		cur, err := lockDashboard(r, tx, a.WorkspaceID, a.Brand, r.PathValue("dashboard"))
		if err != nil {
			return err
		}
		if cur.Tiles >= 200 {
			return errInvalid("a dashboard has at most 200 tiles")
		}
		note := ""
		if in.Note != nil {
			note = strings.TrimSpace(*in.Note)
			if len(note) > 4000 {
				return errInvalid("note is at most 4000 characters")
			}
		}
		var insight *string
		var kind string
		if in.InsightID != nil && *in.InsightID != "" {
			err := tx.QueryRow(r.Context(), `SELECT id::text, kind FROM insights WHERE workspace_id = $1 AND brand_id = $2 AND id = $3`,
				a.WorkspaceID, a.Brand, validOrNil(*in.InsightID)).Scan(&insight, &kind)
			if errors.Is(err, pgx.ErrNoRows) {
				return errInvalid("insight_id isn't a saved insight of this brand")
			}
			if err != nil {
				return err
			}
		}
		if insight == nil && note == "" {
			return errInvalid("a tile is a saved insight (insight_id) or a note")
		}
		// New tiles go below everything, full width for a funnel.
		x, y, wd, h := 0, 0, 6, 4
		if insight != nil && kind == "funnel" {
			wd, h = 12, 6
		}
		if err := tx.QueryRow(r.Context(), `SELECT COALESCE(max(y + h), 0) FROM dashboard_tiles WHERE dashboard_id = $1`, cur.ID).Scan(&y); err != nil {
			return err
		}
		if y > 10000 {
			y = 10000
		}
		if err := in.place(&x, &y, &wd, &h); err != nil {
			return err
		}
		if _, err := tx.Exec(r.Context(), `INSERT INTO dashboard_tiles (workspace_id, brand_id, dashboard_id, insight_id, note, x, y, w, h)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`, a.WorkspaceID, a.Brand, cur.ID, insight, note, x, y, wd, h); err != nil {
			return err
		}
		if err := s.touchDashboard(r, tx, a, cur.ID, []string{"tiles"}); err != nil {
			return err
		}
		out.dashboardView, err = scanDashboard(tx.QueryRow(r.Context(), `SELECT `+dashboardColumns+` FROM dashboards d WHERE d.id = $1`, cur.ID))
		if err != nil {
			return err
		}
		out.TileList, err = s.dashboardTiles(r, tx, cur.ID)
		return err
	})
	if errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "dashboard not found")
		return
	}
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) updateDashboardTile(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in tileInput
	if !decode(w, r, &in) {
		return
	}
	var out dashboardDetail
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		cur, err := lockDashboard(r, tx, a.WorkspaceID, a.Brand, r.PathValue("dashboard"))
		if err != nil {
			return err
		}
		var x, y, wd, h int
		var note string
		var insight *string
		err = tx.QueryRow(r.Context(), `SELECT x, y, w, h, note, insight_id::text FROM dashboard_tiles WHERE dashboard_id = $1 AND id = $2 FOR UPDATE`,
			cur.ID, validOrNil(r.PathValue("tile"))).Scan(&x, &y, &wd, &h, &note, &insight)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		if in.Note != nil {
			note = strings.TrimSpace(*in.Note)
			if len(note) > 4000 {
				return errInvalid("note is at most 4000 characters")
			}
			if insight == nil && note == "" {
				return errInvalid("a note tile needs its note; delete the tile instead")
			}
		}
		if in.InsightID != nil {
			return errInvalid("a tile's insight can't change: add a tile for the other insight")
		}
		if err := in.place(&x, &y, &wd, &h); err != nil {
			return err
		}
		if _, err := tx.Exec(r.Context(), `UPDATE dashboard_tiles SET x = $2, y = $3, w = $4, h = $5, note = $6 WHERE id = $1`,
			r.PathValue("tile"), x, y, wd, h, note); err != nil {
			return err
		}
		if err := s.touchDashboard(r, tx, a, cur.ID, []string{"tiles"}); err != nil {
			return err
		}
		out.dashboardView, err = scanDashboard(tx.QueryRow(r.Context(), `SELECT `+dashboardColumns+` FROM dashboards d WHERE d.id = $1`, cur.ID))
		if err != nil {
			return err
		}
		out.TileList, err = s.dashboardTiles(r, tx, cur.ID)
		return err
	})
	if errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "dashboard or tile not found")
		return
	}
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) removeDashboardTile(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		cur, err := lockDashboard(r, tx, a.WorkspaceID, a.Brand, r.PathValue("dashboard"))
		if err != nil {
			return err
		}
		tag, err := tx.Exec(r.Context(), `DELETE FROM dashboard_tiles WHERE dashboard_id = $1 AND id = $2`, cur.ID, validOrNil(r.PathValue("tile")))
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errNotFound
		}
		return s.touchDashboard(r, tx, a, cur.ID, []string{"tiles"})
	})
	if errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "dashboard or tile not found")
		return
	}
	if s.allocError(w, r, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
