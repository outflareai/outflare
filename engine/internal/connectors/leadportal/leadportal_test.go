package leadportal_test

import (
	"context"
	"encoding/json"
	"fmt"
	c "github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/connectors/leadportal"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestWorkspaceCredentialAndReadOnlyRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer lp_test_not_a_real_key" || r.URL.Path != "/api/v1/reports/sales-reporting" {
			t.Error("changed request or credential")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"schema_version":1}`))
	}))
	defer srv.Close()
	client := &c.Client{}
	creds := c.Creds{"base_url": srv.URL, "api_key": "lp_test_not_a_real_key"}
	v := leadportal.Connector{}
	if _, e := v.Test(context.Background(), client, creds); e != nil {
		t.Fatal(e)
	}
	if _, e := v.Read(context.Background(), client, c.Creds{"base_url": srv.URL}, url.Values{}); e == nil {
		t.Fatal("missing key accepted")
	}
	for _, base := range []string{"ftp://portal.example", "https://user:password@portal.example", "https://portal.example?key=test"} {
		if _, e := v.Read(context.Background(), client, c.Creds{"base_url": base, "api_key": "lp_test_not_a_real_key"}, nil); e == nil {
			t.Fatal("unsafe base accepted")
		}
	}
}

func TestOriginLookupValidatesIdentityAndRequestedLeads(t *testing.T) {
	const leadID = "00000000-0000-4000-8000-000000000001"
	for _, name := range []string{"valid", "extra_lead", "credential", "bad_kind", "bad_id", "bad_date", "missing_map"} {
		t.Run(name, func(t *testing.T) {
			origin := map[string]any{"id": "worker:10000000-0000-4000-8000-000000000001", "name": "Discovery Example", "kind": "worker", "revoked": true}
			id := leadID
			switch name {
			case "extra_lead":
				id = "00000000-0000-4000-8000-000000000002"
			case "credential":
				origin["key_hash"] = "test-not-a-real-hash"
			case "bad_kind":
				origin["kind"] = "researcher"
			case "bad_id":
				origin["id"] = "worker:invalid"
			case "bad_date":
				origin["submitted_at"] = "yesterday"
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if name == "missing_map" {
					json.NewEncoder(w).Encode(map[string]any{})
					return
				}
				json.NewEncoder(w).Encode(map[string]any{"origins": map[string]any{id: origin}})
			}))
			defer srv.Close()
			_, err := (leadportal.Connector{}).Read(context.Background(), &c.Client{}, c.Creds{"base_url": srv.URL, "api_key": "lp_test_not_a_real_key"}, url.Values{"lead_ids": {leadID}})
			if (err == nil) != (name == "valid") {
				t.Fatalf("unexpected validation result: %v", err)
			}
		})
	}
}

func TestDiscoveryBlockIsKeptWhenWellFormedAndDroppedOtherwise(t *testing.T) {
	const report = `{"schema_version":1,"generated_at":"2026-01-02T03:00:00Z","from":"2026-01-01T03:00:00Z","to":"2026-01-02T03:00:00Z","bucket":"hour","buckets":[],"totals":{"discovered":2},"discovery":%s}`
	const good = `{"missions":[{"id":"00000000-0000-4000-8000-000000000001","name":"Example mission","state":"running","budget_cents":2000,"spent_cents":150.25}],
		"totals":{"found":9,"new":6,"passed":3,"graded_ab":2,"sent":2},
		"buckets":[{"at":"2026-01-02T02:00:00+00:00","counts":{"found":9,"sent":2}}],
		"lanes":{"total":2,"by_status":{"productive":1,"low_yield":1},"rows":[{"title":"Example lane","method":"keyword","status":"productive","found":9,"new":6,"passed":3}]},
		"spend":{"window_cents":12.5,"rows":[{"tool":"Example search","units":4,"cents":12.5},{"tool":"Own scraper","units":30,"cents":null}]}}`
	cases := map[string]string{
		"good":            good,
		"absent":          `null`,
		"unknown count":   strings.Replace(good, `"found":9,"new":6`, `"found":9,"emails":6`, 1),
		"negative count":  strings.Replace(good, `"passed":3,"graded_ab"`, `"passed":-3,"graded_ab"`, 1),
		"company in lane": strings.Replace(good, `"status":"productive","found":9`, `"status":"productive","found":"acme.example"`, 1),
		"not an object":   `["found"]`,
	}
	for name, block := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, report, block)
		}))
		out, e := leadportal.Connector{}.Read(context.Background(), &c.Client{}, c.Creds{"base_url": srv.URL, "api_key": "lp_test_not_a_real_key"}, url.Values{"period": {"24h"}})
		srv.Close()
		if e != nil {
			t.Fatalf("%s: a bad discovery block must not fail the report: %v", name, e)
		}
		if out["totals"].(map[string]any)["discovered"] != float64(2) {
			t.Fatalf("%s: stage totals lost", name)
		}
		if kept := out["discovery"] != nil; kept != (name == "good") {
			t.Fatalf("%s: discovery kept=%v", name, kept)
		}
	}
}

func TestLaneBoardPagesAreValidated(t *testing.T) {
	const good = `{"lanes":{"total":1,"offset":0,"rows":[{"id":"00000000-0000-4000-8000-000000000001","title":"Example lane","method":"keyword","origin":"initial","status":"productive",
		"segment":null,"idea":"Example idea","why":null,"verdict":"Scaling","learned":"Example lesson","recipe":{"queries":["example query"],"urls":[],"seeds":[],"describe":[],"jobs":[]},
		"found":9,"new":6,"known":3,"screened":5,"passed":3,"ai_cents":1.25,"expected_size":null,"last_activity_at":"2026-01-02T02:00:00+00:00","created_at":"2026-01-01T02:00:00+00:00"}]}}`
	cases := map[string]string{
		"good":            good,
		"unknown list":    strings.Replace(good, `"jobs":[]`, `"emails":[]`, 1),
		"list not text":   strings.Replace(good, `"queries":["example query"]`, `"queries":[{"x":1}]`, 1),
		"negative count":  strings.Replace(good, `"passed":3`, `"passed":-3`, 1),
		"missing title":   strings.Replace(good, `"title":"Example lane",`, ``, 1),
		"not a lane page": `{"totals":{}}`,
	}
	for name, body := range cases {
		var asked url.Values
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			asked = r.URL.Query()
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(body))
		}))
		out, e := leadportal.Connector{}.Read(context.Background(), &c.Client{}, c.Creds{"base_url": srv.URL, "api_key": "lp_test_not_a_real_key"}, url.Values{"lanes": {"1"}, "lane_status": {"productive"}, "offset": {"0"}})
		srv.Close()
		if asked.Get("lanes") != "1" || asked.Get("lane_status") != "productive" {
			t.Fatalf("%s: lane query not passed through", name)
		}
		if (e == nil) != (name == "good") {
			t.Fatalf("%s: accepted=%v", name, e == nil)
		}
		if name == "good" && out["lanes"].(map[string]any)["total"] != float64(1) {
			t.Fatal("lane page lost")
		}
	}
}

func TestDiscoveryTraceRejectsMalformedReceipts(t *testing.T) {
	good := `{"scope":"latest_20_runs","total":1,"offset":0,"summary":{"calls":1,"errors":0,"recorded_cents":null,"unknown_cost_calls":1},"runs":[],"tools":[],"lanes":[],"rows":[{"id":"example","run_id":"example","mission_id":"example","operation_id":"example","lane_id":null,"company_id":null,"agent":"example","phase":"collect","tool":"search","provider":"example","status":"ok","kind":"tool","started_at":"2026-01-02T00:00:00Z","duration_ms":10,"attempt":1,"cost_cents":null,"input":{"query":"price tracking"},"output":{"results":2}}]}`
	for name, payload := range map[string]string{"valid": good, "negative_cost": strings.Replace(good, `"cost_cents":null`, `"cost_cents":-1`, 1), "credential": strings.Replace(good, `"query":"price tracking"`, `"api_key":"test-not-a-real-key"`, 1), "bad_date": strings.Replace(good, "2026-01-02T00:00:00Z", "yesterday", 1), "bad_status": strings.Replace(good, `"status":"ok"`, `"status":"invented"`, 1)} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("trace") != "1" {
					t.Error("missing trace mode")
				}
				fmt.Fprintf(w, `{"trace":%s}`, payload)
			}))
			defer srv.Close()
			_, err := (leadportal.Connector{}).Read(context.Background(), &c.Client{}, c.Creds{"base_url": srv.URL, "api_key": "lp_test_not_a_real_key"}, url.Values{"trace": {"1"}})
			if (err == nil) != (name == "valid") {
				t.Fatalf("validation: %v", err)
			}
		})
	}
}
