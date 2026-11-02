package outflare

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	spec "github.com/outflareai/outflare/api"
	"github.com/outflareai/outflare/engine/internal/agent"
	"github.com/outflareai/outflare/engine/internal/api"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/llm"
)

// Agents: a loop in an extension acts as an API token of its own on the
// action layer (ADR 13), so every change it makes runs, waits for a person
// or is refused by the grants a person set for it on the Agents page, and
// every one is kept. The token's secret is never kept: only the engine
// can act as it, in process, through the full handler, with scopes, the
// gate, limits, policy checks, the audit log and events applying exactly
// as to an outside agent's call.
type Agents struct{ h *Host }

// Agents gives an extension's loops their tokens and calls.
func (h *Host) Agents() *Agents { return &Agents{h} }

// AgentSpec describes the token to create for a loop.
type AgentSpec = api.AgentSpec

// ErrAgentSpec sorts Agents.Create's refusals (errors.Is): the message
// says what to change.
var ErrAgentSpec = api.ErrAgentSpec

// ErrAgentGone means the agent's token was revoked or expired, or its
// person disabled: the loop should stop.
var ErrAgentGone = api.ErrAgentGone

// Create makes a loop's token in tx and returns its id. The token holds at
// most an operator's scopes and never approves, so a loop can never widen
// its own room; a person sets what it may do on the Agents page.
func (g *Agents) Create(ctx context.Context, tx pgx.Tx, spec AgentSpec) (string, error) {
	if g.h.server == nil {
		return "", errors.New("agents are available once the engine is serving")
	}
	return g.h.server.CreateAgentToken(ctx, tx, spec)
}

// Revoke revokes a loop's token in tx (the loop stops: its calls answer
// ErrAgentGone), as the person byUserID or the system when empty, with
// the token.revoked audit event. Revoking twice is a no-op.
func (g *Agents) Revoke(ctx context.Context, tx pgx.Tx, workspaceID, tokenID, byUserID string) error {
	if g.h.server == nil {
		return errors.New("agents are available once the engine is serving")
	}
	return g.h.server.RevokeAgentToken(ctx, tx, workspaceID, tokenID, byUserID)
}

// AgentCall is the API's answer to a loop's call.
type AgentCall struct {
	Status int
	Body   json.RawMessage
	// HeldActionID is set when the gate held the change for a person
	// (202): read it back with GET .../actions/{id} to learn the decision.
	HeldActionID string
}

// Held reports whether the change waits for a person.
func (c AgentCall) Held() bool { return c.HeldActionID != "" }

// Call makes one API call as the agent token: method, a full API path such
// as "/v1/workspaces/{ws}/sequences", a JSON body (any value, or nil) and
// the reason the agent gives, kept with the action for the person who
// decides it. Reads run; a change runs, is held or is refused by the
// agent's grants.
func (g *Agents) Call(ctx context.Context, tokenID, method, path string, body any, reason string) (AgentCall, error) {
	if g.h.server == nil {
		return AgentCall{}, errors.New("agents are available once the engine is serving")
	}
	var raw json.RawMessage
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return AgentCall{}, err
		}
		raw = b
	}
	status, out, err := g.h.server.CallAsAgent(ctx, tokenID, method, path, raw, reason)
	if err != nil {
		return AgentCall{}, err
	}
	c := AgentCall{Status: status, Body: out}
	if status == 202 {
		var held struct {
			Action *struct {
				ID string `json:"id"`
			} `json:"action"`
		}
		if json.Unmarshal(out, &held) == nil && held.Action != nil {
			c.HeldActionID = held.Action.ID
		}
	}
	return c, nil
}

// EnqueueJobAt is EnqueueJob for a job that runs at a later time, such as
// a loop's next turn. A non-empty idempotency key makes a repeated enqueue
// a no-op.
func (h *Host) EnqueueJobAt(ctx context.Context, kind string, payload any, idempotencyKey string, at time.Time) error {
	_, err := jobs.Enqueue(ctx, h.Pool, jobs.NewJob{Kind: kind, Payload: payload, IdempotencyKey: idempotencyKey, RunAt: at})
	return err
}

// The tools a loop gives its model (AIRequest.Tools) and what comes back.
type (
	AIFunction = llm.Tool
	AIToolCall = llm.ToolCall
)

// APIGuide lists every workspace operation of the API, one line each as
// "METHOD /path: summary" with paths relative to the workspace, for an
// agent's prompt. It is the same every turn, so a model caches it with the
// system prompt. APIDocs gives one path's full definition.
func APIGuide() string { return apiGuide() }

// APIDocs returns the OpenAPI text for one workspace path (all its
// methods) and the schemas it uses, such as "/sequences", for an agent
// to read before calling it the first time.
func APIDocs(path string) string { return agent.Docs(spec.OpenAPI, path) }

var apiGuide = sync.OnceValue(func() string { return agent.Guide(spec.OpenAPI) })
