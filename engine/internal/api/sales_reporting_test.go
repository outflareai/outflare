package api_test

import (
	"context"
	"encoding/json"
	"github.com/outflareai/outflare/engine/internal/track"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestSalesReportingConnectionAndIsolation(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	rec := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Ana Example", "identities": []map[string]any{{"namespace": "email", "value": "ana@customer.example"}}})
	brand := rec["brand_id"].(string)
	root := "/v1/brands/" + brand + "/sales/reporting"
	r := owner.must(200, "GET", root+"?period=24h", nil)
	if r["source"].(map[string]any)["status"] != "missing" {
		t.Fatal("missing connection presented as connected")
	}
	owner.must(400, "GET", root+"?period=invalid", nil)
	owner.must(400, "GET", root+"/leads?stages=invalid", nil)
	owner.must(400, "GET", root+"?from=invalid", nil)
	var calls atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer lp_test_not_a_real_key" {
			t.Error("wrong workspace credential")
		}
		w.Header().Set("Content-Type", "application/json")
		var body any = map[string]any{"schema_version": 1}
		if r.URL.Query().Get("check") != "1" {
			if r.URL.Query().Has("lead_ids") {
				body = map[string]any{"origins": map[string]any{"00000000-0000-4000-8000-000000000099": map[string]any{"id": "worker:10000000-0000-4000-8000-000000000001", "name": "Discovery Example", "kind": "worker", "revoked": true}}}
			} else if r.URL.Query().Has("stages") {
				body = map[string]any{"groups": []any{map[string]any{"stage": "discovered", "total": 1, "offset": 0, "leads": []any{map[string]any{"id": "source-1", "company": "Example", "status": "research", "at": "2026-11-10T12:00:00Z"}}}}}
			} else {
				body = map[string]any{"schema_version": 1, "generated_at": "2026-11-11T12:00:00Z", "from": r.URL.Query().Get("from"), "to": r.URL.Query().Get("to"), "bucket": "hour", "buckets": []any{map[string]any{"at": "2026-11-10T12:00:00Z", "counts": map[string]int{"discovered": 1}}}, "totals": map[string]int{"discovered": 1}}
			}
		}
		json.NewEncoder(w).Encode(body)
	}))
	defer provider.Close()
	e.integrations.Client.BaseURLs["leadportal"] = provider.URL
	conn := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "leadportal", "brand_id": brand, "credentials": map[string]string{"base_url": "https://portal.example", "api_key": "lp_test_not_a_real_key"}})
	r = owner.must(200, "GET", root+"?from=2026-11-10T00:00:00Z&to=2026-11-11T00:00:00Z", nil)
	if r["totals"].(map[string]any)["discovered"] != float64(1) || r["source"].(map[string]any)["status"] != "connected" {
		t.Fatal(r)
	}
	detail := owner.must(200, "GET", root+"/leads?from=2026-11-10T00:00:00Z&to=2026-11-11T00:00:00Z&stages=discovered,sdr", nil)
	if len(detail["groups"].([]any)) != 2 {
		t.Fatal("selected stages lost", detail)
	}
	other := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Other Example", "handle": "other-example"})["id"].(string)
	before := calls.Load()
	owner.must(404, "GET", "/v1/brands/"+other+"/sales/reporting?integration="+conn["id"].(string), nil)
	if calls.Load() != before {
		t.Fatal("called another brand's connection")
	}
	// A read token cannot acquire manager access through its owner's role.
	token := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "Report reader", "scopes": []string{"customers.read"}})
	reader := &client{t: t, h: e.h, bearer: token["token"].(string)}
	reader.must(403, "GET", root, nil)
	// Only recorded native milestones count, and retries remain one company.
	_, err := e.pool.Exec(context.Background(), `INSERT INTO sales_leads(id,workspace_id,brand_id,record_id,source,source_id,source_sealed,details_sealed,state,amount_minor,currency) VALUES('00000000-0000-4000-8000-000000000001',$1,$2,$3,'test','native',decode('00','hex'),decode('00','hex'),'customer',12500,'USD')`, ws, brand, rec["id"])
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.pool.Exec(context.Background(), `INSERT INTO sales_events(id,workspace_id,brand_id,record_id,lead_id,actor_role,action,content_sealed,occurred_at) VALUES(gen_random_uuid(),$1,$2,$3,'00000000-0000-4000-8000-000000000001','sdr','lead_taken',decode('00','hex'),'2026-11-10T12:00:00Z'),(gen_random_uuid(),$1,$2,$3,'00000000-0000-4000-8000-000000000001','sdr','lead_taken',decode('00','hex'),'2026-11-10T13:00:00Z'),(gen_random_uuid(),$1,$2,$3,'00000000-0000-4000-8000-000000000001','ae','customer_confirmed',decode('00','hex'),'2026-11-10T14:00:00Z')`, ws, brand, rec["id"])
	if err != nil {
		t.Fatal(err)
	}
	r = owner.must(200, "GET", root+"?from=2026-11-10T00:00:00Z&to=2026-11-11T00:00:00Z", nil)
	tot := r["totals"].(map[string]any)
	if tot["sdr"] != float64(1) || tot["closed"] != float64(1) {
		t.Fatal("native milestones", tot)
	}
	rows := r["agents"].([]any)
	if len(rows) != 1 || rows[0].(map[string]any)["attribution"].(map[string]any)["id"] != "unknown" || rows[0].(map[string]any)["counts"].(map[string]any)["closed"] != float64(1) || rows[0].(map[string]any)["counts"].(map[string]any)["discovered"] != float64(1) {
		t.Fatal("unknown origins must reconcile with report totals", r)
	}
	nativeDetail := owner.must(200, "GET", root+"/leads?from=2026-11-10T00:00:00Z&to=2026-11-11T00:00:00Z&stages=closed", nil)
	nativeGroup := nativeDetail["groups"].([]any)[0].(map[string]any)
	if nativeGroup["total"] != float64(1) || nativeGroup["leads"].([]any)[0].(map[string]any)["amount_minor"] != float64(12500) {
		t.Fatal("missing native detail or value", nativeDetail)
	}
	otherReport := owner.must(200, "GET", "/v1/brands/"+other+"/sales/reporting?from=2026-11-10T00:00:00Z&to=2026-11-11T00:00:00Z", nil)
	if otherReport["totals"].(map[string]any)["closed"] != float64(0) {
		t.Fatal("cross-brand native results")
	}
}

func TestSalesAttributionSourceLookup(t *testing.T) {
	gotEvents := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	rec := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Alex Example"})
	brand := rec["brand_id"].(string)
	root := "/v1/brands/" + brand + "/sales"
	var calls atomic.Int64
	var failOrigins atomic.Bool
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("check") == "1" {
			json.NewEncoder(w).Encode(map[string]any{"schema_version": 1})
			return
		}
		if !r.URL.Query().Has("lead_ids") {
			json.NewEncoder(w).Encode(map[string]any{"schema_version": 1, "generated_at": "2026-11-11T00:00:00Z", "from": r.URL.Query().Get("from"), "to": r.URL.Query().Get("to"), "bucket": "hour", "buckets": []any{}, "totals": map[string]int{"discovered": 2}, "agents": []any{map[string]any{"attribution": map[string]any{"id": "worker:10000000-0000-4000-8000-000000000001", "name": "Discovery Example", "kind": "worker", "revoked": true}, "counts": map[string]int{"discovered": 2}}}})
			return
		}
		if failOrigins.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if r.URL.Query().Get("lead_ids") != "00000000-0000-4000-8000-000000000099" {
			t.Errorf("unexpected source identities: %s", r.URL.RawQuery)
		}
		if r.Header.Get("Authorization") != "Bearer lp_test_not_a_real_key" {
			t.Error("wrong credential")
		}
		json.NewEncoder(w).Encode(map[string]any{"origins": map[string]any{"00000000-0000-4000-8000-000000000099": map[string]any{"id": "worker:10000000-0000-4000-8000-000000000001", "name": "Discovery Example", "kind": "worker", "revoked": true}}})
	}))
	defer provider.Close()
	e.integrations.Client.BaseURLs["leadportal"] = provider.URL
	owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "leadportal", "brand_id": brand, "credentials": map[string]string{"base_url": "https://portal.example", "api_key": "lp_test_not_a_real_key"}})
	_, err := e.pool.Exec(context.Background(), `INSERT INTO sales_leads(id,workspace_id,brand_id,record_id,source,source_id,source_sealed,state) VALUES('00000000-0000-4000-8000-000000000001',$1,$2,$3,'leads_portal','00000000-0000-4000-8000-000000000099',decode('00','hex'),'new')`, ws, brand, rec["id"])
	if err != nil {
		t.Fatal(err)
	}
	queue := owner.must(200, "GET", root+"/leads?queue=all", nil)
	origin := queue["data"].([]any)[0].(map[string]any)["attribution"].(map[string]any)
	if origin["name"] != "Discovery Example" || origin["revoked"] != true {
		t.Fatal("uploader missing from queue", origin)
	}
	before := calls.Load()
	other := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Other Example", "handle": "other-origin"})["id"].(string)
	owner.must(404, "GET", "/v1/brands/"+other+"/sales/leads/00000000-0000-4000-8000-000000000001", nil)
	if calls.Load() != before {
		t.Fatal("origin queried before brand visibility check")
	}
	_, err = e.pool.Exec(context.Background(), `INSERT INTO sales_events(id,workspace_id,brand_id,record_id,lead_id,actor_role,action,content_sealed,occurred_at) VALUES(gen_random_uuid(),$1,$2,$3,'00000000-0000-4000-8000-000000000001','ae','customer_confirmed',decode('00','hex'),'2026-11-10T12:00:00Z')`, ws, brand, rec["id"])
	if err != nil {
		t.Fatal(err)
	}
	detail := owner.must(200, "GET", root+"/reporting/leads?from=2026-11-10T00:00:00Z&to=2026-11-11T00:00:00Z&stages=closed", nil)
	lead := detail["groups"].([]any)[0].(map[string]any)["leads"].([]any)[0].(map[string]any)
	if lead["attribution"].(map[string]any)["name"] != "Discovery Example" {
		t.Fatal("closure lost its uploader", lead)
	}
	report := owner.must(200, "GET", root+"/reporting?from=2026-11-10T00:00:00Z&to=2026-11-11T00:00:00Z", nil)
	rows := report["agents"].([]any)
	if len(rows) != 1 || rows[0].(map[string]any)["counts"].(map[string]any)["closed"] != float64(1) || rows[0].(map[string]any)["counts"].(map[string]any)["discovered"] != float64(2) || report["attribution_coverage"].(map[string]any)["known"] != float64(1) {
		t.Fatal("agent results do not join to original discovery identity", report)
	}
	failOrigins.Store(true)
	report = owner.must(200, "GET", root+"/reporting?from=2026-11-10T00:00:00Z&to=2026-11-11T00:00:00Z", nil)
	rows = report["agents"].([]any)
	if len(rows) != 2 || rows[1].(map[string]any)["attribution"].(map[string]any)["id"] != "unknown" || rows[1].(map[string]any)["counts"].(map[string]any)["closed"] != float64(1) || report["attribution_coverage"].(map[string]any)["unavailable"] != float64(1) {
		t.Fatal("origin outage must retain unattributed closure counts", report)
	}
	if failures := named(gotEvents(), "sales.origin_lookup_failed"); len(failures) != 1 || failures[0].Props["phase"] != "read" || failures[0].ErrorCode != "connector_unavailable" {
		t.Fatal("missing origin outage event", failures)
	}
	provider.Close()
	queue = owner.must(200, "GET", root+"/leads?queue=all", nil)
	if queue["data"].([]any)[0].(map[string]any)["attribution"].(map[string]any)["status"] != "unavailable" {
		t.Fatal("outage confused with unknown origin")
	}
}
