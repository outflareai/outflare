package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/missions"
)

// Agents for extensions (ADR 13): a loop acts as an API token of its own
// on the action layer, so every change it makes runs, waits for a person
// or is refused by the grants a person set for it, and every one is kept.
// The token's secret is never kept, so only the engine can act as it, in
// process, through the full handler: scopes, the gate, limits, policy
// checks, the audit log and events all apply exactly as to an outside
// agent's call.

// AgentSpec describes the agent token an extension creates for a loop.
type AgentSpec struct {
	WorkspaceID string
	// Name shows in the audit log and on the Agents page, such as "Brand
	// agent: Acme". Up to 200 characters.
	Name string
	// Scopes the agent holds: never more than an operator's, and never one
	// only a person may hold.
	Scopes []string
	// BrandID keeps the token to one brand, or nil for every brand.
	BrandID *string
	// CreatedBy is the id of the person on whose say-so the loop runs, or
	// "". A disabled person's agents stop with them.
	CreatedBy string
}

// ErrAgentSpec is a spec the engine refuses; the message says why.
var ErrAgentSpec = errors.New("invalid agent")

type agentSpecError string

func (e agentSpecError) Error() string        { return string(e) }
func (e agentSpecError) Is(target error) bool { return target == ErrAgentSpec }

// CreateAgentToken makes a loop's token in tx: on the action layer, with
// no secret kept, so only the engine can act as it (CallAsAgent).
func (s *Server) CreateAgentToken(ctx context.Context, tx pgx.Tx, spec AgentSpec) (string, error) {
	name := strings.TrimSpace(spec.Name)
	switch {
	case !validUUID(spec.WorkspaceID):
		return "", agentSpecError("workspace id is required")
	case name == "":
		return "", agentSpecError("name is required")
	case len(spec.Scopes) == 0:
		return "", agentSpecError("at least one scope is required")
	}
	if len(name) > 200 {
		name = name[:200]
	}
	operator := auth.RoleScopes(auth.RoleOperator)
	for _, sc := range spec.Scopes {
		switch {
		case !auth.ValidScope(sc):
			return "", agentSpecError("unknown scope " + sc)
		case slices.Contains(auth.PersonOnly, sc):
			return "", agentSpecError("an agent can't hold " + sc + ": only a signed-in person does")
		case !slices.Contains(operator, sc) || sc == auth.ScopeMissionsApprove || sc == auth.ScopeActionsApprove:
			return "", agentSpecError("an agent can't hold " + sc + ": never more than an operator, and never approving")
		}
	}
	if spec.BrandID != nil {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM brands WHERE id = $1 AND workspace_id = $2)`, *spec.BrandID, spec.WorkspaceID).Scan(&ok); err != nil {
			return "", err
		}
		if !ok {
			return "", agentSpecError("brand not found in this workspace")
		}
	}
	var actorID string
	if err := tx.QueryRow(ctx, `INSERT INTO actors (workspace_id, type, display_name) VALUES ($1, 'api_token', $2) RETURNING id`, spec.WorkspaceID, name).Scan(&actorID); err != nil {
		return "", err
	}
	// A real secret, hashed and then forgotten: nothing outside the engine
	// can ever present it.
	secret, hash := auth.NewToken(auth.PrefixAPIToken)
	var createdBy *string
	if spec.CreatedBy != "" {
		createdBy = &spec.CreatedBy
	}
	var id string
	err := tx.QueryRow(ctx, `
		INSERT INTO api_tokens (workspace_id, actor_id, name, prefix, token_hash, scopes, created_by, brand_id, action_gate)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, true) RETURNING id`,
		spec.WorkspaceID, actorID, name, secret[:len(auth.PrefixAPIToken)+6], hash, slices.Compact(slices.Sorted(slices.Values(spec.Scopes))), createdBy, spec.BrandID).Scan(&id)
	if err != nil {
		return "", err
	}
	p := &principal{ActorType: "system"}
	if spec.CreatedBy != "" {
		p = &principal{ActorType: "human", UserID: spec.CreatedBy}
	}
	return id, record(ctx, tx, p, spec.WorkspaceID, "token.created", "api_token", id,
		map[string]any{"name": name, "scopes": spec.Scopes, "brand_id": spec.BrandID, "action_gate": true, "agent": true})
}

// ctxAgentCallKey marks a request an extension's loop makes in process
// (surface "agent").
type ctxAgentCallKey struct{}

// ErrAgentGone means the agent's token was revoked or expired, or its
// person disabled, so it can't act any more.
var ErrAgentGone = errors.New("the agent's token was revoked or expired")

// agentPrincipal is a token as the handler sees it, with everything the
// gate needs.
func (s *Server) agentPrincipal(ctx context.Context, tokenID string) (*principal, error) {
	if !validUUID(tokenID) {
		return nil, ErrAgentGone
	}
	p := &principal{ActorType: "api_token"}
	err := s.pool.QueryRow(ctx, `SELECT id, actor_id, workspace_id, scopes, brand_id::text, coalesce(created_by::text, ''), require_mission, action_gate FROM api_tokens
		WHERE id = $1 AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at > now())
		  AND NOT EXISTS (SELECT 1 FROM users u WHERE u.id = api_tokens.created_by AND u.disabled_at IS NOT NULL)`, tokenID).
		Scan(&p.TokenID, &p.ActorID, &p.TokenWorkspaceID, &p.TokenScopes, &p.TokenBrandID, &p.TokenOwnerID, &p.TokenRequiresMission, &p.TokenActionGate)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrAgentGone
	}
	return p, err
}

// CallAsAgent makes one API call in process as an agent token: path is a
// full API path ("/v1/workspaces/{ws}/sequences", "/v1/brands/{id}/rules"),
// body is JSON or nil, and reason is what the agent says it is doing, kept
// with the action. The answer is the handler's: a change the gate held
// answers 202 with the action, one it refused 403 action_refused.
func (s *Server) CallAsAgent(ctx context.Context, tokenID, method, path string, body json.RawMessage, reason string) (int, []byte, error) {
	if !strings.HasPrefix(path, "/v1/") || strings.Contains(path, "..") {
		return 0, nil, errors.New("path must be an API path starting with /v1/")
	}
	p, err := s.agentPrincipal(ctx, tokenID)
	if err != nil {
		return 0, nil, err
	}
	var rd io.Reader = http.NoBody
	if len(body) > 0 && method != http.MethodGet && string(body) != "null" {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(context.WithValue(context.WithValue(ctx, ctxInjectedKey{}, p), ctxAgentCallKey{}, true), method, path, rd)
	if err != nil {
		return 0, nil, err
	}
	if rd != http.NoBody {
		req.Header.Set("Content-Type", "application/json")
	}
	if reason = strings.TrimSpace(reason); reason != "" {
		req.Header.Set(missions.ReasonHeader, strings.ReplaceAll(reason, "\n", " "))
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes(), nil
}

// RevokeAgentToken revokes a loop's token in tx, on the say-so of the
// person byUserID (or the system when empty), with its audit event. A
// token already revoked, or not this workspace's, is left as it is.
func (s *Server) RevokeAgentToken(ctx context.Context, tx pgx.Tx, workspaceID, tokenID, byUserID string) error {
	if !validUUID(workspaceID) || !validUUID(tokenID) {
		return ErrAgentGone
	}
	tag, err := tx.Exec(ctx, `UPDATE api_tokens SET revoked_at = now() WHERE id = $1 AND workspace_id = $2 AND revoked_at IS NULL`, tokenID, workspaceID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return nil
	}
	p := &principal{ActorType: "system"}
	if byUserID != "" {
		p = &principal{ActorType: "human", UserID: byUserID}
	}
	return record(ctx, tx, p, workspaceID, "token.revoked", "api_token", tokenID, map[string]any{"agent": true})
}
