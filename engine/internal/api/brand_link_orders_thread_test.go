package api_test

import (
	"context"
	"strings"
	"testing"
	"time"
)

// An order shows the brand's emails with its donor: sends to the site's
// domain, and replies from it or from a stored contact such as the
// intermediary in the donor's notes, with the prices written in replies.
// Other sites' mail and another brand's mail stay out.
func TestLinkOrderThread(t *testing.T) {
	e := freshEnv(t)
	owner, base, _ := sendingSetup(t, e, &fakeClock{t: time.Now()})
	ws := strings.TrimPrefix(base, "/v1/workspaces/")
	ctx := context.Background()
	mb := mailboxIDs(t, owner, base)["ana@acme.test"]
	brand := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "handle": "northpeak"})["id"].(string)
	other := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Acme", "handle": "acme-two"})["id"].(string)
	root := "/v1/brands/" + brand + "/link-orders"
	donor := owner.must(201, "POST", root+"/catalog", map[string]any{"domain": "donor.example", "notes": "Bought via sales@broker.example"})
	order := owner.must(201, "POST", root, map[string]any{"donor_profile_id": donor["id"], "target_url": "https://northpeak.example/page"})

	if _, err := e.pool.Exec(ctx, `
		INSERT INTO messages (workspace_id, brand_id, mailbox_id, from_email, to_email, subject, body, message_id, status, sent_at)
		VALUES ($1, $2, $3, 'ana@acme.test', 'editor@donor.example', 'Guest post', 'Hi, could we place a link?', '<a@acme.test>', 'sent', now() - interval '2 days'),
		       ($1, $2, $3, 'ana@acme.test', 'someone@elsewhere.example', 'Other', 'Not this one', '<b@acme.test>', 'sent', now()),
		       ($1, $4, $3, 'ana@acme.test', 'editor@donor.example', 'Other brand', 'Not this brand', '<c@acme.test>', 'sent', now())`,
		ws, brand, mb, other); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `
		INSERT INTO inbound_messages (workspace_id, brand_id, mailbox_id, uidvalidity, uid, from_email, to_email, kind, subject, body, category, received_at)
		VALUES ($1, $2, $3, 0, nextval('inbound_external_uid_seq'), 'Sales@Broker.example', 'ana@acme.test', 'reply', 'Re: Guest post', 'We can do it for $150, or 120 EUR.', 'interested', now() - interval '1 day'),
		       ($1, $2, $3, 0, nextval('inbound_external_uid_seq'), 'news@elsewhere.example', 'ana@acme.test', 'other', 'Newsletter', 'Not this one', NULL, now())`,
		ws, brand, mb); err != nil {
		t.Fatal(err)
	}

	got := owner.must(200, "GET", root+"/"+order["id"].(string)+"/thread", nil)
	msgs := got["messages"].([]any)
	if got["domain"] != "donor.example" || len(msgs) != 2 {
		t.Fatalf("thread: %v", got)
	}
	reply, sent := msgs[0].(map[string]any), msgs[1].(map[string]any)
	if reply["direction"] != "received" || reply["category"] != "interested" || len(reply["amounts"].([]any)) != 2 {
		t.Fatalf("reply: %v", reply)
	}
	if sent["direction"] != "sent" || sent["to"] != "editor@donor.example" || len(sent["amounts"].([]any)) != 0 {
		t.Fatalf("sent: %v", sent)
	}
	if addrs := got["addresses"].([]any); len(addrs) != 1 || addrs[0] != "sales@broker.example" {
		t.Fatalf("addresses: %v", addrs)
	}
	owner.must(404, "GET", root+"/00000000-0000-0000-0000-000000000000/thread", nil)
}
