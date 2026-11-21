package outflare

import (
	"context"

	"github.com/outflareai/outflare/engine/internal/emailfind"
	"github.com/outflareai/outflare/engine/internal/outcomes"
)

// EmailFind is the extension-facing email finder and verifier. Cloud stages
// (lead generation, enrichment) reach it here because they can only import
// engine/outflare, not the engine's internal packages. Every method takes the
// workspace id the job already holds, never input: finding and verifying read
// and write that workspace's data only.
type EmailFind struct{ h *Host }

// EmailFind returns the email finder/verifier, or nil before the engine has
// started.
func (h *Host) EmailFind() *EmailFind {
	if h.emailFinder == nil {
		return nil
	}
	return &EmailFind{h}
}

// FindEmails proposes an address for each person in the batch from the learned
// pattern and first-party history, and the data network when there is one.
// Free; spends nothing.
func (e *EmailFind) FindEmails(ctx context.Context, workspaceID string, req emailfind.FindRequest) (emailfind.FindResult, error) {
	return e.h.emailFinder.Find(ctx, workspaceID, req)
}

// FindCompanyPattern returns a domain's learned address shape for the
// workspace, building it on first use.
func (e *EmailFind) FindCompanyPattern(ctx context.Context, workspaceID, domain string) (emailfind.Pattern, emailfind.Code, error) {
	return emailfind.FindCompanyPattern(ctx, e.h.emailFinder.Pool, e.h.emailFinder.Resolver, workspaceID, domain)
}

// Resolve runs the full waterfall: free native tiers first, then the
// workspace's own BYO-key verifier only when confidence stays low. Every tier
// is logged to the training corpus.
func (e *EmailFind) Resolve(ctx context.Context, workspaceID string, req emailfind.FindRequest) (emailfind.FindResult, error) {
	return e.h.emailFinder.Resolve(ctx, workspaceID, req)
}

// VerifyEmails checks a batch of addresses with the workspace's verifier,
// mapped to the shared result codes.
func (e *EmailFind) VerifyEmails(ctx context.Context, workspaceID string, req emailfind.VerifyRequest) (emailfind.FindResult, error) {
	return e.h.emailFinder.VerifyEmails(ctx, workspaceID, req)
}

// Asking the workspace's email finders one step at a time, for a feature
// that judges and verifies each address itself before paying for the next
// lookup (docs/email-finding.md, "Asking one step at a time").
type (
	// EmailFindItem is one person to find an address for.
	EmailFindItem = emailfind.FindItem
	// EmailFinderStep is one step the workspace's connected finders offer.
	EmailFinderStep = emailfind.FinderStep
	// EmailStepResult is what one step said about one person.
	EmailStepResult = emailfind.StepResult
)

// FinderSteps lists the steps the workspace's own connected email finders
// can be asked one at a time, in the order to ask them (with Blitz and
// Treg connected: blitz, filtrat, kitt, anymailfinder). A step names the
// connectors that offer it. When the work is for one brand, put it on ctx
// with WithBrand, so only the connections that serve that brand count.
func (e *EmailFind) FinderSteps(ctx context.Context, workspaceID string) ([]emailfind.FinderStep, error) {
	return e.h.emailFinder.FinderSteps(ctx, workspaceID)
}

// AskFinderStep asks one step about one person and returns every address
// the provider offered, unfiltered and unchecked: the caller judges and
// verifies them, and only then decides whether to ask the next step. It
// asks the step's first connector, and the next one that offers the same
// step only when the first could not answer (a refused key, no credits, a
// usage limit, unreachable, or a long wait for its turn); a miss is final.
// It keeps the connection's usage limits and pace, books what the call
// cost in the workspace's spend ledger, and remembers the answer so asking
// again costs nothing (EmailStepResult.Cached).
//
// EmailStepResult.Code is empty for an answer, or EmailPending (ask again
// after RetryAfter), EmailRetry or EmailStop with a StopCode. EmailPending
// with Busy set means the provider behind the step had no room for the call
// and charged nothing: ask the other steps meanwhile and this one again
// later, and never read it as a miss. Asked is
// false when the step doesn't apply to the person (no LinkedIn profile for
// a step that needs one, or no name and domain for the others). As for
// FinderSteps, the brand the work is for comes from ctx (WithBrand).
func (e *EmailFind) AskFinderStep(ctx context.Context, workspaceID, step string, it emailfind.FindItem) (emailfind.StepResult, error) {
	return e.h.emailFinder.AskFinderStep(ctx, workspaceID, step, it)
}

// WithBrand marks ctx as work for one brand, for a background job that
// has no brand route to do it: connections are then chosen among those
// that serve the brand, and what the calls cost is booked to it. brandID
// must come from a row the job already holds for the workspace, never
// from input; an empty one leaves ctx as it is.
func WithBrand(ctx context.Context, brandID string) context.Context {
	return outcomes.WithBrand(ctx, brandID)
}
