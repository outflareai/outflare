package ranks

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/seosync"
)

// AI demand: the questions people ask AI assistants and how often, read
// through a demand reader (DataForSEO's LLM Mentions and AI keyword data)
// on the workspace's own connection, within its monthly budget, with the
// cost in the Outcomes ledger.

const demandTimeout = time.Minute

// AIQuestions finds real questions whose answers mention the brand or its
// competitors. ref is the Outcomes ledger entry's reference.
func AIQuestions(ctx context.Context, pool *pgxpool.Pool, store *connectors.Store, o *connectors.Opened, q connectors.AIQuestionQuery, ref string) (connectors.AIQuestions, error) {
	out := connectors.AIQuestions{Questions: []connectors.AIQuestion{}}
	err := paidDemand(ctx, pool, store, o, ref, "AI questions", q.Country, func(r connectors.DemandReader) connectors.SEOCost { return r.EstimateAIQuestions() },
		func(ctx context.Context, r connectors.DemandReader) (connectors.SEOCost, error) {
			var err error
			out, err = r.AIQuestions(ctx, o.Client, o.Creds, q)
			return out.Cost, err
		})
	metrics.AIDemand("questions", SearchErrorCode(err))
	return out, err
}

// AIVolume reads how many times a month people ask AI assistants about
// each phrase.
func AIVolume(ctx context.Context, pool *pgxpool.Pool, store *connectors.Store, o *connectors.Opened, phrases []string, country, language, ref string) (connectors.AIVolumes, error) {
	out := connectors.AIVolumes{Volume: map[string]int{}}
	err := paidDemand(ctx, pool, store, o, ref, "AI search volume", country, func(r connectors.DemandReader) connectors.SEOCost { return r.EstimateAIVolume(len(phrases)) },
		func(ctx context.Context, r connectors.DemandReader) (connectors.SEOCost, error) {
			var err error
			out, err = r.AIVolume(ctx, o.Client, o.Creds, phrases, country, language)
			return out.Cost, err
		})
	metrics.AIDemand("volume", SearchErrorCode(err))
	return out, err
}

func paidDemand(ctx context.Context, pool *pgxpool.Pool, store *connectors.Store, o *connectors.Opened, ref, what, country string,
	estimate func(connectors.DemandReader) connectors.SEOCost, call func(context.Context, connectors.DemandReader) (connectors.SEOCost, error)) error {
	r, ok := o.Connector.(connectors.DemandReader)
	if !ok {
		return connectors.Errf(connectors.ErrInvalid, "%s cannot read what people ask AI assistants", o.Name)
	}
	b, err := seosync.BudgetIn(ctx, pool, o.WorkspaceID, o.ID, seosync.UnitUSD)
	if err != nil {
		return err
	}
	if err := seosync.CheckBudget(ctx, pool, o.WorkspaceID, o.ID, o.Name, b, estimate(r).USD); err != nil {
		return err
	}
	cctx, cancel := context.WithTimeout(ctx, demandTimeout)
	cost, err := call(cctx, r)
	cancel()
	if cost.USD > 0 {
		if rerr := recordCostRef(ctx, pool, o, seosync.UnitUSD, "", fmt.Sprintf("%s (%s)", what, country), ref, cost); rerr != nil {
			return rerr
		}
	}
	if err != nil && connectors.KindOf(err) == connectors.ErrAuth {
		_ = store.Checked(ctx, pool, o.WorkspaceID, o.ID, nil, err)
	}
	return err
}

// AIBenchmark counts the stored AI answers that mention each target.
func AIBenchmark(ctx context.Context, pool *pgxpool.Pool, store *connectors.Store, o *connectors.Opened, targets []connectors.AIBenchmarkTarget, country, language, ref string) (connectors.AIBenchmark, error) {
	out := connectors.AIBenchmark{Counts: map[string]connectors.AIBenchmarkCount{}}
	err := paidDemand(ctx, pool, store, o, ref, "AI mentions benchmark", country, func(r connectors.DemandReader) connectors.SEOCost { return r.EstimateAIBenchmark(len(targets)) },
		func(ctx context.Context, r connectors.DemandReader) (connectors.SEOCost, error) {
			var err error
			out, err = r.AIBenchmark(ctx, o.Client, o.Creds, targets, country, language)
			return out.Cost, err
		})
	metrics.AIDemand("benchmark", SearchErrorCode(err))
	return out, err
}
