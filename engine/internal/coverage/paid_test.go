package coverage

import (
	"slices"
	"testing"
)

func TestPaidEvidence(t *testing.T) {
	cases := []struct {
		page, title, link string
		sponsored         bool
		want              []string
	}{
		{"https://news.example/2026/best-crm-tools", "The best CRM tools", "https://rival.example/", false, []string{}},
		{"https://news.example/sponsored/rival-launch", "Rival launches", "", false, []string{"address_sponsored"}},
		{"https://news.example/a?utm_medium=Affiliate", "", "", false, []string{"address_utm_affiliate"}},
		{"https://blog.example/post", "Sponsored: why teams switch", "", false, []string{"title_sponsored"}},
		{"https://blog.example/post", "", "https://rival.example/?via=blogger", false, []string{"link_affiliate"}},
		{"https://blog.example/post", "", "https://rival.example/?utm_medium=cpc", true, []string{"link_sponsored", "link_utm_cpc"}},
		{"not a url", "", "", false, []string{}},
	}
	for _, c := range cases {
		got := PaidEvidence(c.page, c.title, c.link, c.sponsored)
		if !slices.Equal(got, c.want) {
			t.Errorf("PaidEvidence(%q, %q, %q) = %v, want %v", c.page, c.title, c.link, got, c.want)
		}
	}
}
