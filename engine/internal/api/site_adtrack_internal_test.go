package api

import (
	"encoding/json"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/adtrack"
)

// site.js reads exactly the click ids, ad parameters and network cookies in
// the adtrack catalog.
func TestSiteScriptMatchesAdCatalog(t *testing.T) {
	b, err := os.ReadFile("site.js")
	if err != nil {
		t.Fatal(err)
	}
	list := func(name string) []string {
		m := regexp.MustCompile(`var ` + name + ` = (\[[^\]]*\]|\{[^}]*\});`).FindSubmatch(b)
		if m == nil {
			t.Fatalf("site.js has no %s", name)
		}
		var out []string
		for _, q := range regexp.MustCompile(`"([^"]+)"|(\w+):`).FindAllSubmatch(m[1], -1) {
			out = append(out, string(q[1])+string(q[2]))
		}
		return out
	}
	if got := list("clickIDs"); !slices.Equal(got, adtrack.Params()) {
		t.Errorf("site.js click ids %v, catalog %v", got, adtrack.Params())
	}
	var params []string
	for _, p := range adtrack.AdParams {
		if p.Param != "of_"+p.Key {
			t.Errorf("site.js reads of_%s for %s", p.Key, p.Param)
		}
		params = append(params, p.Key)
	}
	if got := list("adParams"); !slices.Equal(got, params) {
		t.Errorf("site.js ad params %v, catalog %v", got, params)
	}
	var cookies []string
	for _, c := range adtrack.Cookies {
		cookies = append(cookies, c.Name, c.Key)
	}
	if got := list("adCookies"); !slices.Equal(got, cookies) {
		t.Errorf("site.js cookies %v, catalog %v", got, cookies)
	}
}

// A paid click's ids and the template's ad ids go with the page view; the
// ad pixels' cookies go with it and with conversions, only with consent.
func TestSiteAdTrackingClient(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	type event struct {
		Type       string
		Properties struct {
			ClickIDs map[string]string `json:"click_ids"`
			Ad       map[string]string
			Plan     string
		}
	}
	run := func(consent string) []event {
		opts, _ := json.Marshal(map[string]any{
			"href":    "https://northpeak.example/pricing?ttclid=E.C.P.tt1&ScCid=sc-1&of_campaign=1800001&of_ad=1800003&of_adset=__AID__",
			"consent": consent,
			"cookies": map[string]string{"_fbp": "fb.1.1761600000000.111", "_gcl_aw": "GCL.1761600000.gc1", "_ttp": "ttp-1", "_scid": "scid-1", "other": "x"},
			"track":   "signed_up",
		})
		out, err := exec.Command(node, "testdata/run_site.mjs", "site.js", string(opts)).CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %s", err, out)
		}
		var got struct{ Bodies []struct{ Batch []event } }
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
		var events []event
		for _, b := range got.Bodies {
			events = append(events, b.Batch...)
		}
		if len(events) != 2 || events[0].Type != "page" || events[1].Type != "track" {
			t.Fatalf("events: %+v", events)
		}
		return events
	}

	ev := run("granted")
	page, conv := ev[0].Properties, ev[1].Properties
	want := map[string]string{"ttclid": "E.C.P.tt1", "ScCid": "sc-1", "fbp": "fb.1.1761600000000.111", "gcl_aw": "GCL.1761600000.gc1", "ttp": "ttp-1", "scid": "scid-1"}
	if !sameStrings(page.ClickIDs, want) {
		t.Errorf("page click ids %v, want %v", page.ClickIDs, want)
	}
	// The unfilled __AID__ goes to the server, which drops it.
	if page.Ad["campaign"] != "1800001" || page.Ad["ad"] != "1800003" {
		t.Errorf("page ad %v", page.Ad)
	}
	if conv.Plan != "pro" || !sameStrings(conv.ClickIDs, map[string]string{"fbp": "fb.1.1761600000000.111", "gcl_aw": "GCL.1761600000.gc1", "ttp": "ttp-1", "scid": "scid-1"}) {
		t.Errorf("conversion %+v", conv)
	}

	ev = run("required")
	if _, ok := ev[0].Properties.ClickIDs["fbp"]; ok || ev[0].Properties.ClickIDs["ttclid"] == "" {
		t.Errorf("without consent: page click ids %v", ev[0].Properties.ClickIDs)
	}
	if ev[1].Properties.ClickIDs != nil {
		t.Errorf("without consent: conversion click ids %v", ev[1].Properties.ClickIDs)
	}
}

// The server keeps network cookies with the visit's click ids without
// letting them start a new visit, and a new ad does start one.
func TestSiteTouchCookiesAndAds(t *testing.T) {
	touch := func(props string) siteTouch {
		var p map[string]any
		if err := json.NewDecoder(strings.NewReader(props)).Decode(&p); err != nil {
			t.Fatal(err)
		}
		return touchOf(p, map[string]any{})
	}
	first := touch(`{"click_ids":{"ttclid":"a","fbp":"fb.1.1.1"},"ad":{"campaign":"1"}}`)
	if first.clickIDs["fbp"] != "" || first.cookies["fbp"] != "fb.1.1.1" || first.ad["campaign"] != "1" {
		t.Fatalf("touch %+v", first)
	}
	if later := touch(`{"click_ids":{"fbp":"fb.1.1.1"}}`); later.arrived() {
		t.Error("a page with only a cookie counted as an arrival")
	}
	if !first.same(touch(`{"click_ids":{"ttclid":"a"},"ad":{"campaign":"1"}}`)) {
		t.Error("the same click with and without a cookie are different arrivals")
	}
	if first.same(touch(`{"click_ids":{"ttclid":"a"},"ad":{"campaign":"2"}}`)) {
		t.Error("a different ad is the same arrival")
	}
}

// A page view's ad keeps only filled-in template ids; a track event's own
// ad property is the site's and stays as sent.
func TestSiteMessageCleansAd(t *testing.T) {
	props := func(raw string) map[string]any {
		t.Helper()
		m, err := siteMessageOf(json.RawMessage(raw), "", nil)
		if err != nil {
			t.Fatal(err)
		}
		var p map[string]any
		if err := json.Unmarshal(m["properties"], &p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	page := props(`{"type":"page","anonymousId":"a-1","properties":{"ad":{"campaign":"1","ad":"{{ad.id}}","x":"y"},"click_ids":{"fbp":"fb.1.1.1","nope":"z"}}}`)
	if ad, _ := page["ad"].(map[string]any); len(ad) != 1 || ad["campaign"] != "1" {
		t.Errorf("page ad %v", page["ad"])
	}
	if ids, _ := page["click_ids"].(map[string]any); len(ids) != 1 || ids["fbp"] != "fb.1.1.1" {
		t.Errorf("page click ids %v", page["click_ids"])
	}
	if p := props(`{"type":"page","anonymousId":"a-1","properties":{"ad":{"ad":"__CID__"}}}`); p["ad"] != nil {
		t.Errorf("unfilled ad kept: %v", p["ad"])
	}
	if p := props(`{"type":"track","event":"Banner Clicked","anonymousId":"a-1","properties":{"ad":"spring-banner"}}`); p["ad"] != "spring-banner" {
		t.Errorf("track ad %v", p["ad"])
	}
}

// With the consent banner, ad pixels' cookies follow the marketing choice,
// not the analytics one.
func TestSiteAdCookiesFollowMarketingConsent(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	conversion := func(choice map[string]any) map[string]string {
		t.Helper()
		opts, _ := json.Marshal(map[string]any{
			"href":    "https://www.northpeak.example/pricing",
			"consent": "auto", "banner": testBanner(nil),
			"cookies": map[string]string{"_fbp": "fb.1.1761600000000.111"},
			"calls":   [][]any{{"consent", choice}},
			"track":   "signed_up",
		})
		cmd := exec.Command(node, "testdata/run_site.mjs", "site.js", string(opts))
		cmd.Env = append(os.Environ(), "TZ=Europe/Berlin")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %s", err, out)
		}
		var got struct {
			Bodies []struct {
				Batch []struct {
					Type       string
					Properties struct {
						ClickIDs map[string]string `json:"click_ids"`
					}
				}
			}
		}
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
		for _, b := range got.Bodies {
			for _, e := range b.Batch {
				if e.Type == "track" {
					return e.Properties.ClickIDs
				}
			}
		}
		t.Fatalf("no conversion sent: %s", out)
		return nil
	}
	if ids := conversion(map[string]any{"analytics": true}); ids["fbp"] != "" {
		t.Errorf("analytics only: %v", ids)
	}
	if ids := conversion(map[string]any{"analytics": true, "marketing": true}); ids["fbp"] != "fb.1.1761600000000.111" {
		t.Errorf("with marketing: %v", ids)
	}
}
