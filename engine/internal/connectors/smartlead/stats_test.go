package smartlead_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	c "github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/connectors/smartlead"
)

func TestMailboxDays(t *testing.T) {
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.URL.Path != "/api/v1/analytics/mailbox/name-wise-health-metrics" || q.Get("api_key") != "key" || q.Get("start_date") != q.Get("end_date") {
			w.WriteHeader(400)
			return
		}
		asked = append(asked, q.Get("start_date"))
		if q.Get("start_date") == "2026-03-02" {
			w.Write([]byte(`{"ok":true,"data":{"email_health_metrics":[
				{"email_account":"Ana@acme.test","sent":40,"opened":20,"replied":"3","bounced":1},
				{"email_account":"other@acme.test","sent":9,"replied":0,"bounced":0}]}}`))
			return
		}
		w.Write([]byte(`{"ok":true,"data":{"email_health_metrics":[]}}`))
	}))
	defer srv.Close()
	cl := &c.Client{BaseURLs: map[string]string{smartlead.ID: srv.URL}}
	from := time.Date(2026, 3, 1, 15, 0, 0, 0, time.UTC)
	got, err := (smartlead.Smartlead{}).MailboxDays(context.Background(), cl, c.Creds{"api_key": "key"}, []string{"ana@acme.test"}, from, from.AddDate(0, 0, 2))
	if err != nil {
		t.Fatal(err)
	}
	// One call a day, and only the mailboxes asked for come back.
	if len(asked) != 3 || asked[0] != "2026-03-01" || asked[2] != "2026-03-03" {
		t.Fatalf("days asked for: %v", asked)
	}
	want := c.MailboxDay{Email: "ana@acme.test", Day: "2026-03-02", Sent: 40, Replies: 3, Bounced: 1}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("got %+v", got)
	}
}
