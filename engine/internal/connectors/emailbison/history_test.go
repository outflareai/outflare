package emailbison_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	c "github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/connectors/emailbison"
)

func TestContactSent(t *testing.T) {
	const email = "Kim+x@Prospect.test"
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   c.SentBefore
		kind   string
	}{
		{name: "the lead's overall count", status: 200, body: `{"data":[
				{"id":1,"email":"akim+x@prospect.test","overall_stats":{"emails_sent":9}},
				{"id":2,"email":"kim+x@prospect.test","emails_sent":1,"overall_stats":{"emails_sent":3,"opens":2}}]}`,
			want: c.SentBefore{Found: true, EmailsSent: 3}},
		{name: "no overall stats: the lead's own count", status: 200, body: `{"data":[{"id":2,"email":"KIM+X@prospect.test","emails_sent":2}]}`,
			want: c.SentBefore{Found: true, EmailsSent: 2}},
		{name: "overall stats without a count: the lead's own count", status: 200, body: `{"data":[{"id":2,"email":"kim+x@prospect.test","emails_sent":4,"overall_stats":{"opens":1}}]}`,
			want: c.SentBefore{Found: true, EmailsSent: 4}},
		{name: "a lead never emailed", status: 200, body: `{"data":[{"id":2,"email":"kim+x@prospect.test","overall_stats":{"emails_sent":0}}]}`,
			want: c.SentBefore{Found: true}},
		{name: "a lead with no counts", status: 200, body: `{"data":[{"id":2,"email":"kim+x@prospect.test"}]}`, want: c.SentBefore{Found: true}},
		{name: "a count that is not a number", status: 200, body: `{"data":[{"id":2,"email":"kim+x@prospect.test","emails_sent":"many"}]}`, want: c.SentBefore{Found: true}},
		{name: "only other leads match the search", status: 200, body: `{"data":[{"id":1,"email":"akim+x@prospect.test","emails_sent":9}]}`, want: c.SentBefore{}},
		{name: "nobody", status: 200, body: `{"data":[]}`, want: c.SentBefore{}},
		{name: "a refused key", status: 401, body: `{"message":"Unauthenticated."}`, kind: c.ErrAuth},
		{name: "EmailBison down", status: 503, body: `oops`, kind: c.ErrUnavailable},
		{name: "not JSON", status: 200, body: `<html>`, kind: c.ErrUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != http.MethodGet || r.URL.Path != "/api/leads" || r.URL.RawQuery != "search=Kim%2Bx%40Prospect.test" ||
					r.Header.Get("Authorization") != "Bearer key" || r.Header.Get("Accept") != "application/json" {
					t.Errorf("request: %s %s %v", r.Method, r.URL, r.Header)
				}
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			}))
			defer srv.Close()
			got, err := emailbison.EmailBison{}.ContactSent(context.Background(), &c.Client{}, c.Creds{"base_url": srv.URL, "api_key": "key"}, " "+email+" ")
			if calls != 1 {
				t.Errorf("%d calls, want one", calls)
			}
			if tc.kind != "" {
				if err == nil || c.KindOf(err) != tc.kind {
					t.Fatalf("got %+v %v, want a %s error", got, err, tc.kind)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %+v %v, want %+v", got, err, tc.want)
			}
		})
	}
	// No address: nothing is sent.
	if _, err := (emailbison.EmailBison{}).ContactSent(context.Background(), &c.Client{}, c.Creds{"base_url": "https://send.example.com", "api_key": "key"}, " "); c.KindOf(err) != c.ErrInvalid {
		t.Fatalf("no address: %v", err)
	}
	var conn c.Connector = emailbison.EmailBison{}
	if _, ok := conn.(c.ContactHistory); !ok || !conn.Definition().Has(c.CapContactHistory) {
		t.Fatal("EmailBison does not offer the contact history")
	}
}
