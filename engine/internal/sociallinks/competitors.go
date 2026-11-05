package sociallinks

import (
	"slices"

	"github.com/outflareai/outflare/engine/internal/reviews"
)

// A competitor's profiles: the social profiles and review pages Outflare
// follows for a competitor, kept on the competitor (seo_competitors.links).
// Ones found on its home page are suggested, and only the ones a person
// confirms are read, daily, like the brand's own.

// Profile states.
const (
	Suggested = "suggested"
	Confirmed = "confirmed"
	Dismissed = "dismissed"
)

// Where a competitor's profile came from.
const (
	FromHomePage = "home_page"
	FromPerson   = "manual"
)

// CompetitorLink is one of a competitor's profiles and whether it is
// followed.
type CompetitorLink struct {
	Kind   string `json:"kind"`
	URL    string `json:"url"`
	State  string `json:"state"`
	Source string `json:"source"`
}

// Followable reports whether a link is one Outflare can follow for a
// competitor: a public profile whose counts it can read, or a review page.
func Followable(l Link) bool {
	if reviews.IsPlatform(l.Kind) {
		p, ok := reviews.ParsePage(l.URL)
		return ok && p.Platform == l.Kind
	}
	if !slices.Contains(PublicKinds, l.Kind) {
		return false
	}
	_, ok := PublicProfile(l.Kind, l.URL)
	return ok
}

// Suggest adds the followable links found on a competitor's home page as
// suggestions, one per address, and reports how many are new. A link a
// person confirmed or dismissed keeps its state; a suggestion the page no
// longer shows stays until someone answers it.
func Suggest(list []CompetitorLink, found []Link) ([]CompetitorLink, int) {
	have := map[string]bool{}
	for _, l := range list {
		have[l.URL] = true
	}
	added := 0
	for _, l := range found {
		if have[l.URL] || !Followable(l) {
			continue
		}
		have[l.URL] = true
		list = append(list, CompetitorLink{Kind: l.Kind, URL: l.URL, State: Suggested, Source: FromHomePage})
		added++
	}
	return list, added
}
