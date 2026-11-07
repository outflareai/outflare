package seosync

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Competitor read: one tracked competitor's search data read now, on one
// SEO connection, instead of waiting for the weekly snapshot (which reads
// keywords and linking domains for only the first MaxGapCompetitors). It
// reads their numbers, top keywords, linking domains and top pages, and
// skips any of them this connection read in the last CompetitorDays (the
// connection's setting, a month unless changed), so asking again costs
// nothing more. Everything it reads is stored as
// snapshots, the same as the weekly read.

const (
	CompetitorKeywordRows = 100
	CompetitorDomainRows  = 100
	CompetitorPageRows    = 25
)

// CompetitorReadPlan is what a read would do: the reports it would read
// (none when everything is fresh), about what they'd cost in the
// connection's unit, and the budget.
type CompetitorReadPlan struct {
	Reports  []string `json:"reports"`
	Fresh    []string `json:"fresh"`
	Estimate float64  `json:"estimate"`
	Unit     string   `json:"unit"`
	Budget   Budget   `json:"budget"`
}

func competitorQueries(r connectors.SEOReader, host, country string) []connectors.SEOQuery {
	var out []connectors.SEOQuery
	for _, q := range []connectors.SEOQuery{
		{Report: connectors.SEOOverview, Target: host, Country: country},
		{Report: connectors.SEOKeywords, Target: host, Country: country, Limit: CompetitorKeywordRows},
		{Report: connectors.SEOBacklinks, Target: host, Country: country, Limit: CompetitorDomainRows},
		{Report: connectors.SEOPages, Target: host, Country: country, Limit: CompetitorPageRows},
	} {
		if !connectors.SupportsSEO(r, q.Report) {
			continue
		}
		if q, err := connectors.CheckSEOQuery(q); err == nil {
			out = append(out, q)
		}
	}
	return out
}

// dueCompetitorQueries splits the queries into the ones to read and the
// reports this connection read recently.
func dueCompetitorQueries(ctx context.Context, pool *pgxpool.Pool, o *connectors.Opened, r connectors.SEOReader, host, country string) ([]connectors.SEOQuery, []string, error) {
	rows, err := pool.Query(ctx, `SELECT DISTINCT report FROM seo_snapshots
		WHERE workspace_id = $1 AND integration_id = $2 AND host = $3 AND day >= $4::date`,
		o.WorkspaceID, o.ID, host, time.Now().UTC().AddDate(0, 0, -SettingsOf(o.Config).CompetitorDaysOrDefault()+1).Format(time.DateOnly))
	if err != nil {
		return nil, nil, err
	}
	recent, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, nil, err
	}
	seen := map[string]bool{}
	for _, rep := range recent {
		seen[rep] = true
	}
	var due []connectors.SEOQuery
	fresh := []string{}
	for _, q := range competitorQueries(r, host, country) {
		if seen[q.Report] {
			fresh = append(fresh, q.Report)
		} else {
			due = append(due, q)
		}
	}
	return due, fresh, nil
}

// PlanCompetitorRead says what reading host now would read and cost.
func PlanCompetitorRead(ctx context.Context, pool *pgxpool.Pool, o *connectors.Opened, r connectors.SEOReader, host string) (CompetitorReadPlan, error) {
	plan := CompetitorReadPlan{Reports: []string{}, Unit: Unit(r)}
	due, fresh, err := dueCompetitorQueries(ctx, pool, o, r, host, SettingsOf(o.Config).Country)
	if err != nil {
		return plan, err
	}
	plan.Fresh = fresh
	for _, q := range due {
		plan.Reports = append(plan.Reports, q.Report)
		plan.Estimate += amountOf(plan.Unit, r.Estimate(q))
	}
	plan.Budget, err = BudgetOf(ctx, pool, o, r)
	return plan, err
}

// EnqueueCompetitorRead asks for a read now of one tracked competitor on
// the connection. Asking again within a minute is the same job.
func EnqueueCompetitorRead(ctx context.Context, q jobs.Querier, ws, integrationID, competitorID string) (int64, error) {
	return enqueue(ctx, q, ws, payload{Integration: integrationID, ReadCompetitor: competitorID},
		fmt.Sprintf("%s:competitor:%s:%d", integrationID, competitorID, time.Now().Unix()/60))
}

// readCompetitor is the ReadCompetitor job.
func (s *Syncer) readCompetitor(ctx context.Context, ws, integrationID, competitorID string) error {
	o, err := s.Store.Open(ctx, s.Pool, ws, integrationID)
	if err != nil {
		if connectors.KindOf(err) == connectors.ErrUnavailable && !errors.Is(err, connectors.ErrNotFound) {
			return err
		}
		return nil
	}
	r, ok := o.Connector.(connectors.SEOReader)
	if !ok {
		return nil
	}
	var host, siteID string
	err = s.Pool.QueryRow(ctx, `SELECT host, site_id::text FROM seo_competitors WHERE workspace_id = $1 AND id = $2 AND status = 'tracked'`,
		ws, competitorID).Scan(&host, &siteID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // untracked or removed since
	}
	if err != nil {
		return err
	}
	sites, err := LinkSites(ctx, s.Pool, ws)
	if err != nil {
		return err
	}
	var site *LinkSite
	for i := range sites {
		if sites[i].ID == siteID {
			site = &sites[i]
		}
	}
	if site == nil {
		return nil
	}
	conn := o.Connector.Definition().ID
	props := map[string]any{"connector": conn, "site_id": siteID, "competitor_id": competitorID}
	due, fresh, err := dueCompetitorQueries(ctx, s.Pool, o, r, host, SettingsOf(o.Config).Country)
	if err != nil {
		return err
	}
	props["fresh"] = len(fresh)
	unit := Unit(r)
	day := time.Now().UTC().Format(time.DateOnly)
	var cost float64
	read := 0
	for _, q := range due {
		a, err := Fetch(ctx, s.Pool, o, r, q, Options{Fresh: true})
		cost += amountOf(unit, a.Cost)
		if err == nil {
			_, err = s.store(ctx, ws, o, host, day, a.SEOReport)
		}
		if err == nil && q.Report == connectors.SEOBacklinks {
			_, err = s.newCompetitorLinks(ctx, ws, o.ID, conn, *site, competitorID, host, day, LinkTarget(site.Host))
		}
		if err != nil {
			code := "provider_error"
			var be *BudgetError
			if errors.As(err, &be) {
				code = "budget_reached"
			}
			props["report"], props["read"], props["cost"], props["kind"] = q.Report, read, cost, connectors.KindOf(err)
			track.Fail(ctx, s.Log, track.Event{Name: "seo_competitor.read_failed", WorkspaceID: ws, Surface: "job", Props: props}, code, err)
			return nil // budgets and refused keys won't fix themselves
		}
		read++
	}
	props["read"], props["cost"] = read, cost
	track.Emit(ctx, track.Event{Name: "seo_competitor.read", WorkspaceID: ws, ActorType: "system", Surface: "job", Outcome: track.Success, Props: props})
	return nil
}
