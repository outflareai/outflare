package datanetwork

import (
	"context"
	"errors"
	"strings"

	"github.com/outflareai/outflare/engine/internal/emailfind"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/outcomes"
	"github.com/outflareai/outflare/engine/internal/track"
)

// LookupNetwork asks the network about addresses at a domain for the email
// finder (emailfind.NetworkLookup). A refused or failed lookup is ok=false:
// the finder carries on without the network.
func (s *Service) LookupNetwork(ctx context.Context, ws, domain string, emails []string) (emailfind.NetworkAnswer, bool) {
	n := s.For(ctx, ws)
	domain = strings.ToLower(strings.TrimSpace(domain))
	if n == nil || domain == "" || len(emails) == 0 || outcomes.IsFreeMail(domain) {
		return emailfind.NetworkAnswer{}, false
	}
	ev := track.Event{Name: "data_network.lookup_failed", WorkspaceID: ws, ActorType: "system", Surface: "system"}
	m, err := Load(ctx, s.Pool, ws)
	if err != nil {
		metrics.DataNetworkLookup("error")
		track.Fail(ctx, s.Log, ev, "internal_error", err)
		return emailfind.NetworkAnswer{}, false
	}
	a, err := n.Lookup(ctx, ws, Query{Domain: domain, Emails: emails, Member: m.Status == Joined})
	switch {
	case errors.Is(err, ErrAllowance), errors.Is(err, ErrLimit):
		code, outcome := "data_network_allowance", "refused_allowance"
		if errors.Is(err, ErrLimit) {
			code, outcome = "data_network_limit", "refused_limit"
		}
		metrics.DataNetworkLookup(outcome)
		track.Emit(ctx, track.Event{Name: "data_network.lookup_refused", WorkspaceID: ws, ActorType: "system", Surface: "system",
			Outcome: track.Failure, ErrorCode: code, Props: map[string]any{"member": m.Status == Joined}})
		return emailfind.NetworkAnswer{}, false
	case err != nil:
		metrics.DataNetworkLookup("error")
		track.Fail(ctx, s.Log, ev, "unavailable", err)
		return emailfind.NetworkAnswer{}, false
	}
	out := emailfind.NetworkAnswer{Shape: a.Shape, ShapeConfidence: clamp(a.ShapeConfidence), CatchAll: a.CatchAll,
		Emails: map[string]emailfind.NetworkSignal{}}
	asked := make(map[string]bool, len(emails))
	for _, e := range emails {
		asked[strings.ToLower(e)] = true
	}
	for e, sig := range a.Emails {
		e = strings.ToLower(e)
		if !asked[e] || (sig.Outcome != Delivered && sig.Outcome != Bounced) {
			continue // only what was asked; never more
		}
		out.Emails[e] = emailfind.NetworkSignal{Deliverable: sig.Outcome == Delivered, Confidence: clamp(sig.Confidence)}
	}
	if out.Shape == "" && len(out.Emails) == 0 {
		metrics.DataNetworkLookup("unknown")
	} else {
		metrics.DataNetworkLookup("answered")
	}
	return out, true
}

// Allowance reports the workspace's lookups this month, or ok=false when
// the network isn't turned on for it.
func (s *Service) Allowance(ctx context.Context, ws string, member bool) (Allowance, bool, error) {
	n := s.For(ctx, ws)
	if n == nil {
		return Allowance{}, false, nil
	}
	a, err := n.Allowance(ctx, ws, member)
	return a, err == nil, err
}

func clamp(v int) int { return min(max(v, 0), 100) }
