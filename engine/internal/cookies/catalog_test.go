package cookies

import (
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestCatalogIsWellFormed(t *testing.T) {
	ids := map[string]bool{}
	for _, v := range catalog {
		if v.ID == "" || v.Name == "" || ids[v.ID] {
			t.Errorf("vendor without an id or name, or twice: %+v", v)
		}
		ids[v.ID] = true
		if !slices.Contains(Categories, v.Category) {
			t.Errorf("%s: category %q", v.ID, v.Category)
		}
		for _, it := range append(v.Cookies, v.Storage...) {
			if it.Name == "" || (it.Category != "" && !slices.Contains(Categories, it.Category)) {
				t.Errorf("%s: item %+v", v.ID, it)
			}
		}
	}
}

func TestLookup(t *testing.T) {
	for _, c := range []struct{ kind, name, vendor, category string }{
		{KindCookie, "_ga", "google_analytics", Analytics},
		{KindCookie, "_ga_ABC123", "google_analytics", Analytics},
		{KindCookie, "_gcl_au", "google_ads", Marketing},
		{KindCookie, "_fbp", "meta_pixel", Marketing},
		{KindCookie, "ph_phc_abc_posthog", "posthog", Analytics},
		{KindCookie, "of_consent", "outflare", Necessary},
		{KindCookie, "_shopify_y", "shopify", Analytics},
		{KindCookie, "cart", "shopify", Necessary},
		{KindCookie, "session_id", "", Unknown},
		{KindLocalStorage, "ajs_user_traits", "segment", Analytics},
		{KindScript, "www.googletagmanager.com/gtm.js", "google_tag_manager", Necessary},
		{KindScript, "www.googletagmanager.com/gtag", "google_analytics", Analytics},
		{KindScript, "connect.facebook.net/en_US", "meta_pixel", Marketing},
		{KindScript, "www.google.com/recaptcha", "recaptcha", Necessary},
		{KindIframe, "www.youtube.com/embed", "youtube", Marketing},
		{KindScript, "cdn.northpeak.example/app.js", "", Unknown},
	} {
		if m := Lookup(c.kind, c.name); m.Vendor != c.vendor || m.Category != c.category {
			t.Errorf("%s %s: %+v", c.kind, c.name, m)
		}
	}
	if wildcard("_ga_*", "_gid") || !wildcard("ph_*_posthog", "ph_x_posthog") || wildcard("ph_*_posthog", "ph_x_posthogs") {
		t.Error("wildcard")
	}
}

func TestInPage(t *testing.T) {
	base, _ := url.Parse("https://www.northpeak.example/pricing")
	page := []byte(`<html><head>
<script async src="https://www.googletagmanager.com/gtm.js?id=GTM-TEST"></script>
<script async src="/ofx/site.js" data-key="ofk_test_not_a_real_key"></script>
<script src="/assets/app.js"></script>
<script src="https://cdn.northpeak.example/vendor.js"></script>
<script>!function(f,b,e,v,n,t,s){t.src='https://connect.facebook.net/en_US/fbevents.js'}(window,document,'script');</script>
<script type="text/plain" data-category="marketing">window.uetq=[];s.src="//bat.bing.com/bat.js";</script>
</head><body>
<iframe src="https://www.youtube-nocookie.com/embed/abc"></iframe>
<img src="https://px.ads.linkedin.com/collect/?pid=1" width="1" height="1">
<img src="/logo.png">
</body></html>`)
	var names []string
	for _, s := range inPage(base, page, "/pricing") {
		names = append(names, s.Kind+" "+s.Name+" "+s.Vendor)
	}
	want := []string{
		"script www.googletagmanager.com/gtm.js ",
		"script www.northpeak.example/ofx outflare",
		"script connect.facebook.net/en_US ",
		"script bat.bing.com/bat.js ",
		"iframe www.youtube-nocookie.com/embed ",
		"pixel px.ads.linkedin.com/collect ",
	}
	if !slices.Equal(names, want) {
		t.Errorf("found:\n%q\nwant:\n%q", names, want)
	}
}

func TestSetCookies(t *testing.T) {
	h := http.Header{}
	h.Add("Set-Cookie", "sid=abc; Path=/; HttpOnly")
	h.Add("Set-Cookie", "_ga=GA1.2.3; Max-Age=63072000; Domain=.northpeak.example")
	h.Add("Set-Cookie", "csrftoken=x; Max-Age=3600")
	got := setCookies(h, "www.northpeak.example", "/")
	if len(got) != 3 || got[0].Name != "sid" || got[0].Retention != "Session" || got[1].Host != "northpeak.example" || got[1].Retention != "2 years" ||
		got[2].Retention != "60 minutes" && got[2].Retention != "1 hours" {
		t.Errorf("%+v", got)
	}
	if r := retention(&http.Cookie{MaxAge: int((90 * 24 * time.Hour).Seconds())}); r != "3 months" {
		t.Errorf("90 days: %s", r)
	}
}

func TestRender(t *testing.T) {
	v := Version{Version: 2, Published: time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC), Policy: Policy{Site: "northpeak.example", Categories: []PolicyCategory{
		{ID: Necessary, Vendors: []PolicyVendor{{ID: "", Name: "northpeak.example", Items: []PolicyItem{{Kind: KindCookie, Name: "sid", Purpose: "Keeps you <signed in>."}}}}},
		{ID: Marketing, Vendors: []PolicyVendor{{ID: "meta_pixel", Name: "Meta Pixel", Company: "Meta", PrivacyURL: "https://www.facebook.com/privacy/policy/", Purpose: "Ads.",
			Items: []PolicyItem{{Kind: KindCookie, Name: "_fbp", Retention: "90 days"}}}}},
		{ID: Analytics, Vendors: []PolicyVendor{}},
	}}}
	md := v.Markdown()
	for _, want := range []string{"# Cookie policy for northpeak.example", "Version 2, updated 28 October 2026", "## Necessary", "| `sid` | northpeak.example | Cookie | Keeps you <signed in>. | Until cleared |",
		"| `_fbp` | Meta Pixel (Meta) | Cookie | Ads. | 90 days |"} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown lacks %q:\n%s", want, md)
		}
	}
	if strings.Contains(md, "## Analytics") {
		t.Error("an empty category is listed")
	}
	if h := v.HTML(); !strings.Contains(h, "Keeps you &lt;signed in&gt;.") || !strings.Contains(h, `data-outflare-consent-open`) {
		t.Errorf("html: %s", h)
	}
	if txt := v.Text(); !strings.Contains(txt, "- _fbp (cookie, kept for 90 days): Ads.") {
		t.Errorf("text: %s", txt)
	}
}
