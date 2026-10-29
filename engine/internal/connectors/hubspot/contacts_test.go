package hubspot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

func TestPushContact(t *testing.T) {
	var sent map[string]any
	known := map[string]bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/crm/v3/objects/contacts/batch/upsert" || r.Header.Get("Authorization") != "Bearer pat" {
			w.WriteHeader(404)
			return
		}
		json.NewDecoder(r.Body).Decode(&sent)
		in := sent["inputs"].([]any)[0].(map[string]any)
		email := in["id"].(string)
		if in["properties"].(map[string]any)["nope"] != nil {
			w.WriteHeader(400)
			w.Write([]byte(`{"status":"error","message":"Property \"nope\" does not exist"}`))
			return
		}
		isNew := !known[email]
		known[email] = true
		json.NewEncoder(w).Encode(map[string]any{"results": []any{map[string]any{"id": "501", "new": isNew}}})
	}))
	defer srv.Close()
	cl := &c.Client{BaseURLs: map[string]string{"hubspot": srv.URL}}
	creds := c.Creds{"api_key": "pat"}
	p := c.PushedContact{Email: "kim@prospect.test", FirstName: "Kim", Company: "Prospect", Properties: map[string]string{"lifecyclestage": "lead", "email": "other@prospect.test"}}
	got, err := HubSpot{}.PushContact(context.Background(), cl, creds, p)
	if err != nil || got != (c.ContactPushed{Created: true, ExternalID: "501"}) {
		t.Fatalf("first push = %+v, %v", got, err)
	}
	in := sent["inputs"].([]any)[0].(map[string]any)
	props := in["properties"].(map[string]any)
	if in["idProperty"] != "email" || props["email"] != "kim@prospect.test" || props["firstname"] != "Kim" || props["company"] != "Prospect" ||
		props["lifecyclestage"] != "lead" || props["lastname"] != nil {
		t.Fatalf("sent %v", sent)
	}
	// Again (a retry): the same contact, updated.
	if got, err := (HubSpot{}).PushContact(context.Background(), cl, creds, p); err != nil || got.Created || got.ExternalID != "501" {
		t.Fatalf("second push = %+v, %v", got, err)
	}
	p.Properties = map[string]string{"nope": "x"}
	if _, err := (HubSpot{}).PushContact(context.Background(), cl, creds, p); c.KindOf(err) != c.ErrInvalid {
		t.Fatalf("unknown property: %v", err)
	}
}
