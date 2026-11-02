package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/emailfind"
	"github.com/outflareai/outflare/engine/internal/peoplesteps"
	"github.com/outflareai/outflare/engine/internal/pipeline"
)

type noFinder struct{}

func (noFinder) Resolve(context.Context, string, emailfind.FindRequest) (emailfind.FindResult, error) {
	return emailfind.FindResult{}, nil
}
func (noFinder) VerifyEmails(context.Context, string, emailfind.VerifyRequest) (emailfind.FindResult, error) {
	return emailfind.FindResult{}, nil
}

// The people steps' enroll adds a person to the sequencer campaign a
// sequence is linked to, on the workspace's own connection: once, never
// someone on the opt-out list, and it pauses the run when the sequencer
// refuses the connection.
func TestPeopleStepsEnrollPushesToSequencer(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()

	var mu sync.Mutex
	in := map[string]bool{}
	pushes := 0
	refuse := false
	sl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		no := refuse
		mu.Unlock()
		if no {
			w.WriteHeader(401)
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/api/v1/campaigns/42/leads" {
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			email := body["lead_list"].([]any)[0].(map[string]any)["email"].(string)
			mu.Lock()
			defer mu.Unlock()
			pushes++
			if in[email] {
				w.Write([]byte(`{"ok": true, "upload_count": 0, "already_added_to_campaign": 1}`))
				return
			}
			in[email] = true
			w.Write([]byte(`{"ok": true, "upload_count": 1}`))
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/api/v1/campaigns" {
			w.Write([]byte(`[{"id": 42, "name": "Trial follow-up", "status": "ACTIVE"}]`))
			return
		}
		w.WriteHeader(404)
	}))
	defer sl.Close()
	e.integrations.Client.BaseURLs["smartlead"] = sl.URL
	integ := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "smartlead", "credentials": map[string]string{"api_key": "sl-test-not-a-real-key"}})["id"].(string)
	seq := owner.must(201, "POST", base+"/sequences", map[string]any{"name": "In Smartlead", "steps": []map[string]any{{"subject": "Hello", "body": "Hi"}}})["id"].(string)
	if _, err := e.pool.Exec(ctx, `INSERT INTO campaign_links (workspace_id, sequence_id, integration_id, external_id, external_name)
		VALUES ($1, $2, $3, '42', 'Trial follow-up')`, ws, seq, integ); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `INSERT INTO suppressions (workspace_id, kind, value, reason, method) VALUES ($1, 'email', 'bea@acme.example', 'unsubscribed', 'manual')`, ws); err != nil {
		t.Fatal(err)
	}
	records := map[string]string{}
	if err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		var err error
		records, err = customers.EnsurePeople(ctx, tx, ws, "prospect", "test", []customers.Person{{Email: "ana@acme.example", Name: "Ana Diaz"}, {Email: "bea@acme.example", Name: "Bea Lee"}})
		return err
	}); err != nil {
		t.Fatal(err)
	}

	enrolled := map[string]string{}
	stopped := map[string]string{}
	cfg := peoplesteps.Config{Pipeline: "push_test", Via: "test",
		Contact: func(_ context.Context, it pipeline.Item) (peoplesteps.Contact, error) {
			first := map[string]string{"ana@acme.example": "Ana", "bea@acme.example": "Bea"}[it.Ref]
			return peoplesteps.Contact{FirstName: first, Email: it.Ref, Domain: "acme.example", RecordID: records[it.Ref]}, nil
		},
		Found:  func(context.Context, pipeline.Item, string, emailfind.Code, int) error { return nil },
		Linked: func(context.Context, pgx.Tx, pipeline.Item, string, []string) error { return nil },
		Stopped: func(_ context.Context, it pipeline.Item, code string, _ []string) error {
			stopped[it.Ref] = code
			return nil
		},
		Campaign: func(context.Context, pipeline.Item) (string, bool, error) { return seq, false, nil },
		Enrolled: func(_ context.Context, _ pgx.Tx, it pipeline.Item, s, _ string) error {
			enrolled[it.Ref] = s
			return nil
		},
	}
	var enroll pipeline.Stage
	for _, st := range peoplesteps.Stages(e.pool, noFinder{}, e.integrations, cfg) {
		if st.Name == peoplesteps.Enroll {
			enroll = st
		}
	}
	item := func(ref string) pipeline.Item {
		return pipeline.Item{ID: ref, WorkspaceID: ws, RunID: "00000000-0000-0000-0000-000000000001", Ref: ref, Stage: peoplesteps.Enroll}
	}
	work := func(refs ...string) map[string]pipeline.Result {
		t.Helper()
		var items []pipeline.Item
		for _, r := range refs {
			items = append(items, item(r))
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

	got := work("ana@acme.example", "bea@acme.example")
	if got["ana@acme.example"].Outcome != pipeline.Done || enrolled["ana@acme.example"] != seq {
		t.Fatalf("ana: %+v, enrolled %v", got["ana@acme.example"], enrolled)
	}
	if got["bea@acme.example"].Outcome != pipeline.Skip || stopped["bea@acme.example"] != peoplesteps.OptedOut {
		t.Fatalf("bea: %+v, stopped %v", got["bea@acme.example"], stopped)
	}
	// Again (a retry): already in the campaign, still done, added once.
	if got := work("ana@acme.example"); got["ana@acme.example"].Outcome != pipeline.Done {
		t.Fatalf("again: %+v", got)
	}
	mu.Lock()
	n, added := pushes, len(in)
	mu.Unlock()
	if n != 2 || added != 1 {
		t.Fatalf("again: pushes %d, added %d", n, added)
	}
	var audits int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE workspace_id = $1 AND action = 'sequence.lead_pushed' AND data->>'via' = 'test'`, ws).
		Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("pushed events: %d %v", audits, err)
	}
	// The sequencer refuses the connection: the run pauses with a reason.
	mu.Lock()
	refuse = true
	mu.Unlock()
	if got := work("ana@acme.example"); got["ana@acme.example"].Outcome != pipeline.Pause || got["ana@acme.example"].Code != peoplesteps.CampaignUnavailable {
		t.Fatalf("no connection: %+v", got)
	}
}
