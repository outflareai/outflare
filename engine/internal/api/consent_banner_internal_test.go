package api

import (
	"encoding/json"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// siteBannerOut is what testdata/run_site.mjs prints about consent.
type siteBannerOut struct {
	Anon     string            `json:"anon"`
	Choices  []map[string]any  `json:"choices"`
	Consent  map[string]any    `json:"consent"`
	Data     [][]any           `json:"dataLayer"`
	UETQ     []any             `json:"uetq"`
	FBQ      [][]any           `json:"fbq"`
	Ran      []string          `json:"ran"`
	Cookies  map[string]string `json:"cookies"`
	Requests int               `json:"posts"`
	Asks     int               `json:"bannerAsks"`
}

// runSiteBanner runs site.js in the stand-in browser, in a time zone.
func runSiteBanner(t *testing.T, tz string, opt map[string]any) siteBannerOut {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	if opt["href"] == nil {
		opt["href"] = "https://www.northpeak.example/pricing"
	}
	b, _ := json.Marshal(opt)
	cmd := exec.Command(node, "testdata/run_site.mjs", "site.js", string(b))
	cmd.Env = append(os.Environ(), "TZ="+tz)
	raw, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("node: %v\n%s", err, raw)
	}
	var out siteBannerOut
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("%v: %s", err, raw)
	}
	return out
}

// banner is a GET /v1/site/consent answer for an enabled banner.
func testBanner(change func(map[string]any)) map[string]any {
	b := map[string]any{
		"enabled": true, "version": 3, "consent_version": 2, "country": "",
		"regions":    map[string]any{"eea": "opt_in", "us": "opt_out", "other": "none"},
		"frameworks": map[string]any{"gpc": true, "google_consent_mode": true, "meta": true, "tiktok": true, "microsoft": true},
		"categories": map[string]any{"analytics": true, "marketing": true, "preferences": true},
		"layout":     "bar", "position": "bottom", "theme": "light", "colors": map[string]any{"primary": "", "background": "", "text": ""},
		"logo_url": "", "reopen_button": true, "policy_url": "", "reask_after_days": 365, "lang": "en",
		"texts": bannerTextsFor("en", nil), "custom_css": "", "custom_html": "",
	}
	if change != nil {
		change(b)
	}
	return b
}

// consentState finds the last Consent Mode call of a kind ("default" or
// "update") in the dataLayer.
func consentState(data [][]any, kind string) map[string]any {
	var st map[string]any
	for _, a := range data {
		if len(a) == 3 && a[0] == "consent" && a[1] == kind {
			st = a[2].(map[string]any)
		}
	}
	return st
}

func TestSiteConsentBanner(t *testing.T) {
	// In Europe the banner asks first: nothing is allowed, Consent Mode
	// starts denied, no id cookie, and the marketing script waits.
	out := runSiteBanner(t, "Europe/Berlin", map[string]any{"banner": testBanner(nil), "fbq": true,
		"held": []map[string]any{{"category": "marketing", "src": "https://ads.example/pixel.js"}, {"category": "analytics", "text": "stats()"}}})
	def := consentState(out.Data, "default")
	if out.Consent["region"] != "eea" || out.Consent["rule"] != "opt_in" || out.Consent["analytics"] != false || out.Consent["decided"] != false ||
		def == nil || def["ad_storage"] != "denied" || def["analytics_storage"] != "denied" || def["security_storage"] != "granted" || def["wait_for_update"] != 500.0 ||
		out.Cookies["of_aid"] != "" || len(out.Ran) != 0 || len(out.Choices) != 0 {
		t.Fatalf("before a choice: %+v", out)
	}
	if len(out.UETQ) != 3 || out.UETQ[0] != "consent" || out.UETQ[1] != "default" || out.UETQ[2].(map[string]any)["ad_storage"] != "denied" {
		t.Fatalf("uetq: %v", out.UETQ)
	}

	// The visitor allows analytics only: it is kept and sent once, the id
	// cookie is set, and only the analytics script runs.
	out = runSiteBanner(t, "Europe/Berlin", map[string]any{"banner": testBanner(nil), "fbq": true,
		"held":  []map[string]any{{"category": "marketing", "src": "https://ads.example/pixel.js"}, {"category": "analytics", "text": "stats()"}},
		"calls": [][]any{{"consent", map[string]any{"analytics": true}}, {"consent", map[string]any{"analytics": true}}}})
	upd := consentState(out.Data, "update")
	if len(out.Choices) != 1 || out.Choices[0]["analytics"] != true || out.Choices[0]["marketing"] != false || out.Choices[0]["action"] != "api" ||
		out.Choices[0]["region"] != "eea" || out.Choices[0]["consent_version"] != 2.0 || out.Choices[0]["anonymousId"] != out.Anon || out.Choices[0]["path"] != "/pricing" {
		t.Fatalf("choices sent: %v", out.Choices)
	}
	if !strings.HasPrefix(out.Cookies["of_consent"], "2.100.") || out.Cookies["of_aid"] != out.Anon || upd["analytics_storage"] != "granted" || upd["ad_user_data"] != "denied" ||
		len(out.Ran) != 1 || out.Ran[0] != "stats()" {
		t.Fatalf("after allowing analytics: %+v", out)
	}
	if n := len(out.FBQ); n == 0 || out.FBQ[n-1][1] != "revoke" {
		t.Fatalf("fbq: %v", out.FBQ)
	}

	// A choice made on this consent version stands; one made before it is
	// asked again, and so is one older than reask_after_days.
	now := strconv.FormatInt(time.Now().Unix(), 10)
	if out := runSiteBanner(t, "Europe/Berlin", map[string]any{"banner": testBanner(nil), "cookie": "of_consent=2.111." + now}); out.Consent["decided"] != true ||
		out.Consent["marketing"] != true || consentState(out.Data, "default")["ad_user_data"] != "granted" {
		t.Fatalf("a current choice: %+v", out)
	}
	if out := runSiteBanner(t, "Europe/Berlin", map[string]any{"banner": testBanner(nil), "cookie": "of_consent=1.111." + now}); out.Consent["decided"] != false || out.Consent["marketing"] != false {
		t.Fatalf("a choice on an older version: %+v", out.Consent)
	}
	old := strconv.FormatInt(time.Now().Add(-400*24*time.Hour).Unix(), 10)
	if out := runSiteBanner(t, "Europe/Berlin", map[string]any{"banner": testBanner(nil), "cookie": "of_consent=2.111." + old}); out.Consent["decided"] != false {
		t.Fatalf("a choice over a year old: %+v", out.Consent)
	}

	// The country from the site's proxy wins over the time zone.
	if out := runSiteBanner(t, "America/New_York", map[string]any{"banner": testBanner(func(b map[string]any) { b["country"] = "FR" })}); out.Consent["region"] != "eea" {
		t.Fatalf("country: %v", out.Consent)
	}

	// In the US, everything is on until the visitor opts out, except
	// marketing for a browser sending Global Privacy Control.
	out = runSiteBanner(t, "America/Los_Angeles", map[string]any{"banner": testBanner(nil), "gpc": true, "fbq": true})
	if out.Consent["region"] != "us" || out.Consent["rule"] != "opt_out" || out.Consent["analytics"] != true || out.Consent["marketing"] != false ||
		out.Cookies["of_aid"] != out.Anon || consentState(out.Data, "default")["ad_storage"] != "denied" {
		t.Fatalf("US with GPC: %+v", out)
	}
	if out := runSiteBanner(t, "America/Chicago", map[string]any{"banner": testBanner(nil)}); out.Consent["marketing"] != true {
		t.Fatalf("US without GPC: %v", out.Consent)
	}
	// The workspace can turn GPC, and Consent Mode, off.
	out = runSiteBanner(t, "America/Chicago", map[string]any{"gpc": true, "banner": testBanner(func(b map[string]any) {
		b["frameworks"].(map[string]any)["gpc"] = false
		b["frameworks"].(map[string]any)["google_consent_mode"] = false
		b["frameworks"].(map[string]any)["microsoft"] = false
	})})
	if out.Consent["marketing"] != true || len(out.Data) != 0 || out.UETQ != nil {
		t.Fatalf("frameworks off: %+v", out)
	}
	// A category the banner doesn't ask about isn't sent.
	out = runSiteBanner(t, "Europe/Paris", map[string]any{"calls": [][]any{{"consent", true}}, "banner": testBanner(func(b map[string]any) {
		b["categories"].(map[string]any)["preferences"] = false
	})})
	if _, ok := out.Choices[0]["preferences"]; ok || !strings.HasPrefix(out.Cookies["of_consent"], "2.112.") {
		t.Fatalf("unasked category: %v %v", out.Choices, out.Cookies)
	}

	// Without a banner the script behaves as before: data-consent="auto"
	// waits in Europe until the site's own banner says yes, which is kept
	// in Outflare too, and no Consent Mode is set for the site.
	out = runSiteBanner(t, "Europe/Berlin", map[string]any{"consent": "auto", "calls": [][]any{{"consent", true}}})
	if out.Consent["analytics"] != true || out.Cookies["of_aid"] != out.Anon || len(out.Choices) != 1 || out.Choices[0]["marketing"] != true || len(out.Data) != 0 {
		t.Fatalf("own banner: %+v", out)
	}
	if out := runSiteBanner(t, "Europe/Berlin", map[string]any{"consent": "auto"}); out.Consent["analytics"] != false || out.Cookies["of_aid"] != "" {
		t.Fatalf("auto in Europe: %+v", out)
	}
	// A visitor who allowed it on an earlier page keeps their id.
	if out := runSiteBanner(t, "Europe/Berlin", map[string]any{"consent": "auto", "cookie": "of_aid=visitor-1234"}); out.Anon != "visitor-1234" || out.Consent["analytics"] != true {
		t.Fatalf("earlier consent: %+v", out)
	}
	// Withdrawing removes the id cookie.
	if out := runSiteBanner(t, "Europe/Berlin", map[string]any{"consent": "auto", "cookie": "of_aid=visitor-1234", "calls": [][]any{{"consent", false}}}); out.Cookies["of_aid"] != "" ||
		!strings.HasPrefix(out.Cookies["of_consent"], "1.000.") {
		t.Fatalf("withdrawn: %+v", out)
	}
	// data-banner="off" never asks for the settings or shows the banner.
	if out := runSiteBanner(t, "America/Chicago", map[string]any{"banner": testBanner(nil)}); out.Asks != 1 || out.Requests != 1 {
		t.Fatalf("settings asked %d times, %d page views", out.Asks, out.Requests)
	}
	if out := runSiteBanner(t, "Europe/Berlin", map[string]any{"banner": testBanner(nil), "bannerOff": true, "consent": "auto"}); out.Asks != 0 || len(out.Data) != 0 ||
		out.Consent["rule"] != "opt_in" {
		t.Fatalf("data-banner off: %+v", out)
	}
}
