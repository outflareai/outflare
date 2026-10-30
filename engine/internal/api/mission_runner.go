package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	spec "github.com/outflareai/outflare/api"
	"github.com/outflareai/outflare/engine/internal/agent"
	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/llm"
	"github.com/outflareai/outflare/engine/internal/missions"
	"github.com/outflareai/outflare/engine/internal/track"
)

// The built-in runner: Outflare's own agent carries out a mission in turns
// with the workspace's chosen AI model and its key. It acts as the mission's token, through
// the same gate as an outside agent, so dry runs, held changes, limits and
// the call log all apply. A turn runs when the mission is approved or
// resumed, when a person writes to it or decides on a held change, and on
// the schedule the agent asks for.

const (
	runnerExternal = "external"
	runnerBuiltin  = "builtin"
	// Longest a turn may take before another engine may take the mission.
	turnLease = 20 * time.Minute
	// What a turn is assumed to cost when checking the spend limit first.
	turnEstimateCents = 25
	// Turns that fail in a row before the mission pauses.
	maxTurnFailures = 3
)

// builtinScopes are what the built-in agent's token holds: what an
// operator can do, less approving missions, and never more than the person
// who approves the mission.
func builtinScopes(a *access) []string {
	var out []string
	for _, sc := range auth.RoleScopes(auth.RoleOperator) {
		if sc != auth.ScopeMissionsApprove && a.has(sc) {
			out = append(out, sc)
		}
	}
	return out
}

// useBuiltinRunner gives the mission to the built-in agent: a token of its
// own (whose secret is never kept, so only the engine can act as it) and a
// first turn as soon as the mission runs.
func useBuiltinRunner(ctx context.Context, tx pgx.Tx, a *access, id string) error {
	var name string
	var tokenID *string
	var runner string
	if err := tx.QueryRow(ctx, `SELECT name, token_id::text, runner FROM missions WHERE id = $1`, id).Scan(&name, &tokenID, &runner); err != nil {
		return err
	}
	if runner == runnerBuiltin && tokenID != nil {
		_, err := tx.Exec(ctx, `UPDATE missions SET next_turn_at = now(), turn_error = '', turn_failures = 0 WHERE id = $1`, id)
		return err
	}
	scopes := builtinScopes(a)
	if !slices.Contains(scopes, auth.ScopeMissionsRun) {
		return errDenied("you need the missions.run scope to hand a mission to Outflare's agent")
	}
	label := "Outflare agent"
	var actorID string
	if err := tx.QueryRow(ctx, `INSERT INTO actors (workspace_id, type, display_name) VALUES ($1, 'api_token', $2) RETURNING id`, a.WorkspaceID, label).Scan(&actorID); err != nil {
		return err
	}
	secret, hash := auth.NewToken(auth.PrefixAPIToken)
	var createdBy *string
	if a.UserID != "" {
		createdBy = &a.UserID
	}
	var tok string
	tokenName := label + ": " + name
	if len(tokenName) > 200 {
		tokenName = tokenName[:200]
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO api_tokens (workspace_id, actor_id, name, prefix, token_hash, scopes, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`,
		a.WorkspaceID, actorID, tokenName, secret[:len(auth.PrefixAPIToken)+6], hash, scopes, createdBy).Scan(&tok); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE missions SET runner = 'builtin', token_id = $2, next_turn_at = now(), turn_error = '', turn_failures = 0 WHERE id = $1`, id, tok)
	return err
}

// wakeBuiltin brings the built-in agent's next turn forward to now.
func wakeBuiltin(ctx context.Context, tx pgx.Tx, id string) error {
	_, err := tx.Exec(ctx, `UPDATE missions SET next_turn_at = now() WHERE id = $1 AND runner = 'builtin' AND status = 'running'`, id)
	return err
}

// RunMissions runs the built-in agent's turns as they fall due, until ctx
// ends.
func (s *Server) RunMissions(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if _, err := s.RunDueTurns(ctx); err != nil && ctx.Err() == nil {
			track.Fail(ctx, s.log, track.Event{Name: "mission.runner_failed", ActorType: "system", Surface: "mission"}, "internal", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// RunDueTurns runs every turn that is due now, a few at a time, and
// reports how many ran.
func (s *Server) RunDueTurns(ctx context.Context) (int, error) {
	// Requests for connectors people have since connected close first, so
	// the agents waiting on them take their turn in this pass.
	if _, err := s.resolveConnectedRequests(ctx, ""); err != nil {
		return 0, err
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	n, sem := 0, make(chan struct{}, 4)
	var firstErr error
	for ctx.Err() == nil {
		id, ws, lease, err := s.claimTurn(ctx)
		if err != nil {
			wg.Wait()
			return n, err
		}
		if id == "" {
			break
		}
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer func() { <-sem; wg.Done() }()
			err := s.runTurn(ctx, id, ws, lease)
			mu.Lock()
			defer mu.Unlock()
			n++
			if err != nil && firstErr == nil {
				firstErr = err
			}
		}()
	}
	wg.Wait()
	return n, firstErr
}

func (s *Server) claimTurn(ctx context.Context) (id, ws string, lease time.Time, err error) {
	err = s.pool.QueryRow(ctx, `
		UPDATE missions SET turn_started_at = now(), next_turn_at = now() + $1::interval
		WHERE id = (
			SELECT id FROM missions
			WHERE runner = 'builtin' AND status = 'running' AND next_turn_at <= now()
			  AND (turn_started_at IS NULL OR turn_started_at < now() - $1::interval)
			ORDER BY next_turn_at LIMIT 1 FOR UPDATE SKIP LOCKED)
		RETURNING id, workspace_id, next_turn_at`, fmt.Sprintf("%d seconds", int(turnLease.Seconds()))).Scan(&id, &ws, &lease)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", time.Time{}, nil
	}
	return
}

// runTurn runs one turn of mission id. Problems that are the mission's (no
// Claude key, the spend limit, a revoked token) pause it with a reason a
// person can act on; anything else is retried later.
func (s *Server) runTurn(ctx context.Context, id, ws string, lease time.Time) error {
	ctx = missions.WithID(ctx, id)
	var tokenID *string
	var turns int
	var lastTurn *time.Time
	if err := s.pool.QueryRow(ctx, `SELECT token_id::text, turns, last_turn_at FROM missions WHERE id = $1`, id).Scan(&tokenID, &turns, &lastTurn); err != nil {
		return err
	}
	if tokenID == nil {
		return s.pauseBuiltin(ctx, id, ws, "Outflare's agent lost its token. Approve the mission again to hand it back.")
	}
	p, err := s.tokenPrincipal(ctx, *tokenID)
	if err != nil {
		return s.pauseBuiltin(ctx, id, ws, "Outflare's agent lost its token ("+err.Error()+"). Approve the mission again to hand it back.")
	}
	// Why the turn runs decides the model: a routine check-in can use a
	// cheaper one than a turn that starts work or answers a person.
	trigger, routine, err := s.turnTrigger(ctx, id, ws, turns, lastTurn)
	if err != nil {
		return s.endTurn(ctx, id, lease, agent.Result{}, err)
	}
	task := taskTurn
	if routine {
		task = taskCheckIn
	}
	ai, err := s.aiModel(ctx, ws, task)
	var aiErr errAI
	if errors.As(err, &aiErr) {
		return s.pauseBuiltin(ctx, id, ws, "Outflare's agent can't work: "+aiErr.msg+" Then resume the mission.")
	}
	if err != nil {
		return s.endTurn(ctx, id, lease, agent.Result{}, err)
	}
	if err := s.checkAIBudget(ctx, ws, ai, turnEstimateCents); errors.As(err, &aiErr) {
		return s.pauseBuiltin(ctx, id, ws, aiErr.msg+" Then resume the mission.")
	} else if err != nil {
		return s.endTurn(ctx, id, lease, agent.Result{}, err)
	}
	switch err := missions.CheckSpend(ctx, s.pool, id, turnEstimateCents); {
	case errors.Is(err, missions.ErrSpendLimit):
		return s.pauseBuiltin(ctx, id, ws, "The mission reached its spend limit. Raise the limit, then resume it.")
	case errors.Is(err, missions.ErrNotRunning):
		return s.endTurn(ctx, id, lease, agent.Result{}, nil)
	case err != nil:
		return s.endTurn(ctx, id, lease, agent.Result{}, err)
	}

	caller := missionCaller{s: s, p: p, ws: ws, mission: id}
	code, checkIn, err := caller.Call(ctx, http.MethodGet, "/missions/"+id, nil, "Check in")
	if err != nil || code != http.StatusOK {
		return s.endTurn(ctx, id, lease, agent.Result{}, fmt.Errorf("check-in answered %d: %v", code, err))
	}
	ag := agent.Agent{Model: ai.Model}
	res, turnErr := ag.Turn(ctx, caller, agent.Input{CheckIn: checkIn, Trigger: trigger, Guide: apiGuide(), Spec: spec.OpenAPI})

	// What the turn cost, on the workspace's own bill with the provider, even when it failed.
	mission := id
	if err := recordAISpend(ctx, s.pool, ai, ws, &mission, nil, res.Usage, fmt.Sprintf("mission-turn:%s:%d", id, turns+1),
		fmt.Sprintf("Outflare's agent, %s %d", map[bool]string{false: "turn", true: "check-in, turn"}[routine], turns+1)); err != nil {
		s.log.Error("mission turn spend", "mission", id, "err", err)
	}
	name := ai.Provider.Name
	switch {
	case errors.Is(turnErr, agent.ErrRefused):
		return s.pauseBuiltin(ctx, id, ws, name+" declined to carry on with this mission. Read the plan and the latest notes, change what's needed, then resume it.")
	case errors.Is(turnErr, llm.ErrAuth):
		_ = s.endTurn(ctx, id, lease, res, turnErr)
		return s.pauseBuiltin(ctx, id, ws, name+" did not accept the API key. Reconnect "+name+" under Connectors with a working key, then resume the mission.")
	}
	return s.endTurn(ctx, id, lease, res, turnErr)
}

// endTurn records how a turn went and when the next one runs.
func (s *Server) endTurn(ctx context.Context, id string, lease time.Time, res agent.Result, turnErr error) error {
	ctx = context.WithoutCancel(ctx)
	if turnErr != nil {
		var failures int
		var ws string
		err := s.pool.QueryRow(ctx, `
			UPDATE missions SET turn_started_at = NULL, turn_failures = turn_failures + 1, turn_error = $2,
				next_turn_at = CASE WHEN next_turn_at < $3 THEN next_turn_at ELSE now() + interval '30 minutes' END
			WHERE id = $1 RETURNING turn_failures, workspace_id`, id, turnErr.Error(), lease).Scan(&failures, &ws)
		if err != nil {
			return err
		}
		track.Fail(ctx, s.log, track.Event{Name: "mission.turn_failed", WorkspaceID: ws, ActorType: "agent", Surface: "mission",
			Props: map[string]any{"failures": failures}}, "internal", turnErr)
		if failures >= maxTurnFailures {
			return s.pauseBuiltin(ctx, id, ws, "Outflare's agent failed three turns in a row ("+turnErr.Error()+"). Check the AI model connection, then resume the mission.")
		}
		return nil
	}
	if res.Calls == 0 && res.Summary == "" && res.Usage.InputTokens == 0 {
		// Nothing ran (the mission stopped meanwhile).
		_, err := s.pool.Exec(ctx, `UPDATE missions SET turn_started_at = NULL WHERE id = $1`, id)
		return err
	}
	var next *time.Time
	if !res.Done {
		mins := res.NextCheckInMinutes
		if mins == 0 {
			mins = 60
		}
		mins = max(15, min(mins, 1440))
		t := time.Now().Add(time.Duration(mins) * time.Minute)
		next = &t
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE missions SET turn_started_at = NULL, turns = turns + 1, last_turn_at = now(), turn_summary = $2,
			turn_error = '', turn_failures = 0,
			next_turn_at = CASE WHEN next_turn_at < $3 THEN next_turn_at ELSE $4 END
		WHERE id = $1`, id, res.Summary, lease, next)
	return err
}

// pauseBuiltin pauses a mission the built-in agent can't carry on with.
func (s *Server) pauseBuiltin(ctx context.Context, id, ws, reason string) error {
	ctx = context.WithoutCancel(ctx)
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE missions SET status = 'paused', status_reason = $2, turn_started_at = NULL, next_turn_at = NULL, updated_at = now()
			WHERE id = $1 AND status = 'running'`, id, reason)
		if err != nil || tag.RowsAffected() == 0 {
			_, _ = tx.Exec(ctx, `UPDATE missions SET turn_started_at = NULL WHERE id = $1`, id)
			return err
		}
		if err := addUpdate(ctx, tx, id, ws, "", "system", "note", "Mission paused. "+reason); err != nil {
			return err
		}
		return record(ctx, tx, nil, ws, "mission.paused", "mission", id, map[string]any{"reason": reason, "runner": runnerBuiltin})
	})
}

// turnTrigger says why this turn runs, for the agent, and whether it is
// routine: a scheduled check-in with nothing new from people.
func (s *Server) turnTrigger(ctx context.Context, id, ws string, turns int, last *time.Time) (string, bool, error) {
	if turns == 0 || last == nil {
		return "the mission was just approved; this is your first turn", false, nil
	}
	var wrote, decided, resumed bool
	err := s.pool.QueryRow(ctx, `SELECT
		EXISTS (SELECT 1 FROM mission_updates WHERE mission_id = $1 AND author = 'person' AND created_at > $2),
		EXISTS (SELECT 1 FROM mission_actions WHERE mission_id = $1 AND decided_at > $2),
		EXISTS (SELECT 1 FROM audit_events WHERE workspace_id = $3 AND created_at > $2
			AND action IN ('mission.resumed', 'mission.approved') AND target_type = 'mission' AND target_id = $1::text)`, id, *last, ws).Scan(&wrote, &decided, &resumed)
	switch {
	case err != nil:
		return "", false, err
	case wrote && decided:
		return "a person wrote to you and decided on held changes since your last turn", false, nil
	case wrote:
		return "a person wrote to you since your last turn; answer them first", false, nil
	case decided:
		return "a person approved or rejected held changes since your last turn", false, nil
	case resumed:
		return "a person resumed the mission since your last turn", false, nil
	}
	return "your scheduled check-in", true, nil
}

func (s *Server) tokenPrincipal(ctx context.Context, tokenID string) (*principal, error) {
	p := &principal{ActorType: "api_token"}
	err := s.pool.QueryRow(ctx, `SELECT id, actor_id, workspace_id, scopes FROM api_tokens
		WHERE id = $1 AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at > now())
		  AND NOT EXISTS (SELECT 1 FROM users u WHERE u.id = api_tokens.created_by AND u.disabled_at IS NOT NULL)`, tokenID).
		Scan(&p.TokenID, &p.ActorID, &p.TokenWorkspaceID, &p.TokenScopes)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errors.New("its token was revoked or expired")
	}
	return p, err
}

// missionCaller makes the built-in agent's API calls in-process, as the
// mission's token and inside the mission.
type missionCaller struct {
	s       *Server
	p       *principal
	ws      string
	mission string
}

func (c missionCaller) Call(ctx context.Context, method, path string, body json.RawMessage, reason string) (int, []byte, error) {
	var rd io.Reader
	if len(body) > 0 && method != http.MethodGet && string(body) != "null" {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(context.WithValue(ctx, ctxInjectedKey{}, c.p), method, "/v1/workspaces/"+c.ws+path, rd)
	if err != nil {
		return 0, nil, err
	}
	if rd != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set(missions.Header, c.mission)
	if reason = strings.TrimSpace(reason); reason != "" {
		req.Header.Set(missions.ReasonHeader, strings.ReplaceAll(reason, "\n", " "))
	}
	rec := httptest.NewRecorder()
	c.s.Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes(), nil
}

var apiGuide = sync.OnceValue(func() string { return agent.Guide(spec.OpenAPI) })
