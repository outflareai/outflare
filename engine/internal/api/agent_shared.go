package api

import (
	"sync"
	"time"

	spec "github.com/outflareai/outflare/api"
	"github.com/outflareai/outflare/engine/internal/agent"
	"github.com/outflareai/outflare/engine/internal/auth"
)

// What Outflare's own agent shares across its agent runs and playbook
// automations (missions, which used these first, are retired).

const (
	// Who carries an automation's runs out: Outflare's agent (builtin).
	// external is refused since missions retired; an automation set up
	// with it before is held when it next comes due.
	runnerExternal = "external"
	runnerBuiltin  = "builtin"
	// Longest a turn may take before another engine may take the run.
	turnLease = 20 * time.Minute
	// What a turn is assumed to cost when checking the AI limit first.
	turnEstimateCents = 25
)

// builtinScopes are what Outflare's agent's token may hold: what an
// operator can do, less approving, and never more than the person who
// starts it.
func builtinScopes(a *access) []string {
	var out []string
	for _, sc := range auth.RoleScopes(auth.RoleOperator) {
		if sc != auth.ScopeMissionsApprove && a.has(sc) {
			out = append(out, sc)
		}
	}
	return out
}

// author says who wrote a note: an agent (a token) or a person.
func author(a *access) string {
	if a.TokenID != "" {
		return "agent"
	}
	return "person"
}

// apiGuide is the API guide the agent reads, built once from the contract.
var apiGuide = sync.OnceValue(func() string { return agent.Guide(spec.OpenAPI) })
