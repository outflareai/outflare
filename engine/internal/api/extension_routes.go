package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/seosync"
)

// Workspace routes for extensions. An extension (the hosted Cloud) serves
// its own workspace features behind the same sign-in, workspace check,
// scopes, brand limits, missions and idempotency as the engine's routes,
// and records its changes as the caller.

// WorkspaceRoute is an extension's route under /v1/workspaces/{ws}/...
type WorkspaceRoute struct {
	// Pattern is a net/http pattern with {ws}, such as
	// "POST /v1/workspaces/{ws}/cloud/lead-finder/runs".
	Pattern string
	// Scope is what the caller needs, such as "lists.manage".
	Scope string
	// Handler serves the request. Brand-limited callers are let in, so it
	// keeps them to their brands with Caller.Sees and Caller.Writes.
	Handler func(w http.ResponseWriter, r *http.Request, c *Caller)
	// Brand puts the route under /v1/brands/{brand}/...: the engine finds
	// the brand's workspace, answers 404 to a caller who can't see the
	// brand, and sets Caller.Brand.
	Brand bool
}

// Caller is who is making a workspace request, and what they may do.
type Caller struct {
	WorkspaceID string
	// ActorID is the person's or token's actor; ActorType "human" or
	// "api_token".
	ActorID, ActorType string
	// UserID is the person, also for their API tokens; empty for a token
	// whose maker has left.
	UserID string
	// Role is the person's workspace role, empty for tokens.
	Role string
	// Brands limits the caller to some brands; nil is every brand.
	Brands []string
	// Brand is the brand a brand route is on (WorkspaceRoute.Brand), and
	// empty on a workspace route.
	Brand string
	// Test is true in a test workspace.
	Test bool

	a *access
	s *Server
}

func (s *Server) callerOf(a *access) *Caller {
	return &Caller{WorkspaceID: a.WorkspaceID, ActorID: a.ActorID, ActorType: a.ActorType, UserID: a.personID(),
		Role: a.Role, Brands: a.Brands, Brand: a.Brand, Test: a.Test, a: a, s: s}
}

// Has reports whether the caller has a scope.
func (c *Caller) Has(scope string) bool { return c.a.has(scope) }

// Sees reports whether the caller may see something of a brand (nil for
// shared).
func (c *Caller) Sees(brand *string) bool { return c.a.sees(brand) }

// Writes reports whether the caller may change something of a brand (nil
// for shared). A brand-limited caller only reads shared things.
func (c *Caller) Writes(brand *string) bool { return c.a.writes(brand) }

// Record writes an audit event for the caller in tx, which is also the
// product event of the same name once the request succeeds.
func (c *Caller) Record(ctx context.Context, tx pgx.Tx, action, targetType, targetID string, data map[string]any) error {
	return record(ctx, tx, &c.a.principal, c.WorkspaceID, action, targetType, targetID, data)
}

// MissionDraft is a mission an extension starts for the caller, such as a
// loop's brief. Like any mission it does nothing until a person approves it,
// and everything it does goes through the API's gate, limits and audit log.
type MissionDraft struct {
	Name, Goal, Instructions, Plan string
	// Mode is dry_run (the default), approve_writes or autonomous.
	Mode          string
	MaxSpendCents *int64
	EndsAt        *time.Time
}

// ErrMissionInvalid is a draft the API would refuse with 400; its message
// says why. ErrMissionDenied is a caller who may not start missions (403).
var (
	ErrMissionInvalid = errors.New("invalid mission")
	ErrMissionDenied  = errors.New("not allowed to start missions")
	// ErrMissionRetired is every StartMission's error now: missions are
	// retired (410 mission_retired over the API).
	ErrMissionRetired = errors.New("missions retired")
)

// missionError keeps the API's message under the exported kinds.
type missionError struct {
	kind error
	msg  string
}

func (e missionError) Error() string { return e.msg }
func (e missionError) Unwrap() error { return e.kind }

// StartMission is retired with missions: it always returns
// ErrMissionRetired. Loops start agent runs with StartAgentRun instead.
// It stays so extensions built against it keep compiling until it is
// removed.
func (c *Caller) StartMission(ctx context.Context, tx pgx.Tx, d MissionDraft) (string, error) {
	return "", missionError{ErrMissionRetired, "missions are retired: nothing starts a new one; start an agent run instead (Caller.StartAgentRun)"}
}

// AgentRunDraft is an agent run an extension starts for the caller, such
// as a loop's brief, as POST /v1/workspaces/{ws}/playbooks/{id}/agent-runs
// does for a playbook: Outflare's agent carries it out in turns as an
// agent token of its own on the action layer, and the mode sets what it
// may do on its own. It replaces MissionDraft for new loops.
type AgentRunDraft struct {
	Name, Goal, Instructions, Plan string
	// BrandID keeps the run and its token to one brand; required for a
	// brand-limited caller.
	BrandID *string
	// Mode is copilot (the default: every change waits for a person),
	// autonomous (changes run within MaxSpendCents) or dry_run.
	Mode          string
	MaxSpendCents *int64
	// Note is the run's first update; empty for "Started by …".
	Note string
}

// ErrAgentRunInvalid is a draft the API would refuse with 400; its message
// says why. ErrAgentRunDenied is a caller who may not start runs (403).
var (
	ErrAgentRunInvalid = errors.New("invalid agent run")
	ErrAgentRunDenied  = errors.New("not allowed to start agent runs")
)

// StartAgentRun starts an agent run for the caller in tx: the caller needs
// agents.run, the run gets a token and grants from its mode, its first turn
// is due at once, and agent_run.created is recorded. It returns the run's
// id.
func (c *Caller) StartAgentRun(ctx context.Context, tx pgx.Tx, d AgentRunDraft) (string, error) {
	if !c.Has(auth.ScopeAgentsRun) {
		return "", missionError{ErrAgentRunDenied, "starting an agent run needs the agents.run scope"}
	}
	if d.Mode == "" {
		d.Mode = runModeCopilot
	}
	id, err := c.s.createAgentRun(ctx, tx, c.a, runSpec{Name: d.Name, Goal: d.Goal, Instructions: d.Instructions, Plan: d.Plan,
		BrandID: d.BrandID, Mode: d.Mode, MaxSpendCents: d.MaxSpendCents, Note: d.Note})
	var inv errInvalid
	var denied errDenied
	switch {
	case errors.As(err, &inv):
		return "", missionError{ErrAgentRunInvalid, string(inv)}
	case errors.As(err, &denied):
		return "", missionError{ErrAgentRunDenied, string(denied)}
	case errors.Is(err, ErrAgentSpec):
		return "", missionError{ErrAgentRunInvalid, err.Error()}
	}
	return id, err
}

func exportMissionError(err error) error {
	var inv errInvalid
	var denied errDenied
	switch {
	case errors.As(err, &inv):
		return missionError{ErrMissionInvalid, string(inv)}
	case errors.As(err, &denied):
		return missionError{ErrMissionDenied, string(denied)}
	}
	return err
}

// Internal logs err and answers 500 without saying what went wrong.
func (c *Caller) Internal(w http.ResponseWriter, r *http.Request, err error) { c.s.internal(w, r, err) }

// Decode reads a JSON body, refusing unknown fields, and answers 400 when
// it cannot.
func (c *Caller) Decode(w http.ResponseWriter, r *http.Request, v any) bool { return decode(w, r, v) }

// WriteError writes the standard error body: {"error": {"code", "message",
// "request_id"}}.
func WriteError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	writeError(w, r, status, code, message)
}

// WriteJSON writes v as JSON with a status.
func WriteJSON(w http.ResponseWriter, status int, v any) { writeJSON(w, status, v) }

func (s *Server) routesExtensions(mux *http.ServeMux) {
	for _, rt := range s.workspaceRoutes {
		h := rt.Handler
		serve := func(w http.ResponseWriter, r *http.Request) {
			h(w, r, s.callerOf(accessFrom(r.Context())))
		}
		next := s.inBrand(rt.Scope, serve)
		if rt.Brand {
			next = s.onBrand(rt.Scope, serve)
		}
		mux.HandleFunc(rt.Pattern, func(w http.ResponseWriter, r *http.Request) {
			noteExtension(r)
			next(w, r)
		})
	}
}

type ctxExtensionKey struct{}

// noteExtension tells the package's tests that an extension served the
// request: its routes are documented with the extension, not in
// api/openapi.yaml.
func noteExtension(r *http.Request) {
	if n, ok := r.Context().Value(ctxExtensionKey{}).(*bool); ok {
		*n = true
	}
}

// AddLinkProspects adds pages to site's link prospect list in tx, as the
// caller, recording link_prospect.found for each new one. The caller must
// be able to change site's brand; the pages are already checked
// (seosync.ProspectPage, a known method, not on the site itself).
func (c *Caller) AddLinkProspects(ctx context.Context, tx pgx.Tx, site seosync.ProspectSite, source string, list []seosync.Prospect) (seosync.ProspectsAdded, error) {
	if !c.Writes(site.BrandID) {
		return seosync.ProspectsAdded{}, errSharedRecord
	}
	return seosync.AddProspects(ctx, tx, c.WorkspaceID, site, source, actorRef(c.a), c.s.prospectRecorder(c.a), list)
}
