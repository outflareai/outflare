package cheapinboxes

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"testing"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

func TestParseWebhook(t *testing.T) {
	body := []byte(`{"id":"evt_9","event":"mailbox.credentials_ready","payload":{"mailboxId":"mb1"},"timestamp":"x"}`)
	mac := hmac.New(sha256.New, []byte("s3cret"))
	mac.Write(body)
	h := http.Header{"X-Webhook-Signature": {hex.EncodeToString(mac.Sum(nil))}}
	ev, err := Connector{}.ParseWebhook("s3cret", h, body)
	if err != nil || ev.ID != "evt_9" || ev.Type != c.EventMailboxCredentials || ev.MailboxExternalID != "mb1" || ev.Resync {
		t.Fatalf("%+v %v", ev, err)
	}
	if _, err := (Connector{}).ParseWebhook("other", h, body); c.KindOf(err) != c.ErrAuth {
		t.Fatalf("wrong secret: %v", err)
	}
	if _, err := (Connector{}).ParseWebhook("", http.Header{}, body); err == nil {
		t.Fatal("no secret accepted")
	}

	order := []byte(`{"id":"evt_10","event":"order.completed","payload":{"order_id":"o1"}}`)
	mac = hmac.New(sha256.New, []byte("s3cret"))
	mac.Write(order)
	ev, err = Connector{}.ParseWebhook("s3cret", http.Header{"X-Webhook-Signature": {hex.EncodeToString(mac.Sum(nil))}}, order)
	if err != nil || !ev.Resync || ev.MailboxExternalID != "" {
		t.Fatalf("order event: %+v %v", ev, err)
	}
}

func TestNeutralMailbox(t *testing.T) {
	scheduled, done := "scheduled", "completed"
	cases := []struct {
		status string
		cancel *string
		want   string
	}{
		{"active", nil, c.VendorActive},
		{"provisioning", nil, c.VendorProvisioning},
		{"pending_dns", nil, c.VendorProvisioning},
		{"active", &scheduled, c.VendorCancelling},
		{"active", &done, c.VendorCancelled},
		{"suspended", nil, c.VendorCancelled},
		{"failed", nil, c.VendorFailed},
	}
	for _, tc := range cases {
		if got := neutralStatus(tc.status, tc.cancel); got != tc.want {
			t.Errorf("%s/%v: %s, want %s", tc.status, tc.cancel, got, tc.want)
		}
	}
	m := mailbox{ID: "1", FullEmail: " Ana@X-com.example ", SourceProvider: "google", EmailBisonID: "350", SmartleadID: nil, InstantlyID: ""}
	v := m.neutral()
	if v.Email != "ana@x-com.example" || v.Provider != "google" || len(v.ConnectedTo) != 1 || v.ConnectedTo[0] != "emailbison" || v.Tags == nil {
		t.Fatalf("%+v", v)
	}
}
