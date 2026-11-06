package api_test

import "testing"

// Without GA4 landing-page outcomes, AI referrals come from the brand's
// analytics connection: its referrers from known assistants, site-wide.
func TestAIReferralsFromAnalytics(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	brand := owner.must(200, "GET", base+"/brands", nil)["data"].([]any)[0].(map[string]any)["id"].(string)
	site := owner.must(201, "POST", base+"/seo-sites", map[string]any{"url": "https://example.com", "brand_id": brand})["id"].(string)
	root := "/v1/brands/" + brand + "/seo-sites/" + site

	none := owner.must(200, "GET", root+"/insights?report=ai-referrals", nil)
	if none["status"] != "not_connected" || len(none["data"].([]any)) != 0 {
		t.Fatal(none)
	}

	owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "plausible", "credentials": map[string]string{"api_key": "pk", "site_id": "example.com"}})
	ai := owner.must(200, "GET", root+"/insights?report=ai-referrals", nil)
	sum := ai["summary"].(map[string]any)
	data := ai["data"].([]any)
	if ai["status"] != "ready" || sum["ai_sessions"] != 20.0 || sum["all_sessions"] != 100.0 || sum["ai_share"] != 0.2 || len(data) != 2 {
		t.Fatal(ai)
	}
	if first := data[0].(map[string]any); first["source"] != "chatgpt.com" || first["sessions"] != 15.0 || first["status"] != "measured" {
		t.Fatal(first)
	}

	// Another brand's report never reads this brand's connection.
	other := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Other Example", "website": "https://other.example"})["id"].(string)
	otherSite := owner.must(201, "POST", base+"/seo-sites", map[string]any{"url": "https://other.example", "brand_id": other})["id"].(string)
	theirs := owner.must(200, "GET", "/v1/brands/"+other+"/seo-sites/"+otherSite+"/insights?report=ai-referrals", nil)
	if theirs["status"] != "not_connected" || len(theirs["data"].([]any)) != 0 {
		t.Fatal(theirs)
	}
}
