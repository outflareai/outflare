// Package experiments measures whether a change to a website's pages
// helped. A before and after experiment compares the pages with
// themselves. A holdout experiment splits the pages into matched groups:
// the change goes only to the treatment group, and the control group shows
// what would have happened anyway, so a Google update or a seasonal dip
// isn't mistaken for the change. The measurement starts when the change
// goes live, from a production deploy of the site (package deploys) or a
// person saying when, and outcomes come from the workspace's own Search
// Console data (search_daily).
package experiments

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"math"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/audit"
	"github.com/outflareai/outflare/engine/internal/deploys"
	"github.com/outflareai/outflare/engine/internal/seointel"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Querier is a pool or a transaction.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Designs, metrics, groups and statuses.
const (
	BeforeAfter = "before_after"
	Holdout     = "holdout"

	Treatment = "treatment"
	Control   = "control"

	Draft     = "draft"
	Waiting   = "waiting"
	Measuring = "measuring"
	Done      = "done"
	Stopped   = "stopped"

	// MaxPages is the most pages an experiment takes, the busiest first.
	MaxPages = 1000
)

// Metrics are the outcomes an experiment can measure.
var Metrics = []string{"clicks", "impressions", "ctr", "position"}

// ErrNoSearchData means no Search Console property covers the site, or it
// has no pages that match.
var ErrNoSearchData = errors.New("no search data")

// Experiment is one experiment as kept.
type Experiment struct {
	ID             string     `json:"id"`
	SiteID         string     `json:"site_id"`
	BrandID        *string    `json:"brand_id"`
	Name           string     `json:"name"`
	Hypothesis     string     `json:"hypothesis"`
	Design         string     `json:"design"`
	Pattern        string     `json:"pattern"`
	Metric         string     `json:"metric"`
	HoldoutPercent int        `json:"holdout_percent"`
	BaselineDays   int        `json:"baseline_days"`
	MeasureDays    int        `json:"measure_days"`
	StartOn        string     `json:"start_on"`
	Status         string     `json:"status"`
	ChangedAt      *time.Time `json:"changed_at"`
	DeployID       *string    `json:"deploy_id"`
	ConcludedAt    *time.Time `json:"concluded_at"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	// The pages in each group.
	TreatmentPages int `json:"treatment_pages"`
	ControlPages   int `json:"control_pages"`
	// The result so far while measuring, or the final one.
	Result *Result `json:"result"`
}

// Cols and Scan read an experiment; From is the table with its page counts.
const Cols = `e.id::text, e.site_id::text, e.brand_id::text, e.name, e.hypothesis, e.design, e.pattern, e.metric, e.holdout_percent,
	e.baseline_days, e.measure_days, e.start_on, e.status, e.changed_at, e.deploy_id::text, e.concluded_at, e.created_at, e.updated_at,
	(SELECT count(*) FROM experiment_pages p WHERE p.experiment_id = e.id AND p.grp = 'treatment'),
	(SELECT count(*) FROM experiment_pages p WHERE p.experiment_id = e.id AND p.grp = 'control'), e.result`

func Scan(row pgx.Row) (Experiment, error) {
	var e Experiment
	var result []byte
	err := row.Scan(&e.ID, &e.SiteID, &e.BrandID, &e.Name, &e.Hypothesis, &e.Design, &e.Pattern, &e.Metric, &e.HoldoutPercent,
		&e.BaselineDays, &e.MeasureDays, &e.StartOn, &e.Status, &e.ChangedAt, &e.DeployID, &e.ConcludedAt, &e.CreatedAt, &e.UpdatedAt,
		&e.TreatmentPages, &e.ControlPages, &result)
	if err == nil && result != nil {
		e.Result = &Result{}
		err = json.Unmarshal(result, e.Result)
	}
	return e, err
}

// Get loads one experiment of a workspace.
func Get(ctx context.Context, q Querier, ws, id string) (Experiment, error) {
	return Scan(q.QueryRow(ctx, `SELECT `+Cols+` FROM experiments e WHERE e.id = $1 AND e.workspace_id = $2`, id, ws))
}

// Pattern checks a page pattern: empty, or a path starting with / where *
// stands for anything (/blog/*).
func Pattern(p string) (*regexp.Regexp, error) {
	if p == "" {
		return nil, nil
	}
	if !strings.HasPrefix(p, "/") || len(p) > 300 {
		return nil, errors.New("pattern is a path starting with /, where * stands for anything: /blog/*")
	}
	parts := strings.Split(p, "*")
	for i := range parts {
		parts[i] = regexp.QuoteMeta(parts[i])
	}
	return regexp.MustCompile("^" + strings.Join(parts, ".*") + "$"), nil
}

// pathOf is a page URL's path, for patterns.
func pathOf(page string) string {
	u, err := url.Parse(page)
	if err != nil || u.Path == "" {
		return "/"
	}
	return u.Path
}

// PageValue is one page's metric over a window.
type PageValue struct {
	URL                 string
	Clicks, Impressions float64
	weightedPosition    float64
	Value               float64
}

func (p *PageValue) finish(metric string, days int) {
	switch metric {
	case "clicks":
		p.Value = p.Clicks / float64(days)
	case "impressions":
		p.Value = p.Impressions / float64(days)
	case "ctr":
		if p.Impressions > 0 {
			p.Value = p.Clicks / p.Impressions
		}
	case "position":
		if p.Impressions > 0 {
			p.Value = p.weightedPosition / p.Impressions
		}
	}
}

// Window reads each page's metric from Search Console for [from, to),
// for the pages of a site's hosts (or only those listed).
func Window(ctx context.Context, q Querier, ws, siteHost, metric string, from, to time.Time, only []string) (map[string]*PageValue, error) {
	args := []any{ws, seointel.PageHosts(siteHost), from.Format(time.DateOnly), to.Format(time.DateOnly)}
	filter := ""
	if only != nil {
		args = append(args, only)
		filter = ` AND page = ANY($5)`
	}
	rows, err := q.Query(ctx, `
		SELECT page, sum(clicks)::float8, sum(impressions)::float8, sum(position * impressions)::float8
		FROM search_daily
		WHERE workspace_id = $1 AND `+seointel.PageHostSQL("page")+` = ANY($2) AND day >= $3::date AND day < $4::date`+filter+`
		GROUP BY page`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	days := int(math.Round(to.Sub(from).Hours() / 24))
	if days < 1 {
		days = 1
	}
	out := map[string]*PageValue{}
	for rows.Next() {
		var p PageValue
		if err := rows.Scan(&p.URL, &p.Clicks, &p.Impressions, &p.weightedPosition); err != nil {
			return nil, err
		}
		p.finish(metric, days)
		out[p.URL] = &p
	}
	return out, rows.Err()
}

// Newest is the newest day Search Console has sent for a site, or zero.
func Newest(ctx context.Context, q Querier, ws, siteHost string) (time.Time, error) {
	src, err := seointel.SourceFor(ctx, q, ws, siteHost)
	if err != nil || src == nil || src.NewestDay == nil {
		return time.Time{}, err
	}
	return time.Parse(time.DateOnly, *src.NewestDay)
}

// Assignment is one page and its group.
type Assignment struct {
	URL      string
	Group    string
	Baseline float64
}

// Assign picks a site's pages that match the pattern over the baseline
// days that end on Search Console's newest day, the busiest MaxPages
// first, and splits them. A holdout ranks them by baseline and keeps every
// page whose turn falls on the control share, starting at an offset the
// seed picks, so the groups are matched.
func Assign(ctx context.Context, q Querier, ws, siteHost string, e Experiment, seed string) ([]Assignment, error) {
	newest, err := Newest(ctx, q, ws, siteHost)
	if err != nil {
		return nil, err
	}
	if newest.IsZero() {
		return nil, ErrNoSearchData
	}
	re, err := Pattern(e.Pattern)
	if err != nil {
		return nil, err
	}
	to := newest.AddDate(0, 0, 1)
	vals, err := Window(ctx, q, ws, siteHost, e.Metric, to.AddDate(0, 0, -e.BaselineDays), to, nil)
	if err != nil {
		return nil, err
	}
	list := make([]*PageValue, 0, len(vals))
	for _, v := range vals {
		if v.Impressions > 0 && (re == nil || re.MatchString(pathOf(v.URL))) {
			list = append(list, v)
		}
	}
	if len(list) == 0 {
		return nil, ErrNoSearchData
	}
	// The busiest pages, then ranked by the metric (busiest first).
	sort.Slice(list, func(i, j int) bool {
		if list[i].Impressions != list[j].Impressions {
			return list[i].Impressions > list[j].Impressions
		}
		return list[i].URL < list[j].URL
	})
	if len(list) > MaxPages {
		list = list[:MaxPages]
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].Value > list[j].Value })
	out := make([]Assignment, len(list))
	share := float64(e.HoldoutPercent) / 100
	h := fnv.New32a()
	h.Write([]byte(seed))
	offset := float64(h.Sum32()%1000) / 1000
	for i, v := range list {
		g := Treatment
		if e.Design == Holdout && math.Floor(float64(i+1)*share+offset) > math.Floor(float64(i)*share+offset) {
			g = Control
		}
		out[i] = Assignment{URL: v.URL, Group: g, Baseline: v.Value}
	}
	return out, nil
}

// Group is one group's outcome.
type Group struct {
	Pages int `json:"pages"`
	// The metric per page (daily for clicks and impressions) before and
	// after the change, and the change as a share (0.12 is 12% more).
	// Position falls as it gets better.
	Before float64  `json:"before"`
	After  float64  `json:"after"`
	Change *float64 `json:"change"`
}

// Result is what an experiment found, so far or in the end.
type Result struct {
	// The days compared: the baseline before the change and the days
	// measured after it, so far.
	BaselineFrom string `json:"baseline_from"`
	MeasureFrom  string `json:"measure_from"`
	MeasureTo    string `json:"measure_to"`
	DaysMeasured int    `json:"days_measured"`
	Treatment    Group  `json:"treatment"`
	// Null for a before and after experiment.
	Control *Group `json:"control"`
	// For a holdout, how much better the treatment did than the control
	// (0.08 is 8% better); for before and after, the treatment's change.
	// For position, a positive lift means it ranks higher.
	Lift *float64 `json:"lift"`
	// The chance the difference is noise, from the pages' changes; null
	// when there aren't enough pages to tell.
	PValue *float64 `json:"p_value"`
	// better, worse, no_clear_change, or too_early while it measures.
	Verdict string `json:"verdict"`
	Final   bool   `json:"final"`
}

// Measure works out an experiment's result from Search Console, or nil
// until it has a change time.
func Measure(ctx context.Context, q Querier, ws, siteHost string, e Experiment) (*Result, error) {
	if e.ChangedAt == nil {
		return nil, nil
	}
	newest, err := Newest(ctx, q, ws, siteHost)
	if err != nil {
		return nil, err
	}
	rows, err := q.Query(ctx, `SELECT url, grp FROM experiment_pages WHERE experiment_id = $1`, e.ID)
	if err != nil {
		return nil, err
	}
	groups := map[string][]string{}
	urls := []string{}
	for rows.Next() {
		var u, g string
		if err := rows.Scan(&u, &g); err != nil {
			rows.Close()
			return nil, err
		}
		groups[g] = append(groups[g], u)
		urls = append(urls, u)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Whole days: the change day itself is left out, since it is half one
	// and half the other.
	day := time.Date(e.ChangedAt.UTC().Year(), e.ChangedAt.UTC().Month(), e.ChangedAt.UTC().Day(), 0, 0, 0, 0, time.UTC)
	baseFrom := day.AddDate(0, 0, -e.BaselineDays)
	measureFrom := day.AddDate(0, 0, 1)
	measureTo := measureFrom.AddDate(0, 0, e.MeasureDays)
	if limit := newest.AddDate(0, 0, 1); !newest.IsZero() && limit.Before(measureTo) {
		measureTo = limit
	}
	r := &Result{BaselineFrom: baseFrom.Format(time.DateOnly), MeasureFrom: measureFrom.Format(time.DateOnly),
		MeasureTo: measureTo.AddDate(0, 0, -1).Format(time.DateOnly), Verdict: "too_early"}
	r.DaysMeasured = int(measureTo.Sub(measureFrom).Hours() / 24)
	if r.DaysMeasured < 1 {
		r.DaysMeasured = 0
		r.Treatment = Group{Pages: len(groups[Treatment])}
		if e.Design == Holdout {
			r.Control = &Group{Pages: len(groups[Control])}
		}
		return r, nil
	}
	before, err := Window(ctx, q, ws, siteHost, e.Metric, baseFrom, day, urls)
	if err != nil {
		return nil, err
	}
	after, err := Window(ctx, q, ws, siteHost, e.Metric, measureFrom, measureTo, urls)
	if err != nil {
		return nil, err
	}
	t, tChanges := summarise(e.Metric, groups[Treatment], before, after, e.BaselineDays, r.DaysMeasured)
	r.Treatment = t
	lift := t.Change
	var cChanges []float64
	if e.Design == Holdout {
		c, ch := summarise(e.Metric, groups[Control], before, after, e.BaselineDays, r.DaysMeasured)
		r.Control, cChanges = &c, ch
		lift = nil
		if t.Change != nil && c.Change != nil && *c.Change > -1 {
			l := (1+*t.Change)/(1+*c.Change) - 1
			lift = &l
		}
	}
	if lift != nil && e.Metric == "position" {
		l := -*lift
		lift = &l
	}
	r.Lift = lift
	r.PValue = pValue(tChanges, cChanges, e.Design == Holdout)
	r.Final = r.DaysMeasured >= e.MeasureDays
	if r.Final || r.DaysMeasured >= 7 {
		r.Verdict = verdict(r)
		if !r.Final && r.Verdict == "no_clear_change" {
			r.Verdict = "too_early"
		}
	}
	return r, nil
}

// summarise adds up a group, and each page's change as a log ratio, for
// the test.
func summarise(metric string, pages []string, before, after map[string]*PageValue, bDays, aDays int) (Group, []float64) {
	g := Group{Pages: len(pages)}
	var b, a PageValue
	changes := make([]float64, 0, len(pages))
	for _, u := range pages {
		pb, pa := before[u], after[u]
		if pb == nil {
			pb = &PageValue{}
		}
		if pa == nil {
			pa = &PageValue{}
		}
		b.Clicks += pb.Clicks
		b.Impressions += pb.Impressions
		b.weightedPosition += pb.weightedPosition
		a.Clicks += pa.Clicks
		a.Impressions += pa.Impressions
		a.weightedPosition += pa.weightedPosition
		if pb.Value > 0 || pa.Value > 0 {
			// +1 a day keeps quiet pages from swinging the test.
			changes = append(changes, math.Log((pa.Value+1)/(pb.Value+1)))
		}
	}
	if len(pages) == 0 {
		return g, changes
	}
	b.finish(metric, bDays)
	a.finish(metric, aDays)
	n := float64(len(pages))
	g.Before, g.After = b.Value, a.Value
	if metric == "clicks" || metric == "impressions" {
		g.Before, g.After = b.Value/n, a.Value/n
	}
	if g.Before > 0 {
		c := g.After/g.Before - 1
		g.Change = &c
	}
	return g, changes
}

// pValue is a two-sided Welch t-test (normal approximation) of the pages'
// changes: treatment against control, or treatment against no change.
func pValue(t, c []float64, holdout bool) *float64 {
	mean := func(x []float64) float64 {
		s := 0.0
		for _, v := range x {
			s += v
		}
		return s / float64(len(x))
	}
	variance := func(x []float64, m float64) float64 {
		s := 0.0
		for _, v := range x {
			s += (v - m) * (v - m)
		}
		return s / float64(len(x)-1)
	}
	if len(t) < 5 || (holdout && len(c) < 5) {
		return nil
	}
	mt := mean(t)
	se2 := variance(t, mt) / float64(len(t))
	diff := mt
	if holdout {
		mc := mean(c)
		diff -= mc
		se2 += variance(c, mc) / float64(len(c))
	}
	if se2 == 0 {
		p := 1.0
		if diff != 0 {
			p = 0
		}
		return &p
	}
	z := math.Abs(diff) / math.Sqrt(se2)
	p := math.Erfc(z / math.Sqrt2)
	return &p
}

func verdict(r *Result) string {
	if r.Lift == nil || r.PValue == nil || *r.PValue >= 0.05 {
		return "no_clear_change"
	}
	if *r.Lift > 0 {
		return "better"
	}
	return "worse"
}

// Service starts experiments on deploys and ends them when their days are
// measured.
type Service struct {
	Pool *pgxpool.Pool
	Log  *slog.Logger
	// Now is the clock; nil is time.Now.
	Now func() time.Time
}

// OnDeploy starts the waiting experiments on a deploy's site, in the
// transaction that records it going live. Register it as a deploys hook.
func (s *Service) OnDeploy(ctx context.Context, tx pgx.Tx, d deploys.Deploy) error {
	if d.SiteID == nil {
		return nil
	}
	rows, err := tx.Query(ctx, `UPDATE experiments SET status = 'measuring', changed_at = $3, deploy_id = $4, updated_at = now()
		WHERE workspace_id = $1 AND site_id = $2 AND status = 'waiting' AND start_on = 'deploy'
		RETURNING id::text, brand_id::text`, d.WorkspaceID, *d.SiteID, d.DeployedAt, d.ID)
	if err != nil {
		return err
	}
	type started struct {
		id    string
		brand *string
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (started, error) {
		var x started
		return x, r.Scan(&x.id, &x.brand)
	})
	if err != nil {
		return err
	}
	for _, x := range list {
		if err := audit.Record(ctx, tx, audit.Event{WorkspaceID: d.WorkspaceID, ActorType: "system", Action: "experiment.started",
			TargetType: "experiment", TargetID: x.id,
			Data: map[string]any{"site_id": *d.SiteID, "brand_id": x.brand, "by": "deploy", "deploy_id": d.ID, "changed_at": d.DeployedAt.UTC().Format(time.RFC3339)}}); err != nil {
			return err
		}
	}
	return nil
}

// Run ends experiments until ctx ends, checking every hour.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		if err := s.Tick(ctx); err != nil && ctx.Err() == nil {
			track.Fail(ctx, s.Log, track.Event{Name: "experiments.check_failed", ActorType: "system", Surface: "system"}, "internal", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Tick ends every measuring experiment whose days Search Console has now
// sent: the result is kept and experiment.concluded recorded.
func (s *Service) Tick(ctx context.Context) error {
	rows, err := s.Pool.Query(ctx, `SELECT e.workspace_id::text, `+Cols+`, si.host
		FROM experiments e JOIN seo_sites si ON si.id = e.site_id WHERE e.status = 'measuring'`)
	if err != nil {
		return err
	}
	type item struct {
		ws, host string
		e        Experiment
	}
	var list []item
	for rows.Next() {
		var x item
		var result []byte
		e := &x.e
		if err := rows.Scan(&x.ws, &e.ID, &e.SiteID, &e.BrandID, &e.Name, &e.Hypothesis, &e.Design, &e.Pattern, &e.Metric, &e.HoldoutPercent,
			&e.BaselineDays, &e.MeasureDays, &e.StartOn, &e.Status, &e.ChangedAt, &e.DeployID, &e.ConcludedAt, &e.CreatedAt, &e.UpdatedAt,
			&e.TreatmentPages, &e.ControlPages, &result, &x.host); err != nil {
			rows.Close()
			return err
		}
		list = append(list, x)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, x := range list {
		r, err := Measure(ctx, s.Pool, x.ws, x.host, x.e)
		if err != nil {
			return fmt.Errorf("experiment %s: %w", x.e.ID, err)
		}
		if r == nil || !r.Final {
			continue
		}
		err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
			ok, err := Conclude(ctx, tx, x.ws, x.e, r)
			if err != nil || !ok {
				return err
			}
			return audit.Record(ctx, tx, audit.Event{WorkspaceID: x.ws, ActorType: "system", Action: "experiment.concluded",
				TargetType: "experiment", TargetID: x.e.ID, Data: ConcludedData(x.e, r),
				Props: map[string]any{"design": x.e.Design, "metric": x.e.Metric, "verdict": r.Verdict, "days_measured": r.DaysMeasured}})
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// Conclude keeps an experiment's final result, in tx. ok is false when it
// had ended already. The caller records experiment.concluded with
// ConcludedData.
func Conclude(ctx context.Context, tx pgx.Tx, ws string, e Experiment, r *Result) (bool, error) {
	raw, _ := json.Marshal(r)
	tag, err := tx.Exec(ctx, `UPDATE experiments SET status = 'done', result = $3, concluded_at = now(), updated_at = now()
		WHERE id = $1 AND workspace_id = $2 AND status = 'measuring'`, e.ID, ws, raw)
	return err == nil && tag.RowsAffected() == 1, err
}

// ConcludedData is experiment.concluded's data.
func ConcludedData(e Experiment, r *Result) map[string]any {
	return map[string]any{"site_id": e.SiteID, "brand_id": e.BrandID, "design": e.Design, "metric": e.Metric, "verdict": r.Verdict,
		"lift": r.Lift, "p_value": r.PValue, "days_measured": r.DaysMeasured, "treatment_pages": r.Treatment.Pages}
}
