package api

import (
	"context"
	"net/http"
	"slices"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/connectors/models"
	"github.com/outflareai/outflare/engine/internal/llm"
	"github.com/outflareai/outflare/engine/internal/research"
)

// AIConnection is one of the workspace's connected AI model connections.
type AIConnection struct {
	ID       string
	Provider llm.Provider
	Name     string
}

// AIConnectionsOf lists the workspace's connected AI model connections, for
// a feature that calls several models (each with its own way of searching
// the web) rather than the one the workspace chose.
func (s *Server) AIConnectionsOf(ctx context.Context, ws string) ([]AIConnection, error) {
	conns, err := s.aiConnections(ctx, ws)
	if err != nil {
		return nil, err
	}
	out := make([]AIConnection, 0, len(conns))
	for _, c := range conns {
		p, _ := llm.ProviderFor(c.Connector)
		out = append(out, AIConnection{ID: c.ID, Provider: p, Name: c.Name})
	}
	return out, nil
}

// AIModelOn opens one of the workspace's AI connections with model (empty:
// the workspace's model when it is that connection, else the provider's
// default) for task, refusing when the task's or the workspace's monthly
// AI limit would not cover about cents more. A model whose price Outflare
// doesn't know runs only on a router that reports what each call cost.
func (s *Server) AIModelOn(ctx context.Context, ws, task, integrationID, model string, cents int64) (research.Choice, error) {
	row, err := s.loadAISettings(ctx, s.pool, ws)
	if err != nil {
		return research.Choice{}, err
	}
	conns, err := s.aiConnections(ctx, ws)
	if err != nil {
		return research.Choice{}, err
	}
	i := slices.IndexFunc(conns, func(c connectors.Integration) bool { return c.ID == integrationID })
	if i < 0 {
		return research.Choice{}, errAI{http.StatusConflict, "connector_needed", "That AI model connection is no longer connected. Reconnect it or choose another."}
	}
	p, _ := llm.ProviderFor(conns[i].Connector)
	var price llm.Price
	switch {
	case model == "" && row.IntegrationID != nil && *row.IntegrationID == integrationID:
		_, _, model, price, _, err = pickAI(row, conns)
		if err != nil {
			return research.Choice{}, err
		}
	default:
		if model == "" {
			model = p.DefaultModel
		}
		known, ok := llm.KnownPrice(p.ID, model)
		if !ok {
			return research.Choice{}, errAI{http.StatusConflict, "ai_model_needed",
				"Outflare doesn't know what " + model + " on " + p.Name + " costs, so it can't keep to your spend limits. Choose another model."}
		}
		price = known
	}
	c := aiChoice{Provider: p, IntegrationID: integrationID, Task: task, Price: price, MonthlyLimit: row.MonthlyLimit,
		TaskLimit: row.taskLimit(task), LimitWarnOnly: row.LimitWarnOnly, TaskWarnOnly: row.taskWarnOnly(task)}
	if err := s.checkAIBudget(ctx, ws, c, cents); err != nil {
		return research.Choice{}, err
	}
	o, err := s.integrations.Open(ctx, s.pool, ws, integrationID)
	if err != nil {
		return research.Choice{}, err
	}
	m, err := llm.New(models.Config(p, o.Client, o.Creds["api_key"], model))
	if err != nil {
		return research.Choice{}, err
	}
	return research.Choice{Provider: p, IntegrationID: integrationID, Model: m, Price: price, Task: task}, nil
}
