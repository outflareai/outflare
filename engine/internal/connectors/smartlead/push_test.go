package smartlead_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	c "github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/connectors/smartlead"
)

func TestPushLead(t *testing.T) {
	var sent map[string]any
	answer := `{"ok":true,"upload_count":1,"already_added_to_campaign":0,"duplicate_count":0,"invalid_email_count":0,"unsubscribed_leads":[]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/campaigns/42/leads" || r.URL.Query().Get("api_key") != "key" {
			w.WriteHeader(404)
			return
		}
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &sent)
		w.Write([]byte(answer))
	}))
	defer srv.Close()
	cl := &c.Client{BaseURLs: map[string]string{smartlead.ID: srv.URL}}
	push := func() c.LeadPushed {
		t.Helper()
		got, err := smartlead.Smartlead{}.PushLead(context.Background(), cl, c.Creds{"api_key": "key"}, "42",
			c.PushedLead{Email: "kim@prospect.test", FirstName: "Kim", Company: "Prospect", Variables: map[string]string{"plan": "pro"}})
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := push(); got.Outcome != c.PushAdded {
		t.Fatalf("push = %+v", got)
	}
	lead := sent["lead_list"].([]any)[0].(map[string]any)
	settings := sent["settings"].(map[string]any)
	if lead["email"] != "kim@prospect.test" || lead["company_name"] != "Prospect" || lead["custom_fields"].(map[string]any)["plan"] != "pro" ||
		settings["ignore_duplicate_leads_in_other_campaign"] != false || settings["ignore_global_block_list"] != false {
		t.Fatalf("sent %v", sent)
	}
	for answer, want := range map[string]c.LeadPushed{
		`{"upload_count":0,"already_added_to_campaign":1}`: {Outcome: c.PushAlreadyIn},
		`{"upload_count":0,"duplicate_count":1}`:           {Outcome: c.PushRefused, Reason: "in another Smartlead campaign"},
		`{"upload_count":0,"unsubscribed_leads":1}`:        {Outcome: c.PushRefused, Reason: "unsubscribed in Smartlead"},
		`{"upload_count":0,"unsubscribed_leads":["kim"]}`:  {Outcome: c.PushRefused, Reason: "unsubscribed in Smartlead"},
		`{"upload_count":0,"unsubscribed_leads":[]}`:       {Outcome: c.PushRefused, Reason: "on Smartlead's block list"},
		`{"upload_count":0,"invalid_email_count":1}`:       {Outcome: c.PushRefused, Reason: "Smartlead won't take the address"},
	} {
		srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(answer)) })
		if got := push(); got != want {
			t.Fatalf("%s: %+v", answer, got)
		}
	}
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) })
	if _, err := (smartlead.Smartlead{}).PushLead(context.Background(), cl, c.Creds{"api_key": "key"}, "42", c.PushedLead{Email: "kim@prospect.test"}); c.KindOf(err) != c.ErrAuth {
		t.Fatalf("refused key: %v", err)
	}
}
