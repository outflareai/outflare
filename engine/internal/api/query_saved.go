package api

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/metrics"
)

// Query Studio (plans/brands/README.md, "Data pages"): queries saved for
// one brand, with every version of their SQL and chart kept, and query
// results downloaded as CSV.
//
// The owner's rule: anyone with customers.read runs queries over the
// brands they can see, limited by their scopes; saving and sharing needs
// customers.manage. A query is private to the person who saved it until
// they share it with the brand, and then everyone who may query the brand
// sees and runs it. A saved query runs exactly as an ad hoc one on its
// brand's route (execQuery): the brand is fixed in its query_scopes row,
// so its SQL never reaches another brand, whatever it says.

func (s *Server) routesSavedQueries(mux *http.ServeMux) {
	read, manage := auth.ScopeCustomersRead, auth.ScopeCustomersManage
	mux.HandleFunc("GET /v1/brands/{brand}/queries", s.onBrand(read, s.listSavedQueries))
	mux.HandleFunc("POST /v1/brands/{brand}/queries", s.onBrand(manage, s.createSavedQuery))
	mux.HandleFunc("GET /v1/brands/{brand}/queries/{saved_query}", s.onBrand(read, s.getSavedQuery))
	mux.HandleFunc("PATCH /v1/brands/{brand}/queries/{saved_query}", s.onBrand(manage, s.updateSavedQuery))
	mux.HandleFunc("POST /v1/brands/{brand}/queries/{saved_query}/archive", s.onBrand(manage, s.archiveSavedQuery))
	mux.HandleFunc("GET /v1/brands/{brand}/queries/{saved_query}/versions", s.onBrand(read, s.listSavedQueryVersions))
	mux.HandleFunc("GET /v1/brands/{brand}/queries/{saved_query}/versions/{version}", s.onBrand(read, s.getSavedQueryVersion))
	mux.HandleFunc("POST /v1/brands/{brand}/queries/{saved_query}/versions/{version}/restore", s.onBrand(manage, s.restoreSavedQueryVersion))
	mux.HandleFunc("POST /v1/brands/{brand}/queries/{saved_query}/run", s.onBrand(read, s.runSavedQuery))
	mux.HandleFunc("POST /v1/brands/{brand}/queries/{saved_query}/csv", s.onBrand(read, s.exportSavedQuery))
	mux.HandleFunc("POST /v1/brands/{brand}/query/csv", s.onBrand(read, s.exportQuery))
}

const (
	// A CSV download takes more rows than an answer in JSON: it is for a
	// spreadsheet, not for reading on screen or in an agent's context.
	queryExportMaxRows  = 10000
	queryExportMaxBytes = 16 << 20
	// The newest versions GET .../versions lists.
	savedQueryVersionsShown = 200
	// The saved queries GET .../queries lists, newest first.
	savedQueriesShown = 500
)

// queryChart is how a query's results are drawn: one x column and up to
// eight y columns, as bars, lines or areas. Null is a table.
type queryChart struct {
	Type string   `json:"type"`
	X    string   `json:"x"`
	Y    []string `json:"y"`
}

var queryChartColumn = regexp.MustCompile(`^.{1,63}$`)

func (c *queryChart) check() string {
	switch {
	case c.Type != "bar" && c.Type != "line" && c.Type != "area":
		return "chart.type is bar, line or area"
	case !queryChartColumn.MatchString(c.X):
		return "chart.x is the name of a column in the results"
	case len(c.Y) < 1 || len(c.Y) > 8:
		return "chart.y names 1 to 8 columns in the results"
	}
	for _, y := range c.Y {
		if !queryChartColumn.MatchString(y) {
			return "chart.y names 1 to 8 columns in the results"
		}
	}
	return ""
}

type savedQueryView struct {
	ID             string      `json:"id"`
	BrandID        string      `json:"brand_id"`
	Name           string      `json:"name"`
	Description    string      `json:"description"`
	Visibility     string      `json:"visibility"`
	OwnerID        *string     `json:"owner_id"`
	Mine           bool        `json:"mine"`
	CanEdit        bool        `json:"can_edit"`
	CurrentVersion int         `json:"current_version"`
	SQL            string      `json:"sql"`
	Chart          *queryChart `json:"chart"`
	CreatedBy      *string     `json:"created_by"`
	UpdatedBy      *string     `json:"updated_by"`
	ArchivedAt     *time.Time  `json:"archived_at"`
	CreatedAt      time.Time   `json:"created_at"`
	UpdatedAt      time.Time   `json:"updated_at"`
}

type savedQueryVersionView struct {
	Version      int         `json:"version"`
	SQL          string      `json:"sql"`
	Chart        *queryChart `json:"chart"`
	Note         string      `json:"note"`
	RestoredFrom *int        `json:"restored_from"`
	Current      bool        `json:"current"`
	CreatedBy    *string     `json:"created_by"`
	CreatedAt    time.Time   `json:"created_at"`
}

const savedQueryColumns = `q.id, q.brand_id, q.name, q.description, q.visibility, q.owner_id, q.current_version, v.sql, v.chart,
	q.created_by, q.updated_by, q.archived_at, q.created_at, q.updated_at
	FROM saved_queries q JOIN saved_query_versions v ON v.query_id = q.id AND v.version = q.current_version`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanSavedQuery(row rowScanner, a *access) (savedQueryView, error) {
	var v savedQueryView
	var chart []byte
	err := row.Scan(&v.ID, &v.BrandID, &v.Name, &v.Description, &v.Visibility, &v.OwnerID, &v.CurrentVersion, &v.SQL, &chart,
		&v.CreatedBy, &v.UpdatedBy, &v.ArchivedAt, &v.CreatedAt, &v.UpdatedAt)
	if err != nil {
		return v, err
	}
	if len(chart) > 0 {
		var c queryChart
		if json.Unmarshal(chart, &c) == nil {
			v.Chart = &c
		}
	}
	me := a.personID()
	v.Mine = me != "" && v.OwnerID != nil && *v.OwnerID == me
	v.CanEdit = v.ArchivedAt == nil && a.has(auth.ScopeCustomersManage) && (v.Visibility == "brand" || v.Mine)
	return v, nil
}

// seesQuery reports whether the caller may see a saved query of its route's
// brand: a shared one, or a private one of their own.
func seesQuery(v savedQueryView) bool { return v.Visibility == "brand" || v.Mine }

// findSavedQuery loads one of the route's brand's saved queries, or
// answers 404. lock takes the row for an update in q's transaction.
// adminArchive lets a caller with workspace.manage reach another
// person's private query, which only archiving does.
func (s *Server) findSavedQuery(w http.ResponseWriter, r *http.Request, q querier, a *access, lock, adminArchive bool) (savedQueryView, bool) {
	id := r.PathValue("saved_query")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "saved query not found")
		return savedQueryView{}, false
	}
	if lock {
		// Its own statement, so the read below sees a version a concurrent
		// change made before this one got the lock.
		var one int
		err := q.QueryRow(r.Context(), `SELECT 1 FROM saved_queries WHERE id = $1 AND workspace_id = $2 AND brand_id = $3 FOR UPDATE`, id, a.WorkspaceID, a.Brand).Scan(&one)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			s.internal(w, r, err)
			return savedQueryView{}, false
		}
	}
	v, err := scanSavedQuery(q.QueryRow(r.Context(), `SELECT `+savedQueryColumns+` WHERE q.id = $1 AND q.workspace_id = $2 AND q.brand_id = $3`, id, a.WorkspaceID, a.Brand), a)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !seesQuery(v) && !(adminArchive && a.has(auth.ScopeWorkspaceManage))) {
		writeError(w, r, http.StatusNotFound, "not_found", "saved query not found")
		return savedQueryView{}, false
	}
	if err != nil {
		s.internal(w, r, err)
		return savedQueryView{}, false
	}
	return v, true
}

func (s *Server) listSavedQueries(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	q := r.URL.Query()
	search := strings.TrimSpace(q.Get("q"))
	if len(search) > 200 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "q is at most 200 characters")
		return
	}
	show := q.Get("visibility")
	if show != "" && show != "private" && show != "brand" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "visibility is private (yours) or brand (shared)")
		return
	}
	rows, err := s.pool.Query(r.Context(), `SELECT `+savedQueryColumns+`
		WHERE q.workspace_id = $1 AND q.brand_id = $2 AND q.archived_at IS NULL
			AND (q.visibility = 'brand' OR (q.owner_id IS NOT NULL AND q.owner_id::text = $3::text))
			AND ($4::text = '' OR q.visibility = $4::text)
			AND ($5::text = '' OR q.name ILIKE '%' || $5::text || '%' OR q.description ILIKE '%' || $5::text || '%')
		ORDER BY q.updated_at DESC, q.id LIMIT $6`,
		a.WorkspaceID, a.Brand, a.personID(), show, likeEscape(search), savedQueriesShown)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out := []savedQueryView{}
	for rows.Next() {
		v, err := scanSavedQuery(rows, a)
		if err != nil {
			rows.Close()
			s.internal(w, r, err)
			return
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out})
}

func (s *Server) getSavedQuery(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	if v, ok := s.findSavedQuery(w, r, s.pool, a, false, false); ok {
		writeJSON(w, http.StatusOK, v)
	}
}

type savedQueryInput struct {
	Name        *string              `json:"name"`
	Description *string              `json:"description"`
	SQL         *string              `json:"sql"`
	Chart       optional[queryChart] `json:"chart"`
	Visibility  *string              `json:"visibility"`
	Note        *string              `json:"note"`
}

// check validates the fields sent, trimming them in place.
func (in *savedQueryInput) check() string {
	if in.Name != nil {
		*in.Name = strings.TrimSpace(*in.Name)
		if n := len([]rune(*in.Name)); n < 1 || n > 200 {
			return "name is 1 to 200 characters"
		}
	}
	if in.Description != nil {
		*in.Description = strings.TrimSpace(*in.Description)
		if len([]rune(*in.Description)) > 2000 {
			return "description is at most 2000 characters"
		}
	}
	if in.SQL != nil {
		*in.SQL = strings.TrimSpace(*in.SQL)
		if *in.SQL == "" || len(*in.SQL) > queryMaxSQL {
			return fmt.Sprintf("sql is one SELECT of 1 to %d characters", queryMaxSQL)
		}
	}
	if in.Chart.Value != nil {
		if msg := in.Chart.Value.check(); msg != "" {
			return msg
		}
	}
	if in.Visibility != nil && *in.Visibility != "private" && *in.Visibility != "brand" {
		return "visibility is private (only you) or brand (everyone who can query the brand)"
	}
	if in.Note != nil {
		*in.Note = strings.TrimSpace(*in.Note)
		if len([]rune(*in.Note)) > 500 {
			return "note is at most 500 characters"
		}
	}
	return ""
}

func chartJSON(c *queryChart) []byte {
	if c == nil {
		return nil
	}
	b, _ := json.Marshal(c)
	return b
}

func chartKind(c *queryChart) string {
	if c == nil {
		return "table"
	}
	return c.Type
}

func actorOrNil(a *access) *string {
	if a.ActorID == "" {
		return nil
	}
	return &a.ActorID
}

func (s *Server) createSavedQuery(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in savedQueryInput
	if !decode(w, r, &in) {
		return
	}
	switch {
	case in.Name == nil:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "name is required")
		return
	case in.SQL == nil:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "sql is required: the query to save")
		return
	}
	if msg := in.check(); msg != "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", msg)
		return
	}
	visibility := "private"
	if in.Visibility != nil {
		visibility = *in.Visibility
	}
	owner := nullIfEmpty(a.personID())
	if visibility == "private" && owner == nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "visibility: this token belongs to no person, so it can only save queries shared with the brand (visibility brand)")
		return
	}
	desc, note := "", ""
	if in.Description != nil {
		desc = *in.Description
	}
	if in.Note != nil {
		note = *in.Note
	}
	ctx := r.Context()
	var id string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO saved_queries (workspace_id, brand_id, name, description, visibility, owner_id, created_by, updated_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $7) RETURNING id`, a.WorkspaceID, a.Brand, *in.Name, desc, visibility, owner, actorOrNil(a)).Scan(&id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO saved_query_versions (query_id, workspace_id, brand_id, version, sql, chart, note, created_by)
			VALUES ($1, $2, $3, 1, $4, $5, $6, $7)`, id, a.WorkspaceID, a.Brand, *in.SQL, chartJSON(in.Chart.Value), note, actorOrNil(a)); err != nil {
			return err
		}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "query.saved", "saved_query", id,
			map[string]any{"brand_id": a.Brand, "name": *in.Name, "visibility": visibility, "version": 1},
			map[string]any{"visibility": visibility, "version": 1, "chart": chartKind(in.Chart.Value)})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	r.SetPathValue("saved_query", id)
	if v, ok := s.findSavedQuery(w, r, s.pool, a, false, false); ok {
		writeJSON(w, http.StatusCreated, v)
	}
}

// editable answers 409 for an archived query. Anyone with
// customers.manage edits a shared query; a private one is only ever
// found for its owner.
func editable(w http.ResponseWriter, r *http.Request, v savedQueryView) bool {
	if v.ArchivedAt != nil {
		writeError(w, r, http.StatusConflict, "saved_query_archived", "this saved query is archived: save its SQL as a new query instead")
		return false
	}
	return true
}

func (s *Server) updateSavedQuery(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in savedQueryInput
	if !decode(w, r, &in) {
		return
	}
	if msg := in.check(); msg != "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", msg)
		return
	}
	ctx := r.Context()
	done := false
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		v, ok := s.findSavedQuery(w, r, tx, a, true, false)
		if !ok || !editable(w, r, v) {
			done = true
			return nil
		}
		if in.Visibility != nil && *in.Visibility != v.Visibility && !v.Mine {
			writeError(w, r, http.StatusForbidden, "forbidden", "visibility: only the person who saved this query can share or unshare it")
			done = true
			return nil
		}
		sql, chart := v.SQL, v.Chart
		if in.SQL != nil {
			sql = *in.SQL
		}
		if in.Chart.Set {
			chart = in.Chart.Value
		}
		newVersion := sql != v.SQL || string(chartJSON(chart)) != string(chartJSON(v.Chart))
		var fields []string
		if in.Name != nil && *in.Name != v.Name {
			fields = append(fields, "name")
		}
		if in.Description != nil && *in.Description != v.Description {
			fields = append(fields, "description")
		}
		shared := in.Visibility != nil && *in.Visibility != v.Visibility
		if !newVersion && len(fields) == 0 && !shared {
			return nil // nothing changed
		}
		version := v.CurrentVersion
		if newVersion {
			version++
			note := ""
			if in.Note != nil {
				note = *in.Note
			}
			if _, err := tx.Exec(ctx, `INSERT INTO saved_query_versions (query_id, workspace_id, brand_id, version, sql, chart, note, created_by)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`, v.ID, a.WorkspaceID, a.Brand, version, sql, chartJSON(chart), note, actorOrNil(a)); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE saved_queries SET name = COALESCE($2, name), description = COALESCE($3, description),
			visibility = COALESCE($4, visibility), current_version = $5, updated_by = $6, updated_at = now() WHERE id = $1`,
			v.ID, in.Name, in.Description, in.Visibility, version, actorOrNil(a)); err != nil {
			return err
		}
		if newVersion {
			if err := recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "query.version_created", "saved_query", v.ID,
				map[string]any{"brand_id": a.Brand, "version": version, "previous_version": v.CurrentVersion},
				map[string]any{"version": version, "chart": chartKind(chart), "sql_changed": sql != v.SQL}); err != nil {
				return err
			}
		}
		if len(fields) > 0 {
			if err := recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "query.updated", "saved_query", v.ID,
				map[string]any{"brand_id": a.Brand, "fields": fields}, map[string]any{"fields": fields}); err != nil {
				return err
			}
		}
		if shared {
			if err := recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "query.shared", "saved_query", v.ID,
				map[string]any{"brand_id": a.Brand, "visibility": *in.Visibility}, map[string]any{"visibility": *in.Visibility}); err != nil {
				return err
			}
		}
		return nil
	})
	if done {
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if v, ok := s.findSavedQuery(w, r, s.pool, a, false, false); ok {
		writeJSON(w, http.StatusOK, v)
	}
}

func (s *Server) archiveSavedQuery(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	done := false
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		v, ok := s.findSavedQuery(w, r, tx, a, true, true)
		if !ok {
			done = true
			return nil
		}
		if v.ArchivedAt != nil {
			return nil // already archived
		}
		if _, err := tx.Exec(ctx, `UPDATE saved_queries SET archived_at = now(), archived_by = $2, updated_at = now() WHERE id = $1`, v.ID, actorOrNil(a)); err != nil {
			return err
		}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "query.archived", "saved_query", v.ID,
			map[string]any{"brand_id": a.Brand, "name": v.Name}, map[string]any{"visibility": v.Visibility, "mine": v.Mine})
	})
	if done {
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if v, ok := s.findSavedQuery(w, r, s.pool, a, false, true); ok {
		writeJSON(w, http.StatusOK, v)
	}
}

func scanVersion(row rowScanner, current int) (savedQueryVersionView, error) {
	var v savedQueryVersionView
	var chart []byte
	if err := row.Scan(&v.Version, &v.SQL, &chart, &v.Note, &v.RestoredFrom, &v.CreatedBy, &v.CreatedAt); err != nil {
		return v, err
	}
	if len(chart) > 0 {
		var c queryChart
		if json.Unmarshal(chart, &c) == nil {
			v.Chart = &c
		}
	}
	v.Current = v.Version == current
	return v, nil
}

const versionColumns = `version, sql, chart, note, restored_from, created_by, created_at FROM saved_query_versions`

func (s *Server) listSavedQueryVersions(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	q, ok := s.findSavedQuery(w, r, s.pool, a, false, false)
	if !ok {
		return
	}
	rows, err := s.pool.Query(r.Context(), `SELECT `+versionColumns+` WHERE query_id = $1 AND brand_id = $2 ORDER BY version DESC LIMIT $3`,
		q.ID, a.Brand, savedQueryVersionsShown)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out := []savedQueryVersionView{}
	for rows.Next() {
		v, err := scanVersion(rows, q.CurrentVersion)
		if err != nil {
			rows.Close()
			s.internal(w, r, err)
			return
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out})
}

// findVersion loads version {version} of q, or answers 404.
func (s *Server) findVersion(w http.ResponseWriter, r *http.Request, db querier, q savedQueryView, raw string) (savedQueryVersionView, bool) {
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		writeError(w, r, http.StatusNotFound, "not_found", "version not found: versions are numbered from 1")
		return savedQueryVersionView{}, false
	}
	v, err := scanVersion(db.QueryRow(r.Context(), `SELECT `+versionColumns+` WHERE query_id = $1 AND brand_id = $2 AND version = $3`, q.ID, q.BrandID, n), q.CurrentVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, http.StatusNotFound, "not_found", fmt.Sprintf("version not found: this query has versions 1 to %d", q.CurrentVersion))
		return v, false
	}
	if err != nil {
		s.internal(w, r, err)
		return v, false
	}
	return v, true
}

func (s *Server) getSavedQueryVersion(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	q, ok := s.findSavedQuery(w, r, s.pool, a, false, false)
	if !ok {
		return
	}
	if v, ok := s.findVersion(w, r, s.pool, q, r.PathValue("version")); ok {
		writeJSON(w, http.StatusOK, v)
	}
}

func (s *Server) restoreSavedQueryVersion(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in struct {
		Note *string `json:"note"`
	}
	if !decodeOptional(w, r, &in) {
		return
	}
	if in.Note != nil && len([]rune(strings.TrimSpace(*in.Note))) > 500 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "note is at most 500 characters")
		return
	}
	ctx := r.Context()
	done := false
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q, ok := s.findSavedQuery(w, r, tx, a, true, false)
		if !ok || !editable(w, r, q) {
			done = true
			return nil
		}
		old, ok := s.findVersion(w, r, tx, q, r.PathValue("version"))
		if !ok {
			done = true
			return nil
		}
		if old.Current {
			writeError(w, r, http.StatusConflict, "conflict", fmt.Sprintf("version %d is already the current version", old.Version))
			done = true
			return nil
		}
		note := fmt.Sprintf("Restored version %d", old.Version)
		if in.Note != nil && strings.TrimSpace(*in.Note) != "" {
			note = strings.TrimSpace(*in.Note)
		}
		version := q.CurrentVersion + 1
		if _, err := tx.Exec(ctx, `INSERT INTO saved_query_versions (query_id, workspace_id, brand_id, version, sql, chart, note, restored_from, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`, q.ID, a.WorkspaceID, a.Brand, version, old.SQL, chartJSON(old.Chart), note, old.Version, actorOrNil(a)); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE saved_queries SET current_version = $2, updated_by = $3, updated_at = now() WHERE id = $1`, q.ID, version, actorOrNil(a)); err != nil {
			return err
		}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "query.restored", "saved_query", q.ID,
			map[string]any{"brand_id": a.Brand, "version": version, "restored_from": old.Version},
			map[string]any{"version": version, "restored_from": old.Version})
	})
	if done {
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if v, ok := s.findSavedQuery(w, r, s.pool, a, false, false); ok {
		writeJSON(w, http.StatusOK, v)
	}
}

type savedRunInput struct {
	Version *int `json:"version"`
	Limit   *int `json:"limit"`
}

// savedQuerySQL is the SQL of the version a run or export asks for: the
// current one, or in.Version.
func (s *Server) savedQuerySQL(w http.ResponseWriter, r *http.Request, a *access, in savedRunInput) (savedQueryView, savedQueryVersionView, bool) {
	q, ok := s.findSavedQuery(w, r, s.pool, a, false, false)
	if !ok {
		return q, savedQueryVersionView{}, false
	}
	if q.ArchivedAt != nil {
		writeError(w, r, http.StatusConflict, "saved_query_archived", "this saved query is archived: save its SQL as a new query to run it")
		return q, savedQueryVersionView{}, false
	}
	n := q.CurrentVersion
	if in.Version != nil {
		n = *in.Version
	}
	v, ok := s.findVersion(w, r, s.pool, q, strconv.Itoa(n))
	return q, v, ok
}

// queryOutcome is a query's outcome for metrics.
func queryOutcome(err error) string {
	var qe *queryError
	switch {
	case err == nil:
		return "success"
	case errors.As(err, &qe) && qe.code == "query_timeout":
		return "timeout"
	case errors.As(err, &qe) && qe.code == "query_unavailable":
		return "unavailable"
	case errors.As(err, &qe):
		return "invalid"
	}
	return "error"
}

type savedRunResult struct {
	queryResult
	SavedQueryID string `json:"saved_query_id"`
	Version      int    `json:"version"`
}

func (s *Server) runSavedQuery(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in savedRunInput
	if !decodeOptional(w, r, &in) {
		return
	}
	limit, ok := queryLimit(w, r, in.Limit, queryDefaultRows, queryMaxRows)
	if !ok {
		return
	}
	q, v, ok := s.savedQuerySQL(w, r, a, in)
	if !ok {
		return
	}
	res, used, err := s.execQuery(r.Context(), a, v.SQL, limit, queryMaxBytes)
	metrics.QuerySavedRan(queryOutcome(err))
	if s.queryFailed(w, r, err) {
		return
	}
	s.trackQueryRan(r, a, used, res, map[string]any{"saved_query_id": q.ID, "version": v.Version, "current_version": v.Current})
	writeJSON(w, http.StatusOK, savedRunResult{queryResult: res, SavedQueryID: q.ID, Version: v.Version})
}

type exportInput struct {
	SQL   string `json:"sql"`
	Limit *int   `json:"limit"`
}

func (s *Server) exportQuery(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in exportInput
	if !decode(w, r, &in) {
		return
	}
	limit, ok := queryLimit(w, r, in.Limit, queryExportMaxRows, queryExportMaxRows)
	if !ok {
		return
	}
	s.exportCSV(w, r, a, in.SQL, limit, "outflare-query", nil)
}

func (s *Server) exportSavedQuery(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in savedRunInput
	if !decodeOptional(w, r, &in) {
		return
	}
	limit, ok := queryLimit(w, r, in.Limit, queryExportMaxRows, queryExportMaxRows)
	if !ok {
		return
	}
	q, v, ok := s.savedQuerySQL(w, r, a, in)
	if !ok {
		return
	}
	s.exportCSV(w, r, a, v.SQL, limit, fileSlug(q.Name), map[string]any{"saved_query_id": q.ID, "version": v.Version})
}

var notSlug = regexp.MustCompile(`[^a-z0-9]+`)

// fileSlug is a name made safe for a file name and a header.
func fileSlug(name string) string {
	s := strings.Trim(notSlug.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if len(s) > 60 {
		s = strings.TrimRight(s[:60], "-")
	}
	if s == "" {
		return "outflare-query"
	}
	return s
}

// exportCSV runs sql and answers its rows as a CSV file. Personal data
// leaving the app in bulk, so every download is an audit event
// (query.exported), with the counts but never the SQL or the rows.
func (s *Server) exportCSV(w http.ResponseWriter, r *http.Request, a *access, sql string, limit int, name string, saved map[string]any) {
	ctx := r.Context()
	res, used, err := s.execQuery(ctx, a, sql, limit, queryExportMaxBytes)
	if err != nil {
		metrics.QueryExported(queryOutcome(err), 0)
		s.queryFailed(w, r, err)
		return
	}
	data := map[string]any{"brand_id": a.Brand, "rows": res.RowCount, "columns": len(res.Columns), "truncated": res.Truncated}
	props := queryEventProps(r, used, res)
	for k, v := range saved {
		data[k], props[k] = v, v
	}
	if err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "query.exported", "brand", a.Brand, data, props)
	}); err != nil {
		metrics.QueryExported("error", 0)
		s.internal(w, r, err)
		return
	}
	metrics.QueryExported("success", res.RowCount)
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+"-"+time.Now().UTC().Format("20060102-150405")+`.csv"`)
	w.Header().Set("Outflare-Query-Rows", strconv.Itoa(res.RowCount))
	w.Header().Set("Outflare-Query-Truncated", strconv.FormatBool(res.Truncated))
	cw := csv.NewWriter(w)
	header := make([]string, len(res.Columns))
	for i, c := range res.Columns {
		header[i] = csvSafe(c.Name)
	}
	cw.Write(header) //nolint:errcheck
	row := make([]string, len(res.Columns))
	for _, vals := range res.Rows {
		for i, v := range vals {
			row[i] = csvCell(v)
		}
		cw.Write(row) //nolint:errcheck
	}
	cw.Flush()
}

// csvCell writes one value of a query's results as text: JSON for jsonb
// and arrays, RFC 3339 for times, empty for null.
func csvCell(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return csvSafe(t)
	case bool:
		return strconv.FormatBool(t)
	case time.Time:
		return t.UTC().Format(time.RFC3339Nano)
	case int16, int32, int64, float32, float64:
		return fmt.Sprint(t)
	}
	if b, err := json.Marshal(v); err == nil {
		s := string(b)
		if n, err := strconv.Unquote(s); err == nil {
			s = n // a value that marshals as a JSON string, such as a numeric
		}
		return csvSafe(s)
	}
	return csvSafe(fmt.Sprint(v))
}
