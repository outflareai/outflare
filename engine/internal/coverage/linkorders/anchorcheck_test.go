package linkorders

import (
	"testing"

	"github.com/outflareai/outflare/engine/internal/crawl"
)

func TestMatchAnchor(t *testing.T) {
	for _, c := range []struct {
		seen    string
		ordered []string
		want    string
	}{
		{"Residential Proxies", []string{"residential proxies", ""}, AnchorExact},
		{"  residential-proxies! ", []string{"Residential proxies"}, AnchorExact},
		{"Northpeak", []string{"residential proxies", "northpeak"}, AnchorExact},
		{"the best residential proxies for scraping", []string{"residential proxies"}, AnchorPartial},
		{"proxies", []string{"residential proxies"}, AnchorPartial},
		{"click here", []string{"residential proxies", "Northpeak"}, AnchorDifferent},
		{"click here", []string{"", ""}, ""},
		{"", []string{"residential proxies"}, ""},
	} {
		if got := MatchAnchor(c.seen, c.ordered...); got != c.want {
			t.Errorf("MatchAnchor(%q, %q) = %q, want %q", c.seen, c.ordered, got, c.want)
		}
	}
}

// Among several links to the target, the bought one (closest to the
// ordered anchor) is judged, not the first one on the page.
func TestScoreCheckPicksTheOrderedAnchor(t *testing.T) {
	ev := CheckEvidence{Health: "up", Links: []crawl.Link{
		{URL: "https://northpeak.example/other", Anchor: "home"},
		{URL: "https://northpeak.example/page", Anchor: "Northpeak", Rel: []string{"nofollow"}},
		{URL: "https://northpeak.example/page/", Anchor: "Residential proxies"},
	}}
	live, match, rel, anchor, am := scoreCheck("https://northpeak.example/page", ev, "residential proxies", "")
	if !live || !match || rel != "" || anchor != "Residential proxies" || am != AnchorExact {
		t.Fatalf("got %v %v %q %q %q", live, match, rel, anchor, am)
	}
	_, _, _, anchor, am = scoreCheck("https://northpeak.example/page", ev, "buy proxies")
	if am != AnchorPartial && am != AnchorDifferent {
		t.Fatalf("got %q for %q", am, anchor)
	}
	// No link to the target: no anchor verdict.
	if _, match, _, _, am := scoreCheck("https://northpeak.example/missing", ev, "residential proxies"); match || am != "" {
		t.Fatalf("missing target: %v %q", match, am)
	}
}
