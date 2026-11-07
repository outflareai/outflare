package api_test

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/aivis"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Websites: each name the answers give is sorted once and remembered. A
// listed domain written as words gets the name; a listed competitor's
// product counts as it (an alias); the model, shown the sites the answers
// cite, says the rest: certain websites are suggestions, likely ones
// suggestions that ask about the website, companies that aren't
// competitors get their own list, and descriptions leave the list.
func TestAIVisibilityWebsites(t *testing.T) {
	e := freshEnv(t)
	events := track.Capture(t)
	var bodies []map[string]any
	judge := `{"companies":[],"other_brands":[{"name":"Bright Data","website":""},{"name":"Zyte API","website":""},{"name":"Bright Data Web Unlocker","website":""},
		{"name":"Oxylabs","website":""},{"name":"Crew AI","website":""},{"name":"Playwright","website":""},{"name":"Web scraping API","website":""},
		{"name":"Nobody Knows","website":""},{"name":"Jina Reader","website":""}],"sources":[]}`
	or := httptest.NewServer(fakeOpenRouter{bodies: &bodies, judge: &judge})
	t.Cleanup(or.Close)
	e.integrations.Client.BaseURLs["openrouter"] = or.URL
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()

	owner.must(201, "POST", base+"/brands", map[string]any{"name": "Acme", "website": "acme.example"})
	site := owner.must(201, "POST", base+"/seo-sites", map[string]any{"url": "https://www.acme.example/", "brand_id": "acme"})["id"].(string)
	owner.must(201, "POST", base+"/seo-competitors", map[string]any{"site_id": site, "host": "brightdata.example"})
	owner.must(201, "POST", base+"/seo-competitors", map[string]any{"site_id": site, "host": "zyte.example", "name": "Zyte"})
	conn := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "openrouter", "credentials": map[string]string{"api_key": "sk-or-good"}})["id"].(string)
	owner.must(200, "PUT", base+"/ai-settings", map[string]any{"integration_id": conn, "model": "openai/gpt-5"})
	owner.must(200, "PUT", base+"/ai-visibility/settings?brand=acme", map[string]any{"models": []map[string]any{{"integration_id": conn, "model": "openai/gpt-5"}}})
	cohort := owner.must(201, "POST", base+"/ai-cohorts", map[string]any{"name": "Proxy buyers", "brand_id": "acme", "competitors": []map[string]any{}})
	cid, brand := cohort["id"].(string), cohort["brand_id"].(string)
	owner.must(201, "POST", base+"/ai-cohorts/"+cid+"/prompts", map[string]any{"prompts": []map[string]any{{"text": "What is the best proxy provider?"}}})

	svc := &aivis.Service{Pool: e.pool, Models: e.srv.AIVisibilityModels(), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	rid := owner.must(201, "POST", base+"/ai-cohorts/"+cid+"/runs", nil)["id"].(string)
	if err := svc.Step(ctx, ws, rid, 0); err != nil {
		t.Fatal(err)
	}
	var queued int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE kind = $1 AND workspace_id = $2`, aivis.JobWebsites, ws).Scan(&queued); err != nil || queued != 1 {
		t.Fatalf("websites job after the run: %d %v", queued, err)
	}

	judge = `{"names":[
		{"name":"Oxylabs","kind":"competitor","company":"","product_of":"","label":"","website":"https://www.oxylabs.example/","sure":"certain"},
		{"name":"crew ai","kind":"competitor","company":"","product_of":"","label":"","website":"crewai.example","sure":"likely"},
		{"name":"Playwright","kind":"other","company":"","product_of":"","label":"open-source library","website":"playwright.example","sure":"certain"},
		{"name":"Web scraping API","kind":"not_brand","company":"","product_of":"","label":"","website":"","sure":"unknown"},
		{"name":"Nobody Knows","kind":"unknown","company":"","product_of":"","label":"","website":"nobody.example","sure":"unknown"},
		{"name":"Jina Reader","kind":"product","company":"Jina","product_of":"","label":"","website":"jina.example","sure":"certain"},
		{"name":"Stranger","kind":"competitor","company":"","product_of":"","label":"","website":"stranger.example","sure":"certain"}]}`
	before := len(bodies)
	res, err := svc.FindWebsites(ctx, ws, brand)
	if err != nil {
		t.Fatal(err)
	}
	if res.Named != 1 || res.Products != 2 || res.Suggested != 3 || res.Unsure != 1 || res.Others != 1 || res.NotBrands != 1 || res.Skipped != "" {
		t.Fatalf("result: %+v", res)
	}
	// The model saw the tracked competitors and the sites the answers cite,
	// and wasn't asked about the names already sorted.
	asked := bodies[len(bodies)-1]["messages"].([]any)[1].(map[string]any)["content"].(string)
	if len(bodies) != before+1 || !strings.Contains(asked, "- Zyte (zyte.example)") || !strings.Contains(asked, "sites cited where it was named:") ||
		strings.Contains(namesPart(asked), "Zyte API") || strings.Contains(namesPart(asked), "Bright Data") {
		t.Fatalf("asked: %s", asked)
	}
	type row struct {
		name, how string
		aliases   []string
	}
	list := map[string]row{}
	rows, err := e.pool.Query(ctx, `SELECT host, COALESCE(name, ''), source || '/' || status, aliases FROM seo_competitors WHERE workspace_id = $1`, ws)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var host string
		var r row
		if err := rows.Scan(&host, &r.name, &r.how, &r.aliases); err != nil {
			t.Fatal(err)
		}
		list[host] = r
	}
	if bd := list["brightdata.example"]; bd.name != "Bright Data" || !slices.Equal(bd.aliases, []string{"Bright Data Web Unlocker"}) {
		t.Fatalf("bright data: %+v", bd)
	}
	if z := list["zyte.example"]; !slices.Equal(z.aliases, []string{"Zyte API"}) {
		t.Fatalf("zyte: %+v", z)
	}
	if list["oxylabs.example"].how != "ai_visibility/suggested" || list["crewai.example"].how != "ai_visibility_unsure/suggested" || list["crewai.example"].name != "crew ai" && list["crewai.example"].name != "Crew AI" ||
		list["jina.example"].name != "Jina" || !slices.Equal(list["jina.example"].aliases, []string{"Jina Reader"}) || len(list) != 5 {
		t.Fatalf("competitor list: %+v", list)
	}
	// The AI competitors page: suggestions waiting on Competitors, the name
	// nobody is sure of with its best guess, and the not-competitors apart.
	ov := owner.must(200, "GET", base+"/ai-visibility?brand=acme", nil)["data"].([]any)[0].(map[string]any)
	got := map[string]map[string]any{}
	for _, x := range ov["suggested_competitors"].([]any) {
		got[x.(map[string]any)["name"].(string)] = x.(map[string]any)
	}
	if len(got) != 4 || got["Oxylabs"]["competitor_id"] == nil || got["Crew AI"]["competitor_id"] == nil || got["Jina Reader"]["competitor_id"] == nil ||
		got["Nobody Knows"]["competitor_id"] != nil || got["Nobody Knows"]["host"] != "nobody.example" {
		t.Fatalf("suggested: %v", ov["suggested_competitors"])
	}
	others := ov["named_others"].([]any)
	if len(others) != 1 || others[0].(map[string]any)["name"] != "Playwright" || others[0].(map[string]any)["label"] != "open-source library" || others[0].(map[string]any)["host"] != "playwright.example" {
		t.Fatalf("others: %v", others)
	}
	if !slices.ContainsFunc(events(), func(ev track.Event) bool {
		return ev.Name == "ai_visibility.websites_found" && ev.Props["products"] == 2 && ev.Props["suggested"] == 3
	}) {
		t.Fatalf("no ai_visibility.websites_found event")
	}
	// Everything is remembered: a second pass asks nothing.
	before = len(bodies)
	if res, err = svc.FindWebsites(ctx, ws, brand); err != nil || res != (aivis.WebsitesResult{}) || len(bodies) != before {
		t.Fatalf("again: %+v %v asked %d", res, err, len(bodies)-before)
	}
}

// namesPart is the list of names in a websites prompt, after the competitors it tracks.
func namesPart(prompt string) string {
	_, names, _ := strings.Cut(prompt, "\nNames:\n")
	return names
}
