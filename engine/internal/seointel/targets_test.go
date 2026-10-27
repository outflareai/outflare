package seointel

import "testing"

func TestMentions(t *testing.T) {
	for _, c := range []struct {
		text, keyword string
		want          bool
	}{
		{"Proxy pricing plans", "proxy pricing", true},
		{"Pricing for proxies", "proxy pricing", true},
		{"Residential proxy: plans & prices", "residential proxies", true},
		{"Plans", "proxy plans", false},
		{"", "proxy", false},
		{"Anything", "", true},
		{"Cheap boxes", "box", true},
	} {
		if got := Mentions(c.text, c.keyword); got != c.want {
			t.Errorf("Mentions(%q, %q) = %v", c.text, c.keyword, got)
		}
	}
	if k := Keyword("  Proxy   PRICING "); k != "proxy pricing" {
		t.Errorf("Keyword: %q", k)
	}
}
