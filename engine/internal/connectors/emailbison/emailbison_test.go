package emailbison_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	c "github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/connectors/emailbison"
)

func fakeServer(t *testing.T, calls *[]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer key" {
			w.WriteHeader(401)
			return
		}
		body, _ := io.ReadAll(r.Body)
		*calls = append(*calls, r.Method+" "+r.URL.Path+" "+strings.TrimSpace(string(body)))
		w.Header().Set("Content-Type", "application/json")
		page := r.URL.Query().Get("page")
		switch {
		case r.URL.Path == "/api/users":
			w.Write([]byte(`{"data":{"id":7,"name":"Acme Agency","email":"ops@acme.test"}}`))
		case r.URL.Path == "/api/sender-emails" && page == "1":
			w.Write([]byte(`{"data":[{"id":1,"email":"ana@acme.test","name":"Ana","status":"Connected","daily_limit":30,"warmup_score":97.6},
				{"id":2,"email":"bo@acme.test","name":"Bo","status":"Disconnected","daily_limit":25}],
				"meta":{"current_page":1,"last_page":2},"links":{"next":"x"}}`))
		case r.URL.Path == "/api/sender-emails" && page == "2":
			w.Write([]byte(`{"data":[{"id":"3","email":"cy@acme.test","status":"Connected"}],"meta":{"current_page":2,"last_page":2},"links":{"next":null}}`))
		case r.URL.Path == "/api/campaigns":
			w.Write([]byte(`{"data":[{"id":10,"name":"Q4","status":"active","max_emails_per_day":200},{"id":11,"name":"Done","status":"completed"}],"meta":{"last_page":1}}`))
		case r.URL.Path == "/api/campaigns/10/sender-emails":
			w.Write([]byte(`{"data":[{"id":1,"email":"ana@acme.test"}],"meta":{"last_page":1}}`))
		case r.URL.Path == "/api/blacklisted-emails" && page == "1":
			w.Write([]byte(`{"data":[{"id":1,"email":"Gone@Acme.test"}],"meta":{"last_page":1}}`))
		case r.URL.Path == "/api/blacklisted-domains" && page == "1":
			w.Write([]byte(`{"data":[{"id":2,"domain":"rival.example"}],"meta":{"last_page":1}}`))
		case r.URL.Path == "/api/blacklisted-emails/bulk", r.URL.Path == "/api/blacklisted-domains/bulk":
			w.Write([]byte(`{"data":{"success":true}}`))
		case r.URL.Path == "/api/campaigns/10/attach-sender-emails", r.URL.Path == "/api/campaigns/10/remove-sender-emails",
			r.URL.Path == "/api/sender-emails/3":
			w.Write([]byte(`{"data":{"success":true}}`))
		default:
			w.WriteHeader(404)
		}
	}))
}

func TestObserveAttachDetachAndLimits(t *testing.T) {
	var calls []string
	srv := fakeServer(t, &calls)
	defer srv.Close()
	ctx := context.Background()
	cl := &c.Client{}
	creds := c.Creds{"base_url": srv.URL, "api_key": "key"}
	eb := emailbison.EmailBison{}

	acct, err := eb.Test(ctx, cl, creds)
	if err != nil || acct.Name != "Acme Agency" {
		t.Fatalf("test: %+v %v", acct, err)
	}
	snap, err := eb.Observe(ctx, cl, creds)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Mailboxes) != 3 || len(snap.Campaigns) != 2 {
		t.Fatalf("snapshot %+v", snap)
	}
	ana, bo := snap.Mailboxes[0], snap.Mailboxes[1]
	if ana.Status != c.MailboxOK || !slices.Equal(ana.CampaignIDs, []string{"10"}) || *ana.WarmupScore != 98 || *ana.DailyLimit != 30 {
		t.Fatalf("ana %+v", ana)
	}
	if bo.Status != c.MailboxDisconnected {
		t.Fatalf("bo %+v", bo)
	}
	if q := snap.Campaigns[0]; q.Status != "active" || *q.DailyLimit != 200 {
		t.Fatalf("campaign %+v", q)
	}
	for _, call := range calls {
		if strings.Contains(call, "/api/campaigns/11/") {
			t.Fatalf("read a finished campaign's senders: %s", call)
		}
	}

	calls = nil
	cy := snap.Mailboxes[2]
	if err := eb.Attach(ctx, cl, creds, "10", []c.MailboxState{cy}); err != nil {
		t.Fatal(err)
	}
	if err := eb.Detach(ctx, cl, creds, "10", []c.MailboxState{cy}); err != nil {
		t.Fatal(err)
	}
	if err := eb.SetDailyLimit(ctx, cl, creds, cy, 40); err != nil {
		t.Fatal(err)
	}
	want := []string{
		`POST /api/campaigns/10/attach-sender-emails {"sender_email_ids":[3]}`,
		`DELETE /api/campaigns/10/remove-sender-emails {"sender_email_ids":[3]}`,
		`PATCH /api/sender-emails/3 {"daily_limit":40}`,
	}
	if !slices.Equal(calls, want) {
		t.Fatalf("calls:\n%s", strings.Join(calls, "\n"))
	}

	_, err = eb.Test(ctx, cl, c.Creds{"base_url": srv.URL, "api_key": "wrong"})
	if c.KindOf(err) != c.ErrAuth {
		t.Fatalf("wrong key: %v", err)
	}
	_, err = eb.Test(ctx, cl, c.Creds{"base_url": "not a url", "api_key": "key"})
	if c.KindOf(err) != c.ErrInvalid {
		t.Fatalf("bad url: %v", err)
	}
}

func TestBlocklist(t *testing.T) {
	var calls []string
	srv := fakeServer(t, &calls)
	defer srv.Close()
	ctx := context.Background()
	creds := c.Creds{"base_url": srv.URL, "api_key": "key"}
	got, err := emailbison.EmailBison{}.ListBlocked(ctx, &c.Client{}, creds)
	if err != nil || len(got) != 2 || got[0] != (c.Blocked{Value: "gone@acme.test"}) || got[1] != (c.Blocked{Value: "rival.example", Domain: true}) {
		t.Fatalf("list = %v, %v", got, err)
	}
	calls = nil
	if err := (emailbison.EmailBison{}).Block(ctx, &c.Client{}, creds, []c.Blocked{{Value: "bo@acme.test"}, {Value: "spam.example", Domain: true}}); err != nil {
		t.Fatal(err)
	}
	want := []string{`POST /api/blacklisted-emails/bulk {"emails":["bo@acme.test"]}`, `POST /api/blacklisted-domains/bulk {"domains":["spam.example"]}`}
	if !slices.Equal(calls, want) {
		t.Fatalf("calls = %v", calls)
	}
}
