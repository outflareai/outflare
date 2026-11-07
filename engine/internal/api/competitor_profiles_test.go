package api_test

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/outflareai/outflare/engine/internal/track"
)

// A competitor's social profiles and review pages: suggested from its home
// page, said yes or no to, read daily through the workspace's own page
// reader like the brand's own, kept out of the brand's own social and
// review numbers, and counted in the competition overview.
func TestCompetitorProfiles(t *testing.T) {
	events := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	base := "/v1/workspaces/" + ws
	brand := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "handle": "northpeak", "website": "https://northpeak.example",
		"links": []map[string]any{{"kind": "x", "url": "https://x.com/northpeak"}, {"kind": "trustpilot", "url": "https://www.trustpilot.com/review/northpeak.example"}}})["id"].(string)
	exec := func(q string, args ...any) string {
		t.Helper()
		var id string
		if err := e.pool.QueryRow(ctx, q+` RETURNING id::text`, args...).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	site := exec(`INSERT INTO seo_sites (workspace_id, brand_id, host, start_url, verify_token) VALUES ($1, $2, 'northpeak.example', 'https://northpeak.example/', 'tok')`, ws, brand)
	rival := exec(`INSERT INTO seo_competitors (workspace_id, site_id, brand_id, host, name, status, links) VALUES ($1, $2, $3, 'ghostpeak.example', 'Ghostpeak', 'tracked',
		'[{"kind": "youtube", "url": "https://www.youtube.com/@ghostpeaktv", "state": "suggested", "source": "home_page"}]')`, ws, site, brand)
	gone := exec(`INSERT INTO seo_competitors (workspace_id, site_id, brand_id, host, name, status) VALUES ($1, $2, $3, 'gone.example', 'Gone', 'dismissed')`, ws, site, brand)
	root := "/v1/brands/" + brand + "/competition/" + rival + "/profiles"

	// No page reader yet: the suggestion is kept, and the page says what to
	// connect.
	got := owner.must(200, "GET", root, nil)
	if got["needs_reader"] != true || len(got["profiles"].([]any)) != 1 {
		t.Fatalf("profiles: %v", got)
	}
	e.integrations.Client.BaseURLs["geonode"] = fakeGeonodePages(t).URL
	gn := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "geonode", "credentials": map[string]string{"api_key": "gn-key"}})["id"].(string)

	// Yes to the suggestion, one added by hand, a review page; bad and
	// unknown addresses refused; a dismissed competitor isn't followed.
	owner.must(200, "PATCH", root, map[string]any{"url": "https://www.youtube.com/@ghostpeaktv", "state": "confirmed"})
	owner.must(200, "POST", root, map[string]any{"url": "https://bsky.app/profile/ghostpeak.example"})
	owner.must(200, "POST", root, map[string]any{"url": "https://www.capterra.com/p/777/Ghostpeak/"})
	owner.must(400, "POST", root, map[string]any{"url": "https://ghostpeak.example/pricing"})
	owner.must(400, "PATCH", root, map[string]any{"url": "https://www.youtube.com/@ghostpeaktv", "state": "maybe"})
	owner.must(404, "PATCH", root, map[string]any{"url": "https://x.com/someone", "state": "confirmed"})
	owner.must(409, "POST", "/v1/brands/"+brand+"/competition/"+gone+"/profiles", map[string]any{"url": "https://x.com/gone"})
	owner.must(404, "GET", "/v1/brands/"+brand+"/competition/not-an-id/profiles", nil)
	got = owner.must(200, "GET", root, nil)
	states := map[string]string{}
	for _, p := range got["profiles"].([]any) {
		p := p.(map[string]any)
		states[p["kind"].(string)] = p["state"].(string) + "/" + p["source"].(string)
	}
	if got["needs_reader"] != false || states["youtube"] != "confirmed/home_page" || states["bluesky"] != "confirmed/manual" || states["capterra"] != "confirmed/manual" {
		t.Fatalf("states: %v", states)
	}

	// The daily reads: the competitor's profiles are read with the brand's
	// own, and the brand's social and review pages show only its own.
	if err := e.srv.SyncSocialAccounts(ctx, ws, gn); err != nil {
		t.Fatal(err)
	}
	accts := owner.must(200, "GET", base+"/social/accounts?brand="+brand, nil)["data"].([]any)
	if len(accts) != 1 || accts[0].(map[string]any)["handle"] != "northpeak" {
		t.Fatalf("brand accounts: %v", accts)
	}
	reviewSite := &fakeReviewSite{}
	srv := httptest.NewServer(reviewSite)
	t.Cleanup(srv.Close)
	e.integrations.Client.BaseURLs["geonode"] = srv.URL
	if err := e.srv.ReadReviewPages(ctx, ws, gn); err != nil {
		t.Fatal(err)
	}
	pages := owner.must(200, "GET", base+"/reviews/pages?brand="+brand, nil)["data"].([]any)
	if len(pages) != 1 || pages[0].(map[string]any)["platform"] != "trustpilot" {
		t.Fatalf("brand review pages: %v", pages)
	}
	var kept int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM reviews r JOIN review_pages p ON p.id = r.page_id WHERE p.competitor_id IS NOT NULL`).Scan(&kept); err != nil || kept != 0 {
		t.Fatalf("a competitor's reviews are not kept: %d %v", kept, err)
	}
	got = owner.must(200, "GET", root, nil)
	read := map[string]map[string]any{}
	for _, p := range got["profiles"].([]any) {
		p := p.(map[string]any)
		read[p["kind"].(string)] = p
	}
	if read["youtube"]["followers"] != 4200.0 || read["bluesky"]["followers"] != 845.0 || read["capterra"]["score"] != 4.5 || read["capterra"]["review_count"] != 98.0 {
		t.Fatalf("reads: %v", read)
	}

	// The overview counts them: followers 12,300 against 5,045; reviews
	// 1,284 against 98.
	comp := owner.must(200, "GET", "/v1/brands/"+brand+"/competition", nil)
	for _, en := range comp["entries"].([]any) {
		m := en.(map[string]any)
		areas := m["areas"].(map[string]any)
		social, reviews := areas["social"].(map[string]any)["value"], areas["reviews"].(map[string]any)["value"]
		switch m["id"] {
		case "brand":
			if social != 12300.0 || reviews != 1284.0 {
				t.Fatalf("brand: %v %v", social, reviews)
			}
		case rival:
			if social != 5045.0 || reviews != 98.0 {
				t.Fatalf("rival: %v %v", social, reviews)
			}
		}
	}

	// No to one: it stops being read and drops out of the totals.
	e.integrations.Client.BaseURLs["geonode"] = fakeGeonodePages(t).URL
	owner.must(200, "PATCH", root, map[string]any{"url": "https://www.youtube.com/@ghostpeaktv", "state": "dismissed"})
	if err := e.srv.SyncSocialAccounts(ctx, ws, gn); err != nil {
		t.Fatal(err)
	}
	var active int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM social_accounts WHERE competitor_id = $1 AND status = 'active'`, rival).Scan(&active); err != nil || active != 1 {
		t.Fatalf("active competitor accounts: %d %v", active, err)
	}

	seen := map[string]bool{}
	for _, ev := range events() {
		seen[ev.Name] = true
	}
	for _, name := range []string{"competitor_profile.confirmed", "competitor_profile.added", "competitor_profile.dismissed"} {
		if !seen[name] {
			t.Errorf("no %s event", name)
		}
	}
}
