package seosync

import "testing"

func TestSpamScore(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	codes := func(fs []SpamFactor) map[string]int {
		out := map[string]int{}
		for _, x := range fs {
			out[x.Code] = x.Points
		}
		return out
	}
	// A strong, ordinary link is clean.
	if s, fs := SpamScore(SpamInput{SourceDomain: "news.example.org", Anchor: "Northpeak", SourceRating: f(70)}); s != 0 || len(fs) != 0 {
		t.Fatalf("clean: %d %v", s, fs)
	}
	// The provider's own judgement counts as it is.
	if s, _ := SpamScore(SpamInput{SourceDomain: "a.example", SourceRating: f(40), Providers: map[string]providerView{"dataforseo": {Spam: f(72)}}}); s != 72 || SpamLevel(s) != "toxic" {
		t.Fatalf("provider: %d", s)
	}
	// Outflare's checks add up, and say why.
	s, fs := SpamScore(SpamInput{SourceDomain: "cheap-links.xyz", Anchor: "Best CASINO bonus", SourceRating: f(2),
		Providers: map[string]providerView{"ahrefs": {ExternalLinks: f(800)}}, SameDomain: 150})
	c := codes(fs)
	if s != 100 || c["weak_domain"] != 25 || c["spam_tld"] != 15 || c["spam_anchor"] != 40 || c["link_farm"] != 35 || c["sitewide"] != 10 {
		t.Fatalf("checks: %d %v", s, fs)
	}
	if s, _ := SpamScore(SpamInput{SourceDomain: "blog.example", SourceRating: f(8), Providers: map[string]providerView{"ahrefs": {ExternalLinks: f(200)}}}); SpamLevel(s) != "clean" || s != 25 {
		t.Fatalf("weak: %d", s)
	}
}
