package api

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5"
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
