package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/alerts"
	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/insights"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Alerts on a number (docs/insights.md): a saved trend is checked every
// hour, and when its number crosses a line (above or below a value, or up
// or down by a percentage against the period before) an alert is raised
// once, through the alerts module, so it reaches the app, the webhook,
// email and Slack like every other alert. It is raised again only after
// the number came back over the line.

// AlertInsightThreshold is the alert kind.
const AlertInsightThreshold = "insight.threshold"

const jobInsightAlertCheck = "insight_alerts.check"

// insightAlertEvery is how often each rule is checked.
const insightAlertEvery = time.Hour

var insightAlertConditions = []string{"above", "below", "rises_by", "falls_by"}

func init() {
	alerts.Register(alerts.Kind{ID: AlertInsightThreshold, Title: "A number crossed a line",
		Description: "A saved trend went above or below a value you set, or changed by more than a percentage against the period before. Once per crossing; it goes to whoever set it up.",
		Scope:       auth.ScopeAnalyticsRead, Describe: describeInsightThreshold})
}

func describeInsightThreshold(ctx context.Context, q jobs.Querier, a alerts.Row) (alerts.View, error) {
	var name, condition string
	var threshold float64
	var value *float64
	var insightID string
	err := q.QueryRow(ctx, `SELECT i.name, i.id::text, r.condition, r.threshold, r.last_value FROM insight_alerts r JOIN insights i ON i.id = r.insight_id
		WHERE r.id = $1 AND r.workspace_id = $2`, a.TargetID, a.WorkspaceID).Scan(&name, &insightID, &condition, &threshold, &value)
	if errors.Is(err, pgx.ErrNoRows) {
		return alerts.View{Title: "A number crossed a line", Text: "The alert has since been removed.", Path: "trends"}, nil
	}
	if err != nil {
		return alerts.View{}, err
	}
	v := alerts.View{Path: "trends/" + insightID, Title: name + " " + conditionText(condition, threshold)}
	if value != nil {
		v.Text = "It is now " + formatNumber(*value) + ". Open the trend to see what moved."
	}
	return v, nil
}

func conditionText(condition string, threshold float64) string {
	switch condition {
	case "above":
		return "is above " + formatNumber(threshold)
	case "below":
		return "is below " + formatNumber(threshold)
	case "rises_by":
		return "rose over " + formatNumber(threshold) + "% against the period before"
	default:
		return "fell over " + formatNumber(threshold) + "% against the period before"
	}
}

func formatNumber(v float64) string {
	if v == math.Trunc(v) && math.Abs(v) < 1e15 {
		return strconv.FormatInt(int64(v), 10)
	}
	return strconv.FormatFloat(v, 'f', 2, 64)
}

func (s *Server) routesInsightAlerts(mux *http.ServeMux) {
	read, manage := auth.ScopeAnalyticsRead, auth.ScopeAnalyticsManage
	mux.HandleFunc("GET /v1/brands/{brand}/insight-alerts", s.onBrand(read, s.listInsightAlerts))
	mux.HandleFunc("POST /v1/brands/{brand}/insight-alerts", s.onBrand(manage, s.createInsightAlert))
	mux.HandleFunc("PATCH /v1/brands/{brand}/insight-alerts/{alert}", s.onBrand(manage, s.updateInsightAlert))
	mux.HandleFunc("DELETE /v1/brands/{brand}/insight-alerts/{alert}", s.onBrand(manage, s.deleteInsightAlert))
}

func (s *Server) registerInsightAlertJobs(q *jobs.Queue) {
	q.Handle(jobInsightAlertCheck, s.insightAlertCheckJob)
}

type insightAlertView struct {
	ID            string     `json:"id"`
	BrandID       string     `json:"brand_id"`
	InsightID     string     `json:"insight_id"`
	InsightName   string     `json:"insight_name"`
	Condition     string     `json:"condition"`
	Threshold     float64    `json:"threshold"`
	State         string     `json:"state"`
	LastValue     *float64   `json:"last_value"`
	LastCheckedAt *time.Time `json:"last_checked_at"`
	FiredAt       *time.Time `json:"fired_at"`
	CreatedBy     *string    `json:"created_by"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

const insightAlertColumns = `r.id::text, r.brand_id::text, r.insight_id::text, i.name, r.condition, r.threshold, r.state, r.last_value,
	r.last_checked_at, r.fired_at, r.created_by::text, r.created_at, r.updated_at`

func scanInsightAlert(row pgx.Row) (insightAlertView, error) {
	var v insightAlertView
	err := row.Scan(&v.ID, &v.BrandID, &v.InsightID, &v.InsightName, &v.Condition, &v.Threshold, &v.State, &v.LastValue,
		&v.LastCheckedAt, &v.FiredAt, &v.CreatedBy, &v.CreatedAt, &v.UpdatedAt)
	return v, err
}

type insightAlertInput struct {
	InsightID *string  `json:"insight_id"`
	Condition *string  `json:"condition"`
	Threshold *float64 `json:"threshold"`
}

func checkCondition(condition string, threshold float64) error {
	if !slices.Contains(insightAlertConditions, condition) {
		return errInvalid("condition is above, below, rises_by or falls_by")
	}
	if math.IsNaN(threshold) || math.IsInf(threshold, 0) {
		return errInvalid("threshold is a number")
	}
	if (condition == "rises_by" || condition == "falls_by") && threshold <= 0 {
		return errInvalid("threshold is a percentage over 0 for rises_by and falls_by")
	}
	return nil
}

func (s *Server) listInsightAlerts(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	insight := r.URL.Query().Get("insight_id")
	if insight != "" && !validUUID(insight) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "insight_id is a saved insight's id")
		return
	}
	rows, err := s.pool.Query(r.Context(), `SELECT `+insightAlertColumns+` FROM insight_alerts r JOIN insights i ON i.id = r.insight_id
		WHERE r.workspace_id = $1 AND r.brand_id = $2 AND ($3 = '' OR r.insight_id = $3::uuid) ORDER BY r.created_at DESC LIMIT 500`, a.WorkspaceID, a.Brand, insight)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (insightAlertView, error) { return scanInsightAlert(row) })
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": nonNil(list)})
}

func (s *Server) createInsightAlert(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in insightAlertInput
	if !decode(w, r, &in) {
		return
	}
	if in.InsightID == nil || !validUUID(*in.InsightID) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "insight_id is a saved trend's id")
		return
	}
	if in.Condition == nil || in.Threshold == nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "condition and threshold are required")
		return
	}
	if err := checkCondition(*in.Condition, *in.Threshold); s.allocError(w, r, err) {
		return
	}
	var v insightAlertView
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		var kind string
		err := tx.QueryRow(r.Context(), `SELECT kind FROM insights WHERE id = $1 AND workspace_id = $2 AND brand_id = $3`, *in.InsightID, a.WorkspaceID, a.Brand).Scan(&kind)
		if errors.Is(err, pgx.ErrNoRows) {
			return errInvalid("insight_id is not a saved insight of this brand")
		}
		if err != nil {
			return err
		}
		if kind != "trend" {
			return errInvalid("alerts watch a saved trend's number; this insight is a " + kind)
		}
		var id string
		if err := tx.QueryRow(r.Context(), `INSERT INTO insight_alerts (workspace_id, brand_id, insight_id, condition, threshold, created_by)
			VALUES ($1, $2, $3, $4, $5, $6) RETURNING id::text`, a.WorkspaceID, a.Brand, *in.InsightID, *in.Condition, *in.Threshold, nullIfEmpty(a.ActorID)).Scan(&id); err != nil {
			return err
		}
		if err := recordWithProps(r.Context(), tx, &a.principal, a.WorkspaceID, "insight_alert.created", "insight_alert", id,
			map[string]any{"brand_id": a.Brand, "insight_id": *in.InsightID, "condition": *in.Condition}, map[string]any{"condition": *in.Condition}); err != nil {
			return err
		}
		v, err = scanInsightAlert(tx.QueryRow(r.Context(), `SELECT `+insightAlertColumns+` FROM insight_alerts r JOIN insights i ON i.id = r.insight_id WHERE r.id = $1`, id))
		return err
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

func (s *Server) updateInsightAlert(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in insightAlertInput
	if !decode(w, r, &in) {
		return
	}
	if in.InsightID != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "an alert's insight can't change; make a new alert")
		return
	}
	var v insightAlertView
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		cur, err := scanInsightAlert(tx.QueryRow(r.Context(), `SELECT `+insightAlertColumns+` FROM insight_alerts r JOIN insights i ON i.id = r.insight_id
			WHERE r.id = $1 AND r.workspace_id = $2 AND r.brand_id = $3 FOR UPDATE OF r`, validOrNil(r.PathValue("alert")), a.WorkspaceID, a.Brand))
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		condition, threshold := cur.Condition, cur.Threshold
		if in.Condition != nil {
			condition = *in.Condition
		}
		if in.Threshold != nil {
			threshold = *in.Threshold
		}
		if err := checkCondition(condition, threshold); err != nil {
			return err
		}
		// A changed line is checked afresh, from a clean state.
		if _, err := tx.Exec(r.Context(), `UPDATE insight_alerts SET condition = $2, threshold = $3, state = 'unknown', fired_at = NULL, next_check_at = now(), updated_at = now()
			WHERE id = $1`, cur.ID, condition, threshold); err != nil {
			return err
		}
		if err := recordWithProps(r.Context(), tx, &a.principal, a.WorkspaceID, "insight_alert.updated", "insight_alert", cur.ID,
			map[string]any{"brand_id": a.Brand, "insight_id": cur.InsightID, "condition": condition}, map[string]any{"condition": condition}); err != nil {
			return err
		}
		v, err = scanInsightAlert(tx.QueryRow(r.Context(), `SELECT `+insightAlertColumns+` FROM insight_alerts r JOIN insights i ON i.id = r.insight_id WHERE r.id = $1`, cur.ID))
		return err
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) deleteInsightAlert(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		var id, insight string
		err := tx.QueryRow(r.Context(), `DELETE FROM insight_alerts WHERE id = $1 AND workspace_id = $2 AND brand_id = $3 RETURNING id::text, insight_id::text`,
			validOrNil(r.PathValue("alert")), a.WorkspaceID, a.Brand).Scan(&id, &insight)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		return record(r.Context(), tx, &a.principal, a.WorkspaceID, "insight_alert.deleted", "insight_alert", id, map[string]any{"brand_id": a.Brand, "insight_id": insight})
	})
	if s.allocError(w, r, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// RunInsightAlerts queues the checks of the rules that are due, every
// interval, until ctx ends.
func (s *Server) RunInsightAlerts(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if _, err := s.QueueDueInsightAlertChecks(ctx); err != nil && ctx.Err() == nil {
			track.Fail(ctx, s.log, track.Event{Name: "insight_alert.scheduler_failed", ActorType: "system", Surface: "system"}, "internal", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// QueueDueInsightAlertChecks moves each due rule's next check on and
// queues it, in one transaction, and reports how many it queued.
func (s *Server) QueueDueInsightAlertChecks(ctx context.Context) (int, error) {
	n := 0
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			UPDATE insight_alerts r SET next_check_at = now() + $1::interval
			WHERE r.id IN (SELECT id FROM insight_alerts WHERE next_check_at <= now() ORDER BY next_check_at LIMIT 200 FOR UPDATE SKIP LOCKED)
			RETURNING r.workspace_id::text, r.id::text, r.next_check_at`, fmt.Sprintf("%d seconds", int(insightAlertEvery.Seconds())))
		if err != nil {
			return err
		}
		type due struct {
			ws, id string
			at     time.Time
		}
		var list []due
		var d due
		if _, err := pgx.ForEachRow(rows, []any{&d.ws, &d.id, &d.at}, func() error { list = append(list, d); return nil }); err != nil {
			return err
		}
		for _, d := range list {
			if _, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: jobInsightAlertCheck, WorkspaceID: &d.ws, Payload: map[string]string{"alert_id": d.id},
				IdempotencyKey: jobInsightAlertCheck + ":" + d.id + ":" + strconv.FormatInt(d.at.UnixMilli(), 10), MaxAttempts: 3}); err != nil {
				return err
			}
		}
		n = len(list)
		return nil
	})
	return n, err
}

// crossed reports whether the number is over the line.
func crossed(condition string, threshold float64, value float64, previous *float64) (bool, bool) {
	switch condition {
	case "above":
		return value > threshold, true
	case "below":
		return value < threshold, true
	}
	if previous == nil || *previous == 0 {
		return false, false
	}
	change := (value - *previous) / math.Abs(*previous) * 100
	if condition == "rises_by" {
		return change > threshold, true
	}
	return change < -threshold, true
}

// insightAlertCheckJob runs one rule's trend and raises the alert when
// its number has newly crossed the line.
func (s *Server) insightAlertCheckJob(ctx context.Context, job jobs.Job) error {
	var p map[string]string
	if err := json.Unmarshal(job.Payload, &p); err != nil || job.WorkspaceID == nil || !validUUID(p["alert_id"]) {
		return fmt.Errorf("%w: bad payload", jobs.ErrPermanent)
	}
	ws := *job.WorkspaceID
	var brand, insightID, condition, state string
	var threshold float64
	var spec []byte
	var owner *string
	// The alert is for the person who set the rule up (their user, found
	// from the actor), else the owners and admins.
	err := s.pool.QueryRow(ctx, `SELECT r.brand_id::text, r.insight_id::text, r.condition, r.threshold, r.state, i.spec,
			(SELECT u.id::text FROM users u WHERE u.actor_id = r.created_by)
		FROM insight_alerts r JOIN insights i ON i.id = r.insight_id WHERE r.id = $1 AND r.workspace_id = $2`, p["alert_id"], ws).
		Scan(&brand, &insightID, &condition, &threshold, &state, &spec, &owner)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	start := time.Now()
	var res any
	compare := condition == "rises_by" || condition == "falls_by"
	err = s.readInsight(ctx, func(tx pgx.Tx) error {
		var err error
		res, err = runSavedInsight(ctx, tx, ws, brand, "trend", spec, nil, compare, start)
		return err
	})
	if err != nil {
		outcome := "error"
		var inv insights.Invalid
		if errors.As(err, &inv) {
			outcome = "invalid"
		} else if isStatementTimeout(err) {
			outcome = "timeout"
		}
		metrics.InsightRun("alert_check", outcome, time.Since(start))
		if outcome != "error" {
			// A spec that no longer runs, or outgrew its time: the rule
			// waits for the next check instead of retrying now.
			_, uerr := s.pool.Exec(ctx, `UPDATE insight_alerts SET state = 'unknown', last_checked_at = now() WHERE id = $1`, p["alert_id"])
			return uerr
		}
		return err
	}
	metrics.InsightRun("alert_check", "success", time.Since(start))
	value, previous := trendNumber(res.(insights.TrendResult))
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if value == nil {
			_, err := tx.Exec(ctx, `UPDATE insight_alerts SET state = 'unknown', last_value = NULL, last_checked_at = now() WHERE id = $1`, p["alert_id"])
			return err
		}
		over, known := crossed(condition, threshold, *value, previous)
		next := "ok"
		switch {
		case !known:
			next = "unknown"
		case over:
			next = "firing"
		}
		var fired any
		if next == "firing" && state != "firing" {
			fired = time.Now().UTC()
			data := map[string]any{"insight_id": insightID, "condition": condition, "threshold": threshold, "value": *value}
			if previous != nil {
				data["previous"] = *previous
			}
			if _, err := alerts.Raise(ctx, tx, alerts.Alert{WorkspaceID: ws, BrandID: &brand, Kind: AlertInsightThreshold,
				DedupeKey: p["alert_id"] + ":" + strconv.FormatInt(start.UnixMilli(), 10), TargetType: "insight_alert", TargetID: p["alert_id"],
				OwnerID: owner, Data: data}); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `UPDATE insight_alerts SET state = $2, last_value = $3, last_checked_at = now(),
			fired_at = COALESCE($4, CASE WHEN $2 = 'firing' THEN fired_at END) WHERE id = $1`, p["alert_id"], next, *value, fired)
		return err
	})
}
