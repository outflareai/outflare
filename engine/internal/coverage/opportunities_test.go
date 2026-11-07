package coverage

import "testing"

func TestScore(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	strong, parts := Score("competitor_gap", "https://reviews.example/a-vs-b", "comparison", Factors{DomainRating: f(80), DomainTraffic: f(100000), AICitations: 3, Competitors: 2})
	if parts["seo"] != 30 || parts["ai"] != 21 || parts["competitors"] != 14 || parts["fit"] != 10 || strong != 75 {
		t.Fatalf("strong: %v %v", strong, parts)
	}
	weak, _ := Score("competitor_gap", "https://site.example/page", "", Factors{Competitors: 1})
	if weak != 10 {
		t.Fatalf("weak: %v", weak)
	}
	max, _ := Score("link_removed", "https://reviews.example/review", "review", Factors{DomainRating: f(100), DomainTraffic: f(1e12), AICitations: 10, Competitors: 10})
	if max != 100 {
		t.Fatalf("capped: %v", max)
	}
	// A platform's own numbers count for nothing, and a listing anyone can
	// make keeps 60%.
	listing, parts := Score("competitor_gap", "https://www.youtube.com/channel/acme", "youtube", Factors{DomainRating: f(100), DomainTraffic: f(1e9), Competitors: 2})
	if parts["seo"] != 0 || listing >= strong/2 {
		t.Fatalf("platform: %v %v", listing, parts)
	}
	if Action("unlinked_mention") != "request_link" || Action("nope") != "" {
		t.Fatal("actions")
	}
}
