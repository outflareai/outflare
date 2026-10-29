package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/experiments"
)

// Experiments: did a change to a website's pages help? A holdout splits
// the pages into matched groups, the change goes only to the treatment
// group, and the control group shows what would have happened anyway. The
// measurement starts on the site's next production deploy or when a
// person says the change went live; outcomes come from the workspace's
// own Search Console data (package experiments).

func (s *Server) routesExperiments(mux *http.ServeMux) {
	read, manage := auth.ScopeSEORead, auth.ScopeSEOManage
	mux.HandleFunc("GET /v1/workspaces/{ws}/experiments", s.inBrand(read, s.listExperiments))
	mux.HandleFunc("POST /v1/workspaces/{ws}/experiments", s.inBrand(manage, s.createExperiment))
	mux.HandleFunc("GET /v1/workspaces/{ws}/experiments/{id}", s.inBrand(read, s.getExperiment))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/experiments/{id}", s.inBrand(manage, s.updateExperiment))
	mux.HandleFunc("DELETE /v1/workspaces/{ws}/experiments/{id}", s.inBrand(manage, s.deleteExperiment))
	mux.HandleFunc("GET /v1/workspaces/{ws}/experiments/{id}/pages", s.inBrand(read, s.listExperimentPages))
	mux.HandleFunc("POST /v1/workspaces/{ws}/experiments/{id}/start", s.inBrand(manage, s.startExperiment))
	mux.HandleFunc("POST /v1/workspaces/{ws}/experiments/{id}/stop", s.inBrand(manage, s.stopExperiment))
}

// experimentView adds the site's host.
type experimentView struct {
	experiments.Experiment
	SiteHost string `json:"site_host"`
}

// experimentFor loads the {id} experiment the caller may see, with the
// result so far while it measures, or writes a 404.
func (s *Server) experimentFor(w http.ResponseWriter, r *http.Request, a *access) (*experimentView, bool) {
	id := r.PathValue("id")
	if validUUID(id) {
		e, err := experiments.Get(r.Context(), s.pool, a.WorkspaceID, id)
		if err == nil && a.sees(e.BrandID) {
			v := &experimentView{Experiment: e}
			if err := s.pool.QueryRow(r.Context(), `SELECT host FROM seo_sites WHERE id = $1`, e.SiteID).Scan(&v.SiteHost); err != nil {
				s.internal(w, r, err)
				return nil, false
			}
			if e.Status == experiments.Measuring {
				if v.Result, err = experiments.Measure(r.Context(), s.pool, a.WorkspaceID, v.SiteHost, e); err != nil {
					s.internal(w, r, err)
					return nil, false
				}
			}
			return v, true
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			s.internal(w, r, err)
			return nil, false
		}
	}
	writeError(w, r, http.StatusNotFound, "not_found", "experiment not found")
	return nil, false
}

func (s *Server) listExperiments(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	sel, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	site, status := q.Get("site_id"), q.Get("status")
	if site != "" && !validUUID(site) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "site_id is an id")
		return
	}
	if status != "" && !slices.Contains([]string{experiments.Draft, experiments.Waiting, experiments.Measuring, experiments.Done, experiments.Stopped}, status) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status is draft, waiting, measuring, done or stopped")
		return
	}
	args := []any{a.WorkspaceID, site, status}
	rows, err := s.pool.Query(r.Context(), `SELECT `+experiments.Cols+`, si.host FROM experiments e JOIN seo_sites si ON si.id = e.site_id
		WHERE e.workspace_id = $1 AND ($2 = '' OR e.site_id = NULLIF($2, '')::uuid) AND ($3 = '' OR e.status = $3)`+sel.sql("e.brand_id", &args)+`
		ORDER BY e.created_at DESC LIMIT 100`, args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list := []experimentView{}
	for rows.Next() {
		var v experimentView
		var result []byte
		e := &v.Experiment
		if err := rows.Scan(&e.ID, &e.SiteID, &e.BrandID, &e.Name, &e.Hypothesis, &e.Design, &e.Pattern, &e.Metric, &e.HoldoutPercent,
			&e.BaselineDays, &e.MeasureDays, &e.StartOn, &e.Status, &e.ChangedAt, &e.DeployID, &e.ConcludedAt, &e.CreatedAt, &e.UpdatedAt,
			&e.TreatmentPages, &e.ControlPages, &result, &v.SiteHost); err != nil {
			rows.Close()
			s.internal(w, r, err)
			return
		}
		if result != nil {
			v.Result = &experiments.Result{}
			_ = json.Unmarshal(result, v.Result)
		}
		list = append(list, v)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

func (s *Server) createExperiment(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	var req struct {
		SiteID         string `json:"site_id"`
		Name           string `json:"name"`
		Hypothesis     string `json:"hypothesis"`
		Design         string `json:"design"`
		Pattern        string `json:"pattern"`
		Metric         string `json:"metric"`
		HoldoutPercent *int   `json:"holdout_percent"`
		BaselineDays   *int   `json:"baseline_days"`
		MeasureDays    *int   `json:"measure_days"`
		StartOn        string `json:"start_on"`
	}
	if !decode(w, r, &req) {
		return
	}
	bad := func(msg string) { writeError(w, r, http.StatusBadRequest, "invalid_request", msg) }
	e := experiments.Experiment{Name: strings.TrimSpace(req.Name), Hypothesis: strings.TrimSpace(req.Hypothesis), Design: req.Design,
		Pattern: strings.TrimSpace(req.Pattern), Metric: req.Metric, HoldoutPercent: 50, BaselineDays: 28, MeasureDays: 28, StartOn: req.StartOn}
	if e.Design == "" {
		e.Design = experiments.Holdout
	}
	if e.Metric == "" {
		e.Metric = "clicks"
	}
	if e.StartOn == "" {
		e.StartOn = "deploy"
	}
	for _, x := range []struct {
		p      *int
		dst    *int
		lo, hi int
		name   string
	}{{req.HoldoutPercent, &e.HoldoutPercent, 10, 90, "holdout_percent"}, {req.BaselineDays, &e.BaselineDays, 7, 90, "baseline_days"}, {req.MeasureDays, &e.MeasureDays, 7, 90, "measure_days"}} {
		if x.p != nil {
			if *x.p < x.lo || *x.p > x.hi {
				bad(x.name + " is between " + itoa(x.lo) + " and " + itoa(x.hi))
				return
			}
			*x.dst = *x.p
		}
	}
	switch {
	case e.Name == "" || len(e.Name) > 200:
		bad("name is 1 to 200 characters")
		return
	case len(e.Hypothesis) > 2000:
		bad("hypothesis is at most 2000 characters")
		return
	case e.Design != experiments.Holdout && e.Design != experiments.BeforeAfter:
		bad("design is holdout or before_after")
		return
	case !slices.Contains(experiments.Metrics, e.Metric):
		bad("metric is clicks, impressions, ctr or position")
		return
	case e.StartOn != "deploy" && e.StartOn != "manual":
		bad("start_on is deploy or manual")
		return
	}
	if _, err := experiments.Pattern(e.Pattern); err != nil {
		bad(err.Error())
		return
	}
	var host string
	var brand *string
	if !validUUID(req.SiteID) {
		bad("site_id is an SEO site's id; GET .../seo-sites lists them")
		return
	}
	err := s.pool.QueryRow(ctx, `SELECT host, brand_id::text FROM seo_sites WHERE id = $1 AND workspace_id = $2`, req.SiteID, a.WorkspaceID).Scan(&host, &brand)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !a.sees(brand)) {
		bad("site_id is an SEO site in this workspace; GET .../seo-sites lists them")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if !a.writes(brand) {
		writeError(w, r, http.StatusForbidden, "forbidden", string(errSharedRecord))
		return
	}
	e.SiteID, e.BrandID = req.SiteID, brand
	var id string
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO experiments (workspace_id, site_id, brand_id, name, hypothesis, design, pattern, metric,
			holdout_percent, baseline_days, measure_days, start_on, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13) RETURNING id::text`,
			a.WorkspaceID, e.SiteID, brand, e.Name, e.Hypothesis, e.Design, e.Pattern, e.Metric, e.HoldoutPercent, e.BaselineDays, e.MeasureDays,
			e.StartOn, a.ActorID).Scan(&id); err != nil {
			return err
		}
		pages, err := experiments.Assign(ctx, tx, a.WorkspaceID, host, e, id)
		if err != nil {
			return err
		}
		if e.Design == experiments.Holdout && (len(pages) < 2) {
			return errInvalid("a holdout needs at least 2 pages with search data; widen the pattern or use before_after")
		}
		rows := make([][]any, len(pages))
		for i, p := range pages {
			rows[i] = []any{id, a.WorkspaceID, p.URL, p.Group, p.Baseline}
		}
		if _, err := tx.CopyFrom(ctx, pgx.Identifier{"experiment_pages"}, []string{"experiment_id", "workspace_id", "url", "grp", "baseline"}, pgx.CopyFromRows(rows)); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "experiment.created", "experiment", id,
			map[string]any{"site_id": e.SiteID, "brand_id": brand, "design": e.Design, "metric": e.Metric, "pages": len(pages), "start_on": e.StartOn})
	})
	if errors.Is(err, experiments.ErrNoSearchData) {
		writeError(w, r, http.StatusConflict, "connector_needed",
			"no Search Console pages match on "+host+"; connect Google Search Console for the site (Connectors) and wait for its first sync, or widen the pattern")
		return
	}
	if s.allocError(w, r, err) {
		return
	}
	r.SetPathValue("id", id)
	if v, ok := s.experimentFor(w, r, a); ok {
		writeJSON(w, http.StatusCreated, v)
	}
}

func (s *Server) getExperiment(w http.ResponseWriter, r *http.Request) {
	if v, ok := s.experimentFor(w, r, accessFrom(r.Context())); ok {
		writeJSON(w, http.StatusOK, v)
	}
}

// writableExperiment loads the experiment for a change, or writes why not.
func (s *Server) writableExperiment(w http.ResponseWriter, r *http.Request, a *access) (*experimentView, bool) {
	v, ok := s.experimentFor(w, r, a)
	if !ok {
		return nil, false
	}
	if !a.writes(v.BrandID) {
		writeError(w, r, http.StatusForbidden, "forbidden", string(errSharedRecord))
		return nil, false
	}
	return v, true
}

func (s *Server) updateExperiment(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	var req struct {
		Name       *string `json:"name"`
		Hypothesis *string `json:"hypothesis"`
	}
	if !decode(w, r, &req) {
		return
	}
	v, ok := s.writableExperiment(w, r, a)
	if !ok {
		return
	}
	name, hyp := v.Name, v.Hypothesis
	if req.Name != nil {
		name = strings.TrimSpace(*req.Name)
	}
	if req.Hypothesis != nil {
		hyp = strings.TrimSpace(*req.Hypothesis)
	}
	if name == "" || len(name) > 200 || len(hyp) > 2000 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "name is 1 to 200 characters and hypothesis at most 2000")
		return
	}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE experiments SET name = $3, hypothesis = $4, updated_at = now() WHERE id = $1 AND workspace_id = $2`,
			v.ID, a.WorkspaceID, name, hyp); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "experiment.updated", "experiment", v.ID, map[string]any{"brand_id": v.BrandID})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	s.getExperiment(w, r)
}

func (s *Server) deleteExperiment(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	v, ok := s.writableExperiment(w, r, a)
	if !ok {
		return
	}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM experiments WHERE id = $1 AND workspace_id = $2`, v.ID, a.WorkspaceID); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "experiment.deleted", "experiment", v.ID,
			map[string]any{"brand_id": v.BrandID, "status": v.Status})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listExperimentPages(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	v, ok := s.experimentFor(w, r, a)
	if !ok {
		return
	}
	group := r.URL.Query().Get("group")
	if group != "" && group != experiments.Treatment && group != experiments.Control {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "group is treatment or control")
		return
	}
	rows, err := s.pool.Query(r.Context(), `SELECT url, grp, baseline FROM experiment_pages WHERE experiment_id = $1 AND ($2 = '' OR grp = $2)
		ORDER BY grp DESC, baseline DESC, url`, v.ID, group)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	type page struct {
		URL      string  `json:"url"`
		Group    string  `json:"group"`
		Baseline float64 `json:"baseline"`
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (page, error) {
		var p page
		return p, row.Scan(&p.URL, &p.Group, &p.Baseline)
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if list == nil {
		list = []page{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

// startExperiment starts the measurement: from when the change went live
// (changed_at, by default now), or, for an experiment that starts on a
// deploy and gets no changed_at, from the site's next production deploy.
func (s *Server) startExperiment(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	var req struct {
		ChangedAt *time.Time `json:"changed_at"`
	}
	if r.ContentLength != 0 && !decode(w, r, &req) {
		return
	}
	v, ok := s.writableExperiment(w, r, a)
	if !ok {
		return
	}
	if v.Status != experiments.Draft && v.Status != experiments.Waiting {
		writeError(w, r, http.StatusConflict, "conflict", "the experiment is "+v.Status+"; only a draft or one waiting for a deploy starts")
		return
	}
	now := time.Now()
	if req.ChangedAt != nil && (req.ChangedAt.After(now.Add(time.Hour)) || req.ChangedAt.Before(now.AddDate(0, 0, -90))) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "changed_at is when the change went live, in the last 90 days")
		return
	}
	status, changed := experiments.Measuring, req.ChangedAt
	if changed == nil {
		if v.StartOn == "deploy" {
			status = experiments.Waiting
		} else {
			changed = &now
		}
	}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE experiments SET status = $3, changed_at = $4, updated_at = now() WHERE id = $1 AND workspace_id = $2`,
			v.ID, a.WorkspaceID, status, changed); err != nil {
			return err
		}
		if status == experiments.Waiting {
			return record(ctx, tx, &a.principal, a.WorkspaceID, "experiment.waiting", "experiment", v.ID,
				map[string]any{"site_id": v.SiteID, "brand_id": v.BrandID})
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "experiment.started", "experiment", v.ID,
			map[string]any{"site_id": v.SiteID, "brand_id": v.BrandID, "by": "person", "changed_at": changed.UTC().Format(time.RFC3339)})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	s.getExperiment(w, r)
}

// stopExperiment ends an experiment: one that is measuring keeps its
// result so far and is done; any other is stopped.
func (s *Server) stopExperiment(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	v, ok := s.writableExperiment(w, r, a)
	if !ok {
		return
	}
	if v.Status == experiments.Done || v.Status == experiments.Stopped {
		writeError(w, r, http.StatusConflict, "conflict", "the experiment has ended already")
		return
	}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if v.Status == experiments.Measuring && v.Result != nil {
			ok, err := experiments.Conclude(ctx, tx, a.WorkspaceID, v.Experiment, v.Result)
			if err != nil || !ok {
				return err
			}
			data := experiments.ConcludedData(v.Experiment, v.Result)
			data["early"] = true
			return record(ctx, tx, &a.principal, a.WorkspaceID, "experiment.concluded", "experiment", v.ID, data)
		}
		if _, err := tx.Exec(ctx, `UPDATE experiments SET status = 'stopped', updated_at = now() WHERE id = $1 AND workspace_id = $2`, v.ID, a.WorkspaceID); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "experiment.stopped", "experiment", v.ID, map[string]any{"brand_id": v.BrandID, "status": v.Status})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	s.getExperiment(w, r)
}
