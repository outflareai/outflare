package coverage

import (
	"context"
	"errors"
	"math"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Suggestion is a place worth the team's effort: a page to be included
// on, a mention to turn into a link, a link to win back. Modules suggest
// them (Presence from competitor gaps and its own placements, AI
// visibility from the pages answers cite) and Suggest ranks them by
// leverage, SEO and AI answers together.
type Suggestion struct {
	URL, Title string
	// Kind is a placement type; "" guesses it from the address.
	Kind string
	// Reason: competitor_gap, ai_cited, unlinked_mention or link_removed.
	Reason string
	// Source is the suggesting module: presence, ai_visibility.
	Source string
	// Evidence: ids and fixed labels only (competitor ids, a placement id,
	// a cohort id), never page text.
	Evidence map[string]any
	Factors  Factors
	// PlacementID is the brand's placement it is about, for the link
	// reasons.
	PlacementID string
}

// Factors are the numbers behind a suggestion's score. Unknown numbers
// stay nil and score nothing.
type Factors struct {
	DomainRating  *float64 `json:"domain_rating,omitempty"`
	DomainTraffic *float64 `json:"domain_traffic,omitempty"`
	// AICitations is how often AI answers to the brand's questions cited
	// the site in the last 90 days.
	AICitations int `json:"ai_citations"`
	// Competitors is how many of the brand's competitors appear there.
	Competitors int `json:"competitors"`
}

// ErrReason means the suggestion's reason is not one of the four.
var ErrReason = errors.New("coverage: unknown opportunity reason")

var actions = map[string]string{"competitor_gap": "pitch", "ai_cited": "pitch", "unlinked_mention": "request_link", "link_removed": "reclaim_link"}

// Action is what to do next for a reason: pitch, request_link or
// reclaim_link.
func Action(reason string) string { return actions[reason] }

// Score is a suggestion's leverage from 0 to 100, with each part's
// points: seo (domain rating and traffic, up to 35), ai (citations in AI
// answers, up to 35), competitors (how many are there, up to 20) and fit
// (the kind of page, up to 10). A page the brand already has a
// relationship with gets up to 10 more, since it is the easiest win.
//
// A page on a platform anyone can post to (google.com, youtube.com) gets
// no seo points from the platform's numbers, and one that is free to get
// (a profile, a listing, a forum post: AccessOf) keeps 60% of its score,
// the same judgement Value makes of a competitor's links.
func Score(reason, rawURL, kind string, f Factors) (float64, map[string]float64) {
	parts := map[string]float64{}
	platform := Platform(rawURL)
	if f.DomainRating != nil && !platform {
		parts["seo"] += math.Max(0, math.Min(100, *f.DomainRating)) / 100 * 25
	}
	if f.DomainTraffic != nil && *f.DomainTraffic > 0 && !platform {
		parts["seo"] += math.Min(10, math.Log10(*f.DomainTraffic+1)*2)
	}
	parts["ai"] = math.Min(35, float64(f.AICitations)*7)
	parts["competitors"] = math.Min(20, float64(f.Competitors)*7)
	switch kind {
	case "comparison", "roundup", "review", "directory", "mcp_directory", "app_listing":
		parts["fit"] = 10
	case "blog", "news", "youtube", "forum", "influencer", "newsletter", "podcast", "video", "integration", "partner":
		parts["fit"] = 6
	default:
		parts["fit"] = 3
	}
	if reason == "unlinked_mention" || reason == "link_removed" {
		parts["relationship"] = 10
	}
	total := 0.0
	for k, v := range parts {
		v = math.Round(v*10) / 10
		parts[k] = v
		total += v
	}
	if AccessOf(Link{URL: rawURL, Kind: kind, DomainRating: f.DomainRating, DomainTraffic: f.DomainTraffic}) == AccessFree && reason != "link_removed" && reason != "unlinked_mention" {
		parts["free_to_get"] = -math.Round(total*0.4*10) / 10
		total += parts["free_to_get"]
	}
	return math.Min(100, math.Round(total*10)/10), parts
}

// Suggest keeps a suggestion for the brand in the caller's transaction,
// or refreshes the one it already has for that page and reason: its
// evidence, factors and score move, and a dismissed one stays dismissed.
// One the brand had won opens again. It returns the opportunity's id and
// whether it was new.
func Suggest(ctx context.Context, tx pgx.Tx, ws, brand string, s Suggestion) (string, bool, error) {
	action := Action(s.Reason)
	if action == "" {
		return "", false, ErrReason
	}
	canonical, err := Canonical(s.URL)
	if err != nil || len(canonical) > 2048 {
		return "", false, ErrNoURL
	}
	u, _ := url.Parse(canonical)
	host := strings.TrimPrefix(u.Hostname(), "www.")
	kind := s.Kind
	if kind == "" {
		kind = Classify(canonical)
	}
	source := s.Source
	if source == "" {
		source = "presence"
	}
	score, parts := Score(s.Reason, canonical, kind, s.Factors)
	factors := map[string]any{"ai_citations": s.Factors.AICitations, "competitors": s.Factors.Competitors, "points": parts,
		"access": AccessOf(Link{URL: canonical, Kind: kind, DomainRating: s.Factors.DomainRating, DomainTraffic: s.Factors.DomainTraffic})}
	if s.Factors.DomainRating != nil {
		factors["domain_rating"] = *s.Factors.DomainRating
	}
	if s.Factors.DomainTraffic != nil {
		factors["domain_traffic"] = *s.Factors.DomainTraffic
	}
	evidence := s.Evidence
	if evidence == nil {
		evidence = map[string]any{}
	}
	var placement *string
	if s.PlacementID != "" {
		placement = &s.PlacementID
	}
	title := []rune(strings.TrimSpace(s.Title))
	if len(title) > 500 {
		title = title[:500]
	}
	var id string
	var created bool
	err = tx.QueryRow(ctx, `INSERT INTO placement_opportunities AS o (workspace_id,brand_id,url,host,title,kind,reason,action,source,evidence,factors,score,placement_id)
 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
 ON CONFLICT (workspace_id,brand_id,url,reason) DO UPDATE SET
  title=CASE WHEN excluded.title<>'' THEN excluded.title ELSE o.title END,
  kind=CASE WHEN o.kind='' THEN excluded.kind ELSE o.kind END,
  evidence=excluded.evidence, factors=excluded.factors, score=excluded.score,
  placement_id=coalesce(excluded.placement_id,o.placement_id),
  state=CASE WHEN o.state='won' THEN 'open' ELSE o.state END,
  state_at=CASE WHEN o.state='won' THEN now() ELSE o.state_at END,
  last_seen_at=now(), updated_at=now()
 RETURNING id::text, xmax=0`, ws, brand, canonical, host, string(title), kind, s.Reason, action, source, evidence, factors, score, placement).Scan(&id, &created)
	return id, created, err
}
