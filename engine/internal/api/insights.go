package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/adtrack"
	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/insights"
	"github.com/outflareai/outflare/engine/internal/metrics"
)

// Insights (docs/insights.md): product analytics over everything a brand
// records, starting with people funnels. A funnel's steps can come from
// the website tracker (visits and page views), the brand's own product
// events, saved conversions and payments; ad networks' clicks sit above
// it. Running one is a read; saving one keeps its spec so the same funnel
// opens again, on a dashboard, in the agent or through MCP.

func (s *Server) routesInsights(mux *http.ServeMux) {
	read, manage := auth.ScopeAnalyticsRead, auth.ScopeAnalyticsManage
	mux.HandleFunc("GET /v1/brands/{brand}/insights/catalog", s.onBrand(read, s.insightsCatalog))
	mux.HandleFunc("POST /v1/brands/{brand}/insights/funnel", s.onBrand(read, s.runFunnel))
	// The people behind a step name customers, so they need customers.read.
	mux.HandleFunc("POST /v1/brands/{brand}/insights/funnel/people", s.onBrand(auth.ScopeCustomersRead, s.funnelPeople))
	mux.HandleFunc("POST /v1/brands/{brand}/insights/funnel/people/save", s.onBrand(auth.ScopeCustomersManage, s.saveFunnelPeople))
	mux.HandleFunc("POST /v1/brands/{brand}/insights/funnel/correlation", s.onBrand(read, s.funnelCorrelation))
	mux.HandleFunc("POST /v1/brands/{brand}/insights/paths", s.onBrand(read, s.runPaths))
	mux.HandleFunc("GET /v1/brands/{brand}/annotations", s.onBrand(read, s.listAnnotations))
	mux.HandleFunc("POST /v1/brands/{brand}/annotations", s.onBrand(manage, s.createAnnotation))
	mux.HandleFunc("DELETE /v1/brands/{brand}/annotations/{annotation}", s.onBrand(manage, s.deleteAnnotation))
	mux.HandleFunc("POST /v1/brands/{brand}/insights/trend", s.onBrand(read, s.runTrend))
	mux.HandleFunc("POST /v1/brands/{brand}/insights/retention", s.onBrand(read, s.runRetention))
	mux.HandleFunc("POST /v1/brands/{brand}/insights/lifecycle", s.onBrand(read, s.runLifecycle))
	mux.HandleFunc("GET /v1/brands/{brand}/insights", s.onBrand(read, s.listInsights))
	mux.HandleFunc("POST /v1/brands/{brand}/insights", s.onBrand(manage, s.createInsight))
	mux.HandleFunc("GET /v1/brands/{brand}/insights/{insight}", s.onBrand(read, s.getInsight))
	mux.HandleFunc("PATCH /v1/brands/{brand}/insights/{insight}", s.onBrand(manage, s.updateInsight))
	mux.HandleFunc("DELETE /v1/brands/{brand}/insights/{insight}", s.onBrand(manage, s.deleteInsight))
}

// insightTimeout is how long one insight query may run.
const insightTimeout = 30 * time.Second

// dateRange is when an insight looks: a preset that moves with today, or
// fixed dates (to included).
type dateRange struct {
	Preset string `json:"preset,omitempty"`
	From   string `json:"from,omitempty"`
	To     string `json:"to,omitempty"`
}

var datePresets = []string{"today", "yesterday", "last_7_days", "last_14_days", "last_30_days", "last_90_days", "last_180_days",
	"last_365_days", "this_month", "last_month", "this_year"}

// resolve turns the range into [from, to), as of now, in now's time zone:
// callers pass now in the brand's reporting time zone (B4a), so "today" and
// a date are the brand's days.
func (d dateRange) resolve(now time.Time) (time.Time, time.Time, error) {
	loc := now.Location()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	tomorrow := today.AddDate(0, 0, 1)
	if d.Preset != "" {
		if d.From != "" || d.To != "" {
			return time.Time{}, time.Time{}, insights.Invalid("date_range is a preset or from and to, not both")
		}
		if n, ok := strings.CutPrefix(d.Preset, "last_"); ok {
			if days, err := strconv.Atoi(strings.TrimSuffix(n, "_days")); err == nil && slices.Contains(datePresets, d.Preset) {
				return today.AddDate(0, 0, -(days - 1)), tomorrow, nil
			}
		}
		switch d.Preset {
		case "today":
			return today, tomorrow, nil
		case "yesterday":
			return today.AddDate(0, 0, -1), today, nil
		case "this_month":
			return time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, loc), tomorrow, nil
		case "last_month":
			first := time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, loc)
			return first.AddDate(0, -1, 0), first, nil
		case "this_year":
			return time.Date(today.Year(), 1, 1, 0, 0, 0, 0, loc), tomorrow, nil
		}
		return time.Time{}, time.Time{}, insights.Invalid("date_range.preset is one of " + strings.Join(datePresets, ", "))
	}
	if d.From == "" && d.To == "" {
		return today.AddDate(0, 0, -29), tomorrow, nil
	}
	from, err1 := time.Parse(time.DateOnly, d.From)
	to, err2 := time.Parse(time.DateOnly, d.To)
	if err1 != nil || err2 != nil || to.Before(from) {
		return time.Time{}, time.Time{}, insights.Invalid("date_range.from and date_range.to are dates (2026-10-01), from on or before to")
	}
	return dayStart(from, loc), dayStart(to.AddDate(0, 0, 1), loc), nil
}

// funnelSpec is a funnel as the API, saved insights and MCP send it.
type funnelSpec struct {
	Steps      []insights.Step      `json:"steps"`
	Exclusions []insights.Exclusion `json:"exclusions,omitempty"`
	By         string               `json:"by,omitempty"`
	Order      string               `json:"order,omitempty"`
	DateRange  dateRange            `json:"date_range"`
	Window     *insights.Window     `json:"window,omitempty"`
	Breakdown  string               `json:"breakdown,omitempty"`
	Interval   string               `json:"interval,omitempty"`
	AdClicks   *insights.AdClicks   `json:"ad_clicks,omitempty"`
}

func (f funnelSpec) funnel(now time.Time) (insights.Funnel, error) {
	from, to, err := f.DateRange.resolve(now)
	if err != nil {
		return insights.Funnel{}, err
	}
	out := insights.Funnel{Steps: f.Steps, Exclusions: f.Exclusions, By: f.By, Order: f.Order, From: from, To: to, Window: f.Window, Breakdown: f.Breakdown, Interval: f.Interval, AdClicks: f.AdClicks, Location: now.Location()}
	return out, out.Validate()
}

// trendSpec is a trend as the API, saved insights and MCP send it.
type trendSpec struct {
	Series    []insights.Series `json:"series"`
	DateRange dateRange         `json:"date_range"`
	Interval  string            `json:"interval,omitempty"`
	Breakdown string            `json:"breakdown,omitempty"`
	Formula   string            `json:"formula,omitempty"`
	Compare   bool              `json:"compare,omitempty"`
	// Display is how the app draws it (line, bar, number, table); the
	// engine ignores it.
	Display string `json:"display,omitempty"`
}

func (t trendSpec) trend(now time.Time) (insights.Trend, error) {
	from, to, err := t.DateRange.resolve(now)
	if err != nil {
		return insights.Trend{}, err
	}
	interval := t.Interval
	if interval == "" {
		// A day for up to two months, else a week, else a month.
		switch span := to.Sub(from); {
		case span <= 62*24*time.Hour:
			interval = "day"
		case span <= 200*24*time.Hour:
			interval = "week"
		default:
			interval = "month"
		}
	}
	if t.Display != "" && !slices.Contains([]string{"line", "bar", "number", "table"}, t.Display) {
		return insights.Trend{}, insights.Invalid("display is line, bar, number or table")
	}
	out := insights.Trend{Series: t.Series, From: from, To: to, Interval: interval, Breakdown: t.Breakdown, Formula: t.Formula, Compare: t.Compare, Location: now.Location()}
	return out, out.Validate()
}

// runTrend answers a trend: series over time, with a formula and the
// period before when asked.
func (s *Server) runTrend(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in trendSpec
	if !decode(w, r, &in) {
		return
	}
	start := time.Now()
	// Days are the brand's (B4a), or ?timezone='s.
	loc, ok := s.reportZone(w, r)
	if !ok {
		return
	}
	t, err := in.trend(start.In(loc))
	var res insights.TrendResult
	if err == nil {
		err = s.readInsight(r.Context(), func(tx pgx.Tx) error {
			c, err := insights.CompileTrend(r.Context(), tx, a.WorkspaceID, a.Brand, t)
			if err != nil {
				return err
			}
			res, err = c.Run(r.Context(), tx)
			return err
		})
	}
	if err != nil {
		metrics.InsightRun("trend", s.insightError(w, r, err), time.Since(start))
		return
	}
	metrics.InsightRun("trend", "success", time.Since(start))
	writeJSON(w, http.StatusOK, res)
}

// retentionSpec is a retention question as the API, saved insights and
// MCP send it.
type retentionSpec struct {
	Start     insights.Step  `json:"start"`
	Return    *insights.Step `json:"return,omitempty"`
	DateRange dateRange      `json:"date_range"`
	Interval  string         `json:"interval,omitempty"`
	Periods   int            `json:"periods,omitempty"`
}

func (t retentionSpec) retention(now time.Time) (insights.Retention, error) {
	from, to, err := t.DateRange.resolve(now)
	if err != nil {
		return insights.Retention{}, err
	}
	interval := t.Interval
	if interval == "" {
		interval = "week"
	}
	periods := t.Periods
	if periods == 0 {
		periods = map[string]int{"day": 14, "week": 8, "month": 6}[interval]
	}
	out := insights.Retention{Start: t.Start, Return: t.Return, From: from, To: to, Interval: interval, Periods: periods, Location: now.Location()}
	return out, out.Validate()
}

// runRetention answers a retention question: cohorts by when people
// started, and how many came back in each period after.
func (s *Server) runRetention(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in retentionSpec
	if !decode(w, r, &in) {
		return
	}
	start := time.Now()
	// Days are the brand's (B4a), or ?timezone='s.
	loc, ok := s.reportZone(w, r)
	if !ok {
		return
	}
	q, err := in.retention(start.In(loc))
	var res insights.RetentionResult
	if err == nil {
		err = s.readInsight(r.Context(), func(tx pgx.Tx) error {
			c, err := insights.CompileRetention(r.Context(), tx, a.WorkspaceID, a.Brand, q)
			if err != nil {
				return err
			}
			res, err = c.Run(r.Context(), tx)
			return err
		})
	}
	if err != nil {
		metrics.InsightRun("retention", s.insightError(w, r, err), time.Since(start))
		return
	}
	metrics.InsightRun("retention", "success", time.Since(start))
	writeJSON(w, http.StatusOK, res)
}

// lifecycleSpec is a lifecycle question as the API, saved insights and
// MCP send it.
type lifecycleSpec struct {
	Step      insights.Step `json:"step"`
	DateRange dateRange     `json:"date_range"`
	Interval  string        `json:"interval,omitempty"`
}

func (t lifecycleSpec) lifecycle(now time.Time) (insights.Lifecycle, error) {
	from, to, err := t.DateRange.resolve(now)
	if err != nil {
		return insights.Lifecycle{}, err
	}
	interval := t.Interval
	if interval == "" {
		interval = "week"
	}
	out := insights.Lifecycle{Step: t.Step, From: from, To: to, Interval: interval, Location: now.Location()}
	return out, out.Validate()
}

// runLifecycle answers a lifecycle question: in each period, how many
// active people were new, returning or resurrected, and how many went
// dormant.
func (s *Server) runLifecycle(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in lifecycleSpec
	if !decode(w, r, &in) {
		return
	}
	start := time.Now()
	// Days are the brand's (B4a), or ?timezone='s.
	loc, ok := s.reportZone(w, r)
	if !ok {
		return
	}
	q, err := in.lifecycle(start.In(loc))
	var res insights.LifecycleResult
	if err == nil {
		err = s.readInsight(r.Context(), func(tx pgx.Tx) error {
			c, err := insights.CompileLifecycle(r.Context(), tx, a.WorkspaceID, a.Brand, q)
			if err != nil {
				return err
			}
			res, err = c.Run(r.Context(), tx)
			return err
		})
	}
	if err != nil {
		metrics.InsightRun("lifecycle", s.insightError(w, r, err), time.Since(start))
		return
	}
	metrics.InsightRun("lifecycle", "success", time.Since(start))
	writeJSON(w, http.StatusOK, res)
}

// readInsight runs fn in a read-only transaction with the insight time
// limit, so a heavy question can't hold the database.
func (s *Server) readInsight(ctx context.Context, fn func(pgx.Tx) error) error {
	return pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SET LOCAL statement_timeout = '`+strconv.Itoa(int(insightTimeout.Seconds()))+`s'`); err != nil {
			return err
		}
		return fn(tx)
	})
}

// insightError writes the answer for a failed insight query and says
// which outcome it was.
func (s *Server) insightError(w http.ResponseWriter, r *http.Request, err error) string {
	var inv insights.Invalid
	switch {
	case errors.As(err, &inv):
		writeError(w, r, http.StatusBadRequest, "invalid_request", inv.Error())
		return "invalid"
	case isStatementTimeout(err):
		writeError(w, r, http.StatusBadRequest, "query_timeout", "the question took over 30 seconds: shorten the dates, or add filters to its first step")
		return "timeout"
	}
	s.internal(w, r, err)
	return "error"
}

type funnelOut struct {
	insights.Result
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
}

// runFunnel answers a funnel spec.
func (s *Server) runFunnel(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in funnelSpec
	if !decode(w, r, &in) {
		return
	}
	start := time.Now()
	// Days are the brand's (B4a), or ?timezone='s.
	loc, ok := s.reportZone(w, r)
	if !ok {
		return
	}
	f, err := in.funnel(start.In(loc))
	var res insights.Result
	if err == nil {
		err = s.readInsight(r.Context(), func(tx pgx.Tx) error {
			c, err := insights.Compile(r.Context(), tx, a.WorkspaceID, a.Brand, f)
			if err != nil {
				return err
			}
			res, err = c.Run(r.Context(), tx)
			return err
		})
	}
	if err != nil {
		metrics.InsightRun("funnel", s.insightError(w, r, err), time.Since(start))
		return
	}
	metrics.InsightRun("funnel", "success", time.Since(start))
	writeJSON(w, http.StatusOK, funnelOut{Result: res, From: f.From, To: f.To})
}

type funnelPeopleInput struct {
	Funnel    funnelSpec `json:"funnel"`
	Step      int        `json:"step"`
	Dropped   bool       `json:"dropped"`
	Breakdown *string    `json:"breakdown"`
	Limit     int        `json:"limit"`
	Offset    int        `json:"offset"`
}

// funnelPeople lists the people who reached a step, or who stopped just
// before it, so they can be opened, saved to a list or written to.
func (s *Server) funnelPeople(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in funnelPeopleInput
	if !decode(w, r, &in) {
		return
	}
	if in.Limit == 0 {
		in.Limit = 50
	}
	if in.Limit < 1 || in.Limit > 500 || in.Offset < 0 || in.Offset > 100_000 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "limit is 1 to 500 and offset 0 to 100000")
		return
	}
	start := time.Now()
	// Days are the brand's (B4a), or ?timezone='s.
	loc, ok := s.reportZone(w, r)
	if !ok {
		return
	}
	f, err := in.Funnel.funnel(start.In(loc))
	var people []insights.Person
	var more bool
	if err == nil {
		err = s.readInsight(r.Context(), func(tx pgx.Tx) error {
			c, err := insights.Compile(r.Context(), tx, a.WorkspaceID, a.Brand, f)
			if err != nil {
				return err
			}
			pq := insights.PeopleQuery{Step: in.Step, Dropped: in.Dropped, Limit: in.Limit, Offset: in.Offset}
			if in.Breakdown != nil {
				pq.Breakdown, pq.BreakdownSet = *in.Breakdown, true
			}
			people, more, err = c.People(r.Context(), tx, pq)
			return err
		})
	}
	if err != nil {
		metrics.InsightRun("funnel_people", s.insightError(w, r, err), time.Since(start))
		return
	}
	metrics.InsightRun("funnel_people", "success", time.Since(start))
	writeJSON(w, http.StatusOK, map[string]any{"data": people, "has_more": more})
}

type catalogEvent struct {
	Name     string    `json:"name"`
	Count    int       `json:"count"`
	LastSeen time.Time `json:"last_seen"`
}

type catalogConversion struct {
	Key   string `json:"key"`
	Name  string `json:"name"`
	Event string `json:"event"`
}

type catalogNetwork struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Connected bool   `json:"connected"`
}

// insightsCatalog lists what a funnel can be built from in this brand:
// the events it has seen in the last 90 days, its saved conversions, the
// ad networks, and the fields and breakdowns of each step kind.
func (s *Server) insightsCatalog(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	rows, err := s.pool.Query(ctx, `SELECT name, count(*)::int, max(occurred_at) FROM customer_events
		WHERE workspace_id = $1 AND brand_id = $2 AND occurred_at > now() - interval '90 days'
		GROUP BY name ORDER BY count(*) DESC, name LIMIT 300`, a.WorkspaceID, a.Brand)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	events, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (catalogEvent, error) {
		var e catalogEvent
		return e, row.Scan(&e.Name, &e.Count, &e.LastSeen)
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	// A definition is its brand's; to_jsonb reads brand_id, which a later
	// migration adds, and until then every definition counts for every brand.
	rows, err = s.pool.Query(ctx, `SELECT key, name, event_name FROM conversion_definitions d
		WHERE workspace_id = $1 AND COALESCE(to_jsonb(d) ->> 'brand_id', $2::text) = $2::text AND archived_at IS NULL ORDER BY lower(name)`, a.WorkspaceID, a.Brand)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	convs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (catalogConversion, error) {
		var c catalogConversion
		return c, row.Scan(&c.Key, &c.Name, &c.Event)
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var connected []string
	rows, err = s.pool.Query(ctx, `SELECT DISTINCT a.connector FROM ad_accounts a
		WHERE a.workspace_id = $1 AND a.status = 'active' AND (a.brand_id = $2 OR EXISTS (
			SELECT 1 FROM ad_entities e WHERE e.ad_account_id = a.id AND e.brand_id = $2))`, a.WorkspaceID, a.Brand)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	connected, err = pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		s.internal(w, r, err)
		return
	}
	networks := []catalogNetwork{}
	for _, n := range adtrack.Networks {
		networks = append(networks, catalogNetwork{ID: n.ID, Label: n.Label, Connected: n.Connector != "" && slices.Contains(connected, n.Connector)})
	}
	fields := func(m map[string]string) []string {
		out := make([]string, 0, len(m))
		for k := range m {
			out = append(out, k)
		}
		slices.Sort(out)
		return out
	}
	// A/B tests whose versions funnels and trends can be split by
	// (experiment:<flag key>): running, or started in the last 180 days.
	rows, err = s.pool.Query(ctx, `SELECT DISTINCT ON (f.key) f.key, t.name, t.status FROM ab_tests t JOIN flags f ON f.id = t.flag_id AND f.workspace_id = t.workspace_id
		WHERE t.workspace_id = $1 AND t.brand_id = $2 AND t.started_at IS NOT NULL AND (t.status = 'running' OR t.started_at > now() - interval '180 days')
		ORDER BY f.key, t.started_at DESC LIMIT 50`, a.WorkspaceID, a.Brand)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	type catalogExperiment struct {
		Field  string `json:"field"`
		Name   string `json:"name"`
		Status string `json:"status"`
	}
	experiments, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (catalogExperiment, error) {
		var x catalogExperiment
		var key string
		err := row.Scan(&key, &x.Name, &x.Status)
		x.Field = insights.ExperimentPrefix + key
		return x, err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	breakdowns := map[string][]string{}
	for _, k := range insights.Kinds {
		breakdowns[k] = insights.Breakdowns(k)
	}
	breakdowns[insights.KindAd] = insights.TrendBreakdowns(insights.KindAd)
	writeJSON(w, http.StatusOK, map[string]any{
		"events":      nonNil(events),
		"conversions": nonNil(convs),
		"networks":    networks,
		"fields": map[string][]string{
			"event":   fields(insights.EventFields),
			"visit":   fields(insights.VisitFields),
			"payment": fields(insights.PaymentFields),
		},
		"breakdowns":   breakdowns,
		"experiments":  nonNil(experiments),
		"measures":     insights.Measures,
		"ad_metrics":   insights.AdMetrics,
		"intervals":    insights.Intervals,
		"ops":          insights.Ops,
		"date_presets": datePresets,
	})
}

// Saved insights.

var insightKinds = []string{"funnel", "trend", "retention", "paths", "lifecycle"}

type insightView struct {
	ID          string          `json:"id"`
	BrandID     string          `json:"brand_id"`
	Kind        string          `json:"kind"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Spec        json.RawMessage `json:"spec"`
	CreatedBy   *string         `json:"created_by"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

const insightColumns = `i.id::text, i.brand_id::text, i.kind, i.name, i.description, i.spec, i.created_by::text, i.created_at, i.updated_at`

func scanInsight(row pgx.Row) (insightView, error) {
	var v insightView
	err := row.Scan(&v.ID, &v.BrandID, &v.Kind, &v.Name, &v.Description, &v.Spec, &v.CreatedBy, &v.CreatedAt, &v.UpdatedAt)
	return v, err
}

type insightInput struct {
	Kind        *string         `json:"kind"`
	Name        *string         `json:"name"`
	Description *string         `json:"description"`
	Spec        json.RawMessage `json:"spec"`
}

// checkSpec validates a saved insight's spec for its kind and returns it
// as stored.
func checkSpec(kind string, raw json.RawMessage) ([]byte, error) {
	switch kind {
	case "funnel":
		var f funnelSpec
		dec := json.NewDecoder(strings.NewReader(string(raw)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&f); err != nil {
			return nil, errInvalid("spec is a funnel: " + err.Error())
		}
		if _, err := f.funnel(time.Now()); err != nil {
			return nil, errInvalid("spec: " + err.Error())
		}
		return json.Marshal(f)
	case "trend":
		var t trendSpec
		dec := json.NewDecoder(strings.NewReader(string(raw)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&t); err != nil {
			return nil, errInvalid("spec is a trend: " + err.Error())
		}
		if _, err := t.trend(time.Now()); err != nil {
			return nil, errInvalid("spec: " + err.Error())
		}
		return json.Marshal(t)
	case "retention":
		var t retentionSpec
		dec := json.NewDecoder(strings.NewReader(string(raw)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&t); err != nil {
			return nil, errInvalid("spec is a retention question: " + err.Error())
		}
		if _, err := t.retention(time.Now()); err != nil {
			return nil, errInvalid("spec: " + err.Error())
		}
		return json.Marshal(t)
	case "paths":
		var p pathsSpec
		dec := json.NewDecoder(strings.NewReader(string(raw)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&p); err != nil {
			return nil, errInvalid("spec is a paths question: " + err.Error())
		}
		if _, err := p.paths(time.Now()); err != nil {
			return nil, errInvalid("spec: " + err.Error())
		}
		return json.Marshal(p)
	case "lifecycle":
		var l lifecycleSpec
		dec := json.NewDecoder(strings.NewReader(string(raw)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&l); err != nil {
			return nil, errInvalid("spec is a lifecycle question: " + err.Error())
		}
		if _, err := l.lifecycle(time.Now()); err != nil {
			return nil, errInvalid("spec: " + err.Error())
		}
		return json.Marshal(l)
	}
	return nil, errInvalid("kind is funnel, trend, retention, paths or lifecycle")
}

func (s *Server) createInsight(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in insightInput
	if !decode(w, r, &in) {
		return
	}
	kind := "funnel"
	if in.Kind != nil {
		kind = *in.Kind
	}
	var name, desc string
	if in.Name != nil {
		name = strings.TrimSpace(*in.Name)
	}
	if in.Description != nil {
		desc = strings.TrimSpace(*in.Description)
	}
	switch {
	case !slices.Contains(insightKinds, kind):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "kind is funnel, trend, retention, paths or lifecycle")
		return
	case name == "" || len(name) > 120:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "name is 1 to 120 characters")
		return
	case len(desc) > 1000:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "description is at most 1000 characters")
		return
	case len(in.Spec) == 0:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "spec is the insight's question, the same JSON its run endpoint takes")
		return
	}
	spec, err := checkSpec(kind, in.Spec)
	if s.allocError(w, r, err) {
		return
	}
	var v insightView
	err = pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		var id string
		if err := tx.QueryRow(r.Context(), `INSERT INTO insights (workspace_id, brand_id, kind, name, description, spec, created_by, updated_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $7) RETURNING id::text`, a.WorkspaceID, a.Brand, kind, name, desc, spec, nullIfEmpty(a.ActorID)).Scan(&id); err != nil {
			return err
		}
		if err := recordWithProps(r.Context(), tx, &a.principal, a.WorkspaceID, "insight.created", "insight", id,
			map[string]any{"kind": kind, "brand_id": a.Brand}, insightProps(kind, spec)); err != nil {
			return err
		}
		var err error
		v, err = scanInsight(tx.QueryRow(r.Context(), `SELECT `+insightColumns+` FROM insights i WHERE i.id = $1`, id))
		return err
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

// insightProps are the product event properties of a saved insight:
// counts and enums only.
func insightProps(kind string, spec []byte) map[string]any {
	props := map[string]any{"kind": kind}
	if kind == "funnel" {
		var f funnelSpec
		if json.Unmarshal(spec, &f) == nil {
			props["steps"] = len(f.Steps)
			kinds := []string{}
			for _, s := range f.Steps {
				kinds = append(kinds, s.Kind)
			}
			props["step_kinds"] = strings.Join(kinds, ",")
			props["has_breakdown"] = f.Breakdown != ""
			props["has_ad_clicks"] = f.AdClicks != nil
			props["exclusions"] = len(f.Exclusions)
			props["by_company"] = f.By == insights.ByCompany
			props["any_order"] = f.Order == insights.OrderAny
		}
	}
	if kind == "lifecycle" {
		var l lifecycleSpec
		if json.Unmarshal(spec, &l) == nil {
			props["step_kind"] = l.Step.Kind
			props["interval"] = l.Interval
		}
	}
	if kind == "paths" {
		var p pathsSpec
		if json.Unmarshal(spec, &p) == nil {
			props["has_start"] = p.Start != nil
			props["has_end"] = p.End != nil
			props["path_steps"] = p.Steps
		}
	}
	if kind == "trend" {
		var t trendSpec
		if json.Unmarshal(spec, &t) == nil {
			props["series"] = len(t.Series)
			kinds := []string{}
			for _, x := range t.Series {
				kinds = append(kinds, x.Kind)
			}
			props["series_kinds"] = strings.Join(kinds, ",")
			props["has_breakdown"] = t.Breakdown != ""
			props["has_formula"] = t.Formula != ""
			props["compare"] = t.Compare
			props["display"] = t.Display
		}
	}
	if kind == "retention" {
		var t retentionSpec
		if json.Unmarshal(spec, &t) == nil {
			props["start_kind"] = t.Start.Kind
			if t.Return != nil {
				props["return_kind"] = t.Return.Kind
			}
			props["interval"] = t.Interval
			props["periods"] = t.Periods
		}
	}
	return props
}

func (s *Server) getInsight(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	v, err := scanInsight(s.pool.QueryRow(r.Context(), `SELECT `+insightColumns+` FROM insights i
		WHERE i.workspace_id = $1 AND i.brand_id = $2 AND i.id = $3`, a.WorkspaceID, a.Brand, validOrNil(r.PathValue("insight"))))
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, http.StatusNotFound, "not_found", "insight not found")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// listInsights lists a brand's saved insights, the latest changed first.
func (s *Server) listInsights(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	limit, ok := pageLimit(w, r, 100, 500)
	if !ok {
		return
	}
	offset, ok := pageOffsetParam(w, r)
	if !ok {
		return
	}
	kind := r.URL.Query().Get("kind")
	if kind != "" && !slices.Contains(insightKinds, kind) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "kind is funnel, trend, retention, paths or lifecycle")
		return
	}
	rows, err := s.pool.Query(r.Context(), `SELECT `+insightColumns+` FROM insights i
		WHERE i.workspace_id = $1 AND i.brand_id = $2 AND ($3 = '' OR i.kind = $3)
		ORDER BY i.updated_at DESC, i.id DESC LIMIT $4 OFFSET $5`, a.WorkspaceID, a.Brand, kind, limit+1, offset)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (insightView, error) { return scanInsight(row) })
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

func (s *Server) updateInsight(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in insightInput
	if !decode(w, r, &in) {
		return
	}
	if in.Kind != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "kind can't change: save a new insight")
		return
	}
	var v insightView
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		cur, err := scanInsight(tx.QueryRow(r.Context(), `SELECT `+insightColumns+` FROM insights i
			WHERE i.workspace_id = $1 AND i.brand_id = $2 AND i.id = $3 FOR UPDATE`, a.WorkspaceID, a.Brand, validOrNil(r.PathValue("insight"))))
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		name, desc, spec := cur.Name, cur.Description, []byte(cur.Spec)
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
		if len(in.Spec) > 0 {
			if spec, err = checkSpec(cur.Kind, in.Spec); err != nil {
				return err
			}
			changed = append(changed, "spec")
		}
		if _, err := tx.Exec(r.Context(), `UPDATE insights SET name = $2, description = $3, spec = $4, updated_by = $5, updated_at = now()
			WHERE id = $1`, cur.ID, name, desc, spec, nullIfEmpty(a.ActorID)); err != nil {
			return err
		}
		props := insightProps(cur.Kind, spec)
		props["fields"] = strings.Join(changed, ",")
		if err := recordWithProps(r.Context(), tx, &a.principal, a.WorkspaceID, "insight.updated", "insight", cur.ID,
			map[string]any{"kind": cur.Kind, "brand_id": a.Brand, "fields": changed}, props); err != nil {
			return err
		}
		v, err = scanInsight(tx.QueryRow(r.Context(), `SELECT `+insightColumns+` FROM insights i WHERE i.id = $1`, cur.ID))
		return err
	})
	if errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "insight not found")
		return
	}
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) deleteInsight(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		var id, kind, name string
		err := tx.QueryRow(r.Context(), `DELETE FROM insights WHERE workspace_id = $1 AND brand_id = $2 AND id = $3
			RETURNING id::text, kind, name`, a.WorkspaceID, a.Brand, validOrNil(r.PathValue("insight"))).Scan(&id, &kind, &name)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		return record(r.Context(), tx, &a.principal, a.WorkspaceID, "insight.deleted", "insight", id,
			map[string]any{"kind": kind, "brand_id": a.Brand, "name": name})
	})
	if errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "insight not found")
		return
	}
	if s.allocError(w, r, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
