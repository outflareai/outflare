package api_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/emails"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/track"
)

// A sending domain's records added in one click through the workspace's
// own Cloudflare, the prompt and message for the other ways, and the
// checks that say what's wrong in words.
func TestEmailDomainDNS(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	rs := httptest.NewServer(&fakeResend{})
	defer rs.Close()
	e.integrations.Client.BaseURLs["resend"] = rs.URL
	// Cloudflare holds the zone, with an SPF record already on send. Its
	// fake is set up before the job queue starts: BaseURLs is a plain map
	// the jobs read, so writing it while they run is a data race.
	var mu sync.Mutex
	cfRecords := []map[string]any{{"type": "TXT", "name": "send.example.com", "content": `"v=spf1 include:_spf.example.net ~all"`}}
	cf := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.URL.Path == "/zones":
			if name := r.URL.Query().Get("name"); name == "" || name == "example.com" {
				json.NewEncoder(w).Encode(map[string]any{"result": []any{map[string]any{"id": "z1", "name": "example.com"}}})
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
	defer cf.Close()
	e.integrations.Client.BaseURLs["cloudflare"] = cf.URL
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := &emails.Service{Pool: e.pool, Store: e.integrations, Box: e.box, PublicURL: "https://outflare.test", Log: log, Resolver: e.dns}
	q := jobs.New(e.pool, log)
	q.PollInterval = 20 * time.Millisecond
	svc.Register(q)
	qctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { q.Run(qctx, 2); close(done) }() //nolint:errcheck
	t.Cleanup(func() { cancel(); <-done })

	owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "resend", "credentials": map[string]any{"api_key": "re_test_not_a_real_key"}})
	// The domain already has a DMARC policy, and the domain typed twice
	// in one record's name.
	e.dns.setTXT("_dmarc.example.com", []string{"v=DMARC1; p=reject"})
	e.dns.setTXT("resend._domainkey.example.com.example.com", []string{"p=KEY"})
	did := owner.must(201, "POST", base+"/email-provider-domains", map[string]any{"domain": "example.com"})["id"].(string)
	d := waitFor(t, owner, base+"/email-provider-domains/"+did, func(d map[string]any) bool { return len(d["records"].([]any)) > 0 })
	recs := map[string]map[string]any{}
	for _, r := range d["records"].([]any) {
		r := r.(map[string]any)
		recs[r["purpose"].(string)+" "+r["type"].(string)] = r
	}
	if r := recs["dmarc TXT"]; r["status"] != "verified" || r["found"] != "v=DMARC1; p=reject" {
		t.Fatalf("DMARC in place shows the one there: %v", r)
	}
	if r := recs["dkim TXT"]; r["status"] != "missing" || !strings.Contains(r["hint"].(string), "resend._domainkey.example.com.example.com") {
		t.Fatalf("doubled name hint: %v", r)
	}
	prompt := d["ai_prompt"].(string)
	if !strings.Contains(prompt, "send.example.com") || !strings.Contains(prompt, "Priority: 10") || strings.Contains(prompt, "_dmarc") ||
		!strings.Contains(prompt, "Don't ask me to paste") || !strings.Contains(d["developer"].(map[string]any)["body"].(string), "resend._domainkey.example.com") {
		t.Fatalf("prompt = %q, developer = %v", prompt, d["developer"])
	}
	if len(d["dns_connections"].([]any)) != 0 || d["dns_install"] != nil {
		t.Fatalf("no DNS connection yet: %v", d)
	}

	cfConn := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "cloudflare", "credentials": map[string]string{"api_token": "cf_test_not_a_real_token"}})["id"].(string)
	d = owner.must(200, "GET", base+"/email-provider-domains/"+did, nil)
	if c := d["dns_connections"].([]any); len(c) != 1 || c[0] != cfConn {
		t.Fatalf("dns_connections = %v", c)
	}
	resend := owner.must(200, "GET", base+"/integrations", nil)["data"].([]any)[0].(map[string]any)["id"].(string)
	owner.must(400, "POST", base+"/email-provider-domains/"+did+"/dns", map[string]any{"integration_id": resend})
	owner.must(404, "POST", base+"/email-provider-domains/00000000-0000-4000-8000-000000000000/dns", map[string]any{"integration_id": cfConn})

	// A second SPF record would break both: refused, in words.
	owner.must(202, "POST", base+"/email-provider-domains/"+did+"/dns", map[string]any{"integration_id": cfConn})
	d = waitFor(t, owner, base+"/email-provider-domains/"+did, func(d map[string]any) bool {
		in, _ := d["dns_install"].(map[string]any)
		return in != nil && in["status"] != "queued"
	})
	if in := d["dns_install"].(map[string]any); in["status"] != "failed" || !strings.Contains(in["error"].(string), "already has an SPF record") {
		t.Fatalf("SPF clash: %v", in)
	}

	// Without it, every missing record is added; the DMARC policy stays.
	mu.Lock()
	cfRecords = nil
	mu.Unlock()
	owner.must(202, "POST", base+"/email-provider-domains/"+did+"/dns", map[string]any{"integration_id": cfConn})
	d = waitFor(t, owner, base+"/email-provider-domains/"+did, func(d map[string]any) bool {
		return d["dns_install"].(map[string]any)["status"] != "queued"
	})
	if in := d["dns_install"].(map[string]any); in["status"] != "done" || in["added"] != float64(3) || in["zone"] != "example.com" {
		t.Fatalf("install = %v", in)
	}
	mu.Lock()
	byName := map[string]map[string]any{}
	for _, r := range cfRecords {
		byName[r["type"].(string)+" "+r["name"].(string)] = r
	}
	mu.Unlock()
	if mx := byName["MX send.example.com"]; mx == nil || mx["priority"] != float64(10) || mx["content"] != "feedback-smtp.us-east-1.amazonses.com" ||
		byName["TXT resend._domainkey.example.com"] == nil || byName["TXT send.example.com"] == nil || byName["TXT _dmarc.example.com"] != nil {
		t.Fatalf("records = %v", byName)
	}

	for _, name := range []string{"email_domain.dns_requested", "email_domain.dns_added", "email_domain.dns_failed"} {
		if len(named(got(), name)) == 0 {
			t.Errorf("no %s event", name)
		}
	}
}
