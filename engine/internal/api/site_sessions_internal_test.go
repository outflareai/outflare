package api

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os/exec"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/auth"
)

func TestParseUAAndBots(t *testing.T) {
	cases := []struct{ ua, device, browser, os string }{
		{"Mozilla/5.0 (iPhone; CPU iPhone OS 18_6 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.6 Mobile/15E148 Safari/604.1", "mobile", "safari", "ios"},
		{"Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Mobile Safari/537.36", "mobile", "chrome", "android"},
		{"Mozilla/5.0 (Linux; Android 14; SM-X710) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36", "tablet", "chrome", "android"},
		{"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36 Edg/141.0.0.0", "desktop", "edge", "windows"},
		{"Mozilla/5.0 (Macintosh; Intel Mac OS X 14.6; rv:132.0) Gecko/20100101 Firefox/132.0", "desktop", "firefox", "macos"},
	}
	for _, c := range cases {
		if d, b, o := parseUA(c.ua); d != c.device || b != c.browser || o != c.os {
			t.Errorf("%s: %s %s %s", c.ua, d, b, o)
		}
		if isBotUA(c.ua) {
			t.Errorf("browser taken for a bot: %s", c.ua)
		}
	}
	for _, ua := range []string{"", "Mozilla/5.0 (compatible; bingbot/2.0; +http://www.bing.com/bingbot.htm)", "curl/8.4.0",
		"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) HeadlessChrome/141.0.0.0 Safari/537.36", "facebookexternalhit/1.1"} {
		if !isBotUA(ua) {
			t.Errorf("bot taken for a browser: %q", ua)
		}
	}
}

func TestSourceMedium(t *testing.T) {
	for _, c := range []struct {
		t              siteTouch
		host, src, med string
	}{
		{siteTouch{source: "newsletter", medium: "email"}, "", "newsletter", "email"},
		{siteTouch{clickIDs: map[string]string{"fbclid": "x"}}, "l.facebook.com", "facebook", "cpc"},
		{siteTouch{}, "news.example", "news.example", "referral"},
		{siteTouch{ref: "PARTNER1"}, "", "partner", "affiliate"},
		{siteTouch{}, "", "direct", "none"},
	} {
		if s, m := sourceMedium(c.t, c.host); s != c.src || m != c.med {
			t.Errorf("%+v %s: %s %s", c.t, c.host, s, m)
		}
	}
}

// A carried visitor id (_ofa) is taken only from a link on another of the
// site's listed domains, and is always removed from the address.
func TestSiteCarriedID(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	now := time.Now().Unix()
	carried := fmt.Sprintf("carried-visitor-1.%d", now)
	for _, tc := range []struct {
		name, referrer, ofa string
		adopt               bool
	}{
		{"from another listed domain", "https://northpeak.example/pricing", carried, true},
		{"without a referrer", "", carried, false},
		{"from an unlisted site", "https://forum.test/thread", carried, false},
		{"from the same domain", "https://www.northpeak-app.example/", carried, false},
		{"expired", "https://northpeak.example/", fmt.Sprintf("carried-visitor-1.%d", now-600), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts, _ := json.Marshal(map[string]string{
				"href":     "https://northpeak-app.example/signup?plan=pro&_ofa=" + tc.ofa,
				"referrer": tc.referrer,
				"domains":  "northpeak.example,northpeak-app.example",
			})
			out, err := exec.Command(node, "testdata/run_site.mjs", "site.js", string(opts)).CombinedOutput()
			if err != nil {
				t.Fatalf("%v: %s", err, out)
			}
			var got struct{ Anon, Href string }
			if err := json.Unmarshal(out, &got); err != nil {
				t.Fatalf("%v: %s", err, out)
			}
			if (got.Anon == "carried-visitor-1") != tc.adopt {
				t.Fatalf("visitor id %q, adopt %v", got.Anon, tc.adopt)
			}
			if got.Href != "https://northpeak-app.example/signup?plan=pro" {
				t.Fatalf("address left: %s", got.Href)
			}
		})
	}
}

// The snippet tries a post again after a network or server error, at most
// three more times, and never after the server refused it.
func TestSiteRetries(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	for _, tc := range []struct {
		statuses []any
		posts    int
	}{
		{[]any{200}, 1},
		{[]any{503, "error", 200}, 3},
		{[]any{"error"}, 4},
		{[]any{429}, 1},
		{[]any{400}, 1},
	} {
		opts, _ := json.Marshal(map[string]any{"href": "https://northpeak.example/", "statuses": tc.statuses})
		out, err := exec.Command(node, "testdata/run_site.mjs", "site.js", string(opts)).CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %s", err, out)
		}
		var got struct{ Posts int }
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
		if got.Posts != tc.posts {
			t.Errorf("answers %v: posted %d times, want %d", tc.statuses, got.Posts, tc.posts)
		}
	}
}

// A visitor's address comes from the site's proxy only with the key's
// proxy secret; anything else is the connecting address.
func TestSiteClientIP(t *testing.T) {
	secret, hash := auth.NewToken(auth.PrefixSiteProxy)
	k := siteKeyRow{proxyHash: hash}
	for _, tc := range []struct {
		name   string
		k      siteKeyRow
		secret string
		ip     string
		want   string
	}{
		{"trusted proxy", k, secret, "203.0.113.9", "203.0.113.9"},
		{"trusted proxy, IPv6", k, secret, " 2001:db8::7 ", "2001:db8::7"},
		{"wrong secret", k, "ofp_test_not_a_real_secret", "203.0.113.9", "192.0.2.1"},
		{"no secret sent", k, "", "203.0.113.9", "192.0.2.1"},
		{"address that doesn't parse", k, secret, "203.0.113.9, 198.51.100.4", "192.0.2.1"},
		{"private address", k, secret, "10.0.0.5", "192.0.2.1"},
		{"loopback", k, secret, "127.0.0.1", "192.0.2.1"},
		{"mapped private address", k, secret, "::ffff:192.168.1.2", "192.0.2.1"},
		{"key without a proxy secret", siteKeyRow{}, secret, "203.0.113.9", "192.0.2.1"},
	} {
		r := httptest.NewRequest("POST", "/v1/site/events", nil)
		r.RemoteAddr = "192.0.2.1:4711"
		if tc.secret != "" {
			r.Header.Set("X-Outflare-Proxy", tc.secret)
		}
		r.Header.Set("X-Outflare-Client-IP", tc.ip)
		if got := siteClientIP(r, tc.k); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}
