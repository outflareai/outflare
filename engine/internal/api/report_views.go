package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
)

// Saved views of the drill-down report (plans/ad-tracker.md, B5b): its
// grouping, filters, sort, dates, time zone, model and custom columns,
// with how the app shows it (display, the app's own). A view is its
// maker's until shared with the brand's team. GET .../drilldown?view=id
// fills in what the request doesn't say from the view.

const maxReportViews = 200

func (s *Server) routesReportViews(mux *http.ServeMux) {
	read, manage := auth.ScopeAnalyticsRead, auth.ScopeAnalyticsManage
	mux.HandleFunc("GET /v1/brands/{brand}/report-views", s.onBrand(read, s.listReportViews))
	mux.HandleFunc("POST /v1/brands/{brand}/report-views", s.onBrand(manage, s.createReportView))
	mux.HandleFunc("GET /v1/brands/{brand}/report-views/{id}", s.onBrand(read, s.getReportView))
	mux.HandleFunc("PATCH /v1/brands/{brand}/report-views/{id}", s.onBrand(manage, s.updateReportView))
	mux.HandleFunc("DELETE /v1/brands/{brand}/report-views/{id}", s.onBrand(manage, s.deleteReportView))
}

type reportViewColumn struct {
	Name    string `json:"name"`
	Formula string `json:"formula"`
	// Format is how the app shows it: number, percent or money.
	Format string `json:"format,omitempty"`
}

// reportViewSpec is what a view keeps: the report's query parameters,
// and the app's display settings, which the engine stores as they are.
type reportViewSpec struct {
	GroupBy        []string           `json:"group_by,omitempty"`
	Filters        []string           `json:"filters,omitempty"`
	Excludes       []string           `json:"excludes,omitempty"`
	Sort           string             `json:"sort,omitempty"`
	Limit          int                `json:"limit,omitempty"`
	ConversionTime string             `json:"conversion_time,omitempty"`
	Model          string             `json:"model,omitempty"`
	Timezone       string             `json:"timezone,omitempty"`
	DateRange      *dateRange         `json:"date_range,omitempty"`
	Columns        []reportViewColumn `json:"columns,omitempty"`
	Display        map[string]any     `json:"display,omitempty"`
}

// query is the view as the report's query parameters. A date preset is
// resolved in loc.
func (v reportViewSpec) query(loc *time.Location) (url.Values, error) {
	q := url.Values{}
	if len(v.GroupBy) > 0 {
		q.Set("group_by", strings.Join(v.GroupBy, ","))
	}
	q["filter"], q["exclude"] = v.Filters, v.Excludes
	for k, x := range map[string]string{"sort": v.Sort, "conversion_time": v.ConversionTime, "model": v.Model, "timezone": v.Timezone} {
		if x != "" {
			q.Set(k, x)
		}
	}
	if v.Limit > 0 {
		q.Set("limit", strconv.Itoa(v.Limit))
	}
	for _, c := range v.Columns {
		q.Add("column", c.Name+":"+c.Formula)
	}
	if v.DateRange != nil {
		if v.Timezone != "" {
			loc = zone(v.Timezone)
		}
		from, to, err := v.DateRange.resolve(time.Now().In(loc))
		if err != nil {
			return nil, errInvalid("spec.date_range: " + err.Error())
		}
		q.Set("from", from.Format(time.DateOnly))
		q.Set("to", to.AddDate(0, 0, -1).Format(time.DateOnly))
	}
	for k, x := range q {
		if len(x) == 0 {
			delete(q, k)
		}
	}
	return q, nil
}

// check validates the view the way the report would read it.
func (v reportViewSpec) check() error {
	if v.Timezone != "" && !validTimezone(v.Timezone) {
		return errInvalid("spec.timezone is an IANA time zone, such as Europe/Berlin")
	}
	for _, c := range v.Columns {
		switch c.Format {
		case "", "number", "percent", "money":
		default:
			return errInvalid("spec.columns[].format is number, percent or money")
		}
	}
	if v.Display != nil {
		if b, _ := json.Marshal(v.Display); len(b) > 16_000 {
			return errInvalid("spec.display is at most 16 KB")
		}
	}
	q, err := v.query(time.UTC)
	if err != nil {
		return err
	}
	_, err = drilldownQueryValues(q)
	return err
}

type reportViewView struct {
	ID      string         `json:"id"`
	BrandID string         `json:"brand_id"`
	Name    string         `json:"name"`
	Spec    reportViewSpec `json:"spec"`
	Shared  bool           `json:"shared"`
	// Mine says the caller made it: only its maker sees a view that isn't
	// shared.
	Mine      bool      `json:"mine"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

const reportViewSelect = `SELECT id::text, brand_id::text, name, spec, shared, created_by IS NOT DISTINCT FROM $3::uuid, created_at, updated_at
	FROM report_views WHERE workspace_id = $1 AND brand_id = $2 AND (shared OR created_by IS NOT DISTINCT FROM $3::uuid)`

func scanReportView(row pgx.Row) (reportViewView, error) {
	var v reportViewView
	var spec []byte
	err := row.Scan(&v.ID, &v.BrandID, &v.Name, &spec, &v.Shared, &v.Mine, &v.CreatedAt, &v.UpdatedAt)
	if err == nil {
		err = json.Unmarshal(spec, &v.Spec)
	}
	return v, err
}

// findReportView finds a view the caller can see.
func (s *Server) findReportView(ctx context.Context, id string) (reportViewView, error) {
	a := accessFrom(ctx)
	if !validUUID(id) {
		return reportViewView{}, errNotFound
	}
	v, err := scanReportView(s.pool.QueryRow(ctx, reportViewSelect+` AND id = $4`, a.WorkspaceID, a.Brand, a.ActorID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return v, errNotFound
	}
	return v, err
}

func (s *Server) listReportViews(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	rows, err := s.pool.Query(r.Context(), reportViewSelect+` ORDER BY lower(name), created_at LIMIT 200`, a.WorkspaceID, a.Brand, a.ActorID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (reportViewView, error) { return scanReportView(row) })
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if list == nil {
		list = []reportViewView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

func (s *Server) getReportView(w http.ResponseWriter, r *http.Request) {
	v, err := s.findReportView(r.Context(), r.PathValue("id"))
	if errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "view not found")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

type reportViewInput struct {
	Name   *string         `json:"name"`
	Spec   *reportViewSpec `json:"spec"`
	Shared *bool           `json:"shared"`
}

func (in *reportViewInput) check(create bool) error {
	if in.Name != nil {
		n := strings.TrimSpace(*in.Name)
		if n == "" || len(n) > 120 {
			return errInvalid("name is 1 to 120 characters")
		}
		in.Name = &n
	} else if create {
		return errInvalid("name is required")
	}
	if in.Spec != nil {
		return in.Spec.check()
	}
	return nil
}

func reportViewProps(v reportViewView) map[string]any {
	return map[string]any{"shared": v.Shared, "levels": len(v.Spec.GroupBy), "columns": len(v.Spec.Columns), "filters": len(v.Spec.Filters) + len(v.Spec.Excludes)}
}

func (s *Server) createReportView(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	var in reportViewInput
	if !decode(w, r, &in) || s.allocError(w, r, in.check(true)) {
		return
	}
	if in.Spec == nil {
		in.Spec = &reportViewSpec{}
	}
	spec, _ := json.Marshal(in.Spec)
	shared := in.Shared != nil && *in.Shared
	var id string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM report_views WHERE workspace_id = $1 AND brand_id = $2`, a.WorkspaceID, a.Brand).Scan(&n); err != nil {
			return err
		}
		if n >= maxReportViews {
			return errInvalid("a brand has at most 200 report views")
		}
		if err := tx.QueryRow(ctx, `INSERT INTO report_views (workspace_id, brand_id, name, spec, shared, created_by) VALUES ($1, $2, $3, $4, $5, $6) RETURNING id::text`,
			a.WorkspaceID, a.Brand, *in.Name, spec, shared, a.ActorID).Scan(&id); err != nil {
			return err
		}
		v := reportViewView{Shared: shared, Spec: *in.Spec}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "report_view.created", "report_view", id,
			map[string]any{"brand_id": a.Brand, "name": *in.Name}, reportViewProps(v))
	})
	if s.allocError(w, r, err) {
		return
	}
	v, err := s.findReportView(ctx, id)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

// editableView finds a view the caller may change: their own, or a
// shared one.
func (s *Server) editableView(w http.ResponseWriter, r *http.Request) (reportViewView, bool) {
	v, err := s.findReportView(r.Context(), r.PathValue("id"))
	if errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "view not found")
		return v, false
	}
	if err != nil {
		s.internal(w, r, err)
		return v, false
	}
	return v, true
}

func (s *Server) updateReportView(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	v, ok := s.editableView(w, r)
	if !ok {
		return
	}
	var in reportViewInput
	if !decode(w, r, &in) || s.allocError(w, r, in.check(false)) {
		return
	}
	if in.Shared != nil && !*in.Shared && !v.Mine {
		writeError(w, r, http.StatusForbidden, "forbidden", "only the view's maker can make it private again")
		return
	}
	var spec []byte
	if in.Spec != nil {
		spec, _ = json.Marshal(in.Spec)
		v.Spec = *in.Spec
	}
	if in.Shared != nil {
		v.Shared = *in.Shared
	}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE report_views SET name = COALESCE($3, name), spec = COALESCE($4, spec), shared = COALESCE($5, shared), updated_at = now()
			WHERE workspace_id = $1 AND id = $2`, a.WorkspaceID, v.ID, in.Name, spec, in.Shared); err != nil {
			return err
		}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "report_view.updated", "report_view", v.ID,
			map[string]any{"brand_id": a.Brand, "name": in.Name != nil, "spec": in.Spec != nil, "shared": in.Shared}, reportViewProps(v))
	})
	if s.allocError(w, r, err) {
		return
	}
	out, err := s.findReportView(ctx, v.ID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) deleteReportView(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	v, ok := s.editableView(w, r)
	if !ok {
		return
	}
	if v.Shared && !v.Mine && !a.has(auth.ScopeWorkspaceManage) {
		writeError(w, r, http.StatusForbidden, "forbidden", "only the view's maker or an admin can delete a shared view")
		return
	}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM report_views WHERE workspace_id = $1 AND id = $2`, a.WorkspaceID, v.ID); err != nil {
			return err
		}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "report_view.deleted", "report_view", v.ID,
			map[string]any{"brand_id": a.Brand}, reportViewProps(v))
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// applyReportView fills the request's query from ?view=, keeping what the
// request says itself. It writes the error and returns false for an
// unknown view.
func (s *Server) applyReportView(w http.ResponseWriter, r *http.Request) bool {
	q := r.URL.Query()
	id := q.Get("view")
	if id == "" {
		return true
	}
	v, err := s.findReportView(r.Context(), id)
	if errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "view not found")
		return false
	}
	if err != nil {
		s.internal(w, r, err)
		return false
	}
	loc, ok := s.reportZone(w, r)
	if !ok {
		return false
	}
	vq, err := v.Spec.query(loc)
	if s.allocError(w, r, err) {
		return false
	}
	for k, x := range vq {
		if _, set := q[k]; !set {
			q[k] = x
		}
	}
	r.URL.RawQuery = q.Encode()
	return true
}
