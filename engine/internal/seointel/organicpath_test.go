package seointel

import "testing"

func TestNormalizeOrganicPath(t *testing.T) {
	cases := map[string]string{
		"":                                 "/",
		"/":                                "/",
		"/Pricing/":                        "/pricing",
		"https://www.example.com/Pricing/": "/pricing",
		"/blog/what-is-a-proxy?ref=1":      "/blog/what-is-a-proxy",
		"Free-Proxy-List":                  "/free-proxy-list",
	}
	for in, want := range cases {
		if got := NormalizeOrganicPath(in); got != want {
			t.Errorf("NormalizeOrganicPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCanonicalOrganicPath(t *testing.T) {
	cases := map[string]string{
		"/fr/pricing":             "/pricing",
		"/FR/Pricing/":            "/pricing",
		"/zh":                     "/",
		"/free-proxy-list":        "/free-proxy-list",
		"/from-us":                "/from-us",
		"/pt/proxies-for/gaming":  "/proxies-for/gaming",
		"https://example.com/ja/": "/",
	}
	for in, want := range cases {
		if got := CanonicalOrganicPath(in); got != want {
			t.Errorf("CanonicalOrganicPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestOrganicHost(t *testing.T) {
	if got := OrganicHost("www.Example.com:443", ""); got != "www.example.com" {
		t.Fatalf("host: %q", got)
	}
	if got := OrganicHost("", "https://www.example.com/pricing"); got != "www.example.com" {
		t.Fatalf("from url: %q", got)
	}
}
