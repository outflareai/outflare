package emailfind

import (
	"context"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/verify"
)

// highConfidence is the native confidence at or above which the waterfall
// trusts the learned pattern and returns without spending on a third-party
// verifier.
const highConfidence = 75

// Resolve runs the full waterfall for a batch of people. Free, first-party
// tiers run first: a proven first-party history, then the learned pattern
// checked with the free built-in checks. Only when we cannot prove an address
// and confidence stays low does it roll up to the workspace's own connected
// verifier, which is BYO-key and billed to that workspace's account. Every
// tier is logged to the training corpus. One item never spends another's
// budget: the third-party tier uses this workspace's connection only.
func (s *Service) Resolve(ctx context.Context, ws string, req FindRequest) (FindResult, error) {
	set, err := verify.LoadSettings(ctx, s.Pool, ws)
	if err != nil {
		return FindResult{}, err
	}
	hasVerifier := set.IntegrationID != nil

	native, err := FindEmails(ctx, s.Pool, s.Resolver, ws, req)
	if err != nil {
		return FindResult{}, err
	}
	out := FindResult{Items: make([]ItemResult, 0, len(native.Items))}
	for _, r := range native.Items {
		out.Items = append(out.Items, s.waterfall(ctx, ws, r, hasVerifier))
	}
	return out, nil
}

// waterfall takes one native find result up the tiers.
func (s *Service) waterfall(ctx context.Context, ws string, r ItemResult, hasVerifier bool) ItemResult {
	// Proven by our own history, or nothing to check: native already decided.
	if r.Code == CodeVerified {
		_ = logCheck(ctx, s.Pool, ws, r.Email, "history", connectors.Deliverable, intPtr(r.Confidence), nil)
		return r
	}
	if r.Code == CodeNotFound || r.Email == "" {
		return r
	}
	_ = logCheck(ctx, s.Pool, ws, r.Email, EvidencePattern, "", intPtr(r.Confidence),
		map[string]any{"guess": true})

	// Free built-in check: syntax, MX, disposable, role. Can only prove an
	// address undeliverable, never that it exists.
	bv, err := s.Verify.Builtin.Verify(ctx, nil, nil, r.Email)
	if err == nil {
		_ = logCheck(ctx, s.Pool, ws, r.Email, "builtin", bv.Status, nil,
			map[string]any{"reason": bv.Reason, "catch_all": bv.CatchAll, "role": bv.Role})
		if bv.Status == connectors.Undeliverable {
			r.Code, r.Evidence = CodeUndeliverable, "builtin"
			return r
		}
	}

	// Confident enough on the pattern alone: don't spend.
	if r.Confidence >= highConfidence {
		r.Code = CodeFound
		return r
	}

	// Low confidence and we still can't tell: roll up to the workspace's own
	// verifier if it has one. Otherwise return the best guess as risky.
	if !hasVerifier {
		r.Code = CodeRisky
		return r
	}
	res, err := s.Verify.Verify(ctx, ws, []string{r.Email}, false)
	if err != nil {
		if k := connectors.KindOf(err); k == connectors.ErrAuth || k == connectors.ErrQuota {
			r.Code, r.StopCode = CodeStop, stopCode(k)
			return r
		}
		r.Code, r.RetryAfter = CodeRetry, 0
		return r
	}
	if len(res) == 0 {
		r.Code = CodeRisky
		return r
	}
	v := res[0]
	r.Code = verifyCode(v.Status)
	r.Evidence = "provider:" + v.Provider
	r.Cost = Cost{Provider: v.Provider}
	if v.IntegrationID != nil {
		// One credit an address, as the spend ledger books it.
		r.Cost.Connection, r.Cost.Units = *v.IntegrationID, 1
	}
	if v.Score != nil {
		r.Confidence = *v.Score
	}
	conf := 0
	if v.Score != nil {
		conf = *v.Score
	}
	_ = logCheck(ctx, s.Pool, ws, r.Email, "provider:"+v.Provider, v.Status, intPtr(conf),
		map[string]any{"reason": v.Reason, "catch_all": v.CatchAll, "provider_status": v.ProviderStatus})
	return r
}
