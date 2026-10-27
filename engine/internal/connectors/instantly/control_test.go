package instantly_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	c "github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/connectors/instantly"
)

func TestControl(t *testing.T) {
	list := []string{"ana@acme.test"}
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer key" {
			w.WriteHeader(401)
			return
		}
		body, _ := io.ReadAll(r.Body)
		if r.Method != http.MethodGet {
			calls = append(calls, r.Method+" "+r.URL.Path+" "+string(body))
		}
		switch {
		case r.URL.Path == "/api/v2/campaigns":
			b, _ := json.Marshal(map[string]any{"items": []any{
				map[string]any{"id": "c1", "name": "Q4", "status": 1, "daily_limit": 120, "email_list": list,
					"sequences": []any{map[string]any{"steps": []any{map[string]any{"delay": 3}, map[string]any{"delay": 5}, map[string]any{"delay": 0}}}}},
				map[string]any{"id": "c2", "name": "Old", "status": 3, "email_list": []string{"bo@acme.test"}},
			}})
			w.Write(b)
		case r.URL.Path == "/api/v2/accounts":
			w.Write([]byte(`{"items":[{"email":"ana@acme.test","first_name":"Ana","status":1,"warmup_status":1,"daily_limit":30,"stat_warmup_score":99},
				{"email":"bo@acme.test","status":-1}]}`))
		case r.URL.Path == "/api/v2/campaigns/c1" && r.Method == http.MethodGet:
			b, _ := json.Marshal(map[string]any{"id": "c1", "email_list": list})
			w.Write(b)
		case r.URL.Path == "/api/v2/campaigns/c1" && r.Method == http.MethodPatch:
			var p struct {
				EmailList []string `json:"email_list"`
			}
			json.Unmarshal(body, &p)
			list = p.EmailList
			w.Write([]byte(`{}`))
		default:
			w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()
	ctx := context.Background()
	cl := &c.Client{BaseURLs: map[string]string{instantly.ID: srv.URL}}
	cr := c.Creds{"api_key": "key"}
	in := instantly.Instantly{}
	snap, err := in.Observe(ctx, cl, cr)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Campaigns) != 2 || *snap.Campaigns[0].SequenceDays != 8 || *snap.Campaigns[0].DailyLimit != 120 {
		t.Fatalf("campaigns %+v", snap.Campaigns)
	}
	ana, bo := snap.Mailboxes[0], snap.Mailboxes[1]
	if !slices.Equal(ana.CampaignIDs, []string{"c1"}) || *ana.WarmupScore != 99 || ana.Status != c.MailboxOK {
		t.Fatalf("ana %+v", ana)
	}
	if bo.Status != c.MailboxError || len(bo.CampaignIDs) != 0 {
		t.Fatalf("bo %+v (a completed campaign does not count)", bo)
	}
	if err := in.Attach(ctx, cl, cr, "c1", []c.MailboxState{bo}); err != nil {
		t.Fatal(err)
	}
	if err := in.Attach(ctx, cl, cr, "c1", []c.MailboxState{bo}); err != nil { // already there: no call
		t.Fatal(err)
	}
	if err := in.Detach(ctx, cl, cr, "c1", []c.MailboxState{ana}); err != nil {
		t.Fatal(err)
	}
	if err := in.SetDailyLimit(ctx, cl, cr, bo, 25); err != nil {
		t.Fatal(err)
	}
	want := []string{
		`PATCH /api/v2/campaigns/c1 {"email_list":["ana@acme.test","bo@acme.test"]}`,
		`PATCH /api/v2/campaigns/c1 {"email_list":["bo@acme.test"]}`,
		`PATCH /api/v2/accounts/bo@acme.test {"daily_limit":25}`,
	}
	if !slices.Equal(calls, want) {
		t.Fatalf("calls:\n%s", strings.Join(calls, "\n"))
	}
}
