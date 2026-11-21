package api_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/emailfind"
	"github.com/outflareai/outflare/engine/internal/peoplesteps"
	"github.com/outflareai/outflare/engine/internal/pipeline"
	"github.com/outflareai/outflare/engine/internal/track"
)

// The enroll step pushes people into an EmailBison campaign the way the
// earlier pipeline did: it reads the campaign's readiness first and holds
// everyone while it isn't ready; it keeps to the campaign's daily
// capacity; it creates the batch's new contacts in one call with notes and
// attaches them in one; it leaves out a contact the account already holds;
// and after the feature's attempts it sets a person aside.
func TestPeopleStepsEnrollEmailBison(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	events := track.Capture(t)

	var mu sync.Mutex
	status := "paused"
	capacity := 2
	failUpsert := false
	contacts := map[string]int{"bo@known.example": 7} // already in the account, emailed from another campaign
	attached := map[int]bool{}
	next := 100
	var upserts, attaches int
	var notes []string
	bison := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		enc := json.NewEncoder(w)
		var body map[string]any
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &body)
		search := r.URL.Query().Get("search")
		found := func(ok func(int) bool) {
			out := []map[string]any{}
			if id, has := contacts[search]; has && ok(id) {
				out = append(out, map[string]any{"id": id, "email": search})
			}
			enc.Encode(map[string]any{"data": out, "meta": map[string]any{"current_page": 1, "last_page": 1}})
		}
		switch {
		case r.URL.Path == "/api/users":
			enc.Encode(map[string]any{"data": map[string]any{"name": "Pat", "email": "pat@bison.example", "team": map[string]any{"name": "Example"}}})
		case r.URL.Path == "/api/campaigns/42":
			enc.Encode(map[string]any{"data": map[string]any{"status": status, "max_new_leads_per_day": capacity, "max_emails_per_day": 100}})
		case r.URL.Path == "/api/campaigns/v1.1/42/sequence-steps":
			enc.Encode(map[string]any{"data": map[string]any{"sequence_steps": []map[string]any{{"email_subject": "Hi {FIRST_NAME}", "email_body": "Hello"}}}})
		case r.URL.Path == "/api/campaigns/42/schedule":
			enc.Encode(map[string]any{"data": map[string]any{"monday": true, "tuesday": true, "wednesday": true, "thursday": true, "friday": true,
				"saturday": false, "sunday": false, "start_time": "08:00", "end_time": "18:00", "timezone": "UTC"}})
		case r.URL.Path == "/api/campaigns/42/sender-emails":
			enc.Encode(map[string]any{"data": []map[string]any{{"id": 1, "email": "s@send.example", "status": "Connected", "daily_limit": 50}},
				"meta": map[string]any{"current_page": 1, "last_page": 1}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/campaigns/42/leads":
			found(func(id int) bool { return attached[id] })
		case r.Method == http.MethodGet && r.URL.Path == "/api/leads":
			found(func(int) bool { return true })
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/leads/"):
			enc.Encode(map[string]any{"data": map[string]any{"id": 7, "email": "bo@known.example", "notes": "",
				"lead_campaign_data": []any{map[string]any{"campaign_id": 3}}, "overall_stats": map[string]any{"emails_sent": 2, "replies": 0}}})
		case r.Method == http.MethodPost && r.URL.Path == "/api/leads/create-or-update/multiple":
			if failUpsert {
				w.WriteHeader(500)
				return
			}
			upserts++
			out := []map[string]any{}
			for _, raw := range body["leads"].([]any) {
				l := raw.(map[string]any)
				next++
				contacts[l["email"].(string)] = next
				notes = append(notes, l["notes"].(string))
				out = append(out, map[string]any{"id": next, "email": l["email"]})
			}
			enc.Encode(map[string]any{"data": out})
		case r.Method == http.MethodPost && r.URL.Path == "/api/campaigns/42/leads/attach-leads":
			attaches++
			for _, id := range body["lead_ids"].([]any) {
				attached[int(id.(float64))] = true
			}
			w.Write([]byte(`{"data":{"success":true}}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer bison.Close()
	e.integrations.Client.BaseURLs["emailbison"] = bison.URL
	integ := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "emailbison",
		"credentials": map[string]string{"base_url": "https://bison.example", "api_key": "eb-test-not-a-real-key"}})["id"].(string)
	seq := owner.must(201, "POST", base+"/sequences", map[string]any{"name": "In EmailBison", "steps": []map[string]any{{"subject": "Hello", "body": "Hi"}}})["id"].(string)
	if _, err := e.pool.Exec(ctx, `INSERT INTO campaign_links (workspace_id, sequence_id, integration_id, external_id, external_name)
		VALUES ($1, $2, $3, '42', 'Sales')`, ws, seq, integ); err != nil {
		t.Fatal(err)
	}
	people := []string{"ana@acme.example", "bea@acme.example", "cai@acme.example", "bo@known.example", "dee@acme.example", "eve@acme.example"}
	records := map[string]string{}
	if err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		var ps []customers.Person
		for _, p := range people {
			ps = append(ps, customers.Person{Email: p, Name: "Pat Example"})
		}
		var err error
		records, err = customers.EnsurePeople(ctx, tx, ws, "prospect", "test", ps)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	enrolled := map[string]bool{}
	stopped := map[string]string{}
	recheck := map[string]bool{}
	cfg := peoplesteps.Config{Pipeline: "bison_push_test", Via: "test", EnrollAttempts: 3,
		Contact: func(_ context.Context, it pipeline.Item) (peoplesteps.Contact, error) {
			return peoplesteps.Contact{FirstName: "Pat", LastName: "Example", Email: it.Ref, Domain: "acme.example", RecordID: records[it.Ref]}, nil
		},
		Found:  func(context.Context, pipeline.Item, string, emailfind.Code, int) error { return nil },
		Linked: func(context.Context, pgx.Tx, pipeline.Item, string, []string) error { return nil },
		Stopped: func(_ context.Context, it pipeline.Item, code string, _ []string) error {
			stopped[it.Ref] = code
			return nil
		},
		Campaign: func(_ context.Context, it pipeline.Item) (string, bool, error) {
			if recheck[it.Ref] {
				return "", false, peoplesteps.ErrRecheckEmail
			}
			return seq, false, nil
		},
		Enrolled: func(_ context.Context, _ pgx.Tx, it pipeline.Item, _, _ string) error {
			enrolled[it.Ref] = true
			return nil
		},
	}
	var enroll pipeline.Stage
	// fresh is a new enroll step, which reads the campaign's readiness
	// afresh (a ready campaign is otherwise reused for two minutes).
	fresh := func() {
		for _, st := range peoplesteps.Stages(e.pool, noFinder{}, e.integrations, cfg) {
			if st.Name == peoplesteps.Enroll {
				enroll = st
			}
		}
	}
	fresh()
	if enroll.MaxAttempts != 3 {
		t.Fatalf("enroll attempts: %d", enroll.MaxAttempts)
	}
	work := func(attempt int, refs ...string) map[string]pipeline.Result {
		t.Helper()
		var items []pipeline.Item
		for _, r := range refs {
			items = append(items, pipeline.Item{ID: r, WorkspaceID: ws, RunID: "00000000-0000-0000-0000-000000000001", Ref: r,
				Stage: peoplesteps.Enroll, Attempt: attempt})
		}
		res, err := enroll.Work(ctx, items)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]pipeline.Result{}
		for _, r := range res {
			out[r.ItemID] = r
		}
		return out
	}

	// A paused campaign holds everyone, without an attempt, and nothing is
	// created or attached.
	got := work(1, people[:4]...)
	for _, p := range people[:4] {
		if r := got[p]; r.Outcome != pipeline.Wait || r.Code != peoplesteps.CampaignNotReady || !strings.Contains(r.Reason, "paused") {
			t.Fatalf("not ready %s: %+v", p, r)
		}
	}
	if upserts+attaches != 0 {
		t.Fatalf("pushed into a paused campaign: %d %d", upserts, attaches)
	}

	// Active, with room for two new people today: ana goes in, bo, whom the
	// account already holds, is left out, and bea waits for tomorrow.
	mu.Lock()
	status = "active"
	mu.Unlock()
	got = work(1, "ana@acme.example", "bo@known.example", "bea@acme.example")
	if r := got["ana@acme.example"]; r.Outcome != pipeline.Done || !enrolled["ana@acme.example"] {
		t.Fatalf("pushed: %+v", r)
	}
	if r := got["bo@known.example"]; r.Outcome != pipeline.Skip || r.Code != peoplesteps.SequencerRefused || stopped["bo@known.example"] != peoplesteps.SequencerRefused {
		t.Fatalf("already in the account: %+v", r)
	}
	if r := got["bea@acme.example"]; r.Outcome != pipeline.Wait || r.Code != peoplesteps.CampaignDailyCapacity || r.RetryAfter <= 0 {
		t.Fatalf("past today's capacity: %+v", r)
	}
	if attached[contacts["bo@known.example"]] {
		t.Fatal("the account's contact was attached")
	}
	// One place left today: two people go in with one create and one attach
	// call between them.
	mu.Lock()
	capacity = 3
	mu.Unlock()
	fresh()
	upserts, attaches = 0, 0
	got = work(1, "cai@acme.example", "bea@acme.example")
	if got["cai@acme.example"].Outcome != pipeline.Done || got["bea@acme.example"].Outcome != pipeline.Done {
		t.Fatalf("batch: %+v", got)
	}
	if upserts != 1 || attaches != 1 || len(notes) != 3 || notes[0] != "Outflare test ana@acme.example" {
		t.Fatalf("one create and one attach for the batch: %d %d %v", upserts, attaches, notes)
	}
	if r := work(1, "eve@acme.example")["eve@acme.example"]; r.Outcome != pipeline.Wait || r.Code != peoplesteps.CampaignDailyCapacity {
		t.Fatalf("today's capacity used: %+v", r)
	}

	// A push that keeps failing sets the person aside on the last attempt.
	mu.Lock()
	capacity, failUpsert = 50, true
	mu.Unlock()
	fresh()
	if r := work(1, "dee@acme.example")["dee@acme.example"]; r.Outcome != pipeline.Retry {
		t.Fatalf("first failure: %+v", r)
	}
	if r := work(3, "dee@acme.example")["dee@acme.example"]; r.Outcome != pipeline.Skip || r.Code != peoplesteps.PushFailed ||
		stopped["dee@acme.example"] != peoplesteps.PushFailed {
		t.Fatalf("set aside: %+v", r)
	}

	// A feature that wants the address checked again sends the person back
	// to find_email.
	recheck["cai@acme.example"] = true
	if r := work(1, "cai@acme.example")["cai@acme.example"]; r.Outcome != pipeline.Advance || r.Next != peoplesteps.FindEmail {
		t.Fatalf("recheck: %+v", r)
	}

	var notReady int
	for _, ev := range events() {
		if ev.Name == "sequencer.campaign_not_ready" {
			notReady++
		}
	}
	if notReady != 1 {
		t.Fatalf("campaign_not_ready events: %d", notReady)
	}
}
