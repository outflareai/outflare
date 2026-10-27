package emailfind

import (
	"context"
	"math"
	"strings"

	"github.com/outflareai/outflare/engine/internal/connectors"
)

// confThreshold is the confidence at or above which a guessed address is
// returned as found rather than risky.
const confThreshold = 50

// Pattern is a domain's learned shape, returned by FindCompanyPattern.
type Pattern struct {
	Domain     string `json:"domain"`
	TopShape   string `json:"top_shape"`
	Confidence int    `json:"confidence"`
	Provider   string `json:"provider"`
	CatchAll   bool   `json:"catch_all"`
	SampleSize int    `json:"sample_size"`
}

// FindCompanyPattern returns a domain's learned address shape for this
// workspace, building it on first use. The Code is found when we have a
// confident shape, risky for a catch-all or thin domain, not_found when there
// is nothing to go on.
func FindCompanyPattern(ctx context.Context, q Querier, res Resolver, ws, domain string) (Pattern, Code, error) {
	p, ok, err := load(ctx, q, ws, domain)
	if err != nil {
		return Pattern{}, CodeStop, err
	}
	if !ok {
		if p, err = Rebuild(ctx, q, res, ws, domain); err != nil {
			return Pattern{}, CodeStop, err
		}
	}
	pat := Pattern{Domain: p.Domain, TopShape: p.TopShape, Confidence: p.Confidence,
		Provider: p.Provider, CatchAll: p.CatchAll, SampleSize: p.SampleSize}
	switch {
	case p.TopShape == "":
		return pat, CodeNotFound, nil
	case p.CatchAll || p.Confidence < confThreshold:
		return pat, CodeRisky, nil
	default:
		return pat, CodeFound, nil
	}
}

// FindEmails proposes an address for each person in the batch. It is
// idempotent in effect: it only reads and (re)derives profiles, so a repeat
// with the same input returns the same answer. It spends nothing.
func FindEmails(ctx context.Context, q Querier, res Resolver, ws string, req FindRequest) (FindResult, error) {
	out := FindResult{Items: make([]ItemResult, 0, len(req.Items))}
	profiles := map[string]Profile{} // domain -> profile, cached across the batch
	for _, it := range req.Items {
		out.Items = append(out.Items, findOne(ctx, q, res, ws, it, profiles))
	}
	return out, nil
}

func findOne(ctx context.Context, q Querier, res Resolver, ws string, it FindItem, profiles map[string]Profile) ItemResult {
	r := ItemResult{Ref: it.Ref, RecordID: it.RecordID, Cost: Cost{Provider: "outflare"}}
	domain := strings.ToLower(strings.TrimSpace(it.Domain))
	domain = strings.TrimPrefix(domain, "www.")
	cands := candidatesFor(it.FirstName, it.LastName)
	if domain == "" || len(cands) == 0 {
		r.Code, r.Evidence = CodeNotFound, EvidenceDefault
		return r
	}

	// First-party history: has any candidate for this person already been
	// checked for this workspace? A deliverable one is the answer; an
	// undeliverable one is dropped from the guess set.
	hist, err := history(ctx, q, ws, domain, cands)
	if err != nil {
		r.Code, r.StopCode = CodeStop, "internal_error"
		return r
	}
	live := cands[:0]
	for _, c := range cands {
		switch hist[c.local] {
		case connectors.Deliverable:
			r.Email, r.Confidence, r.Code, r.Evidence = c.local+"@"+domain, 95, CodeVerified, EvidenceHistory
			return r
		case connectors.Undeliverable:
			// drop: a shape we know is wrong here
		default:
			live = append(live, c)
		}
	}
	if len(live) == 0 {
		r.Code, r.Evidence = CodeNotFound, EvidencePattern
		return r
	}

	p, ok := profiles[domain]
	if !ok {
		if lp, found, lerr := load(ctx, q, ws, domain); lerr == nil && found {
			p = lp
		} else if rb, rerr := Rebuild(ctx, q, res, ws, domain); rerr == nil {
			p = rb
		} else {
			p = Profile{Domain: domain, Shapes: map[string]tally{}}
		}
		profiles[domain] = p
	}

	best, bestScore := live[0], -1.0
	for _, c := range live {
		if s := p.shapeScore(c.shape); s > bestScore {
			best, bestScore = c, s
		}
	}
	conf := int(math.Round(bestScore * 100))
	if p.CatchAll && conf > confThreshold {
		conf = confThreshold // acceptance on a catch-all proves nothing
	}
	r.Email, r.Confidence = best.local+"@"+domain, conf
	r.Evidence = EvidencePattern
	if p.SampleSize == 0 {
		r.Evidence = EvidenceDefault
	}
	switch {
	case p.CatchAll || conf < confThreshold:
		r.Code = CodeRisky
	default:
		r.Code = CodeFound
	}
	return r
}

// history returns the stored verification status for each candidate local part
// at the domain, for this workspace. Keys are local parts.
func history(ctx context.Context, q Querier, ws, domain string, cands []candidate) (map[string]string, error) {
	emails := make([]string, len(cands))
	for i, c := range cands {
		emails[i] = c.local + "@" + domain
	}
	rows, err := q.Query(ctx, `
		SELECT email, status FROM email_verifications
		WHERE workspace_id = $1 AND email = ANY($2)`, ws, emails)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var email, status string
		if err := rows.Scan(&email, &status); err != nil {
			return nil, err
		}
		out[localPart(email)] = status
	}
	return out, rows.Err()
}
