package instantly_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	c "github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/connectors/instantly"
)

func TestMailboxDays(t *testing.T) {
	var batches []int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.URL.Path != "/api/v2/accounts/analytics/daily" || r.Header.Get("Authorization") != "Bearer key" ||
			q.Get("start_date") != "2026-03-01" || q.Get("end_date") != "2026-03-07" {
			w.WriteHeader(400)
			return
		}
		batches = append(batches, len(q["emails"]))
		if q["emails"][0] != "m0@acme.test" {
			w.Write([]byte(`[]`))
			return
		}
		w.Write([]byte(`[{"date":"2026-03-02","email_account":"M0@acme.test","sent":150,"bounced":3,"replies":8,"unique_replies":7},
			{"date":"not a date","email_account":"m0@acme.test","sent":1}]`))
	}))
	defer srv.Close()
	cl := &c.Client{BaseURLs: map[string]string{instantly.ID: srv.URL}}
	emails := []string{}
	for i := range 250 {
		emails = append(emails, fmt.Sprintf("m%d@acme.test", i))
	}
	from := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	got, err := (instantly.Instantly{}).MailboxDays(context.Background(), cl, c.Creds{"api_key": "key"}, emails, from, from.AddDate(0, 0, 6))
	if err != nil {
		t.Fatal(err)
	}
	// At most 200 accounts a call.
	if len(batches) != 2 || batches[0] != 200 || batches[1] != 50 {
		t.Fatalf("batches: %v", batches)
	}
	want := c.MailboxDay{Email: "m0@acme.test", Day: "2026-03-02", Sent: 150, Replies: 7, Bounced: 3}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("got %+v", got)
	}
}
