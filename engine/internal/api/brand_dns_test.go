package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/track"
)

// Every record a brand needs, in one list, added in one go through the
// workspace's own Cloudflare.
func TestBrandDNSInOneGo(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	e.runServerJobs(t)

	var mu sync.Mutex
	var cfRecords []map[string]any
	cf := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.URL.Path == "/zones":
			if name := r.URL.Query().Get("name"); name == "" || name == "northpeak.example" {
				json.NewEncoder(w).Encode(map[string]any{"result": []any{map[string]any{"id": "z1", "name": "northpeak.example"}}})
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"result": []any{}})
		case r.URL.Path == "/zones/z1/dns_records" && r.Method == "GET":
			out := []any{}
			for _, rec := range cfRecords {
				if rec["name"] == r.URL.Query().Get("name") && rec["type"] == r.URL.Query().Get("type") {
					out = append(out, rec)
				}
			}
			json.NewEncoder(w).Encode(map[string]any{"result": out})
		case r.URL.Path == "/zones/z1/dns_records" && r.Method == "POST":
			var rec map[string]any
			json.NewDecoder(r.Body).Decode(&rec)
			cfRecords = append(cfRecords, rec)
			json.NewEncoder(w).Encode(map[string]any{"result": rec})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(cf.Close)
	e.integrations.Client.BaseURLs["cloudflare"] = cf.URL

	owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "website": "northpeak.example"})
	dns := base + "/brands/northpeak/dns"

	// No site yet: nothing to list, and nothing to add without a connection.
	d := owner.must(200, "GET", dns, nil)
	if d["domain"] != "northpeak.example" || len(d["groups"].([]any)) != 0 || d["missing"] != 0.0 || len(d["connections"].([]any)) != 0 {
		t.Fatalf("new brand: %v", d)
	}
	owner.must(400, "POST", dns, map[string]any{"integration_id": "00000000-0000-4000-8000-000000000000"})

	// A tracking domain on the brand's domain joins the list; one elsewhere doesn't.
	owner.must(201, "POST", base+"/tracking-domains", map[string]string{"domain": "links.northpeak.example"})
	owner.must(201, "POST", base+"/tracking-domains", map[string]string{"domain": "links.acme.example"})
	cfConn := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "cloudflare", "credentials": map[string]string{"api_token": "cf_test_not_a_real_token"}})["id"].(string)

	// Adding them all makes the site to verify first, so its proof goes in too.
	res := owner.must(202, "POST", dns, map[string]any{"integration_id": cfConn})
	if res["groups"] != 2.0 || res["records"] != 2.0 {
		t.Fatalf("add all: %v", res)
	}
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(25 * time.Millisecond) {
		d = owner.must(200, "GET", dns, nil)
		done := 0
		for _, g := range d["groups"].([]any) {
			if last, _ := g.(map[string]any)["last"].(map[string]any); last != nil && last["status"] == "done" {
				done++
			}
		}
		if done == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out: %v", d)
		}
	}
	subjects := map[string]map[string]any{}
	for _, g := range d["groups"].([]any) {
		subjects[g.(map[string]any)["subject"].(string)] = g.(map[string]any)
	}
	site, link := subjects["seo_site"], subjects["tracking_domain"]
	if site == nil || link == nil || len(subjects) != 2 || link["domain"] != "links.northpeak.example" {
		t.Fatalf("groups: %v", d["groups"])
	}
	if c := d["connections"].([]any); len(c) != 1 || c[0] != cfConn {
		t.Fatalf("connections: %v", c)
	}
	mu.Lock()
	n := len(cfRecords)
	mu.Unlock()
	if n != 2 {
		t.Fatalf("records added in Cloudflare: %v", cfRecords)
	}
	if d["developer"].(map[string]any)["body"] == "" || d["ai_prompt"] == "" {
		t.Fatalf("no developer message or prompt: %v", d)
	}
	if len(named(got(), "brand.dns_requested")) != 1 {
		t.Errorf("no brand.dns_requested event")
	}

	// Once DNS shows the site's proof, that group is in place.
	rec := site["records"].([]any)[0].(map[string]any)
	e.dns.txt["northpeak.example"] = []string{rec["value"].(string)}
	if _, err := e.pool.Exec(context.Background(), `UPDATE seo_sites SET verify_checked_at = now() - interval '2 minutes'`); err != nil {
		t.Fatal(err)
	}
	e.srv.CheckInstalls(context.Background())
	d = owner.must(200, "GET", dns, nil)
	for _, g := range d["groups"].([]any) {
		if g := g.(map[string]any); g["subject"] == "seo_site" && g["missing"] != 0.0 {
			t.Fatalf("verified site still missing: %v", g)
		}
	}
}

// Records added by hand are looked for as soon as the person says so.
func TestBrandDNSCheckNow(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "website": "northpeak.example"})
	owner.must(200, "POST", base+"/brands/northpeak/setup/prepare", map[string]any{"step": "verify_site"})
	owner.must(201, "POST", base+"/tracking-domains", map[string]string{"domain": "links.northpeak.example"})
	check := base + "/brands/northpeak/dns/check"

	missing := func(d map[string]any) map[string]float64 {
		out := map[string]float64{}
		for _, g := range d["groups"].([]any) {
			g := g.(map[string]any)
			out[g["subject"].(string)] = g["missing"].(float64)
		}
		return out
	}
	d := owner.must(200, "POST", check, nil)
	if m := missing(d); d["missing"] != 2.0 || m["seo_site"] != 1 || m["tracking_domain"] != 1 {
		t.Fatalf("nothing added yet: %v", d)
	}

	// The person adds both by hand, then checks: both are found at once.
	var proof string
	for _, g := range d["groups"].([]any) {
		if g := g.(map[string]any); g["subject"] == "seo_site" {
			proof = g["records"].([]any)[0].(map[string]any)["value"].(string)
		}
	}
	e.dns.txt["northpeak.example"] = []string{proof}
	e.dns.cname = map[string]string{"links.northpeak.example": "localhost"}
	d = owner.must(200, "POST", check, nil)
	if m := missing(d); d["missing"] != 0.0 || m["seo_site"] != 0 || m["tracking_domain"] != 0 {
		t.Fatalf("added by hand: %v", d)
	}
	for _, g := range d["groups"].([]any) {
		for _, r := range g.(map[string]any)["records"].([]any) {
			if r.(map[string]any)["status"] != "in_place" {
				t.Fatalf("record not in place: %v", r)
			}
		}
	}
	checked := named(got(), "brand.dns_checked")
	if len(checked) != 2 {
		t.Fatalf("brand.dns_checked events: %d", len(checked))
	}
	if len(named(got(), "seo_site.verified")) != 1 {
		t.Errorf("no seo_site.verified event")
	}
}
