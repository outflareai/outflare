package api_test

import (
	"strings"
	"testing"
)

// A no-code test of a page: Outflare makes the flag (off, shown only on
// that page, split between the control and the new versions), keeps the
// page on the test for the editor and previews, and starting it turns the
// flag on, so the website script runs it with nothing added to the site.
func TestABTestOfAPage(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	signup := []map[string]any{{"kind": "event", "name": "signup_started"}}

	for _, body := range []map[string]any{
		{"page": map[string]any{"url": "pricing"}, "goals": signup},
		{"page": map[string]any{"url": "javascript:alert(1)"}, "goals": signup},
		{"page": map[string]any{"url": "https://northpeak.example/pricing", "versions": 5}, "goals": signup},
		{"page": map[string]any{"url": "https://northpeak.example/pricing"}, "flag": "hero", "goals": signup},
		{"page": map[string]any{"url": "https://northpeak.example/pricing"}},
	} {
		owner.must(400, "POST", base+"/ab-tests", body)
	}
	// A refused test leaves no flag behind.
	if fl := owner.must(200, "GET", base+"/flags", nil)["data"].([]any); len(fl) != 0 {
		t.Fatalf("flag left by a refused test: %v", fl)
	}

	ab := owner.must(201, "POST", base+"/ab-tests", map[string]any{"name": "Pricing headline", "goals": signup,
		"page": map[string]any{"url": "https://northpeak.example/pricing?ref=x", "versions": 2}})
	if ab["page"] != "https://northpeak.example/pricing" || ab["control"] != "control" || ab["name"] != "Pricing headline" || !strings.HasPrefix(ab["flag_key"].(string), "pricing-headline-") {
		t.Fatalf("test: %v", ab)
	}
	f := owner.must(200, "GET", base+"/flags/"+ab["flag_id"].(string), nil)
	vs := f["variants"].([]any)
	rules := f["rules"].([]any)
	if f["status"] != "off" || f["on_site"] != true || len(vs) != 3 || vs[1].(map[string]any)["key"] != "b" || vs[2].(map[string]any)["key"] != "c" {
		t.Fatalf("flag: %v", f)
	}
	if len(rules) != 1 || rules[0].(map[string]any)["attribute"] != "path" || rules[0].(map[string]any)["op"] != "is" || rules[0].(map[string]any)["values"].([]any)[0] != "/pricing" {
		t.Fatalf("rules: %v", rules)
	}
	weights := 0
	for _, v := range vs {
		weights += int(v.(map[string]any)["weight"].(float64))
	}
	if weights != 100 {
		t.Fatalf("weights: %v", vs)
	}

	// Previews open on the test's page.
	pv := owner.must(200, "GET", base+"/ab-tests/"+ab["id"].(string)+"/previews", nil)
	if p := pv["pages"].([]any); len(p) == 0 || p[0].(map[string]any)["url"] != "https://northpeak.example/pricing" {
		t.Fatalf("previews: %v", pv)
	}

	// Every page under a path, with a name made from it.
	blog := owner.must(201, "POST", base+"/ab-tests", map[string]any{"goals": signup, "page": map[string]any{"url": "https://northpeak.example/blog/*"}})
	bf := owner.must(200, "GET", base+"/flags/"+blog["flag_id"].(string), nil)
	if blog["page"] != "https://northpeak.example/blog/*" || blog["name"] != "Test of /blog/*" || bf["rules"].([]any)[0].(map[string]any)["op"] != "starts_with" {
		t.Fatalf("blog: %v %v", blog, bf)
	}

	// A split URL test: each new version sends its visitors to a page, only
	// on the brand's own domains.
	owner.must(400, "POST", base+"/ab-tests", map[string]any{"goals": signup, "page": map[string]any{"url": "https://northpeak.example/pricing", "redirects": []string{"https://elsewhere.example/"}}})
	owner.must(400, "POST", base+"/ab-tests", map[string]any{"goals": signup, "page": map[string]any{"url": "https://northpeak.example/blog/*", "redirects": []string{"/b"}}})
	owner.must(400, "POST", base+"/ab-tests", map[string]any{"goals": signup, "page": map[string]any{"url": "https://northpeak.example/pricing", "versions": 2, "redirects": []string{"/b"}}})
	split := owner.must(201, "POST", base+"/ab-tests", map[string]any{"goals": signup, "page": map[string]any{"url": "https://northpeak.example/pricing", "redirects": []string{"/pricing-b", "/pricing-c"}}})
	sv := owner.must(200, "GET", base+"/flags/"+split["flag_id"].(string), nil)["variants"].([]any)
	if len(sv) != 3 || sv[0].(map[string]any)["payload"] != nil || sv[2].(map[string]any)["payload"].(map[string]any)["redirect"] != "/pricing-c" {
		t.Fatalf("split versions: %v", sv)
	}

	// A test whose new versions don't change the page yet can't start:
	// every visitor would see the same page.
	owner.must(409, "POST", base+"/ab-tests/"+ab["id"].(string)+"/start", nil)
	for _, c := range owner.must(200, "GET", base+"/ab-tests/"+ab["id"].(string)+"/checklist", nil)["checks"].([]any) {
		if c := c.(map[string]any); c["id"] == "flag" && c["status"] != "fail" {
			t.Fatalf("unchanged page test checklist: %v", c)
		}
	}
	// A version that sends visitors to another page changes it.
	owner.must(200, "POST", base+"/ab-tests/"+split["id"].(string)+"/start", nil)

	// A duplicate keeps the page.
	dup := owner.must(201, "POST", base+"/ab-tests/"+ab["id"].(string)+"/duplicate", nil)
	if dup["page"] != ab["page"] {
		t.Fatalf("duplicate: %v", dup)
	}
}
