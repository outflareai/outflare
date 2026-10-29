// Package ranks tracks where an SEO site ranks for the keywords a
// workspace follows. Each check reads a search engine's results page
// through the workspace's own SERP connection (DataForSEO or Geonode, for
// Google),
// within the connection's monthly budget, and records what it cost in the
// Outcomes ledger. Checks run as jobs: daily or weekly per keyword, or
// when someone asks.
package ranks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/outcomes"
	"github.com/outflareai/outflare/engine/internal/retry"
	"github.com/outflareai/outflare/engine/internal/seointel"
	"github.com/outflareai/outflare/engine/internal/seosync"
	"github.com/outflareai/outflare/engine/internal/track"
)

const (
	JobCheck = "seo.ranks"
	// Depth is how many results a check reads.
	Depth = 100
	// topKept is how many results a check keeps, to show who outranks the
	// site.
	topKept = 10
	// maxPerRun is how many keywords one job checks; the rest wait for the
	// next.
	maxPerRun = 200
)

// due is the SQL that says a tracked keyword needs a check, a little early
// so a daily check does not drift later each day.
const due = `(t.last_checked_at IS NULL OR t.last_checked_at < now() - CASE t.frequency WHEN 'daily' THEN interval '20 hours' ELSE interval '6 days 20 hours' END)`

// ErrNoConnection means the workspace has no SERP connection.
var ErrNoConnection = connectors.Errf(connectors.ErrInvalid, "rank tracking needs a SERP connection: connect DataForSEO or Geonode on the Connectors page")

// Service runs rank checks.
type Service struct {
	Pool  *pgxpool.Pool
	Store *connectors.Store
	Log   *slog.Logger
	// Now is the clock; tests set it.
	Now func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

type payload struct {
	Site string `json:"site_id"`
	// All checks every active keyword not checked today, not only those due.
	All bool `json:"all,omitempty"`
}

// Register adds the job handler.
func (s *Service) Register(q *jobs.Queue) { q.Handle(JobCheck, s.job) }

// Enqueue asks for a check of a site's keywords. all checks every active
// keyword not checked today; key de-duplicates (a request's id, or "" for
// one per 30 seconds).
func Enqueue(ctx context.Context, q jobs.Querier, ws, siteID string, all bool, key string) (int64, error) {
	if key == "" {
		key = fmt.Sprintf("ranks:%s:now:%d", siteID, time.Now().Unix()/30)
	}
	return jobs.Enqueue(ctx, q, jobs.NewJob{Kind: JobCheck, WorkspaceID: &ws, Payload: payload{Site: siteID, All: all}, IdempotencyKey: key, MaxAttempts: 2})
}

// Run schedules checks until ctx ends.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		if _, err := s.QueueDue(ctx); err != nil && ctx.Err() == nil {
			track.Fail(ctx, s.Log, track.Event{Name: "rank.schedule_failed", ActorType: "system", Surface: "job"}, "internal", fmt.Errorf("ranks: schedule checks: %w", err))
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// QueueDue queues a check of each site with due keywords, and reports how
// many it queued. A keyword whose
// check failed is due again after retry.After, so its day still gets a
// position: the job's key carries the latest failure, so each retry is a
// new job, while a day with no failures stays one job.
func (s *Service) QueueDue(ctx context.Context) (int, error) {
	rows, err := s.Pool.Query(ctx, `SELECT t.workspace_id::text, t.site_id::text,
			COALESCE(max(extract(epoch FROM t.last_failed_at)) FILTER (WHERE t.failures > 0), 0)::bigint FROM tracked_keywords t
		JOIN seo_sites ss ON ss.id = t.site_id AND ss.status = 'active'
		WHERE t.status = 'active' AND `+due+`
		  AND (t.failures = 0 OR t.last_failed_at IS NULL OR t.last_failed_at <= now() - `+retry.SQL("t.failures")+`)
		GROUP BY 1, 2`)
	if err != nil {
		return 0, err
	}
	type site struct {
		ws, id string
		failed int64
	}
	var list []site
	var x site
	if _, err := pgx.ForEachRow(rows, []any{&x.ws, &x.id, &x.failed}, func() error { list = append(list, x); return nil }); err != nil {
		return 0, err
	}
	day := s.now().UTC().Format(time.DateOnly)
	n := 0
	for _, x := range list {
		key := "ranks:" + x.id + ":" + day
		if x.failed > 0 {
			key += ":retry:" + strconv.FormatInt(x.failed, 10)
		}
		if _, err := Enqueue(ctx, s.Pool, x.ws, x.id, false, key); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

func (s *Service) job(ctx context.Context, job jobs.Job) error {
	var p payload
	if err := json.Unmarshal(job.Payload, &p); err != nil || job.WorkspaceID == nil {
		return fmt.Errorf("%w: bad payload", jobs.ErrPermanent)
	}
	_, err := s.CheckSite(ctx, *job.WorkspaceID, p.Site, p.All)
	// What failed is on each keyword; a job retry would only spend again.
	if err != nil && connectors.KindOf(err) != connectors.ErrUnavailable {
		return nil
	}
	return err
}

// Result is what one run checked.
type Result struct {
	Checked int     `json:"checked"`
	Ranked  int     `json:"ranked"`
	Failed  int     `json:"failed"`
	CostUSD float64 `json:"cost_usd"`
}

type tracked struct {
	id, keyword, country, device, engine string
	integration                          *string
}

// SERPConnectors are the connectors that can check ranks, by name.
func SERPConnectors(reg *connectors.Registry) []string {
	var ids []string
	for _, d := range reg.All() {
		if c, ok := reg.Get(d.ID); ok && d.Status == connectors.Available {
			if _, ok := c.(connectors.SERPReader); ok {
				ids = append(ids, d.ID)
			}
		}
	}
	return ids
}

// DefaultConnection is the workspace's SERP connection for keywords that
// do not name one, or "": the first connector in SERPConnectors' order
// (DataForSEO before Geonode, so a workspace that tracked ranks before
// Geonode could keeps its source), then the oldest connection.
func DefaultConnection(ctx context.Context, q jobs.Querier, reg *connectors.Registry, ws string) (string, error) {
	var id string
	err := q.QueryRow(ctx, `SELECT id::text FROM integrations WHERE workspace_id = $1 AND connector = ANY($2) AND status = 'connected'
		ORDER BY array_position($2, connector), created_at, id LIMIT 1`, ws, SERPConnectors(reg)).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// CheckSite checks a site's due keywords (or, with all, every active one
// not checked today), connection by connection, until a budget runs out.
func (s *Service) CheckSite(ctx context.Context, ws, siteID string, all bool) (Result, error) {
	var res Result
	ev := track.Event{Name: "rank.checked", WorkspaceID: ws, ActorType: "system", Surface: "job", Props: map[string]any{"site_id": siteID}}
	var host string
	if err := s.Pool.QueryRow(ctx, `SELECT host FROM seo_sites WHERE id = $1 AND workspace_id = $2`, siteID, ws).Scan(&host); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return res, nil
		}
		return res, err
	}
	// One check of a site at a time, so two jobs never pay for the same
	// keyword: the second waits, then finds it checked.
	conn, err := s.Pool.Acquire(ctx)
	if err != nil {
		return res, err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(hashtextextended('ranks:' || $1, 0))`, siteID); err != nil {
		return res, err
	}
	defer conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock(hashtextextended('ranks:' || $1, 0))`, siteID) //nolint:errcheck
	cond := due
	if all {
		cond = `(t.last_checked_at IS NULL OR t.last_checked_at < date_trunc('day', now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC')`
	}
	var list []tracked
	rows, err := s.Pool.Query(ctx, `SELECT t.id::text, t.keyword, t.country, t.device, t.engine, t.integration_id::text FROM tracked_keywords t
		WHERE t.workspace_id = $1 AND t.site_id = $2 AND t.status = 'active' AND `+cond+`
		ORDER BY t.last_checked_at NULLS FIRST, t.keyword LIMIT $3`, ws, siteID, maxPerRun)
	if err != nil {
		return res, err
	}
	for rows.Next() {
		var t tracked
		if err := rows.Scan(&t.id, &t.keyword, &t.country, &t.device, &t.engine, &t.integration); err != nil {
			rows.Close()
			return res, err
		}
		list = append(list, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(list) == 0 {
		return res, err
	}

	def, err := DefaultConnection(ctx, s.Pool, s.Store.Registry, ws)
	if err != nil {
		return res, err
	}
	groups := map[string][]tracked{}
	var order []string
	for _, t := range list {
		id := def
		if t.integration != nil {
			id = *t.integration
		}
		if _, ok := groups[id]; !ok {
			order = append(order, id)
		}
		groups[id] = append(groups[id], t)
	}
	var firstErr error
	for _, id := range order {
		err := s.checkWith(ctx, ws, siteID, host, id, groups[id], &res)
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	ev.Props["checked"], ev.Props["ranked"], ev.Props["failed"], ev.Props["cost_usd"] = res.Checked, res.Ranked, res.Failed, math.Round(res.CostUSD*10000)/10000
	if res.Checked > 0 {
		// New positions can be drops: the site's regressions follow them.
		seointel.CheckRegressions(ctx, s.Pool, s.Log, ws, "rank_check", []string{siteID})
	}
	if firstErr != nil {
		code, outcome := "connector_"+connectors.KindOf(firstErr), track.Failure
		var be *seosync.BudgetError
		switch {
		case errors.As(firstErr, &be):
			code = "budget_reached"
		case errors.Is(firstErr, ErrNoConnection):
			code = "connector_needed"
		case connectors.KindOf(firstErr) == connectors.ErrUnavailable:
			outcome = track.Error
		}
		ev.Outcome = outcome
		track.Fail(ctx, s.Log, ev, code, fmt.Errorf("rank check of site %s: %w", siteID, firstErr))
	} else {
		track.Emit(ctx, ev)
	}
	return res, firstErr
}

// fail notes why keywords could not be checked.
func (s *Service) fail(ctx context.Context, ws string, list []tracked, err error, res *Result) error {
	ids := make([]string, len(list))
	for i, t := range list {
		ids[i] = t.id
	}
	res.Failed += len(list)
	_, dbErr := s.Pool.Exec(ctx, `UPDATE tracked_keywords SET last_error = $3, failures = failures + 1, last_failed_at = now()
		WHERE workspace_id = $1 AND id = ANY($2::uuid[])`, ws, ids, err.Error())
	if dbErr != nil {
		return dbErr
	}
	return err
}

func (s *Service) checkWith(ctx context.Context, ws, siteID, host, integrationID string, list []tracked, res *Result) error {
	if integrationID == "" {
		return s.fail(ctx, ws, list, ErrNoConnection, res)
	}
	o, err := s.Store.Open(ctx, s.Pool, ws, integrationID)
	if err != nil {
		return s.fail(ctx, ws, list, err, res)
	}
	r, ok := o.Connector.(connectors.SERPReader)
	if !ok {
		return s.fail(ctx, ws, list, connectors.Errf(connectors.ErrInvalid, "%s cannot check rankings", o.Name), res)
	}
	unit := seosync.UnitUSD
	if r.EstimateSERP(connectors.SERPQuery{Depth: Depth}).USD == 0 {
		unit = seosync.UnitAPIUse
	}
	day := s.now().UTC().Format(time.DateOnly)
	hosts := seointel.PageHosts(host)
	var kwErr error
	for i, t := range list {
		q := connectors.SERPQuery{Keyword: t.keyword, Country: t.country, Device: t.device, Depth: Depth}
		b, err := seosync.BudgetIn(ctx, s.Pool, ws, o.ID, unit)
		if err != nil {
			return err
		}
		if err := seosync.CheckBudget(ctx, s.Pool, ws, o.ID, o.Name, b, amount(unit, r.EstimateSERP(q))); err != nil {
			var be *seosync.BudgetError
			if errors.As(err, &be) {
				return s.fail(ctx, ws, list[i:], err, res)
			}
			return err
		}
		cctx, cancel := context.WithTimeout(ctx, 90*time.Second)
		out, err := r.SERP(cctx, o.Client, o.Creds, t.engine, q)
		cancel()
		// What a refused or failed call cost is still spent.
		if out.Cost.USD > 0 || out.Cost.Units > 0 {
			if rerr := s.record(ctx, o, unit, host, t, out.Cost); rerr != nil {
				return rerr
			}
			res.CostUSD += out.Cost.USD
		}
		if err != nil {
			if connectors.KindOf(err) == connectors.ErrAuth {
				_ = s.Store.Checked(ctx, s.Pool, ws, o.ID, nil, err)
				return s.fail(ctx, ws, list[i:], err, res)
			}
			if ferr := s.fail(ctx, ws, list[i:i+1], err, res); ferr != nil && !errors.Is(ferr, err) {
				return ferr
			}
			kwErr = err
			if connectors.KindOf(err) == connectors.ErrQuota {
				return s.fail(ctx, ws, list[i+1:], err, res)
			}
			continue
		}
		pos, page := Find(out.Items, hosts)
		if pos == nil && out.Partial {
			// Not in the pages that came back is not "not in the top 100":
			// try again next time.
			err := connectors.Errf(connectors.ErrUnavailable, "%s returned fewer results than asked, so the check was incomplete; it will run again", o.Name)
			if ferr := s.fail(ctx, ws, list[i:i+1], err, res); ferr != nil && !errors.Is(ferr, err) {
				return ferr
			}
			kwErr = err
			continue
		}
		top := out.Items[:min(len(out.Items), topKept)]
		raw, _ := json.Marshal(top)
		_, err = s.Pool.Exec(ctx, `
			WITH obs AS (
				INSERT INTO rank_observations (workspace_id, tracked_id, day, position, url, depth, engine, top, cost_usd, integration_id)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
				ON CONFLICT (tracked_id, day) DO UPDATE SET position = EXCLUDED.position, url = EXCLUDED.url, depth = EXCLUDED.depth,
					engine = EXCLUDED.engine, top = EXCLUDED.top, cost_usd = rank_observations.cost_usd + EXCLUDED.cost_usd,
					integration_id = EXCLUDED.integration_id, checked_at = now()
				RETURNING 1
			)
			UPDATE tracked_keywords SET last_checked_at = now(), last_error = NULL, failures = 0 WHERE id = $2 AND workspace_id = $1`,
			ws, t.id, day, pos, page, Depth, out.Engine, raw, out.Cost.USD, o.ID)
		if err != nil {
			return err
		}
		res.Checked++
		if pos != nil {
			res.Ranked++
		}
	}
	return kwErr
}

// Find is the site's best organic position in a results page, and its
// page: the first result on one of hosts.
func Find(items []connectors.SERPItem, hosts []string) (*int, string) {
	for _, it := range items {
		h := it.Domain
		if u, err := url.Parse(it.URL); err == nil && u.Hostname() != "" {
			h = u.Hostname()
		}
		if slices.Contains(hosts, strings.ToLower(h)) {
			p := it.Position
			return &p, it.URL
		}
	}
	return nil, ""
}

func amount(unit string, c connectors.SEOCost) float64 {
	if unit == seosync.UnitUSD {
		return c.USD
	}
	return c.Units
}

// record puts one check's cost in the Outcomes ledger, counted against the
// connection's budget.
func (s *Service) record(ctx context.Context, o *connectors.Opened, unit, host string, t tracked, cost connectors.SEOCost) error {
	return recordCost(ctx, s.Pool, o, unit, host, fmt.Sprintf("Rank check on %s (%s, %s)", t.engine, t.country, t.device), cost)
}

// recordCost puts a SERP call's cost in the Outcomes ledger, counted
// against the connection's budget.
func recordCost(ctx context.Context, pool *pgxpool.Pool, o *connectors.Opened, unit, host, note string, cost connectors.SEOCost) error {
	units := amount(unit, cost)
	id := o.ID
	e := outcomes.Entry{
		WorkspaceID: o.WorkspaceID, Category: outcomes.CategoryData, Provider: o.Connector.Definition().ID,
		IntegrationID: &id, Units: &units, UnitName: unit, OccurredAt: time.Now(), Source: outcomes.SourceAuto,
		Domain: host, Note: note,
	}
	if cost.Estimated {
		e.Note += ", estimated from the price list"
	}
	if unit == seosync.UnitUSD {
		cents := int64(math.Round(cost.USD * 100))
		e.AmountCents, e.Currency = &cents, "USD"
	}
	_, _, err := outcomes.Record(ctx, pool, e)
	return err
}
