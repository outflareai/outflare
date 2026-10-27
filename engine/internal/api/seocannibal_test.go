package api_test

import (
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/track"
)

func TestCannibalisation(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	brand := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "handle": "northpeak"})["id"].(string)
	site := owner.must(201, "POST", base+"/seo-sites", map[string]any{"url": "https://www.example.com/", "brand_id": brand})["id"].(string)
	sb := base + "/seo-sites/" + site

	competing := true
	f := &fakeSearchConsole{rows: func(day string) [][]any {
		rows := [][]any{
			{"https://www.example.com/pricing", "proxy pricing", 10, 100, 3.0},
			{"https://www.example.com/pricing", "cheap proxies", 2, 50, 9.0},
			// Searches for the brand show several pages, as they should.
			{"https://www.example.com/", "northpeak proxies", 30, 200, 1.0},
			{"https://www.example.com/pricing", "northpeak proxies", 5, 100, 2.0},
			// Too far down to count.
			{"https://www.example.com/old", "cheap proxies", 0, 40, 55.0},
		}
		if competing {
			rows = append(rows, []any{"https://www.example.com/blog/proxies", "cheap proxies", 1, 80, 12.0})
		}
		return rows
	}}
	sc := connectSearchConsole(t, e, owner, base, f)
	owner.must(200, "POST", base+"/analytics/"+sc+"/sync", nil)

	c := owner.must(200, "GET", sb+"/cannibalisation", nil)
	list := c["data"].([]any)
	if len(list) != 1 {
		t.Fatalf("cannibalisation: %v", c)
	}
	q := list[0].(map[string]any)
	if q["query"] != "cheap proxies" || len(q["pages"].([]any)) != 2 || q["keep"] != "https://www.example.com/pricing" {
		t.Fatalf("competing search: %v", q)
	}
	fixFor := func(rule string) map[string]any {
		for _, f := range owner.must(200, "GET", base+"/fixes?source=seo_keywords&subject_id="+site, nil)["data"].([]any) {
			if f := f.(map[string]any); f["rule"].(map[string]any)["id"] == rule {
				return f
			}
		}
		return nil
	}
	fx := fixFor("keyword_cannibalised")
	if fx == nil || fx["group"] != "cheap proxies" || fx["count"].(float64) != 2 || fx["brand_id"] != brand {
		t.Fatalf("cannibalisation fix: %v", fx)
	}
	p := owner.must(200, "GET", base+"/fixes/"+fx["id"].(string)+"/prompt", nil)
	if pr := p["prompt"].(string); len(p["pages"].([]any)) != 2 || !strings.Contains(pr, "[cheap proxies]") || !strings.Contains(pr, "the page to keep") {
		t.Fatalf("prompt: %v", p)
	}

	// The page that targets the search is the one to keep.
	owner.must(200, "PUT", sb+"/targets", map[string]any{"url": "https://www.example.com/blog/proxies", "keyword": "cheap proxies"})
	q = owner.must(200, "GET", sb+"/cannibalisation", nil)["data"].([]any)[0].(map[string]any)
	if q["keep"] != "https://www.example.com/blog/proxies" {
		t.Fatalf("keep the target: %v", q)
	}
	// Two pages with one target keyword is a fix of its own.
	owner.must(200, "PUT", sb+"/targets", map[string]any{"url": "https://www.example.com/pricing", "keyword": "cheap proxies"})
	shared := fixFor("target_shared")
	if shared == nil || shared["status"] != "open" || shared["count"].(float64) != 2 {
		t.Fatalf("shared target: %v", shared)
	}
	owner.must(200, "PUT", sb+"/targets", map[string]any{"url": "https://www.example.com/pricing", "keyword": "proxy pricing"})
	if f := fixFor("target_shared"); f["status"] != "done" {
		t.Fatalf("shared target fixed: %v", f)
	}

	// Marked done, it holds until the next sync still finds it.
	owner.must(200, "PATCH", base+"/fixes/"+fx["id"].(string), map[string]any{"status": "done"})
	owner.must(200, "PUT", sb+"/targets", map[string]any{"url": "https://www.example.com/", "keyword": "northpeak"})
	if f := fixFor("keyword_cannibalised"); f["status"] != "done" {
		t.Fatalf("a target change does not reopen it: %v", f)
	}
	owner.must(200, "POST", base+"/analytics/"+sc+"/sync", nil)
	if f := fixFor("keyword_cannibalised"); f["status"] != "open" {
		t.Fatalf("the sync reopens it: %v", f)
	}
	// Once one page has the search, the next sync confirms it.
	competing = false
	if _, err := e.pool.Exec(t.Context(), `DELETE FROM search_daily WHERE page = 'https://www.example.com/blog/proxies'`); err != nil {
		t.Fatal(err)
	}
	owner.must(200, "POST", base+"/analytics/"+sc+"/sync", nil)
	if f := fixFor("keyword_cannibalised"); f["status"] != "done" || f["confirmed_by"] == nil {
		t.Fatalf("confirmed: %v", f)
	}
	if n := len(owner.must(200, "GET", sb+"/cannibalisation", nil)["data"].([]any)); n != 0 {
		t.Fatalf("nothing competes: %d", n)
	}
	owner.must(400, "GET", sb+"/cannibalisation?limit=500", nil)

	synced := false
	for _, ev := range got() {
		if ev.Name == "search_console.synced" && ev.Props["fixes_new"] != nil {
			synced = true
		}
		if ev.Name == "search_insights.failed" {
			t.Fatalf("refresh failed: %+v", ev)
		}
	}
	if !synced {
		t.Fatal("search_console.synced carries the fixes it changed")
	}
}
