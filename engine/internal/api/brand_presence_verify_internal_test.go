package api

import (
	"regexp"
	"testing"

	"github.com/outflareai/outflare/engine/internal/crawl"
)

// A page is about the brand when it links to it or names its website; a
// page that only uses the name (another product can share it) is a maybe.
func TestCoverageVerdict(t *testing.T) {
	name := regexp.MustCompile(`(^|[^\pL\pN])northpeak($|[^\pL\pN])`)
	hosts := []string{"northpeak.example"}
	cases := []struct {
		read *crawl.Read
		want string
	}{
		{nil, "unreachable"},
		{&crawl.Read{Status: 404}, "unreachable"},
		{&crawl.Read{Status: 200, Links: []crawl.Link{{URL: "https://www.northpeak.example/pricing"}}}, "links"},
		{&crawl.Read{Status: 200, Text: "Try northpeak.example for proxies"}, "mentions"},
		{&crawl.Read{Status: 200, Title: "Northpeak 2.4 docs", Text: "install the GIS server"}, "name_only"},
		{&crawl.Read{Status: 200, Text: "northpeakish things"}, "absent"},
	}
	for i, c := range cases {
		if got := coverageVerdict(c.read, name, hosts); got != c.want {
			t.Errorf("case %d: %s, want %s", i, got, c.want)
		}
	}
}
