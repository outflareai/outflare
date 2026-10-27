package api

import (
	"context"
	"net/http"

	"github.com/outflareai/outflare/engine/internal/classify"
	"github.com/outflareai/outflare/engine/internal/llm"
	"github.com/outflareai/outflare/engine/internal/research"
)

// Research is the long tail of email domains: the ones the reviewed
// providers, the public lists and DNS cannot place. A workspace that
// connects a page reader and its own AI model can have their homepages
// read; this file is where a classification picks that research up, and
// where the research service borrows the workspace's model.

// aiModels lets the research service open the workspace's model and book
// what it spends, so the monthly AI limit is enforced in one place.
type aiModels struct{ s *Server }

// AIModels is the engine's implementation of research.Models.
func (s *Server) AIModels() research.Models { return aiModels{s} }

// ForTask opens the model the workspace chose, refusing when its month's
// AI spend would not cover about cents more.
//
// The task is the model task name (classify.ResearchTask), so the workspace
// can give it a cheaper model than missions use.
func (m aiModels) ForTask(ctx context.Context, ws, task string, cents int64) (research.Choice, error) {
	c, err := m.s.aiModel(ctx, ws, task)
	if err != nil {
		return research.Choice{}, err
	}
	if err := m.s.checkAIBudget(ctx, ws, c, cents); err != nil {
		return research.Choice{}, err
	}
	return research.Choice{Provider: c.Provider, IntegrationID: c.IntegrationID, Model: c.Model, Price: c.Price, Task: c.Task}, nil
}

// Spent books a research call on the workspace's own provider bill.
func (m aiModels) Spent(ctx context.Context, ws string, c research.Choice, u llm.Usage, ref, note string) error {
	return recordAISpend(ctx, m.s.pool, aiChoice{Provider: c.Provider, IntegrationID: c.IntegrationID,
		Model: c.Model, Price: c.Price, Task: c.Task}, ws, nil, nil, nil, u, ref, note)
}

// withResearch adds the workspace's own research to a result: a verdict it
// already has, or a job to go and find one. A workspace with research off
// gets neither, and the answer stays as the global data left it.
func (s *Server) withResearch(r *http.Request, ws string, res *classify.Result) error {
	if s.research == nil || res.Global.Category != classify.Unknown {
		return nil
	}
	ctx := r.Context()
	row, ok, err := s.research.Of(ctx, s.pool, ws, res.NormalizedDomain)
	if err != nil {
		return err
	}
	if ok {
		res.ApplyResearch(row.Verdict, row.Model, row.ResearchedAt)
		return nil
	}
	queued, err := s.research.Queue(ctx, s.pool, ws, res.NormalizedDomain)
	if err != nil {
		return err
	}
	if queued {
		res.Analysis = classify.Analysis{Status: classify.AnalysisQueued, Provisional: true}
	}
	return nil
}

// researchMonthSpend is what domain research cost the workspace this
// calendar month (UTC), in US cents on its own provider bill.
func (s *Server) researchMonthSpend(ctx context.Context, ws string) (int64, error) {
	var cents int64
	err := s.pool.QueryRow(ctx, `SELECT COALESCE(sum(cost_cents), 0) FROM domain_research
		WHERE workspace_id = $1 AND researched_at >= date_trunc('month', now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'`, ws).
		Scan(&cents)
	return cents, err
}
