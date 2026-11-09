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

// AI answers: asking ChatGPT or Gemini a question on its own screen
// through an answer reader (DataForSEO's LLM Scraper), paid like a search
// on the workspace's own connection, within its monthly budget, with the
// cost in the Outcomes ledger.

// answerTimeout is how long one answer may take; the assistant searches
// the web and writes, which takes a minute or more.
const answerTimeout = 3 * time.Minute

// Answer asks one assistant one question within the connection's budget.
// ref is the Outcomes ledger entry's reference ("" for none). Its outcome
// is counted in metrics; the caller records the event.
func Answer(ctx context.Context, pool *pgxpool.Pool, store *connectors.Store, o *connectors.Opened, engine string, q connectors.AnswerQuery, ref string) (connectors.AnswerResult, error) {
	res, err := answer(ctx, pool, store, o, engine, q, ref)
	metrics.AIAnswer(engine, SearchErrorCode(err))
	return res, err
}

func answer(ctx context.Context, pool *pgxpool.Pool, store *connectors.Store, o *connectors.Opened, engine string, q connectors.AnswerQuery, ref string) (connectors.AnswerResult, error) {
	r, ok := o.Connector.(connectors.AnswerReader)
	if !ok {
		return connectors.AnswerResult{Engine: engine}, connectors.Errf(connectors.ErrInvalid, "%s cannot ask AI assistants", o.Name)
	}
	b, err := seosync.BudgetIn(ctx, pool, o.WorkspaceID, o.ID, seosync.UnitUSD)
	if err != nil {
		return connectors.AnswerResult{Engine: engine}, err
	}
	if err := seosync.CheckBudget(ctx, pool, o.WorkspaceID, o.ID, o.Name, b, r.EstimateAnswer(engine).USD); err != nil {
		return connectors.AnswerResult{Engine: engine}, err
	}
	cctx, cancel := context.WithTimeout(ctx, answerTimeout)
	out, err := r.Answer(cctx, o.Client, o.Creds, engine, q)
	cancel()
	// What a refused or failed call cost is still spent.
	if out.Cost.USD > 0 {
		note := fmt.Sprintf("AI answer from %s (%s)", engine, q.Country)
		if rerr := recordCostRef(ctx, pool, o, seosync.UnitUSD, "", note, ref, "", out.Cost); rerr != nil {
			return out, rerr
		}
	}
	if err != nil && connectors.KindOf(err) == connectors.ErrAuth {
		_ = store.Checked(ctx, pool, o.WorkspaceID, o.ID, nil, err)
	}
	return out, err
}
