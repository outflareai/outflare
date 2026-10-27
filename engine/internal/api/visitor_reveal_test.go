package api_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestVisitorCompanyReveal(t *testing.T) {
	e := freshEnv(t)
	var mu sync.Mutex
	calls := map[string]int{}
	sn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/company/find" || r.Header.Get("Authorization") != "Bearer test-not-a-real-key" {
			w.WriteHeader(401)
			return
		}
		ip := r.URL.Query().Get("ip")
		mu.Lock()
		calls[ip]++
		mu.Unlock()
		switch ip {
		case "192.0.2.10":
			io.WriteString(w, `{"type":"business","company":{"name":"Northpeak","domain":"northpeak.example"}}`)
		case "192.0.2.12":
			io.WriteString(w, `{"type":"business","company":{"name":"Acme Inc","domain":"acme.example"}}`)
		case "192.0.2.15":
			io.WriteString(w, `{"type":"business","company":{"name":"Initech","domain":"initech.example"}}`)
		default:
			io.WriteString(w, `{"type":"isp","company":{"name":"Home ISP","domain":"isp.example"}}`)
		}
	}))
	defer sn.Close()
	e.integrations.Client.BaseURLs["snitcher"] = sn.URL
	called := func(ip string) int {
		mu.Lock()
		defer mu.Unlock()
		return calls[ip]
	}
	e.runCRMImports(t) // works the server's own jobs, lookups among them
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	key := owner.must(201, "POST", base+"/site-keys", map[string]any{"name": "Site", "origins": []string{"https://acme.example"}})["key"].(string)

	visit := func(anon, ip string, consent bool) {
		t.Helper()
		b, _ := json.Marshal(map[string]any{"key": key, "consent": consent, "batch": []map[string]any{{"type": "page", "anonymousId": anon,
			"context": map[string]any{"page": map[string]any{"path": "/pricing", "url": "https://acme.example/pricing"}}}}})
		req := httptest.NewRequest("POST", "/v1/site/events", strings.NewReader(string(b)))
		req.Header.Set("Content-Type", "text/plain;charset=UTF-8")
		req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 14_0) AppleWebKit/605.1.15 Safari/605.1.15")
		req.Header.Set("Origin", "https://acme.example")
		req.RemoteAddr = ip + ":4321"
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("visit: %d %s", rec.Code, rec.Body)
		}
		for range 500 {
			var pending int
			if err := e.pool.QueryRow(t.Context(), `SELECT count(*) FROM jobs WHERE kind = 'visitor_reveal' AND status IN ('queued', 'running')`).Scan(&pending); err != nil {
				t.Fatal(err)
			}
			if pending == 0 {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		var why string
		e.pool.QueryRow(t.Context(), `SELECT coalesce(string_agg(payload->>'anonymous_id' || ': ' || coalesce(last_error, ''), '; '), '') FROM jobs WHERE kind = 'visitor_reveal' AND status IN ('queued', 'running')`).Scan(&why)
		t.Fatal("lookups still pending: " + why)
	}
	count := func(q string, args ...any) int {
		t.Helper()
		var n int
		if err := e.pool.QueryRow(t.Context(), q, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// Without a provider connected, nothing is looked up.
	visit("b-none", "192.0.2.10", true)
	if n := count(`SELECT count(*) FROM jobs WHERE kind = 'visitor_reveal'`); n != 0 {
		t.Fatalf("looked up with no provider: %d", n)
	}

	conn := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "snitcher", "credentials": map[string]any{"api_key": "test-not-a-real-key"}})
	owner.must(400, "PUT", base+"/visitors/settings", map[string]any{"reveal_daily_limit": 0})
	owner.must(400, "PUT", base+"/visitors/settings", map[string]any{"reveal_integration_id": "00000000-0000-0000-0000-000000000000"})
	// Connecting a provider doesn't turn lookups on: the workspace does.
	if set := owner.must(200, "GET", base+"/visitors/settings", nil); set["reveal_companies"] != false {
		t.Fatalf("on by default: %v", set)
	}
	visit("b-off", "192.0.2.10", true)
	if called("192.0.2.10") != 0 {
		t.Fatal("looked up before it was turned on")
	}
	owner.must(200, "PUT", base+"/visitors/settings", map[string]any{"hot_score": 10, "reveal_integration_id": conn["id"], "reveal_companies": true})

	// A visitor who didn't accept cookies is not looked up.
	visit("b-1", "192.0.2.10", false)
	if called("192.0.2.10") != 0 {
		t.Fatalf("looked up without consent: %v", called("192.0.2.13"))
	}
	// With consent: Northpeak is found, but a lookup never makes a CRM
	// record by itself; it is listed as not in the CRM.
	visit("b-1", "192.0.2.10", true)
	if called("192.0.2.10") != 1 {
		t.Fatalf("calls: %d", called("192.0.2.10"))
	}
	if n := count(`SELECT count(*) FROM site_visitors WHERE anonymous_id = 'b-1' AND company_domain = 'northpeak.example' AND company_name = 'Northpeak'
		AND account_id IS NULL AND identified_by = 'company_ip'`); n != 1 {
		t.Fatalf("visitor not marked with the company: %d", n)
	}
	if n := count(`SELECT count(*) FROM records WHERE workspace_id = $1 AND record_type = 'account'`, ws); n != 0 {
		t.Fatalf("a lookup made %d records", n)
	}
	if n := count(`SELECT count(*) FROM audit_events WHERE action = 'visitor.company_revealed'`); n != 1 {
		t.Fatalf("reveal events: %d", n)
	}
	// No address is stored anywhere.
	if n := count(`SELECT count(*) FROM visitor_ip_lookups WHERE ip_hash LIKE '%192.0.2%'`) + count(`SELECT count(*) FROM jobs WHERE payload::text LIKE '%192.0.2%'`); n != 0 {
		t.Fatalf("address stored: %d", n)
	}

	// Another browser on the same address is answered from the last lookup.
	visit("b-2", "192.0.2.10", true)
	if called("192.0.2.10") != 1 || count(`SELECT count(*) FROM site_visitors WHERE anonymous_id = 'b-2' AND company_domain = 'northpeak.example'`) != 1 {
		t.Fatalf("cache: %d", called("192.0.2.10"))
	}
	companies := owner.must(200, "GET", base+"/visitors", nil)["data"].([]any)
	if len(companies) != 1 || companies[0].(map[string]any)["record_id"] != nil || companies[0].(map[string]any)["in_crm"] != false ||
		companies[0].(map[string]any)["name"] != "Northpeak" || companies[0].(map[string]any)["domain"] != "northpeak.example" ||
		companies[0].(map[string]any)["browsers"] != 2.0 || companies[0].(map[string]any)["identified_by"].([]any)[0] != "company_ip" {
		t.Fatalf("companies: %v", companies)
	}

	// Someone adds it to the CRM: a prospect, marked as from a lookup.
	owner.must(404, "POST", base+"/visitors/companies", map[string]any{"domain": "nobody.example"})
	added := owner.must(201, "POST", base+"/visitors/companies", map[string]any{"domain": "northpeak.example"})
	acct := added["record_id"].(string)
	if n := count(`SELECT count(*) FROM records r JOIN record_identities i ON i.record_id = r.id WHERE r.id = $1::uuid AND r.origin = 'prospect' AND r.name = 'Northpeak'
		AND i.namespace = 'domain' AND i.value = 'northpeak.example' AND i.source = 'visitor_reveal' AND NOT i.verified`, acct); n != 1 {
		t.Fatal("added company not marked as from a lookup")
	}
	if again := owner.must(200, "POST", base+"/visitors/companies", map[string]any{"domain": "northpeak.example"}); again["record_id"] != acct || again["created"] != false {
		t.Fatalf("added twice: %v", again)
	}
	if n := count(`SELECT count(*) FROM site_visitors WHERE anonymous_id IN ('b-1', 'b-2') AND account_id = $1::uuid`, acct); n != 2 {
		t.Fatalf("visitors not on the added company: %d", n)
	}
	if c := owner.must(200, "GET", base+"/visitors", nil)["data"].([]any); len(c) != 1 || c[0].(map[string]any)["record_id"] != acct || c[0].(map[string]any)["in_crm"] != true {
		t.Fatalf("after adding: %v", c)
	}

	// A company the CRM has already is put on the visitor straight away, and
	// can turn hot.
	existing := owner.must(201, "POST", base+"/records", map[string]any{"type": "account", "name": "Acme",
		"identities": []map[string]any{{"namespace": "domain", "value": "acme.example"}}})["id"].(string)
	visit("b-4", "192.0.2.12", true)
	if n := count(`SELECT count(*) FROM site_visitors WHERE anonymous_id = 'b-4' AND account_id = $1::uuid`, existing); n != 1 {
		t.Fatal("existing company not linked")
	}
	if n := count(`SELECT count(*) FROM audit_events WHERE action = 'visitor.became_hot' AND data->>'kind' = 'company' AND target_id = $1`, existing); n != 1 {
		t.Fatalf("company hot alerts: %d", n)
	}

	// A home connection names nobody; an excluded domain is left alone.
	visit("b-3", "192.0.2.11", true)
	if n := count(`SELECT count(*) FROM site_visitors WHERE anonymous_id = 'b-3' AND network = 'isp' AND company_domain = ''`); n != 1 {
		t.Fatalf("home connection not marked: %d", n)
	}
	owner.must(200, "PUT", base+"/visitors/settings", map[string]any{"exclude_domains": []string{"initech.example"}})
	visit("b-5", "192.0.2.15", true)
	if n := count(`SELECT count(*) FROM site_visitors WHERE anonymous_id = 'b-5' AND (company_domain <> '' OR account_id IS NOT NULL)`); n != 0 || called("192.0.2.15") != 1 {
		t.Fatalf("excluded got a company: %d", n)
	}
	set := owner.must(200, "GET", base+"/visitors/settings", nil)
	if set["reveals_today"] != 4.0 || len(set["reveal_connections"].([]any)) != 1 || set["reveal_daily_limit"] != 500.0 || len(set["not_companies"].([]any)) != 0 {
		t.Fatalf("settings: %v", set)
	}

	// Marked as not a company (say the provider took an ISP's customers for
	// the ISP): its visitors lose it, and a record only the lookups put in
	// the CRM is archived.
	owner.must(400, "PUT", base+"/visitors/settings", map[string]any{"not_companies": []string{"not a domain"}})
	owner.must(200, "PUT", base+"/visitors/settings", map[string]any{"not_companies": []string{"Northpeak.example"}})
	if n := count(`SELECT count(*) FROM site_visitors WHERE anonymous_id IN ('b-1', 'b-2') AND account_id IS NULL AND company_domain = '' AND network = 'isp' AND identified_by = ''`); n != 2 {
		t.Fatalf("visitors kept the company: %d", n)
	}
	if n := count(`SELECT count(*) FROM records WHERE id = $1::uuid AND archived_at IS NOT NULL`, acct); n != 1 {
		t.Fatal("lookup-only company not archived")
	}

	// The daily limit stops lookups.
	owner.must(200, "PUT", base+"/visitors/settings", map[string]any{"reveal_daily_limit": 4})
	visit("b-5", "192.0.2.13", true)
	if called("192.0.2.13") != 0 {
		t.Fatalf("over the limit: %v", called("192.0.2.13"))
	}
	// Turned off, nothing is queued.
	owner.must(200, "PUT", base+"/visitors/settings", map[string]any{"reveal_companies": false})
	visit("b-6", "192.0.2.14", true)
	if n := count(`SELECT count(*) FROM jobs WHERE kind = 'visitor_reveal' AND payload->>'anonymous_id' = 'b-6'`); n != 0 {
		t.Fatalf("queued while off: %d", n)
	}
}
