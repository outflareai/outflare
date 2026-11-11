package api_test

import (
	"context"
	"net/http/httptest"
	"net/url"
	"regexp"
	"testing"

	"github.com/outflareai/outflare/engine/internal/geoip/geoiptest"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Flows (plans/ad-tracker.md, T6c): a brand's reusable paths and
// destinations, shared by campaign links, so one edit changes every link
// that routes by the flow.
func TestTrackerFlows(t *testing.T) {
	geoiptest.Use(t)
	events := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	var brand string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand
	host := ownTrackingDomain(t, e, ws, brand, "go.northpeak.example")
	springfield := geoiptest.SpringfieldIP + ":51000"

	a := owner.must(201, "POST", base+"/offers", map[string]any{"name": "Offer A", "url": "https://offers.northpeak.example/a"})
	b := owner.must(201, "POST", base+"/offers", map[string]any{"name": "Offer B", "url": "https://offers.northpeak.example/b"})
	l := owner.must(201, "POST", base+"/landers", map[string]any{"name": "Quiz", "url": "https://quiz.northpeak.example/q"})

	f := owner.must(201, "POST", base+"/flows", map[string]any{"name": "US first",
		"paths": []any{map[string]any{"name": "US", "conditions": []any{map[string]any{"field": "country", "op": "is", "values": []any{"US"}}},
			"destinations": []any{map[string]any{"offer_id": a["id"]}}}},
		"destinations": []any{map[string]any{"url": "https://northpeak.example/default"}},
	})
	fid := f["id"].(string)
	if len(f["paths"].([]any)) != 1 || len(f["destinations"].([]any)) != 1 || f["links"] != float64(0) {
		t.Fatalf("flow: %v", f)
	}
	owner.must(409, "POST", base+"/flows", map[string]any{"name": "us first"})
	owner.must(400, "POST", base+"/flows", map[string]any{"name": "Bad", "paths": []any{map[string]any{"name": "x", "destinations": []any{}}}})
	owner.must(400, "POST", base+"/flows", map[string]any{"name": "Bad", "destinations": []any{map[string]any{"offer_id": "00000000-0000-0000-0000-000000000000"}}})

	// Two campaigns' links route by the flow.
	codes := []string{}
	links := []string{}
	for _, name := range []string{"Push one", "Push two"} {
		c := owner.must(201, "POST", base+"/tracking-campaigns", map[string]any{"name": name, "channel": "paid_ads", "source": "acme", "redirect_links": true})
		cpath := base + "/tracking-campaigns/" + c["code"].(string)
		lk := owner.must(201, "POST", cpath+"/links", map[string]any{"url": "https://northpeak.example/own"})
		codes = append(codes, regexp.MustCompile(`/l/([a-z0-9]{8})$`).FindStringSubmatch(lk["redirect_url"].(string))[1])
		lpath := cpath + "/links/" + lk["id"].(string) + "/flow"
		links = append(links, lpath)
		if got := owner.must(200, "PUT", lpath, map[string]any{"flow_id": fid}); got["flow_id"] != fid {
			t.Fatalf("link flow: %v", got)
		}
	}
	if got := owner.must(200, "GET", links[0], nil); got["flow_id"] != fid {
		t.Fatalf("link flow: %v", got)
	}
	owner.must(400, "PUT", links[0], map[string]any{"flow_id": "00000000-0000-0000-0000-000000000000"})
	if got := owner.must(200, "GET", base+"/flows/"+fid, nil); got["links"] != float64(2) {
		t.Fatalf("flow after two links: %v", got)
	}

	go1 := func(addr, path string) *url.URL {
		t.Helper()
		req := httptest.NewRequest("GET", path, nil)
		req.Host = host
		req.RemoteAddr = addr
		req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.1 Safari/605.1.15")
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		if rec.Code != 302 {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body)
		}
		loc, err := url.Parse(rec.Header().Get("Location"))
		if err != nil {
			t.Fatal(err)
		}
		return loc
	}
	elsewhere := "198.51.100.7:51000"
	for _, code := range codes {
		if loc := go1(springfield, "/l/"+code); loc.Path != "/a" {
			t.Fatalf("a US click went to %s", loc)
		}
		if loc := go1(elsewhere, "/l/"+code); loc.Path != "/default" {
			t.Fatalf("another click went to %s", loc)
		}
	}

	// One edit changes both links: the flow's default is now a lander
	// leading to offer B.
	owner.must(200, "PUT", base+"/flows/"+fid+"/destinations", map[string]any{"destinations": []any{
		map[string]any{"lander_id": l["id"]}, map[string]any{"offer_id": b["id"]},
	}})
	for _, code := range codes {
		loc := go1(elsewhere, "/l/"+code)
		if loc.Host != "quiz.northpeak.example" {
			t.Fatalf("a click after the edit went to %s", loc)
		}
		if next := go1(elsewhere, "/click/1?ofclk="+loc.Query().Get("ofclk")); next.Path != "/b" {
			t.Fatalf("the lander's click went to %s", next)
		}
	}
	got := owner.must(200, "PUT", base+"/flows/"+fid+"/paths", map[string]any{"paths": []any{}})
	if len(got["paths"].([]any)) != 0 || len(got["destinations"].([]any)) != 2 {
		t.Fatalf("flow after its paths were taken off: %v", got)
	}
	if loc := go1(springfield, "/l/"+codes[0]); loc.Host != "quiz.northpeak.example" {
		t.Fatalf("a US click without the path went to %s", loc)
	}

	// Taken off a link, the link routes by its own destinations again (none:
	// its own page); a flow in use can't be archived.
	owner.must(200, "PUT", links[0], map[string]any{"flow_id": nil})
	if loc := go1(elsewhere, "/l/"+codes[0]); loc.Path != "/own" {
		t.Fatalf("a click on a link without its flow went to %s", loc)
	}
	owner.must(400, "PATCH", base+"/flows/"+fid, map[string]any{"status": "archived"})
	owner.must(200, "PUT", links[1], map[string]any{"flow_id": nil})
	if got := owner.must(200, "PATCH", base+"/flows/"+fid, map[string]any{"status": "archived", "name": "US first, old"}); got["status"] != "archived" {
		t.Fatalf("archived flow: %v", got)
	}
	owner.must(400, "PUT", links[0], map[string]any{"flow_id": fid})
	if n := len(owner.must(200, "GET", base+"/flows", nil)["data"].([]any)); n != 0 {
		t.Fatalf("active flows: %d", n)
	}

	// Another brand sees none of it, and can't use it.
	other := owner.must(201, "POST", "/v1/workspaces/"+ws+"/brands", map[string]any{"name": "Repocket"})
	obase := "/v1/brands/" + other["id"].(string)
	if n := len(owner.must(200, "GET", obase+"/flows?status=all", nil)["data"].([]any)); n != 0 {
		t.Fatalf("another brand sees %d flows", n)
	}
	owner.must(404, "GET", obase+"/flows/"+fid, nil)
	owner.must(404, "PUT", obase+"/flows/"+fid+"/destinations", map[string]any{"destinations": []any{}})

	found := map[string]bool{}
	for _, ev := range events() {
		found[ev.Name] = true
	}
	for _, name := range []string{"tracker_flow.created", "tracker_flow.destinations_updated", "tracker_flow.paths_updated", "tracker_flow.archived", "tracking_link.flow_set"} {
		if !found[name] {
			t.Errorf("no %s event", name)
		}
	}
}
