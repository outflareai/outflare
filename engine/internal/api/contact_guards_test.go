package api_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The two contact guards connect like any API key connector: the address
// and the key in credentials, proved with one read-only call before
// anything is saved, and refused as they stand when the service refuses
// the key or the address is not an https one.
func TestContactGuardsConnect(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	refuse := false
	lookup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/check" || r.Header.Get("Authorization") != "Bearer test_not_a_real_key" || refuse {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		rows := []any{}
		for _, q := range r.URL.Query()["q"] {
			rows = append(rows, map[string]any{"input": q, "result": "no_match", "matchedBy": nil, "sources": []string{}, "reason": nil})
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"success": true, "result": map[string]any{"checkedAt": time.Now().UTC().Format(time.RFC3339), "results": rows}})
	}))
	defer lookup.Close()
	blocklist := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Emails []string `json:"emails"`
		}
		raw, _ := io.ReadAll(r.Body)
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/check" || r.Header.Get("Authorization") != "Bearer test_not_a_real_key" || json.Unmarshal(raw, &body) != nil {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		rows := []any{}
		for _, email := range body.Emails {
			rows = append(rows, map[string]any{"input": email, "status": "not_blocked"})
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"success": true, "result": map[string]any{"results": rows}})
	}))
	defer blocklist.Close()
	e.integrations.Client.BaseURLs["accountlookup"] = lookup.URL
	e.integrations.Client.BaseURLs["outreachblocklist"] = blocklist.URL

	for connector, address := range map[string]string{
		"accountlookup":     "https://admin.product.example/api/admin/v1/account-lookup",
		"outreachblocklist": "https://blocklist.company.example",
	} {
		conn := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": connector,
			"credentials": map[string]string{"base_url": address, "api_key": "test_not_a_real_key"}})
		if conn["connector"] != connector || conn["status"] != "connected" {
			t.Fatalf("%s: %v", connector, conn)
		}
		// The key never comes back.
		if raw, _ := json.Marshal(conn); strings.Contains(string(raw), "test_not_a_real_key") {
			t.Fatalf("%s: the key is in the answer: %s", connector, raw)
		}
		// An address that is not https, or has a sign-in in it, is refused
		// before any call.
		for _, bad := range []string{"http://admin.product.example/lookup", "https://user:pass@admin.product.example", "https://127.0.0.1/lookup"} {
			if st, out := owner.do("POST", base+"/integrations", map[string]any{"connector": connector,
				"credentials": map[string]string{"base_url": bad, "api_key": "test_not_a_real_key"}}); st != 422 || errCode(out) != "connector_invalid" {
				t.Fatalf("%s at %s: %d %v, want connector_invalid", connector, bad, st, out)
			}
		}
		// Both fields are needed.
		if st, out := owner.do("POST", base+"/integrations", map[string]any{"connector": connector,
			"credentials": map[string]string{"api_key": "test_not_a_real_key"}}); st < 400 || st >= 500 {
			t.Fatalf("%s without an address: %d %v", connector, st, out)
		}
	}
	// A key the service refuses is not saved.
	refuse = true
	if st, out := owner.do("POST", base+"/integrations", map[string]any{"connector": "accountlookup",
		"credentials": map[string]string{"base_url": "https://admin.product.example/lookup", "api_key": "test_not_a_real_key"}}); st < 400 || errCode(out) != "connector_auth" {
		t.Fatalf("a refused key: %d %v, want connector_auth", st, out)
	}
	// A blocklist address with a path is refused: it is an origin.
	if st, out := owner.do("POST", base+"/integrations", map[string]any{"connector": "outreachblocklist",
		"credentials": map[string]string{"base_url": "https://blocklist.company.example/v2", "api_key": "test_not_a_real_key"}}); st != 422 || errCode(out) != "connector_invalid" {
		t.Fatalf("a blocklist address with a path: %d %v", st, out)
	}
	if list := owner.must(200, "GET", base+"/integrations", nil)["data"].([]any); len(list) != 2 {
		t.Fatalf("%d connections saved, want the 2 that passed", len(list))
	}

	// Both are in the catalog with the capability and their two fields.
	cat := owner.must(200, "GET", "/v1/connectors", nil)
	found := 0
	for _, d := range cat["data"].([]any) {
		d := d.(map[string]any)
		if d["id"] != "accountlookup" && d["id"] != "outreachblocklist" {
			continue
		}
		found++
		caps, _ := json.Marshal(d["capabilities"])
		if string(caps) != `["guard.contact"]` || d["status"] != "available" {
			t.Errorf("%v: capabilities %s, status %v", d["id"], caps, d["status"])
		}
	}
	if found != 2 {
		t.Fatalf("%d guards in the catalog, want 2", found)
	}
}

// Each guard whose whole list can be read has a list sync: listed with
// how fresh its copy is, and turned off or run less often by a person.
func TestGuardListSyncs(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	lookup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rows := []any{}
		for _, q := range r.URL.Query()["q"] {
			rows = append(rows, map[string]any{"input": q, "result": "no_match", "matchedBy": nil, "sources": []string{}, "reason": nil})
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"success": true, "result": map[string]any{"checkedAt": time.Now().UTC().Format(time.RFC3339), "results": rows}})
	}))
	defer lookup.Close()
	e.integrations.Client.BaseURLs["accountlookup"] = lookup.URL
	if list := owner.must(200, "GET", base+"/guard-list-syncs", nil)["data"].([]any); len(list) != 0 {
		t.Fatalf("no guard connected: %v", list)
	}
	conn := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "accountlookup",
		"credentials": map[string]string{"base_url": "https://admin.product.example/api/admin/v1/account-lookup", "api_key": "test_not_a_real_key"}})
	id := conn["id"].(string)
	list := owner.must(200, "GET", base+"/guard-list-syncs", nil)["data"].([]any)
	if len(list) != 1 {
		t.Fatalf("syncs %v", list)
	}
	s := list[0].(map[string]any)
	if s["integration_id"] != id || s["mode"] != "feed" || s["reason"] != "customer" || s["source"] != "accountlookup_sync" ||
		s["enabled"] != true || s["interval_minutes"] != float64(5) || s["fresh"] != false || s["backfilled_at"] != nil {
		t.Fatalf("a new sync: %v", s)
	}
	got := owner.must(200, "PATCH", base+"/guard-list-syncs/"+id, map[string]any{"interval_minutes": 15, "enabled": false})
	if got["interval_minutes"] != float64(15) || got["enabled"] != false || got["default_interval_minutes"] != float64(5) {
		t.Fatalf("after the change: %v", got)
	}
	got = owner.must(200, "PATCH", base+"/guard-list-syncs/"+id, map[string]any{"interval_minutes": nil})
	if got["interval_minutes"] != float64(5) || got["enabled"] != false {
		t.Fatalf("back to the default: %v", got)
	}
	for _, body := range []map[string]any{{}, {"interval_minutes": 0}, {"interval_minutes": 2000}, {"interval_minutes": "5"}} {
		if st, out := owner.do("PATCH", base+"/guard-list-syncs/"+id, body); st != 400 {
			t.Fatalf("%v: %d %v, want 400", body, st, out)
		}
	}
	if st, _ := owner.do("PATCH", base+"/guard-list-syncs/00000000-0000-0000-0000-000000000001", map[string]any{"enabled": true}); st != 404 {
		t.Fatalf("an unknown connection: %d, want 404", st)
	}
}
