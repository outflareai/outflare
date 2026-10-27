package api_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/track"
)

// A site's DNS proof and a tracking domain's CNAME added in one click
// through the workspace's own Cloudflare, like a sending domain's records.
func TestDNSOneClickElsewhere(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	e.runServerAndMailJobs(t)

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
	// A connection whose sign-in broke isn't offered.
	if _, err := e.pool.Exec(t.Context(), `UPDATE integrations SET status = 'error' WHERE id = $1`, cfConn); err != nil {
		t.Fatal(err)
	}
	if c := owner.must(200, "GET", sitePath, nil)["dns_connections"].([]any); len(c) != 0 {
		t.Fatalf("broken connection offered: %v", c)
	}
	if _, err := e.pool.Exec(t.Context(), `UPDATE integrations SET status = 'connected' WHERE id = $1`, cfConn); err != nil {
		t.Fatal(err)
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

	// A cold email domain on Google with no SPF or DMARC record yet.
	e.dns.mx["northpeak.example"] = []*net.MX{{Host: "aspmx.l.google.com.", Pref: 1}}
	dPath := base + "/sending-domains/northpeak.example"
	owner.must(201, "POST", base+"/sending-domains", map[string]any{"domain": "northpeak.example"})
	d := waitFor(t, owner, dPath, func(m map[string]any) bool { return m["checked_at"] != nil })
	if c := d["dns_connections"].([]any); len(c) != 1 || c[0] != cfConn || d["dns_install"] != nil {
		t.Fatalf("sending domain: %v", d)
	}
	owner.must(404, "POST", base+"/sending-domains/nowhere.example/dns", map[string]any{"integration_id": cfConn})
	owner.must(202, "POST", dPath+"/dns", map[string]any{"integration_id": cfConn})
	d = waitFor(t, owner, dPath, func(m map[string]any) bool {
		in, _ := m["dns_install"].(map[string]any)
		return in != nil && in["status"] != "queued"
	})
	if in := d["dns_install"].(map[string]any); in["status"] != "done" || in["added"] != 2.0 {
		t.Fatalf("sending domain install = %v", in)
	}
	mu.Lock()
	spf := slices.ContainsFunc(cfRecords, func(r map[string]any) bool {
		return r["name"] == "northpeak.example" && strings.Contains(r["content"].(string), "v=spf1 include:_spf.google.com")
	})
	mu.Unlock()
	if !spf {
		t.Fatalf("SPF not added: %v", cfRecords)
	}
	if added("TXT", "_dmarc.northpeak.example") == nil {
		t.Fatalf("DMARC not added: %v", cfRecords)
	}
	// Once DNS has them, there's nothing left to add.
	e.dns.txt["northpeak.example"] = []string{"v=spf1 include:_spf.google.com ~all"}
	e.dns.txt["_dmarc.northpeak.example"] = []string{"v=DMARC1; p=none"}
	owner.must(202, "POST", dPath+"/check", nil)
	waitFor(t, owner, dPath, func(m map[string]any) bool { return !strings.Contains(issueCodes(m), "spf_missing") })
	owner.must(400, "POST", dPath+"/dns", map[string]any{"integration_id": cfConn})

	for _, name := range []string{"seo_site.dns_requested", "seo_site.dns_added", "tracking_domain.dns_requested", "tracking_domain.dns_added", "sending_domain.dns_requested", "sending_domain.dns_added"} {
		if len(named(got(), name)) == 0 {
			t.Errorf("no %s event", name)
		}
	}
}

// runServerAndMailJobs works the server's jobs and the mail jobs (domain
// checks) on one queue until the test ends.
func (e *env) runServerAndMailJobs(t *testing.T) {
	q := jobs.New(e.pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
	q.PollInterval = 20 * time.Millisecond
	e.srv.RegisterJobs(q)
	e.mail.Register(q)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { q.Run(ctx, 2); close(done) }() //nolint:errcheck
	t.Cleanup(func() { cancel(); <-done })
}
