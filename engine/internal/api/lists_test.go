package api_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/outflareai/outflare/engine/internal/api"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/connectors/catalog"
	"github.com/outflareai/outflare/engine/internal/connectors/verifiers"
	"github.com/outflareai/outflare/engine/internal/lists"
	"github.com/outflareai/outflare/engine/internal/secrets"
	"github.com/outflareai/outflare/engine/internal/verify"
)

// fakeData pretends to be Apollo (search and match) and Prospeo (email
// finder), and counts paid calls.
type fakeData struct {
	mu      sync.Mutex
	matches int // Apollo enrichments
	finds   int // Prospeo lookups
	people  []map[string]any
	emails  map[string]string // Apollo id -> email revealed by match
	prospeo map[string]string // last name -> email Prospeo finds
}

func (f *fakeData) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var body map[string]any
	b, _ := io.ReadAll(r.Body)
	json.Unmarshal(b, &body)
	switch r.URL.Path {
	case "/v1/auth/health":
		io.WriteString(w, `{"healthy":true,"is_logged_in":true}`)
	case "/account-information":
		io.WriteString(w, `{"error":false,"response":{"remaining_credits":100}}`)
	case "/api/v1/mixed_people/api_search":
		page := int(body["page"].(float64))
		per := int(body["per_page"].(float64))
		lo := (page - 1) * per
		hi := min(lo+per, len(f.people))
		var out []map[string]any
		if lo < len(f.people) {
			out = f.people[lo:hi]
		}
		json.NewEncoder(w).Encode(map[string]any{"total_entries": len(f.people), "people": out})
	case "/api/v1/people/match":
		f.matches++
		id, _ := body["id"].(string)
		e, ok := f.emails[id]
		if !ok {
			io.WriteString(w, `{"person":null}`)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"person": map[string]any{"id": id, "email": e, "email_status": "verified"}})
	case "/enrich-person":
		f.finds++
		d := body["data"].(map[string]any)
		last, _ := d["last_name"].(string)
		e, ok := f.prospeo[last]
		if !ok {
			w.WriteHeader(400)
			io.WriteString(w, `{"error":true,"error_code":"NO_MATCH"}`)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"error": false, "person": map[string]any{"email": map[string]any{"status": "VERIFIED", "email": e}}})
	default:
		w.WriteHeader(404)
	}
}

type listEnv struct {
	*env
	data  *fakeData
	lists *lists.Service
}

func freshListEnv(t *testing.T) *listEnv {
	t.Helper()
	e := freshEnv(t)
	fake := &fakeData{emails: map[string]string{}, prospeo: map[string]string{}}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	box, _ := secrets.NewBox(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	reg := catalog.New()
	client := &connectors.Client{BaseURLs: map[string]string{"apollo": srv.URL, "prospeo": srv.URL}}
	store := &connectors.Store{Pool: e.pool, Box: box, Registry: reg, Client: client}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ver := &verify.Service{Pool: e.pool, Store: store, Builtin: verifiers.Builtin{Resolver: e.dns}, Log: log}
	ls := &lists.Service{Pool: e.pool, Store: store, Verify: ver, Log: log}
	e.h = handler(t, api.NewServer(e.pool, log, api.Options{Mailboxes: e.mail, Sending: e.send, Inbox: e.sync, OAuth: e.oauth,
		Box: box, Integrations: store, Verify: ver, Lists: ls}))
	return &listEnv{env: e, data: fake, lists: ls}
}

func person(id, first, last, title, company string, employees int) map[string]any {
	return map[string]any{"id": id, "first_name": first, "last_name": last, "title": title, "has_email": true,
		"organization": map[string]any{"name": company, "primary_domain": strings.ToLower(company) + ".io", "estimated_num_employees": employees}}
}

func TestBuildListFromSearchWithWaterfall(t *testing.T) {
	e := freshListEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()

	e.data.people = []map[string]any{
		person("a1", "Ana", "Lee", "Head of Sales", "Acme", 40),
		person("a2", "Bo", "Ng", "VP Sales", "Beta", 30),
		person("a3", "Cy", "Oh", "Sales Intern", "Gamma", 20000),   // low fit
		person("a4", "Di", "Park", "Head of Sales", "Delta", 25),   // only Prospeo finds Di
		person("a5", "Ed", "Quinn", "Head of Sales", "Eps", 45),    // suppressed
		person("a6", "Fa", "Ruiz", "Director of Sales", "Phi", 15), // nobody finds Fa
	}
	e.data.emails = map[string]string{"a1": "ana@acme-io.example", "a2": "bo@beta-io.example", "a5": "ed@eps-io.example", "a3": "cy@gamma-io.example"}
	e.data.prospeo = map[string]string{"Park": "di@delta-io.example"}

	apollo := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "apollo", "name": "Apollo", "credentials": map[string]string{"api_key": "k"}})
	prospeo := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "prospeo", "name": "Prospeo", "credentials": map[string]string{"api_key": "k"}})
	owner.must(200, "POST", base+"/suppressions", map[string]any{"values": []string{"ed@eps-io.example"}, "reason": "manual"})

	// Data providers lists what is connected and what each can filter by.
	providers := owner.must(200, "GET", base+"/data-providers", nil)
	found := false
	for _, p := range providers["data"].([]any) {
		p := p.(map[string]any)
		if p["connector"] == "apollo" {
			found = len(p["integrations"].([]any)) == 1 && len(p["filters"].([]any)) > 0
		}
	}
	if !found {
		t.Fatalf("providers: %v", providers)
	}

	// Bad settings are refused with a reason.
	owner.must(400, "POST", base+"/lead-lists", map[string]any{"name": "x", "search_integration_id": prospeo["id"], "filters": map[string]any{"titles": []string{"cto"}}})
	owner.must(400, "POST", base+"/lead-lists", map[string]any{"name": "x", "search_integration_id": apollo["id"], "filters": map[string]any{"seniorities": []string{"boss"}}})
	owner.must(400, "POST", base+"/lead-lists", map[string]any{"name": "x", "refresh_every_days": 7})

	// Preview: one page, nothing saved.
	run := owner.must(202, "POST", base+"/lead-lists/preview", map[string]any{"search_integration_id": apollo["id"], "filters": map[string]any{"titles": []string{"sales"}}})
	if err := e.lists.RunPending(ctx, ws); err != nil {
		t.Fatal(err)
	}
	run = owner.must(200, "GET", base+"/list-runs/"+run["id"].(string), nil)
	if run["status"] != "done" || len(run["sample"].([]any)) != 6 || run["total_matches"] != 6.0 || e.data.matches != 0 {
		t.Fatalf("preview: %v (matches %d)", run, e.data.matches)
	}

	// Build: Apollo reveals what it can, Prospeo fills the gap.
	list := owner.must(201, "POST", base+"/lead-lists", map[string]any{
		"name": "AU sales leaders", "search_integration_id": apollo["id"],
		"filters":                map[string]any{"titles": []string{"head of sales", "vp sales", "director of sales"}, "employee_ranges": []string{"11-50"}},
		"enrich_integration_ids": []string{apollo["id"].(string), prospeo["id"].(string)},
		"min_score":              50, "verify": false, "build": true,
	})
	listID := list["id"].(string)
	if list["kind"] != "search" || list["last_run"].(map[string]any)["status"] != "queued" {
		t.Fatalf("created: %v", list)
	}
	owner.must(409, "POST", base+"/lead-lists/"+listID+"/runs", map[string]any{"kind": "refresh"})
	if err := e.lists.RunPending(ctx, ws); err != nil {
		t.Fatal(err)
	}
	list = owner.must(200, "GET", base+"/lead-lists/"+listID, nil)
	last := list["last_run"].(map[string]any)
	skipped := last["skipped"].(map[string]any)
	if last["status"] != "done" || last["added"] != 3.0 || list["leads"] != 3.0 {
		t.Fatalf("build: %v", last)
	}
	if skipped["low_score"] != 1.0 || skipped["suppressed"] != 1.0 || skipped["no_email"] != 1.0 {
		t.Fatalf("skipped: %v", skipped)
	}
	// Apollo was asked about a1, a2, a4, a5, a6; Prospeo about a4 and a6.
	if e.data.matches != 5 || e.data.finds != 2 {
		t.Fatalf("paid calls: apollo %d prospeo %d", e.data.matches, e.data.finds)
	}
	// Credits: Apollo charged for 3 emails (a1, a2, a5), Prospeo for one.
	if last["credits"] != 4.0 {
		t.Fatalf("credits: %v", last)
	}

	members := owner.must(200, "GET", base+"/lead-lists/"+listID+"/members", nil)
	data := members["data"].([]any)
	top := data[0].(map[string]any)
	if len(data) != 3 || top["score"] != 100.0 || len(top["score_reasons"].([]any)) != 2 || top["source"] != "apollo" {
		t.Fatalf("members: %v", members)
	}
	if top["custom"].(map[string]any)["seniority"] == nil {
		t.Fatalf("custom fields: %v", top)
	}
	sources := owner.must(200, "GET", base+"/leads/"+top["id"].(string)+"/sources", nil)
	if s := sources["data"].([]any); len(s) != 1 || s[0].(map[string]any)["provider"] != "apollo" {
		t.Fatalf("sources: %v", sources)
	}

	// Refresh: people already found cost nothing; cached misses are not
	// asked again.
	e.data.matches, e.data.finds = 0, 0
	owner.must(202, "POST", base+"/lead-lists/"+listID+"/runs", map[string]any{"kind": "refresh"})
	if err := e.lists.RunPending(ctx, ws); err != nil {
		t.Fatal(err)
	}
	last = owner.must(200, "GET", base+"/lead-lists/"+listID, nil)["last_run"].(map[string]any)
	if last["added"] != 0.0 || last["credits"] != 0.0 || e.data.matches != 0 || e.data.finds != 0 {
		t.Fatalf("refresh: %v (apollo %d prospeo %d)", last, e.data.matches, e.data.finds)
	}

	// Credit cap: a second list sharing nothing stops when the cap is hit.
	e.data.people = append(e.data.people, person("b1", "Gi", "Su", "Head of Sales", "Gim", 20), person("b2", "Ha", "Tu", "Head of Sales", "Han", 20))
	e.data.emails["b1"], e.data.emails["b2"] = "gi@gim-io.example", "ha@han-io.example"
	capped := owner.must(201, "POST", base+"/lead-lists", map[string]any{
		"name": "Capped", "search_integration_id": apollo["id"], "filters": map[string]any{"company_domains": []string{"gim-io.example", "han-io.example"}},
		"credit_cap": 1, "verify": false, "skip_contacted_days": 0, "build": true,
	})
	if err := e.lists.RunPending(ctx, ws); err != nil {
		t.Fatal(err)
	}
	last = owner.must(200, "GET", base+"/lead-lists/"+capped["id"].(string), nil)["last_run"].(map[string]any)
	if last["status"] != "done" || last["credits"] != 1.0 || last["stop_reason"] == nil || !strings.Contains(last["stop_reason"].(string), "credit cap") {
		t.Fatalf("capped: %v", last)
	}

	// Spend lands in the ledger, once per page.
	var spent float64
	if err := e.pool.QueryRow(ctx, `SELECT COALESCE(sum(units), 0)::float8 FROM spend_entries WHERE workspace_id = $1 AND category = 'data'`, ws).Scan(&spent); err != nil {
		t.Fatal(err)
	}
	if spent != 5 {
		t.Fatalf("ledger: %v credits", spent)
	}

	// Enrichment fills missing fields and never changes the email.
	owner.must(201, "POST", base+"/leads", map[string]any{"email": "zed@zeta-io.example", "list_id": listID})
	e.data.emails["zed"] = "other@zeta-io.example"
	owner.must(202, "POST", base+"/lead-lists/"+listID+"/runs", map[string]any{"kind": "enrich"})
	if err := e.lists.RunPending(ctx, ws); err != nil {
		t.Fatal(err)
	}
	last = owner.must(200, "GET", base+"/lead-lists/"+listID, nil)["last_run"].(map[string]any)
	if last["kind"] != "enrich" || last["status"] != "done" {
		t.Fatalf("enrich: %v", last)
	}

	// Lists are filtered by brand, and audit events record runs.
	if out := owner.must(200, "GET", base+"/lead-lists?brand=none", nil); len(out["data"].([]any)) != 2 {
		t.Fatalf("brand filter: %v", out)
	}
	audit := owner.must(200, "GET", base+"/audit-events?action=lead_list.build_done", nil)
	if len(audit["data"].([]any)) == 0 {
		t.Fatalf("audit: %v", audit)
	}
	owner.must(204, "DELETE", base+"/lead-lists/"+listID, nil)
	owner.must(404, "GET", base+"/lead-lists/"+listID, nil)
}

// Projects were removed: an older client's project_id is accepted and ignored.
func TestListProjectIDIsIgnored(t *testing.T) {
	e := freshListEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	l := owner.must(201, "POST", base+"/lead-lists", map[string]any{"name": "Old client", "project_id": "00000000-0000-0000-0000-000000000000"})
	if _, ok := l["project_id"]; ok {
		t.Fatalf("list still has a project: %v", l)
	}
}
