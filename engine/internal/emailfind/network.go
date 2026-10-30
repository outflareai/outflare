package emailfind

import (
	"context"
	"strings"
)

// Find proposes an address for each person like FindEmails, then asks the
// data network (when the server has one) about people the workspace's own
// data can't settle. It spends nothing; a network lookup counts against the
// workspace's network allowance, not its money.
func (s *Service) Find(ctx context.Context, ws string, req FindRequest) (FindResult, error) {
	out, err := FindEmails(ctx, s.Pool, s.Resolver, ws, req)
	if err != nil {
		return out, err
	}
	s.assist(ctx, ws, req.Items, out.Items)
	return out, nil
}

// assist asks the network about each result the workspace's own data
// didn't prove or confidently settle. FindEmails returns one result per
// item, in order.
func (s *Service) assist(ctx context.Context, ws string, items []FindItem, res []ItemResult) {
	if s.Network == nil {
		return
	}
	for i := range res {
		if i >= len(items) {
			break
		}
		r := &res[i]
		switch {
		case r.Code == CodeVerified, r.Code == CodeNotFound, r.Code == CodeStop:
			continue // proven, nothing to build, or broken
		case r.Evidence == EvidencePattern && r.Confidence >= highConfidence:
			continue // the workspace's own data settles it; save the lookup
		}
		domain := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(items[i].Domain)), "www.")
		cands := candidatesFor(items[i].FirstName, items[i].LastName)
		if domain == "" || len(cands) == 0 {
			continue
		}
		emails := make([]string, len(cands))
		for j, c := range cands {
			emails[j] = c.local + "@" + domain
		}
		a, ok := s.Network.LookupNetwork(ctx, ws, domain, emails)
		if !ok {
			continue
		}
		if signal := applyNetwork(r, cands, domain, a); signal != "" {
			_ = logCheck(ctx, s.Pool, ws, r.Email, EvidenceNetwork, networkResult(r.Code), intPtr(r.Confidence),
				map[string]any{"signal": signal, "catch_all": a.CatchAll})
		}
	}
}

// applyNetwork folds the network's answer into a result and says what it
// used: "address" (another workspace's outcome for an address), "shape"
// (the domain's shape) or "" when the workspace's own guess stands. The
// network only ever answers about candidates for the person asked about.
func applyNetwork(r *ItemResult, cands []candidate, domain string, a NetworkAnswer) string {
	bounced := map[string]bool{}
	best, bestConf := "", -1
	for _, c := range cands {
		e := c.local + "@" + domain
		sig, ok := a.Emails[e]
		switch {
		case !ok:
		case !sig.Deliverable:
			if sig.Confidence >= confThreshold {
				bounced[e] = true
			}
		case sig.Confidence > bestConf:
			best, bestConf = e, sig.Confidence
		}
	}
	use := func(e string, conf int, signal string) string {
		if a.CatchAll && conf > confThreshold {
			conf = confThreshold // delivered on a catch-all proves nothing
		}
		if e == r.Email {
			conf = max(conf, r.Confidence)
		} else if conf <= r.Confidence && !bounced[r.Email] {
			return "" // the workspace's own guess is at least as sure
		}
		r.Email, r.Confidence, r.Evidence = e, conf, EvidenceNetwork
		r.Code = CodeFound
		if a.CatchAll || conf < confThreshold {
			r.Code = CodeRisky
		}
		return signal
	}
	if best != "" {
		if s := use(best, bestConf, "address"); s != "" {
			return s
		}
	}
	if a.Shape != "" {
		for _, c := range cands {
			if e := c.local + "@" + domain; c.shape == a.Shape && !bounced[e] {
				if s := use(e, a.ShapeConfidence, "shape"); s != "" {
					return s
				}
				break
			}
		}
	}
	if bounced[r.Email] {
		// The workspace's guess bounced for others and nothing better is
		// known.
		r.Code, r.Evidence = CodeUndeliverable, EvidenceNetwork
		return "address"
	}
	return ""
}

func networkResult(c Code) string {
	switch c {
	case CodeFound:
		return "deliverable"
	case CodeUndeliverable:
		return "undeliverable"
	}
	return "risky"
}

// verifyByNetwork answers the addresses the network is confident about,
// and returns the rest for the workspace's verifier. One lookup per
// address.
func (s *Service) verifyByNetwork(ctx context.Context, ws string, emails []string, refs map[string]string) ([]ItemResult, []string) {
	if s.Network == nil {
		return nil, emails
	}
	var answered []ItemResult
	rest := make([]string, 0, len(emails))
	for _, e := range emails {
		at := strings.LastIndexByte(e, '@')
		if at < 1 {
			rest = append(rest, e)
			continue
		}
		a, ok := s.Network.LookupNetwork(ctx, ws, e[at+1:], []string{e})
		sig, known := a.Emails[e]
		if !ok || !known || sig.Confidence < highConfidence || (sig.Deliverable && a.CatchAll) {
			rest = append(rest, e)
			continue
		}
		it := ItemResult{Ref: refs[e], Email: e, Confidence: sig.Confidence, Code: CodeFound,
			Evidence: EvidenceNetwork, Cost: Cost{Provider: "outflare"}}
		if !sig.Deliverable {
			it.Code = CodeUndeliverable
		}
		_ = logCheck(ctx, s.Pool, ws, e, EvidenceNetwork, networkResult(it.Code), intPtr(it.Confidence),
			map[string]any{"signal": "address"})
		answered = append(answered, it)
	}
	return answered, rest
}
