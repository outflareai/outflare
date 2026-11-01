package outflare

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/outflareai/outflare/engine/internal/policy"
)

// RecipientDecision is the engine's purpose-aware recipient policy verdict.
type RecipientDecision = policy.Decision

// EmailPolicyQuerier is satisfied by a pool or a transaction. Extensions should
// check inside their send transaction and again immediately before dispatch.
type EmailPolicyQuerier interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

// CheckEmailRecipients applies the engine's recipient policy to an extension's
// email. It honors shared blocks, bounced/complained addresses, review holds and
// the contact/consent rules for purpose (cold, lifecycle, transactional). The
// workspace must come from an authorized caller or the job's persisted owner.
// This neither grants access nor sends mail; extensions also enforce their own
// sending budgets, mailbox eligibility and provider restrictions.
func CheckEmailRecipients(ctx context.Context, q EmailPolicyQuerier, workspaceID, purpose string, emails []string) ([]RecipientDecision, error) {
	switch purpose {
	case policy.PurposeCold, policy.PurposeLifecycle, policy.PurposeTransactional:
	default:
		return nil, fmt.Errorf("unsupported email purpose")
	}
	if workspaceID == "" {
		return nil, fmt.Errorf("workspace is required")
	}
	return policy.CheckContacts(ctx, q, workspaceID, purpose, emails)
}
