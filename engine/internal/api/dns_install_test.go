package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/track"
)

// A site's DNS proof and a tracking domain's CNAME added in one click
// through the workspace's own Cloudflare, like a sending domain's records.
func TestDNSOneClickElsewhere(t *testing.T) {
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
	added := func(typ, name string) map[string]any {
		mu.Lock()
		defer mu.Unlock()
		for _, r := range cfRecords {
			if r["type"] == typ && r["name"] == name {
				return r
			}
		}
		return nil
	}

	// A site to prove: no DNS connection yet.
	site := owner.must(201, "POST", base+"/seo-sites", map[string]any{"url": "https://www.northpeak.example/"})
	sitePath := base + "/seo-sites/" + site["id"].(string)
	if c := site["dns_connections"].([]any); len(c) != 0 || site["dns_install"] != nil {
		t.Fatalf("no connection yet: %v", site)
	}
	cfConn := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "cloudflare", "credentials": map[string]string{"api_token": "cf_test_not_a_real_token"}})["id"].(string)
	site = owner.must(200, "GET", sitePath, nil)
	if c := site["dns_connections"].([]any); len(c) != 1 || c[0] != cfConn {
		t.Fatalf("dns_connections = %v", c)
	}
	owner.must(400, "POST", sitePath+"/dns", map[string]any{"integration_id": "00000000-0000-4000-8000-000000000000"})
	owner.must(404, "POST", base+"/seo-sites/00000000-0000-4000-8000-000000000000/dns", map[string]any{"integration_id": cfConn})
	owner.must(202, "POST", sitePath+"/dns", map[string]any{"integration_id": cfConn})
	site = waitFor(t, owner, sitePath, func(s map[string]any) bool {
		in, _ := s["dns_install"].(map[string]any)
		return in != nil && in["status"] != "queued"
	})
	proof := site["verification"].(map[string]any)
	if in := site["dns_install"].(map[string]any); in["status"] != "done" || in["added"] != 1.0 || in["zone"] != "northpeak.example" {
		t.Fatalf("site install = %v", in)
	}
	if r := added("TXT", proof["dns_name"].(string)); r == nil || !strings.Contains(r["content"].(string), proof["dns_value"].(string)) {
		t.Fatalf("TXT proof not added: %v", cfRecords)
	}

	// A tracking domain's CNAME, pointing at this server.
	td := owner.must(201, "POST", base+"/tracking-domains", map[string]string{"domain": "links.northpeak.example"})
	tdID := td["id"].(string)
	if c := td["dns_connections"].([]any); len(c) != 1 || td["dns_install"] != nil {
		t.Fatalf("tracking domain: %v", td)
	}
	owner.must(404, "POST", base+"/tracking-domains/00000000-0000-4000-8000-000000000000/dns", map[string]any{"integration_id": cfConn})
	owner.must(202, "POST", base+"/tracking-domains/"+tdID+"/dns", map[string]any{"integration_id": cfConn})
	waitForTracking := func() map[string]any {
		for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(25 * time.Millisecond) {
			list := owner.must(200, "GET", base+"/tracking-domains", nil)["data"].([]any)
			d := list[0].(map[string]any)
			if in, _ := d["dns_install"].(map[string]any); in != nil && in["status"] != "queued" {
				return d
			}
		}
		t.Fatal("timed out waiting on the tracking domain")
		return nil
	}
	td = waitForTracking()
	if in := td["dns_install"].(map[string]any); in["status"] != "done" || in["added"] != 1.0 {
		t.Fatalf("tracking install = %v", in)
	}
	if r := added("CNAME", "links.northpeak.example"); r == nil || r["content"] != td["target"] {
		t.Fatalf("CNAME not added: %v", cfRecords)
	}

	for _, name := range []string{"seo_site.dns_requested", "seo_site.dns_added", "tracking_domain.dns_requested", "tracking_domain.dns_added"} {
		if len(named(got(), name)) == 0 {
			t.Errorf("no %s event", name)
		}
	}
}
