package api_test

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// text sends a GET as the client and returns the body as text.
func (c *client) text(path string) (int, string, string) {
	c.t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	if c.cookie != nil {
		req.AddCookie(c.cookie)
	}
	rec := httptest.NewRecorder()
	c.h.ServeHTTP(rec, req)
	return rec.Code, rec.Header().Get("Content-Type"), rec.Body.String()
}

func TestBacklinkSpamReviewAndDisavow(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	t.Cleanup(func() { setDFSLinks() })

	id := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "dataforseo", "credentials": map[string]string{"login": "jo", "password": "pw"}})["id"].(string)
	siteID := owner.must(201, "POST", base+"/seo-sites", map[string]any{"url": "https://www.example.com/"})["id"].(string)

	old := time.Now().AddDate(-1, 0, 0)
	good := dfsLink("https://news.example.org/story", "https://www.example.com/", "news.example.org", 70, old, nil)
	flagged := dfsLink("https://spam.example.net/a", "https://www.example.com/", "spam.example.net", 30, old, nil)
	flagged["backlink_spam_score"] = 80
	farm1 := dfsLink("https://links.example.xyz/1", "https://www.example.com/", "links.example.xyz", 3, old, nil)
	farm1["anchor"], farm1["page_from_external_links"] = "cheap casino bonus", 900
	farm2 := dfsLink("https://links.example.xyz/2", "https://www.example.com/pricing", "links.example.xyz", 3, old, nil)
	setDFSLinks(good, flagged, farm1, farm2)
	owner.must(200, "POST", base+"/seo/"+id+"/sync", nil)

	toxic := owner.must(200, "GET", base+"/backlinks?spam=toxic&order=spam", nil)["data"].([]any)
	if len(toxic) != 2 {
		t.Fatalf("toxic: %v", toxic)
	}
	top := toxic[0].(map[string]any)
	if top["source_domain"] != "links.example.xyz" || top["spam_score"].(float64) != 100 || top["spam_level"] != "toxic" || top["review"] != "" {
		t.Fatalf("top: %v", top)
	}
	factors := map[string]bool{}
	for _, f := range top["spam_factors"].([]any) {
		factors[f.(map[string]any)["code"].(string)] = true
	}
	if !factors["spam_anchor"] || !factors["link_farm"] || !factors["spam_tld"] || !factors["weak_domain"] {
		t.Fatalf("factors: %v", top["spam_factors"])
	}
	clean := owner.must(200, "GET", base+"/backlinks?spam=clean", nil)["data"].([]any)
	if len(clean) != 1 || clean[0].(map[string]any)["source_domain"] != "news.example.org" {
		t.Fatalf("clean: %v", clean)
	}
	sum := owner.must(200, "GET", base+"/backlinks/summary", nil)["data"].([]any)[0].(map[string]any)["spam"].(map[string]any)
	// The farm's second link scores lower: only its weak domain and spammy
	// domain ending count.
	if sum["toxic"].(float64) != 2 || sum["suspect"].(float64) != 1 || sum["unreviewed"].(float64) != 3 || sum["disavowed"].(float64) != 0 {
		t.Fatalf("summary: %v", sum)
	}

	// Nothing is disavowed until a person says so.
	code, ct, body := owner.text(base + "/backlinks/disavow?site_id=" + siteID)
	if code != 200 || !strings.HasPrefix(ct, "text/plain") || strings.Contains(body, "domain:") || strings.Contains(body, "https://") {
		t.Fatalf("empty file: %d %s %q", code, ct, body)
	}
	// A whole domain, and one link of another.
	var farmID, spamID string
	for _, l := range toxic {
		l := l.(map[string]any)
		switch l["source_domain"] {
		case "links.example.xyz":
			farmID = l["id"].(string)
		case "spam.example.net":
			spamID = l["id"].(string)
		}
	}
	res := owner.must(200, "PATCH", base+"/backlinks/"+farmID, map[string]any{"review": "disavow", "domain": true, "note": "link farm"})
	if res["updated"].(float64) != 2 || res["backlink"].(map[string]any)["review"] != "disavow" || res["backlink"].(map[string]any)["review_note"] != "link farm" {
		t.Fatalf("review domain: %v", res)
	}
	owner.must(200, "PATCH", base+"/backlinks/"+spamID, map[string]any{"review": "disavow"})
	_, _, body = owner.text(base + "/backlinks/disavow?site_id=" + siteID)
	if !strings.Contains(body, "\ndomain:links.example.xyz\n") || !strings.Contains(body, "\ndomain:spam.example.net\n") || strings.Contains(body, "news.example.org") {
		t.Fatalf("file: %q", body)
	}
	// A link someone keeps is left out, and can be cleared again.
	owner.must(200, "PATCH", base+"/backlinks/"+spamID, map[string]any{"review": "ok"})
	if got := owner.must(200, "GET", base+"/backlinks?review=ok", nil)["data"].([]any); len(got) != 1 {
		t.Fatalf("ok: %v", got)
	}
	owner.must(200, "PATCH", base+"/backlinks/"+spamID, map[string]any{"review": "none"})
	if got := owner.must(200, "GET", base+"/backlinks?review=none", nil)["data"].([]any); len(got) != 2 {
		t.Fatalf("unreviewed: %v", got)
	}
	owner.must(400, "PATCH", base+"/backlinks/"+spamID, map[string]any{"review": "maybe"})
	owner.must(400, "GET", base+"/backlinks?spam=bad", nil)
	owner.must(404, "PATCH", base+"/backlinks/00000000-0000-4000-8000-000000000000", map[string]any{"review": "ok"})
	owner.must(400, "GET", base+"/backlinks/disavow", nil)
}
