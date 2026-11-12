package paidads

import (
	"context"
	"sync/atomic"
)

// Approver says whether a change of a kind may be made on a brand's ad
// account without a person approving it.
type Approver func(ctx context.Context, workspaceID, brandID, kind string) bool

// approver is the hook set at start-up (SetAutoApprove).
var approver atomic.Pointer[Approver]

// SetAutoApprove installs the one hook that lets a finding apply without
// a person: an extension's Host.SetAdAutoApprove, wired at start-up. nil
// removes it. The open engine has none, so every change to an ad account
// waits for a person; tests install one to exercise the path.
func SetAutoApprove(fn Approver) {
	if fn == nil {
		approver.Store(nil)
		return
	}
	approver.Store(&fn)
}

// AutoApproving reports whether a hook is installed.
func AutoApproving() bool { return approver.Load() != nil }

// AutoApprove is the single decision point for applying a finding
// without a person: the savings the rules find (adsuggest), the
// protective overspend pause (adsync) and an agent's approval of a saving
// (the API, through Approver.Allows with the same function). It answers
// false without a hook, for a finding with no brand (an account every
// brand shares), and for any kind that can spend more: the hook is only
// ever asked about savings (Saves).
func AutoApprove(ctx context.Context, workspaceID, brandID, kind string) bool {
	p := approver.Load()
	if p == nil {
		return false
	}
	return (*p).Allows(ctx, workspaceID, brandID, kind)
}

// Allows is AutoApprove's rule with fn as the hook: false for a nil fn, a
// brandless finding or a kind that isn't a saving, else what fn says.
func (fn Approver) Allows(ctx context.Context, workspaceID, brandID, kind string) bool {
	if fn == nil || brandID == "" || !Saves(kind) {
		return false
	}
	return fn(ctx, workspaceID, brandID, kind)
}
