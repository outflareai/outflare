package emailbison_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	c "github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/connectors/emailbison"
)

// fakeBison is an EmailBison account with contacts and one campaign (10).
type fakeBison struct {
	mu       sync.Mutex
	next     int
	contacts map[string]*fakeContact // by email
	attached map[int]bool            // in campaign 10
	calls    []string
	upserts  []map[string]any
	attaches []map[string]any
	// failAttach makes the attach call fail once.
	failAttach bool
	// skip leaves these addresses out of the create-or-update answer.
	skip map[string]bool
}

type fakeContact struct {
	id            int
	email, notes  string
	campaigns     []int
	sent, replies int
}

func newFakeBison() *fakeBison {
	return &fakeBison{next: 100, contacts: map[string]*fakeContact{}, attached: map[int]bool{}, skip: map[string]bool{}}
}

func (f *fakeBison) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer key" {
		w.WriteHeader(401)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	var body map[string]any
	b, _ := io.ReadAll(r.Body)
	json.Unmarshal(b, &body)
	search := r.URL.Query().Get("search")
	list := func(only func(*fakeContact) bool) {
		out := []map[string]any{}
		if k, ok := f.contacts[search]; ok && only(k) {
			out = append(out, map[string]any{"id": k.id, "email": k.email})
		}
		json.NewEncoder(w).Encode(map[string]any{"data": out, "meta": map[string]any{"current_page": 1, "last_page": 1}})
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/campaigns/10/leads":
		list(func(k *fakeContact) bool { return f.attached[k.id] })
	case r.Method == http.MethodGet && r.URL.Path == "/api/leads":
		list(func(*fakeContact) bool { return true })
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/leads/"):
		n, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/api/leads/"))
		for _, k := range f.contacts {
			if k.id == n {
				camps := []map[string]any{}
				for _, cid := range k.campaigns {
					camps = append(camps, map[string]any{"campaign_id": cid})
				}
				json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"id": k.id, "email": k.email, "notes": k.notes,
					"lead_campaign_data": camps, "overall_stats": map[string]any{"emails_sent": k.sent, "replies": k.replies}}})
				return
			}
		}
		w.WriteHeader(404)
	case r.Method == http.MethodPost && r.URL.Path == "/api/leads/create-or-update/multiple":
		f.upserts = append(f.upserts, body)
		out := []map[string]any{}
		for _, raw := range body["leads"].([]any) {
			l := raw.(map[string]any)
			email := l["email"].(string)
			if f.skip[email] {
				continue
			}
			k := f.contacts[email]
			if k == nil {
				f.next++
				k = &fakeContact{id: f.next, email: email}
				f.contacts[email] = k
			}
			k.notes, _ = l["notes"].(string)
			out = append(out, map[string]any{"id": k.id, "email": email})
		}
		json.NewEncoder(w).Encode(map[string]any{"data": out})
	case r.Method == http.MethodPost && r.URL.Path == "/api/campaigns/10/leads/attach-leads":
		if f.failAttach {
			f.failAttach = false
			w.WriteHeader(502)
			return
		}
		f.attaches = append(f.attaches, body)
		for _, id := range body["lead_ids"].([]any) {
			f.attached[int(id.(float64))] = true
			for _, k := range f.contacts {
				if k.id == int(id.(float64)) {
					k.campaigns = append(k.campaigns, 10)
				}
			}
		}
		w.Write([]byte(`{"data":{"success":true}}`))
	default:
		w.WriteHeader(404)
	}
}

// People go in the way the earlier pipeline added them: someone already in
// the campaign is left as they are; someone the account already holds is
// left out (they may be emailed from another campaign already), unless it
// is this push's own contact from an earlier try that never went into a
// campaign; new people are created together in one create-or-update call
// with their notes, and attached together in one call.
func TestPushLeads(t *testing.T) {
	f := newFakeBison()
	f.contacts["bo@known.test"] = &fakeContact{id: 7, email: "bo@known.test", campaigns: []int{3}, sent: 2}
	f.contacts["cy@quiet.test"] = &fakeContact{id: 8, email: "cy@quiet.test", notes: "imported by hand"}
	f.contacts["di@retry.test"] = &fakeContact{id: 9, email: "di@retry.test", notes: "Outflare lead_finder contact:4"}
	f.contacts["ed@used.test"] = &fakeContact{id: 11, email: "ed@used.test", notes: "Outflare lead_finder contact:5", sent: 1}
	srv := httptest.NewServer(f)
	defer srv.Close()
	creds := c.Creds{"base_url": srv.URL, "api_key": "key"}
	lead := func(email, note string) c.PushedLead {
		return c.PushedLead{Email: email, FirstName: "Kim", LastName: "Lee", Company: "Prospect", Note: note,
			Variables: map[string]string{"plan": "pro", "city": "Lyon"}}
	}
	in := []c.PushedLead{
		lead("kim@prospect.test", "Outflare lead_finder contact:1"),
		lead("bo@known.test", "Outflare lead_finder contact:2"),
		lead("cy@quiet.test", "Outflare lead_finder contact:3"),
		lead("di@retry.test", "Outflare lead_finder contact:4"),
		lead("ed@used.test", "Outflare lead_finder contact:5"),
		lead("fay@prospect.test", "Outflare lead_finder contact:6"),
	}
	out := emailbison.EmailBison{}.PushLeads(context.Background(), &c.Client{}, creds, "10", in)
	want := []string{c.PushAdded, c.PushRefused, c.PushRefused, c.PushAdded, c.PushRefused, c.PushAdded}
	for i, o := range out {
		if o.Err != nil || o.Outcome != want[i] {
			t.Errorf("%s: %+v, want %s", in[i].Email, o, want[i])
		}
	}
	if out[1].Reason == "" || out[1].ExternalID != "7" {
		t.Errorf("refused: %+v", out[1])
	}
	// One create-or-update for the three, with notes and variables.
	if len(f.upserts) != 1 {
		t.Fatalf("upserts %v", f.upserts)
	}
	up := f.upserts[0]
	if up["existing_lead_behavior"] != "patch" || len(up["leads"].([]any)) != 3 {
		t.Fatalf("upsert %v", up)
	}
	first := up["leads"].([]any)[0].(map[string]any)
	if first["email"] != "kim@prospect.test" || first["first_name"] != "Kim" || first["last_name"] != "Lee" || first["company"] != "Prospect" ||
		first["notes"] != "Outflare lead_finder contact:1" || len(first["custom_variables"].([]any)) != 2 ||
		first["custom_variables"].([]any)[0].(map[string]any)["name"] != "city" {
		t.Fatalf("upserted %v", first)
	}
	// One attach for the three.
	if len(f.attaches) != 1 || len(f.attaches[0]["lead_ids"].([]any)) != 3 {
		t.Fatalf("attaches %v", f.attaches)
	}
	for _, call := range f.calls {
		if call == "POST /api/leads" {
			t.Fatalf("one contact a call: %v", f.calls)
		}
	}

	// Again (a retry): already in the campaign, nothing made or attached.
	f.upserts, f.attaches = nil, nil
	got, err := emailbison.EmailBison{}.PushLead(context.Background(), &c.Client{}, creds, "10", in[0])
	if err != nil || got.Outcome != c.PushAlreadyIn || f.upserts != nil || f.attaches != nil {
		t.Fatalf("again: %+v, %v, %v %v", got, err, f.upserts, f.attaches)
	}

	// Allowed elsewhere (a journey that says so): the account's contact is
	// attached as it is.
	allow := lead("cy@quiet.test", "")
	allow.AllowElsewhere = true
	if got, err := (emailbison.EmailBison{}).PushLead(context.Background(), &c.Client{}, creds, "10", allow); err != nil ||
		got.Outcome != c.PushAdded || got.ExternalID != "8" || f.upserts != nil {
		t.Fatalf("allowed elsewhere: %+v, %v", got, err)
	}

	// The attach fails: the person's error. Their contact is this push's
	// own and unused, so the next try takes it back.
	f.failAttach = true
	g := lead("gus@prospect.test", "Outflare lead_finder contact:7")
	if _, err := (emailbison.EmailBison{}).PushLead(context.Background(), &c.Client{}, creds, "10", g); err == nil {
		t.Fatal("a failed attach is an error")
	}
	if got, err := (emailbison.EmailBison{}).PushLead(context.Background(), &c.Client{}, creds, "10", g); err != nil || got.Outcome != c.PushAdded {
		t.Fatalf("retry of our own unused contact: %+v, %v", got, err)
	}
	// Another push for the same address (another item) is left out.
	if got, _ := (emailbison.EmailBison{}).PushLead(context.Background(), &c.Client{}, creds, "10",
		lead("gus@prospect.test", "Outflare lead_finder contact:8")); got.Outcome != c.PushAlreadyIn {
		t.Fatalf("in the campaign already: %+v", got)
	}

	// An address EmailBison doesn't take is that person's error only.
	f.skip["hal@prospect.test"] = true
	two := emailbison.EmailBison{}.PushLeads(context.Background(), &c.Client{}, creds, "10",
		[]c.PushedLead{lead("hal@prospect.test", "n1"), lead("ivy@prospect.test", "n2")})
	if two[0].Err == nil || two[1].Err != nil || two[1].Outcome != c.PushAdded {
		t.Fatalf("not taken: %+v", two)
	}
}
