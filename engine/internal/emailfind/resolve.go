package emailfind

import (
	"context"
	"time"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/verify"
)

// highConfidence is the native confidence at or above which the waterfall
// trusts the learned pattern and returns without spending on a third-party
// verifier.
const highConfidence = 75

// Resolve runs the full waterfall for a batch of people. Free, first-party
// tiers run first: a proven first-party history, then the data network
// when the server has one, then the learned pattern. When those leave only
// a guess below highConfidence, or no address at all, the workspace's own
// connected email finder is asked (finder.go). The address that comes out,
// a finder's or a guess, gets the free built-in checks, and rolls up to the
// workspace's own connected verifier when it is still unconfirmed. Finder
// and verifier are BYO-key and billed to that workspace's account. Every
// tier is logged to the training corpus. One item never spends another's
// budget: the third-party tiers use this workspace's connections only.
//
// A repeat for the same person does not spend again: a finder's answer is
// remembered per person (enrichment_cache) and a verifier's per address
// (email_verifications).
func (s *Service) Resolve(ctx context.Context, ws string, req FindRequest) (FindResult, error) {
	set, err := verify.LoadSettings(ctx, s.Pool, ws)
	if err != nil {
		return FindResult{}, err
	}
	hasVerifier := set.IntegrationID != nil

	native, err := s.Find(ctx, ws, req)
	if err != nil {
		return FindResult{}, err
	}
	found, err := s.askFinders(ctx, ws, req.Items, native.Items)
	if err != nil {
		return FindResult{}, err
	}
	out := FindResult{Items: make([]ItemResult, 0, len(native.Items))}
	for i, r := range native.Items {
		var fa *finderAnswer
		if i < len(found) {
			fa = found[i]
		}
		out.Items = append(out.Items, s.resolveOne(ctx, ws, r, hasVerifier, fa))
	}
	return out, nil
}

// waterfall takes one native find result up the tiers, when no finder
// answered for the person.
func (s *Service) waterfall(ctx context.Context, ws string, r ItemResult, hasVerifier bool) ItemResult {
	// Proven by our own history, or nothing to check: native already decided.
	if r.Code == CodeVerified {
		_ = logCheck(ctx, s.Pool, ws, r.Email, "history", connectors.Deliverable, intPtr(r.Confidence), nil)
		return r
	}
	if r.Code == CodeNotFound || r.Code == CodeUndeliverable || r.Email == "" {
		return r
	}
	if r.Evidence != EvidenceNetwork { // the network's answer is logged already
		_ = logCheck(ctx, s.Pool, ws, r.Email, EvidencePattern, "", intPtr(r.Confidence),
			map[string]any{"guess": true})
	}

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
		r.Code, r.RetryAfter = CodeRetry, verifyRetryAfter(err)
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
		r.Cost.Connection, r.Cost.Units, r.Cost.Micros = *v.IntegrationID, 1, micros(v.CostMicros)
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

// verifyRetryAfter is how long to wait before checking an address again
// after the verifier failed: what a busy verifier (a rate limit answer)
// said, and at least verifyBusyWait, or zero (the caller's own backoff)
// for any other failure. A busy verifier is never a stop: nothing needs a
// person.
func verifyRetryAfter(err error) time.Duration {
	if wait, busy := connectors.IsBusy(err); busy {
		return max(wait, verifyBusyWait)
	}
	return 0
}

// verifyBusyWait is the shortest wait after a busy verifier.
const verifyBusyWait = 30 * time.Second
