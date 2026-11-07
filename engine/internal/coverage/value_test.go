package coverage

import "testing"

func f(v float64) *float64 { return &v }
func b(v bool) *bool       { return &v }

func TestAccessOf(t *testing.T) {
	cases := []struct {
		name string
		l    Link
		want string
	}{
		{"google listing", Link{URL: "https://www.google.com/maps/place/rival", DomainRating: f(100)}, AccessFree},
		{"own youtube upload", Link{URL: "https://youtube.com/watch?v=1", Title: "Rival setup tutorial", DomainRating: f(100)}, AccessFree},
		{"someone's youtube review", Link{URL: "https://youtube.com/watch?v=2", Title: "Rival review after 6 months"}, AccessEarned},
		{"profile", Link{URL: "https://x.com/rivalhq"}, AccessFree},
		{"someone's post", Link{URL: "https://x.com/someone/status/123"}, AccessEarned},
		{"forum", Link{URL: "https://forum.example/t/which-proxy"}, AccessFree},
		{"directory", Link{URL: "https://alternativeto.net/software/rival"}, AccessFree},
		{"paid", Link{URL: "https://reviews.example/best-proxies", Paid: true}, AccessPaid},
		{"comparison", Link{URL: "https://reviews.example/rival-vs-acme", DomainRating: f(55), DomainTraffic: f(20000)}, AccessEarned},
		{"strong editorial", Link{URL: "https://bigmag.example/best-proxies", DomainRating: f(88), DomainTraffic: f(2_000_000), Nofollow: b(false)}, AccessCantBuy},
		{"news", Link{URL: "https://paper.example/news/rival-raises"}, AccessCantBuy},
		{"university", Link{URL: "https://cs.uni.edu/resources"}, AccessCantBuy},
		{"wikipedia", Link{URL: "https://en.wikipedia.org/wiki/Proxy_server"}, AccessCantBuy},
		{"unknown", Link{URL: "https://site.example/page"}, AccessUnknown},
	}
	for _, c := range cases {
		if got := AccessOf(c.l); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
}

// The owner's complaint: google.com and youtube.com (DR 100) topped the
// list. A relevant earned comparison on a real site must beat them.
func TestValueRanksEarnedOverPlatforms(t *testing.T) {
	topic := NewTopic([]string{"residential proxies", "rotating proxy", "datacenter proxies", "proxy for scraping", "web scraping api"}, []string{"Rival", "Geonode"})
	google := Value(Link{URL: "https://www.google.com/url?q=rival", DomainRating: f(100), DomainTraffic: f(5e9), Topic: topic})
	youtube := Value(Link{URL: "https://www.youtube.com/channel/rival", DomainRating: f(100), DomainTraffic: f(3e9), Topic: topic})
	comparison := Value(Link{URL: "https://reviews.example/best-residential-proxies", Title: "The 9 best residential proxies, tested",
		DomainRating: f(62), URLRating: f(30), DomainTraffic: f(80_000), Nofollow: b(false), Competitors: 6, Topic: topic})
	farm := Value(Link{URL: "https://linkfarm.example/proxy-tools", DomainRating: f(70), DomainTraffic: f(40), Topic: topic})
	if comparison.Value <= google.Value || comparison.Value <= youtube.Value {
		t.Fatalf("comparison %v should beat google %v and youtube %v", comparison, google, youtube)
	}
	if comparison.Access != AccessEarned || google.Access != AccessFree {
		t.Fatalf("access: %v %v", comparison.Access, google.Access)
	}
	if farm.Value >= comparison.Value {
		t.Fatalf("a high rating with no visits shouldn't rank: %v", farm)
	}
	want := map[string]bool{"on your topic": true, "features 6 competitors": true, "DR 62": true, "80k visits a month": true}
	for _, r := range comparison.Reasons {
		delete(want, r)
	}
	if len(want) > 0 {
		t.Fatalf("missing reasons %v in %v", want, comparison.Reasons)
	}
}

func TestTopicLeavesOutNames(t *testing.T) {
	topic := NewTopic([]string{"geonode proxies", "geonode pricing", "residential proxy"}, []string{"Geonode"})
	if topic.Match("https://geonode.example/") != 0 || topic.Match("Best proxies for scraping") != 1 {
		t.Fatalf("topic words: %v", topic.words)
	}
}
