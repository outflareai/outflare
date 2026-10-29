package emailbison_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	c "github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/connectors/emailbison"
)

func TestPushLead(t *testing.T) {
	leads := map[string]int{"bo@known.test": 7} // already in the workspace
	attached := map[int]bool{}
	var made, attach map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer key" {
			w.WriteHeader(401)
			return
		}
		var body map[string]any
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &body)
		search := r.URL.Query().Get("search")
		list := func(only func(int) bool) {
			out := []map[string]any{}
			if id, ok := leads[search]; ok && only(id) {
				out = append(out, map[string]any{"id": id, "email": search})
			}
			json.NewEncoder(w).Encode(map[string]any{"data": out, "meta": map[string]any{"last_page": 1}})
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/campaigns/10/leads":
			list(func(id int) bool { return attached[id] })
		case r.Method == http.MethodGet && r.URL.Path == "/api/leads":
			list(func(int) bool { return true })
		case r.Method == http.MethodPost && r.URL.Path == "/api/leads":
			made = body
			leads[body["email"].(string)] = 8
			w.Write([]byte(`{"data":{"id":8,"email":"kim@prospect.test"}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/campaigns/10/leads/attach-leads":
			attach = body
			for _, id := range body["lead_ids"].([]any) {
				attached[int(id.(float64))] = true
			}
			w.Write([]byte(`{"data":{"success":true}}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	creds := c.Creds{"base_url": srv.URL, "api_key": "key"}
	push := func(email string) c.LeadPushed {
		t.Helper()
		got, err := emailbison.EmailBison{}.PushLead(context.Background(), &c.Client{}, creds, "10",
			c.PushedLead{Email: email, FirstName: "Kim", Company: "Prospect", Variables: map[string]string{"plan": "pro", "city": "Lyon"}})
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	// New: made, then attached.
	if got := push("kim@prospect.test"); got != (c.LeadPushed{Outcome: c.PushAdded, ExternalID: "8"}) {
		t.Fatalf("new: %+v", got)
	}
	if made["company"] != "Prospect" || len(made["custom_variables"].([]any)) != 2 ||
		made["custom_variables"].([]any)[0].(map[string]any)["name"] != "city" {
		t.Fatalf("made %v", made)
	}
	if ids := attach["lead_ids"].([]any); len(ids) != 1 || ids[0] != 8.0 {
		t.Fatalf("attached %v", attach)
	}
	// Known in the workspace: attached as it is.
	made = nil
	if got := push("bo@known.test"); got != (c.LeadPushed{Outcome: c.PushAdded, ExternalID: "7"}) || made != nil {
		t.Fatalf("known: %+v, %v", got, made)
	}
	// Again (a retry): already in the campaign.
	attach = nil
	if got := push("kim@prospect.test"); got != (c.LeadPushed{Outcome: c.PushAlreadyIn, ExternalID: "8"}) || attach != nil {
		t.Fatalf("again: %+v, %v", got, attach)
	}
}
