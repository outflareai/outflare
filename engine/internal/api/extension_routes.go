package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
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
	// Test is true in a test workspace.
	Test bool

	a *access
	s *Server
}

func (s *Server) callerOf(a *access) *Caller {
	return &Caller{WorkspaceID: a.WorkspaceID, ActorID: a.ActorID, ActorType: a.ActorType, UserID: a.personID(),
		Role: a.Role, Brands: a.Brands, Test: a.Test, a: a, s: s}
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
)

// missionError keeps the API's message under the exported kinds.
type missionError struct {
	kind error
	msg  string
}

func (e missionError) Error() string { return e.msg }
func (e missionError) Unwrap() error { return e.kind }

// StartMission creates a mission draft for the caller in tx, as POST
// /v1/workspaces/{ws}/missions does: the caller needs missions.run, it
// records mission.created, and it waits for a person's approval. It
// returns the mission's id.
func (c *Caller) StartMission(ctx context.Context, tx pgx.Tx, d MissionDraft) (string, error) {
	if !c.Has(auth.ScopeMissionsRun) {
		return "", missionError{ErrMissionDenied, "starting a mission needs the missions.run scope"}
	}
	in := missionInput{Name: &d.Name, Goal: &d.Goal, Instructions: &d.Instructions, Plan: &d.Plan,
		MaxSpendCents: optional[int64]{Set: d.MaxSpendCents != nil, Value: d.MaxSpendCents}}
	if d.EndsAt != nil {
		at := d.EndsAt.Format(time.RFC3339)
		in.EndsAt = optional[string]{Set: true, Value: &at}
	}
	if d.Mode != "" {
		in.Mode = &d.Mode
	}
	if err := in.validate(); err != nil {
		return "", exportMissionError(err)
	}
	id, err := c.s.insertMission(ctx, tx, c.a, &in)
	return id, exportMissionError(err)
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
		next := s.inBrand(rt.Scope, func(w http.ResponseWriter, r *http.Request) {
			h(w, r, s.callerOf(accessFrom(r.Context())))
		})
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
