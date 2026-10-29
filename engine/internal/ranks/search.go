package ranks

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/seosync"
)

// Search reads one search engine results page for any query, through the
// same SERP connections rank tracking uses: for a person, an agent or a
// playbook looking for pages (resource pages on a topic, the pages that
// rank for it, pages that name a brand). Each search is paid on the
// workspace's own connection, within its monthly budget, and its cost is
// recorded in the Outcomes ledger. Nothing else is kept.

// SearchDepths are the result counts a search may ask for.
const (
	MinSearchDepth     = 10
	MaxSearchDepth     = 100
	DefaultSearchDepth = 10
)

// SearchQuery is one search.
type SearchQuery struct {
	connectors.SERPQuery
	// Engine is one of the connector's SERPEngines; "" is its default.
	Engine string
}

// searchUnit is the budget unit of a SERP connection: US dollars, or API
// units when its price list has no dollar price.
func searchUnit(r connectors.SERPReader) string {
	if r.EstimateSERP(connectors.SERPQuery{Depth: DefaultSearchDepth}).USD == 0 {
		return seosync.UnitAPIUse
	}
	return seosync.UnitUSD
}

// searchEngine is q's engine on r, or an error naming the ones it has.
func searchEngine(r connectors.SERPReader, name, engine string) (string, error) {
	engines := r.SERPEngines()
	if engine == "" && len(engines) > 0 {
		return engines[0], nil
	}
	if !slices.Contains(engines, engine) {
		return "", connectors.Errf(connectors.ErrInvalid, "%s searches %v", name, engines)
	}
	return engine, nil
}

// EstimateSearch is what a search would cost on the connection, in its
// budget's unit, and the budget left.
func EstimateSearch(ctx context.Context, pool *pgxpool.Pool, o *connectors.Opened, q SearchQuery) (float64, seosync.Budget, error) {
	r, ok := o.Connector.(connectors.SERPReader)
	if !ok {
		return 0, seosync.Budget{}, connectors.Errf(connectors.ErrInvalid, "%s cannot search", o.Name)
	}
	if _, err := searchEngine(r, o.Name, q.Engine); err != nil {
		return 0, seosync.Budget{}, err
	}
	unit := searchUnit(r)
	b, err := seosync.BudgetIn(ctx, pool, o.WorkspaceID, o.ID, unit)
	return amount(unit, r.EstimateSERP(q.SERPQuery)), b, err
}

// SearchResult is a search's results and what it cost.
type SearchResult struct {
	connectors.SERPResult
	Spent float64 `json:"spent"`
	Unit  string  `json:"unit"`
}

// Search runs a search within the connection's budget. Its outcome is
// counted in metrics; the caller records the event.
func Search(ctx context.Context, pool *pgxpool.Pool, store *connectors.Store, o *connectors.Opened, q SearchQuery) (SearchResult, error) {
	res, err := search(ctx, pool, store, o, q)
	metrics.WebSearch(SearchErrorCode(err))
	return res, err
}

func search(ctx context.Context, pool *pgxpool.Pool, store *connectors.Store, o *connectors.Opened, q SearchQuery) (SearchResult, error) {
	r, ok := o.Connector.(connectors.SERPReader)
	if !ok {
		return SearchResult{}, connectors.Errf(connectors.ErrInvalid, "%s cannot search", o.Name)
	}
	engine, err := searchEngine(r, o.Name, q.Engine)
	if err != nil {
		return SearchResult{}, err
	}
	unit := searchUnit(r)
	b, err := seosync.BudgetIn(ctx, pool, o.WorkspaceID, o.ID, unit)
	if err != nil {
		return SearchResult{}, err
	}
	if err := seosync.CheckBudget(ctx, pool, o.WorkspaceID, o.ID, o.Name, b, amount(unit, r.EstimateSERP(q.SERPQuery))); err != nil {
		return SearchResult{}, err
	}
	cctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	out, err := r.SERP(cctx, o.Client, o.Creds, engine, q.SERPQuery)
	cancel()
	res := SearchResult{SERPResult: out, Spent: amount(unit, out.Cost), Unit: unit}
	// What a refused or failed call cost is still spent.
	if out.Cost.USD > 0 || out.Cost.Units > 0 {
		note := fmt.Sprintf("Search on %s (%s, %s)", engine, q.Country, q.Device)
		if rerr := recordCost(ctx, pool, o, unit, "", note, out.Cost); rerr != nil {
			return res, rerr
		}
	}
	if err != nil && connectors.KindOf(err) == connectors.ErrAuth {
		_ = store.Checked(ctx, pool, o.WorkspaceID, o.ID, nil, err)
	}
	if res.Items == nil {
		res.Items = []connectors.SERPItem{}
	}
	return res, err
}

// SearchErrorCode is the outcome of a search for metrics and events:
// done, or why not.
func SearchErrorCode(err error) string {
	var be *seosync.BudgetError
	switch {
	case err == nil:
		return "done"
	case errors.As(err, &be):
		return "budget_reached"
	case connectors.KindOf(err) == connectors.ErrInvalid:
		return "connector_invalid"
	case connectors.KindOf(err) == connectors.ErrAuth:
		return "connector_auth"
	case connectors.KindOf(err) == connectors.ErrQuota:
		return "connector_quota"
	default:
		return "connector_unavailable"
	}
}
