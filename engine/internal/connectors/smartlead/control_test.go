package smartlead_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	c "github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/connectors/smartlead"
)

func TestControl(t *testing.T) {
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("api_key") != "key" {
			w.WriteHeader(401)
			return
		}
		body, _ := io.ReadAll(r.Body)
		if r.Method != http.MethodGet {
			calls = append(calls, r.Method+" "+r.URL.Path+" "+string(body))
		}
		switch r.URL.Path {
		case "/api/v1/campaigns":
			w.Write([]byte(`[{"id":5,"name":"Q4","status":"ACTIVE"},{"id":6,"name":"Old","status":"COMPLETED"}]`))
		case "/api/v1/campaigns/5/sequences":
			w.Write([]byte(`[{"seq_delay_details":{"delay_in_days":0}},{"seq_delay_details":{"delay_in_days":3}},{"seq_delay_details":{"delay_in_days":4}}]`))
		case "/api/v1/campaigns/5/email-accounts":
			w.Write([]byte(`[{"id":"101"}]`))
		case "/api/v1/email-accounts/":
			w.Write([]byte(`[{"id":101,"from_email":"ana@acme.test","from_name":"Ana","message_per_day":30,"is_smtp_success":true,"is_imap_success":true,"warmup_details":{"status":"ACTIVE","warmup_reputation":"96%"}},
				{"id":102,"from_email":"bo@acme.test","is_smtp_success":false,"is_imap_success":true}]`))
		default:
			w.Write([]byte(`{"ok":true}`))
		}
	}))
	defer srv.Close()
	ctx := context.Background()
	cl := &c.Client{BaseURLs: map[string]string{smartlead.ID: srv.URL}}
	cr := c.Creds{"api_key": "key"}
	s := smartlead.Smartlead{}
	snap, err := s.Observe(ctx, cl, cr)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Campaigns) != 2 || *snap.Campaigns[0].SequenceDays != 7 || snap.Campaigns[1].Status != "completed" {
		t.Fatalf("campaigns %+v", snap.Campaigns)
	}
	ana, bo := snap.Mailboxes[0], snap.Mailboxes[1]
	if !slices.Equal(ana.CampaignIDs, []string{"5"}) || *ana.WarmupScore != 96 || !ana.WarmupEnabled || ana.Status != c.MailboxOK {
		t.Fatalf("ana %+v", ana)
	}
	if bo.Status != c.MailboxError {
		t.Fatalf("bo %+v", bo)
	}
	if err := s.Attach(ctx, cl, cr, "5", []c.MailboxState{bo}); err != nil {
		t.Fatal(err)
	}
	if err := s.Detach(ctx, cl, cr, "5", []c.MailboxState{bo}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDailyLimit(ctx, cl, cr, bo, 20); err != nil {
		t.Fatal(err)
	}
	want := []string{
		`POST /api/v1/campaigns/5/email-accounts {"email_account_ids":[102]}`,
		`DELETE /api/v1/campaigns/5/email-accounts {"email_accounts_ids":[102]}`,
		`POST /api/v1/email-accounts/102 {"max_email_per_day":20}`,
	}
	if !slices.Equal(calls, want) {
		t.Fatalf("calls:\n%s", strings.Join(calls, "\n"))
	}
}
