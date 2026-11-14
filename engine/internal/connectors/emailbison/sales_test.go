package emailbison_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	c "github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/connectors/emailbison"
)

func TestSalesThreadReceiptAndExplicitCC(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "GET" {
			w.Write([]byte(`{"data":{"current_reply":{"id":2,"parent_id":1,"sender_email_id":7,"raw_message_id":"<reply@customer.example>","from_email_address":"ana@customer.example","to":[{"address":"sender@company.example"}],"cc":[{"address":"colleague@company.example"}],"date_received":"2026-11-11T20:00:00Z","folder":"Inbox"},"older_messages":[{"id":1,"raw_message_id":"<root@company.example>","folder":"Sent"}],"newer_messages":[]}}`))
			return
		}
		calls++
		var body struct {
			CC []struct {
				Email string `json:"email_address"`
			} `json:"cc_emails"`
			Sender  int    `json:"sender_email_id"`
			Content string `json:"content_type"`
			All     bool   `json:"reply_all"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if len(body.CC) != 2 || body.CC[1].Email != "ae@company.example" || body.Sender != 7 || body.Content != "text" || body.All {
			t.Errorf("unapproved recipients or changed sender: %+v", body)
		}
		w.Write([]byte(`{"data":{"success":true,"reply":{"id":3,"raw_message_id":"<intro@company.example>","folder":"Sent"}}}`))
	}))
	defer srv.Close()
	eb := emailbison.EmailBison{}
	cl := &c.Client{}
	creds := c.Creds{"base_url": srv.URL, "api_key": "test_not_a_real_key"}
	thread, err := eb.ReadReplyThread(context.Background(), cl, creds, "2")
	if err != nil {
		t.Fatal(err)
	}
	if len(thread) != 2 || thread[0].Direction != "outgoing" || thread[1].InReplyTo != "<root@company.example>" || !slices.Equal(thread[1].CC, []string{"colleague@company.example"}) {
		t.Fatalf("lost thread evidence: %+v", thread)
	}
	receipt, err := eb.SendReplyReceipt(context.Background(), cl, creds, c.ReplyOut{ReplyExternalID: "2", MailboxExternalID: "7", ToEmail: "ana@customer.example", CC: []string{"colleague@company.example", "ae@company.example"}, Body: "Introducing your account manager"})
	if err != nil || receipt.ExternalID != "3" || receipt.MessageID != "intro@company.example" || calls != 1 {
		t.Fatalf("receipt %+v: %v, calls %d", receipt, err, calls)
	}
}
