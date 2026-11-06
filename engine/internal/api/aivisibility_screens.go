package api

import (
	"context"
	"errors"

	"github.com/outflareai/outflare/engine/internal/aivis"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/ranks"
	"github.com/outflareai/outflare/engine/internal/seosync"
)

// AI visibility on the assistants' own screens: ChatGPT and Gemini as a
// signed-out person in a country sees them, read through one of the
// workspace's own answer reader connections (DataForSEO's LLM Scraper)
// and paid from that connection's budget, like a search.

type aivisScreens struct{ s *Server }

// AIVisibilityScreens is the engine's implementation of aivis.Screens.
func (s *Server) AIVisibilityScreens() aivis.Screens { return aivisScreens{s} }

// answerConnections are the workspace's connected answer readers.
func (s *Server) answerConnections(ctx context.Context, ws string) ([]connectors.Integration, error) {
	if s.integrations == nil {
		return nil, nil
	}
	list, err := s.integrations.List(ctx, s.pool, ws, "")
	if err != nil {
		return nil, err
	}
	out := []connectors.Integration{}
	for _, i := range list {
		if i.Status != connectors.StatusConnected {
			continue
		}
		if c, ok := s.integrations.Registry.Get(i.Connector); ok {
			if _, ok := c.(connectors.AnswerReader); ok {
				out = append(out, i)
			}
		}
	}
	return out, nil
}

// answerEngines are the assistants a connector can ask, or none.
func (s *Server) answerEngines(connector string) []connectors.AnswerEngine {
	if s.integrations == nil {
		return nil
	}
	c, ok := s.integrations.Registry.Get(connector)
	if !ok {
		return nil
	}
	r, ok := c.(connectors.AnswerReader)
	if !ok {
		return nil
	}
	return r.AnswerEngines()
}

// Ask asks the target's assistant one question on its own screen.
func (m aivisScreens) Ask(ctx context.Context, ws string, t aivis.Target, q connectors.AnswerQuery, ref string) (connectors.AnswerResult, error) {
	out := connectors.AnswerResult{Engine: t.Model}
	conns, err := m.s.answerConnections(ctx, ws)
	if err != nil {
		return out, err
	}
	found := false
	for _, c := range conns {
		found = found || c.ID == t.IntegrationID
	}
	if !found {
		return out, aivis.Refused{Code: "connector_needed",
			Message: "The DataForSEO connection chosen under SEO → AI visibility → Settings is no longer connected. Reconnect it under Connectors or choose another."}
	}
	o, err := m.s.integrations.Open(ctx, m.s.pool, ws, t.IntegrationID)
	if err != nil {
		return out, err
	}
	res, err := ranks.Answer(ctx, m.s.pool, m.s.integrations, o, t.Model, q, ref)
	if res.Cost.USD > 0 && ref != "" {
		// AI answers is its own channel in the spend ledger, with the brand
		// the cohort measures.
		if _, uerr := m.s.pool.Exec(ctx, `
			UPDATE spend_entries e SET channel = 'ai_answers', brand_id = COALESCE(e.brand_id, (
				SELECT c.brand_id FROM ai_runs r JOIN ai_cohorts c ON c.id = r.cohort_id
				WHERE r.workspace_id = $1 AND r.id::text = split_part($2, ':', 2)))
			WHERE e.workspace_id = $1 AND e.ref = $2 AND e.channel = ''`, ws, ref); uerr != nil {
			return res, uerr
		}
	}
	var be *seosync.BudgetError
	if errors.As(err, &be) {
		return res, aivis.Refused{Code: "budget_reached", Message: be.Error()}
	}
	return res, err
}

// connectorName is how people know a connector, or its id.
func (s *Server) connectorName(id string) string {
	if s.integrations != nil {
		if c, ok := s.integrations.Registry.Get(id); ok {
			return c.Definition().Name
		}
	}
	return id
}
