package linkorders

import (
	"regexp"
	"strings"

	"github.com/outflareai/outflare/engine/internal/crawl"
)

// Anchor match results stored in last_check_anchor_match.
const (
	AnchorExact     = "exact"
	AnchorPartial   = "partial"
	AnchorDifferent = "different"
)

var anchorNonWord = regexp.MustCompile(`[^\p{L}\p{N}]+`)

func anchorWords(s string) []string {
	return strings.Fields(anchorNonWord.ReplaceAllString(strings.ToLower(s), " "))
}

// MatchAnchor compares link text seen on the donor page with the ordered
// anchors. It returns "" when nothing was ordered or there is no text.
func MatchAnchor(seen string, ordered ...string) string {
	got := strings.Join(anchorWords(seen), " ")
	if got == "" {
		return ""
	}
	best := ""
	for _, o := range ordered {
		want := strings.Join(anchorWords(o), " ")
		if want == "" {
			continue
		}
		if got == want {
			return AnchorExact
		}
		if strings.Contains(" "+got+" ", " "+want+" ") || strings.Contains(" "+want+" ", " "+got+" ") || wordOverlap(got, want) >= 0.5 {
			best = AnchorPartial
		} else if best == "" {
			best = AnchorDifferent
		}
	}
	return best
}

// wordOverlap is the share of the ordered anchor's words found in the seen text.
func wordOverlap(got, want string) float64 {
	seen := map[string]bool{}
	for _, w := range strings.Fields(got) {
		seen[w] = true
	}
	ws := strings.Fields(want)
	if len(ws) == 0 {
		return 0
	}
	n := 0
	for _, w := range ws {
		if seen[w] {
			n++
		}
	}
	return float64(n) / float64(len(ws))
}

// bestAnchorLink picks, among links to the target, the one whose text is
// closest to an ordered anchor, so a page with a nav link and the bought
// link in the article is judged by the bought one.
func bestAnchorLink(links []*crawl.Link, ordered ...string) *crawl.Link {
	rank := map[string]int{AnchorExact: 3, AnchorPartial: 2, AnchorDifferent: 1, "": 0}
	var best *crawl.Link
	bestRank := -1
	for _, l := range links {
		if r := rank[MatchAnchor(l.Anchor, ordered...)]; r > bestRank {
			best, bestRank = l, r
		}
	}
	return best
}
