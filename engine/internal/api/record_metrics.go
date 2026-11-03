package api

import (
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/recmetrics"
)

// Metrics (docs/metrics.md): live totals per company and person, such as
// spend in the last 30 days. A brand turns them on, which adds the default
// metrics and fills them quietly from stored events; internal/recmetrics
// keeps them live.

func (s *Server) routesRecordMetrics(mux *http.ServeMux) {
	read, catalog := auth.ScopeCustomersRead, auth.ScopeCatalogManage
	base := "/v1/brands/{brand}/metrics"
	mux.HandleFunc("GET "+base, s.onBrand(read, s.listMetrics))
	mux.HandleFunc("PATCH "+base, s.onBrand(catalog, s.updateMetricSettings))
	mux.HandleFunc("POST "+base, s.onBrand(catalog, s.createMetric))
	mux.HandleFunc("PATCH "+base+"/{metric}", s.onBrand(catalog, s.updateMetric))
	mux.HandleFunc("POST "+base+"/{metric}/archive", s.onBrand(catalog, s.archiveMetric))
	mux.HandleFunc("POST "+base+"/{metric}/backfill", s.onBrand(catalog, s.backfillMetric))
	mux.HandleFunc("GET /v1/brands/{brand}/records/{record}/metrics", s.onBrand(read, s.recordMetrics))
}

type metricView struct {
	recmetrics.Definition
	Status       string     `json:"status"`
	Backfilling  bool       `json:"backfilling"`
	BackfilledAt *time.Time `json:"backfilled_at"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

const metricColumns = recmetrics.Columns + `, status, backfilling, backfilled_at, created_at, updated_at`

func scanMetric(row pgx.CollectableRow) (metricView, error) {
	var v metricView
	d := &v.Definition
	err := row.Scan(&d.ID, &d.Key, &d.Name, &d.Description, &d.Levels, &d.Aggregate, &d.Events, &d.Usage, &d.Property, &d.WindowDays, &d.OffsetDays,
		&d.PerProduct, &d.Version, &v.Status, &v.Backfilling, &v.BackfilledAt, &v.CreatedAt, &v.UpdatedAt)
	return v, err
}

func findMetric(r *http.Request, tx pgx.Tx, a *access, id string) (metricView, error) {
	if !validUUID(id) {
		return metricView{}, errNotFound
	}
	rows, err := tx.Query(r.Context(), `SELECT `+metricColumns+` FROM metric_definitions WHERE id = $1 AND workspace_id = $2 AND brand_id = $3`,
		id, a.WorkspaceID, a.Brand)
	if err != nil {
		return metricView{}, err
	}
	v, err := pgx.CollectExactlyOneRow(rows, scanMetric)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, errNotFound
	}
	return v, err
}

// metricsOn reports whether the brand has metrics on.
func metricsOn(r *http.Request, tx pgx.Tx, a *access) (*time.Time, error) {
	var at *time.Time
	err := tx.QueryRow(r.Context(), `SELECT enabled_at FROM metric_settings WHERE brand_id = $1 AND workspace_id = $2`, a.Brand, a.WorkspaceID).Scan(&at)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return at, err
}

func readMetrics(r *http.Request, tx pgx.Tx, a *access, archived bool) (map[string]any, error) {
	at, err := metricsOn(r, tx, a)
	if err != nil {
		return nil, err
	}
	status := "active"
	if archived {
		status = "archived"
	}
	rows, err := tx.Query(r.Context(), `SELECT `+metricColumns+` FROM metric_definitions WHERE workspace_id = $1 AND brand_id = $2 AND status = $3
		ORDER BY key, created_at`, a.WorkspaceID, a.Brand, status)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, scanMetric)
	if err != nil {
		return nil, err
	}
	return map[string]any{"enabled": at != nil, "enabled_at": at, "data": nonNil(list)}, nil
}

func (s *Server) listMetrics(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var out map[string]any
	err := s.readTx(r.Context(), func(tx pgx.Tx) (err error) {
		out, err = readMetrics(r, tx, a, r.URL.Query().Get("archived") == "true")
		return err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// updateMetricSettings turns metrics on or off. Turning them on adds the
// default metrics to a brand with none and fills every metric quietly from
// stored events: nothing fires for the past.
func (s *Server) updateMetricSettings(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var f struct {
		Enabled *bool `json:"enabled"`
	}
	if !decode(w, r, &f) {
		return
	}
	if f.Enabled == nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "enabled is required")
		return
	}
	ctx := r.Context()
	var out map[string]any
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		was, err := metricsOn(r, tx, a)
		if err != nil {
			return err
		}
		if *f.Enabled == (was != nil) {
			out, err = readMetrics(r, tx, a, false)
			return err
		}
		action := "metrics.disabled"
		if *f.Enabled {
			action = "metrics.enabled"
			if _, err := recmetrics.Seed(ctx, tx, a.WorkspaceID, a.Brand); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO metric_settings (brand_id, workspace_id, enabled_at) VALUES ($1, $2, now())
				ON CONFLICT (brand_id) DO UPDATE SET enabled_at = now(), updated_at = now()`, a.Brand, a.WorkspaceID); err != nil {
				return err
			}
			// Fill every metric from stored events, quietly.
			rows, err := tx.Query(ctx, `SELECT id::text, version FROM metric_definitions WHERE brand_id = $1 AND status = 'active'`, a.Brand)
			if err != nil {
				return err
			}
			type ref struct {
				id string
				v  int
			}
			refs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (ref, error) {
				var x ref
				return x, row.Scan(&x.id, &x.v)
			})
			if err != nil {
				return err
			}
			for _, x := range refs {
				if _, err := tx.Exec(ctx, `UPDATE metric_definitions SET version = version + 1, backfilling = true WHERE id = $1`, x.id); err != nil {
					return err
				}
				if err := recmetrics.EnqueueBackfill(ctx, tx, a.WorkspaceID, x.id, x.v+1); err != nil {
					return err
				}
			}
		} else if _, err := tx.Exec(ctx, `UPDATE metric_settings SET enabled_at = NULL, updated_at = now() WHERE brand_id = $1`, a.Brand); err != nil {
			return err
		}
		if out, err = readMetrics(r, tx, a, false); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, action, "brand", a.Brand, map[string]any{"metrics": len(out["data"].([]metricView))})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

type metricFields struct {
	Key         *string   `json:"key"`
	Name        *string   `json:"name"`
	Description *string   `json:"description"`
	Levels      *[]string `json:"levels"`
	Aggregate   *string   `json:"aggregate"`
	Events      *[]string `json:"events"`
	Usage       *bool     `json:"usage"`
	Property    *string   `json:"property"`
	WindowDays  *int      `json:"window_days"`
	OffsetDays  *int      `json:"offset_days"`
	PerProduct  *bool     `json:"per_product"`
}

var metricAggregates = []string{recmetrics.Count, recmetrics.Sum, recmetrics.Min, recmetrics.Max, recmetrics.Days, recmetrics.Last}

// apply sets f on d and reports whether what it counts changed.
func (f metricFields) apply(d *recmetrics.Definition) (bool, error) {
	changed := false
	if f.Name != nil {
		n := strings.TrimSpace(*f.Name)
		if n == "" || len(n) > 100 {
			return false, errInvalid("name is 1 to 100 characters")
		}
		d.Name = n
	}
	if f.Description != nil {
		if len(*f.Description) > 500 {
			return false, errInvalid("description is at most 500 characters")
		}
		d.Description = *f.Description
	}
	set := func(cond bool, apply func()) {
		if cond {
			apply()
			changed = true
		}
	}
	set(f.Levels != nil, func() { d.Levels = *f.Levels })
	set(f.Aggregate != nil, func() { d.Aggregate = *f.Aggregate })
	set(f.Events != nil, func() { d.Events = *f.Events })
	set(f.Usage != nil, func() { d.Usage = *f.Usage })
	set(f.Property != nil, func() { d.Property = strings.TrimSpace(*f.Property) })
	set(f.WindowDays != nil, func() { d.WindowDays = *f.WindowDays })
	set(f.OffsetDays != nil, func() { d.OffsetDays = *f.OffsetDays })
	set(f.PerProduct != nil, func() { d.PerProduct = *f.PerProduct })
	switch {
	case !recmetrics.KeyRE.MatchString(d.Key) || d.Key == "by_product":
		return false, errInvalid("key is lowercase letters, digits and underscores, such as spend_30d (not by_product)")
	case len(d.Levels) == 0 || len(d.Levels) > 2:
		return false, errInvalid("levels are company, person or both")
	case !slices.Contains(metricAggregates, d.Aggregate):
		return false, errInvalid("aggregate is count, sum, min, max, days or last")
	case len(d.Events) == 0 && !d.Usage:
		return false, errInvalid("a metric counts events (their names) or usage, or both")
	case len(d.Events) > 20:
		return false, errInvalid("a metric names at most 20 events")
	case (d.Aggregate == recmetrics.Sum || d.Aggregate == recmetrics.Min || d.Aggregate == recmetrics.Max) && !fieldNameRE.MatchString(d.Property):
		return false, errInvalid("sum, min and max read a number property of the events, such as amount_minor")
	case d.Aggregate != recmetrics.Sum && d.Aggregate != recmetrics.Min && d.Aggregate != recmetrics.Max && d.Property != "":
		return false, errInvalid("only sum, min and max read a property")
	case d.WindowDays < 1 || d.WindowDays > 365:
		return false, errInvalid("window_days is 1 to 365")
	case d.OffsetDays < 0 || d.OffsetDays > 365:
		return false, errInvalid("offset_days is 0 to 365")
	}
	for _, l := range d.Levels {
		if l != recmetrics.LevelCompany && l != recmetrics.LevelPerson {
			return false, errInvalid("levels are company, person or both")
		}
	}
	for _, e := range d.Events {
		if !validEventName(e) {
			return false, errInvalid("events are event names, such as payment.succeeded")
		}
	}
	return changed, nil
}

var fieldNameRE = recmetrics.KeyRE

func (s *Server) createMetric(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var f metricFields
	if !decode(w, r, &f) {
		return
	}
	if f.Key == nil || f.Name == nil || f.Aggregate == nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "key, name and aggregate are required")
		return
	}
	d := recmetrics.Definition{Key: *f.Key, Levels: []string{recmetrics.LevelCompany, recmetrics.LevelPerson}, Events: []string{},
		WindowDays: 30, PerProduct: true}
	if _, err := f.apply(&d); s.allocError(w, r, err) {
		return
	}
	ctx := r.Context()
	var v metricView
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		id, err := recmetrics.Insert(ctx, tx, a.WorkspaceID, a.Brand, d)
		if err != nil {
			return err
		}
		if on, err := metricsOn(r, tx, a); err != nil {
			return err
		} else if on != nil {
			if _, err := tx.Exec(ctx, `UPDATE metric_definitions SET backfilling = true WHERE id = $1`, id); err != nil {
				return err
			}
			if err := recmetrics.EnqueueBackfill(ctx, tx, a.WorkspaceID, id, 1); err != nil {
				return err
			}
		}
		if v, err = findMetric(r, tx, a, id); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "metric.created", "brand", a.Brand, metricData(v))
	})
	if uniqueViolation(err) {
		writeError(w, r, http.StatusConflict, "conflict", "the brand already has a metric with this key")
		return
	}
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

func metricData(v metricView) map[string]any {
	return map[string]any{"metric_id": v.ID, "aggregate": v.Aggregate, "window_days": v.WindowDays, "offset_days": v.OffsetDays,
		"per_product": v.PerProduct, "events": len(v.Events), "usage": v.Usage, "version": v.Version}
}

// updateMetric changes a metric. A change to what it counts makes a new
// version, refilled quietly from stored events: nothing fires for the past.
func (s *Server) updateMetric(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var f metricFields
	if !decode(w, r, &f) {
		return
	}
	if f.Key != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "a metric's key never changes, so rules and lists keep working; make a new metric instead")
		return
	}
	ctx := r.Context()
	var v metricView
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := findMetric(r, tx, a, r.PathValue("metric"))
		if err != nil {
			return err
		}
		if cur.Status != "active" {
			return errInvalid("this metric is archived")
		}
		d := cur.Definition
		changed, err := f.apply(&d)
		if err != nil {
			return err
		}
		version := cur.Version
		if changed {
			version++
		}
		if _, err := tx.Exec(ctx, `UPDATE metric_definitions SET name = $2, description = $3, levels = $4, aggregate = $5, events = $6, usage = $7,
				property = NULLIF($8, ''), window_days = $9, offset_days = $10, per_product = $11, version = $12, backfilling = backfilling OR $13,
				updated_at = now()
			WHERE id = $1`, cur.ID, d.Name, d.Description, d.Levels, d.Aggregate, d.Events, d.Usage, d.Property, d.WindowDays, d.OffsetDays,
			d.PerProduct, version, changed); err != nil {
			return err
		}
		if changed {
			if err := recmetrics.EnqueueBackfill(ctx, tx, a.WorkspaceID, cur.ID, version); err != nil {
				return err
			}
		}
		if v, err = findMetric(r, tx, a, cur.ID); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "metric.updated", "brand", a.Brand, metricData(v))
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// archiveMetric takes a metric off every record, quietly.
func (s *Server) archiveMetric(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	var v metricView
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := findMetric(r, tx, a, r.PathValue("metric"))
		if err != nil {
			return err
		}
		if cur.Status == "active" {
			if _, err := tx.Exec(ctx, `UPDATE metric_definitions SET status = 'archived', updated_at = now() WHERE id = $1`, cur.ID); err != nil {
				return err
			}
			if err := recmetrics.Unpublish(ctx, tx, a.WorkspaceID, cur.ID); err != nil {
				return err
			}
		}
		if v, err = findMetric(r, tx, a, cur.ID); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "metric.archived", "brand", a.Brand, metricData(v))
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// backfillMetric refills a metric from stored events, quietly.
func (s *Server) backfillMetric(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	var v metricView
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := findMetric(r, tx, a, r.PathValue("metric"))
		if err != nil {
			return err
		}
		if cur.Status != "active" {
			return errInvalid("this metric is archived")
		}
		if on, err := metricsOn(r, tx, a); err != nil {
			return err
		} else if on == nil {
			return errInvalid("metrics are off for this brand: turn them on with PATCH .../metrics")
		}
		if _, err := tx.Exec(ctx, `UPDATE metric_definitions SET version = version + 1, backfilling = true, updated_at = now() WHERE id = $1`, cur.ID); err != nil {
			return err
		}
		if err := recmetrics.EnqueueBackfill(ctx, tx, a.WorkspaceID, cur.ID, cur.Version+1); err != nil {
			return err
		}
		if v, err = findMetric(r, tx, a, cur.ID); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "metric.backfill_started", "brand", a.Brand, metricData(v))
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusAccepted, v)
}

type recordMetricView struct {
	MetricID   string    `json:"metric_id"`
	Key        string    `json:"key"`
	ProductID  *string   `json:"product_id"`
	ProductKey *string   `json:"product_key"`
	Value      any       `json:"value"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// recordMetrics is a company's or person's metrics, overall and per
// product.
func (s *Server) recordMetrics(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	var list []recordMetricView
	err := s.readTx(ctx, func(tx pgx.Tx) error {
		rec, err := loadRecord(ctx, tx, a, r.PathValue("record"))
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT v.metric_id::text, d.key, NULLIF(v.product_id::text, $3), p.key, v.value, v.updated_at
			FROM metric_values v JOIN metric_definitions d ON d.id = v.metric_id AND d.status = 'active'
			LEFT JOIN products p ON p.id = v.product_id
			WHERE v.record_id = $1 AND v.workspace_id = $2 ORDER BY d.key, p.key NULLS FIRST`, rec.ID, a.WorkspaceID, recmetrics.NoProduct)
		if err != nil {
			return err
		}
		list, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (recordMetricView, error) {
			var v recordMetricView
			return v, row.Scan(&v.MetricID, &v.Key, &v.ProductID, &v.ProductKey, &v.Value, &v.UpdatedAt)
		})
		return err
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": nonNil(list)})
}
