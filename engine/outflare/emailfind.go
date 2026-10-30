package outflare

import (
	"context"

	"github.com/outflareai/outflare/engine/internal/emailfind"
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
