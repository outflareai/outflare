package api_test

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/flags"
	"github.com/outflareai/outflare/engine/internal/track"
)

// askSiteFlags asks for a visitor's flags the way the script does.
func askSiteFlags(t *testing.T, e *env, origin, key, anon, url string, preview ...map[string]string) (int, map[string]any) {
	t.Helper()
	body := map[string]any{"key": key, "anonymousId": anon, "url": url}
	if len(preview) > 0 {
		body["preview"] = preview[0]
	}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/v1/site/flags", strings.NewReader(string(b)))
	req.Header.Set("Content-Type", "text/plain;charset=UTF-8")
	req.Header.Set("User-Agent", iphoneUA)
	req.Header.Set("Origin", origin)
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if rec.Header().Get("Access-Control-Allow-Origin") != "*" || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("headers: %v", rec.Header())
	}
	var out map[string]any
	json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func TestFlags(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws

	geo := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "website": "https://northpeak.example"})["id"].(string)
	rp := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Summitly", "website": "https://summitly.example"})["id"].(string)

	// Bad flags are refused with a reason.
	for _, body := range []map[string]any{
		{},
		{"key": "New Pricing"},
		{"key": "hero", "variants": []map[string]any{}},
		{"key": "hero", "variants": []map[string]any{{"key": "a", "weight": 0}}},
		{"key": "hero", "rollout": 101},
		{"key": "hero", "status": "archived"},
		{"key": "hero", "rules": []map[string]any{{"attribute": "country", "op": "is", "values": []string{"au"}}}},
		{"key": "hero", "brand_id": "00000000-0000-0000-0000-000000000000"},
	} {
		owner.must(400, "POST", base+"/flags", body)
	}

	// A switch: one variant, on, off until turned on.
	sw := owner.must(201, "POST", base+"/flags", map[string]any{"key": "chat-widget"})
	if sw["status"] != "off" || sw["name"] != "chat-widget" || len(sw["variants"].([]any)) != 1 || sw["rollout"] != float64(100) || sw["on_site"] != true {
		t.Fatalf("switch: %v", sw)
	}
	owner.must(409, "POST", base+"/flags", map[string]any{"key": "chat-widget"})

	// A split for Northpeak's site, on pricing pages only, with a headline each.
	split := owner.must(201, "POST", base+"/flags", map[string]any{
		"key": "pricing-headline", "name": "Pricing headline", "brand_id": geo, "status": "on",
		"variants": []map[string]any{
			{"key": "control", "weight": 50},
			{"key": "faster", "weight": 50, "payload": map[string]any{"headline": "Ship faster"}},
		},
		"rules": []map[string]any{{"attribute": "path", "op": "starts_with", "values": []string{"/pricing"}}},
	})
	splitID := split["id"].(string)
	// One only servers read.
	owner.must(201, "POST", base+"/flags", map[string]any{"key": "billing-v2", "status": "on", "on_site": false})
	// One for the other brand.
	owner.must(201, "POST", base+"/flags", map[string]any{"key": "summitly-banner", "status": "on", "brand_id": rp})

	// A flag is one brand's: one made without a brand is the first
	// brand's, and none is shared.
	if list := owner.must(200, "GET", base+"/flags?brand="+geo, nil)["data"].([]any); len(list) != 1 {
		t.Fatalf("northpeak's flags: %v", list)
	}
	if one := owner.must(200, "GET", base+"/flags/pricing-headline", nil); one["id"] != splitID {
		t.Fatalf("by key: %v", one)
	}

	// The site asks with Northpeak's key.
	k := owner.must(201, "POST", base+"/site-keys", map[string]any{"name": "northpeak.example", "brand_id": geo, "origins": []string{"northpeak.example"}})["key"].(string)
	code, out := askSiteFlags(t, e, "https://northpeak.example", k, "visitor-0001", "https://northpeak.example/pricing?plan=team")
	if code != 200 {
		t.Fatalf("site flags: %d %v", code, out)
	}
	fl := out["flags"].(map[string]any)
	// Off flags, server-only flags and other brands' flags stay out.
	if _, ok := fl["chat-widget"]; ok || fl["billing-v2"] != nil || fl["summitly-banner"] != nil || len(fl) != 1 {
		t.Fatalf("site got %v", fl)
	}
	variant := fl["pricing-headline"].(string)
	if variant == "faster" && out["payloads"].(map[string]any)["pricing-headline"].(map[string]any)["headline"] != "Ship faster" {
		t.Fatalf("payload: %v", out["payloads"])
	}
	// The same visitor gets the same variant every time.
	for range 3 {
		if _, again := askSiteFlags(t, e, "https://northpeak.example", k, "visitor-0001", "https://northpeak.example/pricing"); again["flags"].(map[string]any)["pricing-headline"] != variant {
			t.Fatalf("variant moved: %v", again)
		}
	}
	// And the split is about even across visitors.
	seen := map[any]int{}
	for i := range 200 {
		_, o := askSiteFlags(t, e, "https://northpeak.example", k, fmt.Sprintf("visitor-%04d", i), "https://northpeak.example/pricing")
		seen[o["flags"].(map[string]any)["pricing-headline"]]++
	}
	if seen["control"] < 70 || seen["faster"] < 70 {
		t.Fatalf("split: %v", seen)
	}
	// Off the pricing page, the rule leaves the visitor out.
	if _, o := askSiteFlags(t, e, "https://northpeak.example", k, "visitor-0001", "https://northpeak.example/blog"); o["flags"].(map[string]any)["pricing-headline"] != false {
		t.Fatalf("rule: %v", o)
	}
	// A person previewing gets the version they asked for and its content,
	// even of a flag that is still off; a version the flag doesn't have
	// changes nothing. chat-widget is the first brand's, not Northpeak's.
	_, o := askSiteFlags(t, e, "https://northpeak.example", k, "visitor-0001", "https://northpeak.example/blog",
		map[string]string{"pricing-headline": "faster", "chat-widget": "on", "billing-v2": "on", "summitly-banner": "on"})
	if fl := o["flags"].(map[string]any); fl["pricing-headline"] != "faster" || fl["chat-widget"] != nil || fl["billing-v2"] != nil || fl["summitly-banner"] != nil ||
		o["payloads"].(map[string]any)["pricing-headline"].(map[string]any)["headline"] != "Ship faster" {
		t.Fatalf("preview: %v", o)
	}
	if _, o := askSiteFlags(t, e, "https://northpeak.example", k, "visitor-0001", "https://northpeak.example/blog", map[string]string{"pricing-headline": "nope"}); o["flags"].(map[string]any)["pricing-headline"] != false {
		t.Fatalf("preview of a missing version: %v", o)
	}

	// A wrong origin, a bad key or no visitor id are refused.
	if c, _ := askSiteFlags(t, e, "https://elsewhere.example", k, "visitor-0001", ""); c != 403 {
		t.Fatalf("origin: %d", c)
	}
	if c, _ := askSiteFlags(t, e, "https://northpeak.example", "ofk_test_not_a_real_key", "visitor-0001", ""); c != 401 {
		t.Fatalf("bad key: %d", c)
	}
	if c, _ := askSiteFlags(t, e, "https://northpeak.example", k, "", ""); c != 400 {
		t.Fatalf("no visitor: %d", c)
	}

	// A server decides with a token, and agrees with the site.
	secret := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "Server", "scopes": []string{"analytics.read"}})["token"].(string)
	bot := &client{t: t, h: e.h, bearer: secret}
	owner.must(400, "POST", base+"/flags/evaluate", map[string]any{})
	// Each brand's flags are decided on its own route.
	ev := bot.must(200, "POST", base+"/flags/evaluate?brand="+geo, map[string]any{"anonymous_id": "visitor-0001", "url": "https://northpeak.example/pricing"})
	if ev["flags"].(map[string]any)["pricing-headline"] != variant || ev["flags"].(map[string]any)["billing-v2"] != nil || ev["reasons"].(map[string]any)["pricing-headline"] != "match" {
		t.Fatalf("evaluate: %v", ev)
	}
	ev = bot.must(200, "POST", base+"/flags/evaluate", map[string]any{"user_id": "u-1", "keys": []string{"billing-v2"}})
	if len(ev["flags"].(map[string]any)) != 1 || ev["flags"].(map[string]any)["billing-v2"] != "on" {
		t.Fatalf("keys: %v", ev)
	}
	// A server library reads the brand's flags that are on, to decide in
	// its own memory: deciding from them agrees with evaluate, visitor by
	// visitor.
	defs := bot.must(200, "GET", "/v1/brands/"+geo+"/flags/definitions", nil)
	if defs["refresh_seconds"] != float64(30) {
		t.Fatalf("definitions: %v", defs)
	}
	var local []struct {
		flags.Flag
		GroupFrom int `json:"group_from"`
		GroupTo   int `json:"group_to"`
	}
	raw, _ := json.Marshal(defs["flags"])
	if err := json.Unmarshal(raw, &local); err != nil || len(local) == 0 {
		t.Fatalf("definitions: %v %s", err, raw)
	}
	byKey := map[string]flags.Flag{}
	for _, d := range local {
		if d.Salt == "" || len(d.Variants) == 0 {
			t.Fatalf("definition %s lacks its salt or versions: %s", d.Key, raw)
		}
		d.Flag.Status, d.Flag.GroupFrom, d.Flag.GroupTo = "on", d.GroupFrom, d.GroupTo
		byKey[d.Key] = d.Flag
	}
	if _, ok := byKey["pricing-headline"]; !ok {
		t.Fatalf("definitions should hold Northpeak's flags that are on: %s", raw)
	}
	// billing-v2 and chat-widget are another brand's, as for evaluate.
	if _, other := byKey["billing-v2"]; other || byKey["chat-widget"].Key != "" {
		t.Fatalf("definitions crossed brands: %s", raw)
	}
	for i := range 40 {
		id := fmt.Sprintf("visitor-%04d", i)
		for _, page := range []string{"https://northpeak.example/pricing", "https://northpeak.example/blog"} {
			ev := bot.must(200, "POST", base+"/flags/evaluate?brand="+geo, map[string]any{"anonymous_id": id, "url": page})
			for key, f := range byKey {
				var want any = false
				if res := flags.Evaluate(f, flags.Visitor{Subject: id, URL: page}); res.Variant != "" {
					want = res.Variant
				}
				if got := ev["flags"].(map[string]any)[key]; got != want {
					t.Fatalf("%s on %s: evaluate gave %v, the definitions decide %v", id, page, got, want)
				}
			}
		}
	}
	// Unchanged definitions answer 304 to the ETag they came with; a
	// changed flag gives a new one.
	readDefs := func(etag string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/v1/brands/"+geo+"/flags/definitions", nil)
		req.Header.Set("Authorization", "Bearer "+secret)
		if etag != "" {
			req.Header.Set("If-None-Match", etag)
		}
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		return rec
	}
	etag := readDefs("").Header().Get("ETag")
	if etag == "" {
		t.Fatal("definitions carry no ETag")
	}
	if rec := readDefs(etag); rec.Code != 304 || rec.Body.Len() != 0 {
		t.Fatalf("unchanged definitions: %d %s", rec.Code, rec.Body)
	}
	if rec := readDefs(`"stale"`); rec.Code != 200 {
		t.Fatalf("stale etag: %d", rec.Code)
	}
	// A flag can't take the route's name.
	owner.must(400, "POST", base+"/flags", map[string]any{"key": "definitions", "brand_id": geo})
	// The read token can't change flags.
	bot.must(403, "PATCH", base+"/flags/"+splitID, map[string]any{"status": "off"})

	// Turning it off takes it out of the page at once in this process.
	owner.must(400, "PATCH", base+"/flags/"+splitID, map[string]any{"key": "other"})
	owner.must(200, "PATCH", base+"/flags/"+splitID, map[string]any{"status": "off", "brand_id": nil})
	if _, o := askSiteFlags(t, e, "https://northpeak.example", k, "visitor-0001", "https://northpeak.example/pricing"); len(o["flags"].(map[string]any)) != 0 {
		t.Fatalf("after off: %v", o)
	}

	// Archiving frees the key.
	owner.must(204, "DELETE", base+"/flags/"+splitID, nil)
	owner.must(409, "PATCH", base+"/flags/"+splitID, map[string]any{"status": "on"})
	owner.must(404, "GET", base+"/flags/pricing-headline", nil)
	owner.must(201, "POST", base+"/flags", map[string]any{"key": "pricing-headline"})
	if list := owner.must(200, "GET", base+"/flags?status=archived&brand="+geo, nil)["data"].([]any); len(list) != 1 {
		t.Fatalf("archived: %v", list)
	}

	names := map[string]int{}
	for _, ev := range got() {
		names[ev.Name]++
	}
	for _, name := range []string{"flag.created", "flag.updated", "flag.archived"} {
		if names[name] == 0 {
			t.Errorf("no %s event", name)
		}
	}
}
