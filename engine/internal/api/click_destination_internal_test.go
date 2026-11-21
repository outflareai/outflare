package api

import (
	"net/url"
	"strings"
	"testing"
)

// One builder decides what a tracker click's destination carries
// (clickDestination): the click's ofclk once, the campaign's tags once, a
// network's own parameter as written, and everything else untouched.
func TestClickDestination(t *testing.T) {
	const key, other = "abcdefghjkmnpqrs", "tuvwxyz234567892"
	target := redirectTarget{campaign: trackingCampaignView{Slug: "spring", Code: "campcode23", Source: "acme", Medium: "paid"}}
	click := bufferedClick{key: key, sourceClickID: "net-77", fields: map[string]string{"1": "a b"}}
	build := func(raw, k string) *url.URL {
		t.Helper()
		out := clickDestination(target, trackingLinkView{URL: raw, Content: "blue"}, click, "0.25", k)
		u, err := url.Parse(out)
		if err != nil {
			t.Fatalf("%s built %q: %v", raw, out, err)
		}
		// Built again from its own result, for the same click, it is the
		// same URL.
		if again := clickDestination(target, trackingLinkView{URL: out, Content: "blue"}, click, "0.25", k); again != out {
			t.Errorf("%s isn't stable:\n first %s\n again %s", raw, out, again)
		}
		return u
	}
	once := func(u *url.URL, name, want string) {
		t.Helper()
		if got := u.Query()[name]; len(got) != 1 || got[0] != want {
			t.Errorf("%s: %s = %v, want one %q", u, name, got, want)
		}
	}
	managed := func(u *url.URL) {
		t.Helper()
		once(u, "ofclk", key)
		once(u, "ofc", "campcode23")
		once(u, "utm_source", "acme")
		once(u, "utm_medium", "paid")
		once(u, "utm_campaign", "spring")
		once(u, "utm_content", "blue")
	}

	for name, raw := range map[string]string{
		"a bare page":                 "https://northpeak.example/buy",
		"the click id as ofclk":       "https://northpeak.example/buy?ofclk={click_id}",
		"an ofclk saved before":       "https://northpeak.example/buy?ofclk=" + other,
		"an empty ofclk saved before": "https://northpeak.example/buy?ofclk=&x=1",
		"two of them":                 "https://northpeak.example/buy?ofclk={click_id}&ofclk={click_id}",
		"tags of its own":             "https://northpeak.example/buy?utm_source=mine&utm_campaign=old&ofc=zzzzzzzzzz&utm_id=9",
	} {
		managed(build(raw, key))
		_ = name
	}

	// A network's own parameter keeps its mapping, and the network's click
	// id stays apart from Outflare's.
	u := build("https://offers.northpeak.example/a?o=7&subid={click_id}&ext={source_click_id}&f={field1}&cost={cost}", key)
	managed(u)
	once(u, "subid", key)
	once(u, "ext", "net-77")
	once(u, "f", "a b")
	once(u, "cost", "0.25")
	once(u, "o", "7")

	// Everything else stays as written: repeated parameters, encoded
	// values, and the fragment at the end.
	u = build("https://northpeak.example/buy?tag=a&tag=b&q=caf%C3%A9+au+lait&next=https%3A%2F%2Fnorthpeak.example%2Fx%3Fy%3D1#plans", key)
	managed(u)
	if got := u.Query()["tag"]; len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("repeated parameter: %v", got)
	}
	once(u, "q", "café au lait")
	once(u, "next", "https://northpeak.example/x?y=1")
	if u.Fragment != "plans" || !strings.HasPrefix(u.RawQuery, "tag=a&tag=b&q=caf%C3%A9+au+lait&") {
		t.Errorf("the rest changed: %s", u)
	}

	// A click that wasn't counted carries no key, and none it could be
	// mistaken for.
	if u := build("https://northpeak.example/buy?ofclk={click_id}&sub={click_id}", ""); u.Query().Has("ofclk") || u.Query().Get("ofc") != "campcode23" {
		t.Errorf("an uncounted click: %s", u)
	}

	// Two clicks keep their own keys.
	a := build("https://northpeak.example/buy?subid={click_id}", key)
	click.key = other
	b := clickDestination(target, trackingLinkView{URL: "https://northpeak.example/buy?subid={click_id}"}, click, "", other)
	if bu, _ := url.Parse(b); a.Query().Get("ofclk") != key || bu.Query().Get("ofclk") != other || bu.Query().Get("subid") != other {
		t.Errorf("two clicks: %s and %s", a, b)
	}
}

// A destination can say ofclk={click_id}, which is what the redirect adds
// anyway; any other ofclk would name a different click, and is refused.
func TestCheckClickKeyParam(t *testing.T) {
	for _, ok := range []string{
		"https://northpeak.example/buy",
		"https://northpeak.example/buy?subid={click_id}&x=ofclk",
		"https://northpeak.example/buy?ofclk={click_id}",
		"https://northpeak.example/buy?a=1&ofclk=%7Bclick_id%7D#ofclk=1",
		"https://northpeak.example/buy?myofclk=1&ofclk2=2",
	} {
		if err := checkDestinationTokens("url", ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{
		"https://northpeak.example/buy?ofclk=abcdefghjkmnpqrs",
		"https://northpeak.example/buy?ofclk=",
		"https://northpeak.example/buy?ofclk",
		"https://northpeak.example/buy?a=1&ofclk={source_click_id}",
		"https://northpeak.example/buy?ofclk={click_id}&ofclk=other",
		"https://northpeak.example/buy?ofclk={click_id|none}",
		"https://northpeak.example/buy?%6ffclk=1",
	} {
		if err := checkDestinationTokens("url", bad); err == nil || !strings.Contains(err.Error(), "url: ofclk is the click id Outflare adds") {
			t.Errorf("%s: %v, want a refusal", bad, err)
		}
	}
}

// The warning reads the URL a redirect sends and the offer's setting.
func TestClickIDVia(t *testing.T) {
	own := []string{"northpeak.example", "go.northpeak.example"}
	for _, c := range []struct{ delivery, raw, tracking, want string }{
		{"auto", "https://northpeak.example/buy", "https://northpeak.example/buy", "ofclk"},
		{"auto", "https://shop.northpeak.example/buy", "https://shop.northpeak.example/buy", "ofclk"},
		{"auto", "https://notnorthpeak.example/buy", "https://notnorthpeak.example/buy", "none"},
		{"auto", "https://network.example/o", "https://network.example/o", "none"},
		{"auto", "https://network.example/o?s={click_id}", "https://network.example/o?s={click_id}", "template"},
		{"auto", "https://network.example/o", "https://network.example/o?aff_sub={click_id}", "network"},
		{"first_party", "https://pages.example/buy", "https://pages.example/buy", "ofclk"},
		{"affiliate", "https://northpeak.example/buy", "https://northpeak.example/buy", "none"},
		{"affiliate", "https://network.example/o?s={click_id}", "https://network.example/o?s={click_id}", "template"},
		{"unattributed", "https://network.example/o?s={click_id}", "https://network.example/o?s={click_id}", "none"},
	} {
		if got := clickIDVia(c.delivery, c.raw, c.tracking, own); got != c.want {
			t.Errorf("%s %s: %s, want %s", c.delivery, c.raw, got, c.want)
		}
	}
}
