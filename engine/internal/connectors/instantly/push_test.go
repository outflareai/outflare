package instantly_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	c "github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/connectors/instantly"
)

func TestPushLead(t *testing.T) {
	inCampaign := map[string]bool{}
	var made map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer key" {
			w.WriteHeader(401)
			return
		}
		var body map[string]any
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &body)
		switch r.URL.Path {
		case "/api/v2/leads/list":
			if body["campaign"] == "camp-1" && inCampaign[body["search"].(string)] {
				w.Write([]byte(`{"items":[{"id":"lead-1","email":"Kim@Prospect.test"}]}`))
				return
			}
			w.Write([]byte(`{"items":[]}`))
		case "/api/v2/leads":
			made = body
			if body["email"] == "bo@elsewhere.test" {
				w.Write([]byte(`{}`)) // skipped: in the workspace already
				return
			}
			inCampaign[body["email"].(string)] = true
			w.Write([]byte(`{"id":"lead-1","email":"kim@prospect.test"}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	cl := &c.Client{BaseURLs: map[string]string{instantly.ID: srv.URL}}
	push := func(email string, elsewhere bool) c.LeadPushed {
		t.Helper()
		got, err := instantly.Instantly{}.PushLead(context.Background(), cl, c.Creds{"api_key": "key"}, "camp-1",
			c.PushedLead{Email: email, FirstName: "Kim", Variables: map[string]string{"plan": "pro"}, AllowElsewhere: elsewhere})
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := push("kim@prospect.test", false); got != (c.LeadPushed{Outcome: c.PushAdded, ExternalID: "lead-1"}) {
		t.Fatalf("added: %+v", got)
	}
	if made["campaign"] != "camp-1" || made["skip_if_in_workspace"] != true || made["skip_if_in_campaign"] != true || made["custom_variables"].(map[string]any)["plan"] != "pro" {
		t.Fatalf("made %v", made)
	}
	// Again (a retry): it is found in the campaign, not added twice.
	made = nil
	if got := push("kim@prospect.test", false); got.Outcome != c.PushAlreadyIn || made != nil {
		t.Fatalf("again: %+v, %v", got, made)
	}
	if got := push("bo@elsewhere.test", false); got != (c.LeadPushed{Outcome: c.PushRefused, Reason: "in another Instantly campaign or list"}) {
		t.Fatalf("skipped: %+v", got)
	}
	if push("bo@elsewhere.test", true); made["skip_if_in_workspace"] != false {
		t.Fatalf("allowed elsewhere: %v", made)
	}
}
