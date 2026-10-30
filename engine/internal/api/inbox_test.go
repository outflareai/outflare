package api_test

import (
	"context"
	"fmt"
	"net/mail"
	"strings"
	"testing"
	"time"
)

func TestInboxSyncMatchesAndActs(t *testing.T) {
	e := freshEnv(t)
	clock := &fakeClock{}
	clock.Set(time.Date(2030, 1, 8, 10, 0, 0, 0, time.UTC))
	owner, base, srv := sendingSetup(t, e, clock)
	ws := strings.TrimPrefix(base, "/v1/workspaces/")

	_, imp := owner.raw("POST", base+"/leads/import?list=L", "text/csv",
		"email,first_name,company\nli@one-io.example,Li,One\nmo@two-io.example,Mo,Two\nni@three-io.example,Ni,Three\noo@four-io.example,Oo,Four\npp@five-io.example,Pp,Five\n")
	seq := owner.must(201, "POST", base+"/sequences", map[string]any{
		"name": "Pilot", "target_daily": 1000, "send_days": []int{1, 2, 3, 4, 5, 6, 7},
		"steps": []map[string]any{{"subject": "Hello {{first_name}}", "body": "Hi {{first_name}}"}, {"delay_days": 2, "body": "Following up"}},
	})
	seqID := seq["id"].(string)
	owner.must(200, "POST", base+"/sequences/"+seqID+"/enrollments", map[string]any{"list_id": imp["list_id"]})
	owner.must(200, "PATCH", base+"/sequences/"+seqID, map[string]any{"status": "active"})
	for range 3 {
		e.pump(t)
		clock.Add(5 * time.Minute)
	}
	if n := len(srv.Messages()); n != 5 {
		t.Fatalf("sent %d", n)
	}
	type sent struct{ id, mailbox, subject string }
	first := map[string]sent{}
	for _, m := range srv.Messages() {
		p := parse(t, m)
		from, _ := mail.ParseAddress(p.Header.Get("From"))
		first[m.To[0]] = sent{strings.Trim(p.Header.Get("Message-Id"), "<>"), from.Address, p.Header.Get("Subject")}
	}

	li, mo, ni, oo := first["li@one-io.example"], first["mo@two-io.example"], first["ni@three-io.example"], first["oo@four-io.example"]
	deliver := func(to, raw string) { srv.DeliverAt(t, to, raw, clock.Now()) }
	// A reply in the thread. The quoted text mentions unsubscribing, which must not count.
	deliver(li.mailbox, fmt.Sprintf("From: Li <li@one-io.example>\nTo: %s\nSubject: Re: %s\nMessage-ID: <r1@one-io.example>\nIn-Reply-To: <%s>\nReferences: <%s>\n\n"+
		"Sounds good, how about Tuesday?\n\nOn Tue, 8 Jan 2030, Ana wrote:\n> Hi Li\n> unsubscribe here\n", li.mailbox, li.subject, li.id, li.id))
	// An opt-out with no threading headers, matched by sender.
	deliver(mo.mailbox, "From: mo@two-io.example\nTo: "+mo.mailbox+"\nSubject: hi\nMessage-ID: <r2@two-io.example>\n\nPlease remove me from your list.\n")
	// An out-of-office does not stop the sequence.
	deliver(ni.mailbox, fmt.Sprintf("From: ni@three-io.example\nTo: %s\nSubject: Out of Office: %s\nAuto-Submitted: auto-replied\nMessage-ID: <r3@three-io.example>\nIn-Reply-To: <%s>\n\nI am away until Monday.\n",
		ni.mailbox, ni.subject, ni.id))
	// A delivery status notification for oo.
	deliver(oo.mailbox, fmt.Sprintf("From: Mail Delivery System <MAILER-DAEMON@mx.four-io.example>\nTo: %s\nSubject: Undelivered Mail Returned to Sender\nMessage-ID: <dsn1@mx.four-io.example>\n"+
		"Content-Type: multipart/report; report-type=delivery-status; boundary=\"b1\"\n\n"+
		"--b1\nContent-Type: text/plain\n\nThe mail system could not deliver your message.\n"+
		"--b1\nContent-Type: message/delivery-status\n\nReporting-MTA: dns; mx.four-io.example\n\nFinal-Recipient: rfc822; oo@four-io.example\nAction: failed\nStatus: 5.1.1\n"+
		"--b1\nContent-Type: text/rfc822-headers\n\nMessage-ID: <%s>\nTo: oo@four-io.example\n--b1--\n", oo.mailbox, oo.id))
	// A forged report for an address never emailed is ignored.
	deliver(oo.mailbox, "From: MAILER-DAEMON@evil.test\nTo: "+oo.mailbox+"\nSubject: Undeliverable\nMessage-ID: <dsn2@evil.test>\n"+
		"Content-Type: multipart/report; report-type=delivery-status; boundary=\"b2\"\n\n"+
		"--b2\nContent-Type: message/delivery-status\n\nReporting-MTA: dns; evil.test\n\nFinal-Recipient: rfc822; ceo@bigco-com.example\nAction: failed\nStatus: 5.1.1\n--b2--\n")
	deliver(li.mailbox, "From: news@letters.test\nTo: "+li.mailbox+"\nSubject: Weekly news\nMessage-ID: <n1@letters.test>\n\nHello\n")

	ctx := context.Background()
	mailboxes := owner.must(200, "GET", base+"/mailboxes", nil)["data"].([]any)
	total := 0
	for _, m := range mailboxes {
		n, err := e.sync.Sync(ctx, ws, m.(map[string]any)["id"].(string))
		if err != nil {
			t.Fatal(err)
		}
		total += n
	}
	if total != 6 {
		t.Fatalf("synced %d", total)
	}
	for _, m := range mailboxes {
		if n, _ := e.sync.Sync(ctx, ws, m.(map[string]any)["id"].(string)); n != 0 {
			t.Fatalf("re-sync stored %d again", n)
		}
	}
	got := owner.must(200, "GET", base+"/mailboxes/"+mailboxes[0].(map[string]any)["id"].(string), nil)
	if got["last_synced_at"] == nil || got["sync_error"] != nil {
		t.Fatalf("sync state: %v", got)
	}

	ens := enrollments(t, owner, base, seqID)
	want := map[string]string{"li@one-io.example": "replied", "mo@two-io.example": "unsubscribed", "ni@three-io.example": "active", "oo@four-io.example": "bounced", "pp@five-io.example": "active"}
	for email, status := range want {
		if ens[email]["status"] != status {
			t.Errorf("%s: %v, want %s", email, ens[email]["status"], status)
		}
	}
	check := owner.must(200, "POST", base+"/recipients/check", map[string]any{"emails": []string{"mo@two-io.example", "oo@four-io.example", "ceo@bigco-com.example", "li@one-io.example"}})["data"].([]any)
	for i, allowed := range []bool{false, false, true, true} {
		if d := check[i].(map[string]any); d["allowed"] != allowed {
			t.Errorf("recipient check %v", d)
		}
	}

	inbox := owner.must(200, "GET", base+"/inbox", nil)
	list := inbox["data"].([]any)
	if len(list) != 3 || inbox["unread_replies"] != 1.0 {
		t.Fatalf("inbox: %v", inbox)
	}
	byFrom := map[string]map[string]any{}
	for _, m := range owner.must(200, "GET", base+"/inbox?kind=all", nil)["data"].([]any) {
		byFrom[m.(map[string]any)["from"].(string)] = m.(map[string]any)
	}
	kinds := map[string]string{"li@one-io.example": "reply", "mo@two-io.example": "reply", "ni@three-io.example": "auto_reply", "mailer-daemon@mx.four-io.example": "bounce", "news@letters.test": "other"}
	for from, kind := range kinds {
		if byFrom[from]["kind"] != kind {
			t.Errorf("%s: kind %v, want %s", from, byFrom[from]["kind"], kind)
		}
	}
	liMsg := byFrom["li@one-io.example"]
	if liMsg["lead_name"] != "Li" || liMsg["sequence_name"] != "Pilot" || byFrom["mo@two-io.example"]["opt_out"] != true || liMsg["opt_out"] != false {
		t.Fatalf("matching: %v / %v", liMsg, byFrom["mo@two-io.example"])
	}

	// The conversation, and a reply from the inbox in the same thread.
	path := base + "/inbox/" + liMsg["id"].(string)
	if th := owner.must(200, "GET", path, nil)["thread"].([]any); len(th) != 2 || th[0].(map[string]any)["direction"] != "out" {
		t.Fatalf("thread: %v", th)
	}
	before := len(srv.Messages())
	owner.must(200, "POST", path+"/reply", map[string]any{"body": "Tuesday at 10 works."})
	msgs := srv.Messages()
	if len(msgs) != before+1 {
		t.Fatalf("reply not sent")
	}
	rp := parse(t, msgs[len(msgs)-1])
	if rp.Header.Get("In-Reply-To") != "<r1@one-io.example>" || !strings.Contains(rp.Header.Get("References"), "<"+li.id+">") || rp.Header.Get("Subject") != "Re: "+li.subject || msgs[len(msgs)-1].From != li.mailbox {
		t.Fatalf("reply headers: %v from %s", rp.Header, msgs[len(msgs)-1].From)
	}
	if th := owner.must(200, "GET", path, nil); len(th["thread"].([]any)) != 3 || th["message"].(map[string]any)["read"] != true {
		t.Fatalf("after reply: %v", th)
	}
	owner.must(409, "POST", base+"/inbox/"+byFrom["mo@two-io.example"]["id"].(string)+"/reply", map[string]any{"body": "Sorry!"})
	owner.must(200, "PATCH", path, map[string]any{"read": false})
	if out := owner.must(200, "GET", base+"/inbox?unread=true", nil); len(out["data"].([]any)) != 3 || out["unread_replies"] != 1.0 {
		t.Fatalf("unread filter: %v", out)
	}

	// Triage: the rules sorted what they could on arrival.
	for from, want := range map[string]string{"li@one-io.example": "interested", "mo@two-io.example": "unsubscribe", "ni@three-io.example": "out_of_office"} {
		if byFrom[from]["category"] != want || byFrom[from]["category_source"] != "rules" {
			t.Errorf("%s: category %v (%v), want %s", from, byFrom[from]["category"], byFrom[from]["category_source"], want)
		}
	}
	members := owner.must(200, "GET", base+"/members", nil)["data"].([]any)
	uid := members[0].(map[string]any)["user_id"].(string)
	tri := owner.must(200, "PATCH", path, map[string]any{"category": "meeting_booked", "assigned_to": uid, "labels": []string{" VIP ", "vip", "q4"}})
	if tri["category"] != "meeting_booked" || tri["category_source"] != "human" || tri["assignee_name"] == nil || len(tri["labels"].([]any)) != 2 {
		t.Fatalf("triage: %v", tri)
	}
	if mine := owner.must(200, "GET", base+"/inbox?assigned_to=me&tag=vip&category=meeting_booked", nil)["data"].([]any); len(mine) != 1 {
		t.Fatalf("assigned to me: %v", mine)
	}
	owner.must(200, "PATCH", path, map[string]any{"snoozed_until": time.Now().Add(time.Hour).Format(time.RFC3339)})
	// Unsubscribes and out-of-office replies arrive already done.
	if byFrom["mo@two-io.example"]["done"] != true || byFrom["ni@three-io.example"]["done"] != true || liMsg["done"] != false {
		t.Fatalf("done on arrival: %v %v %v", byFrom["mo@two-io.example"]["done"], byFrom["ni@three-io.example"]["done"], liMsg["done"])
	}
	owner.must(200, "PATCH", base+"/inbox/"+byFrom["ni@three-io.example"]["id"].(string), map[string]any{"done": false})
	if open := owner.must(200, "GET", base+"/inbox?status=open", nil)["data"].([]any); len(open) != 1 || open[0].(map[string]any)["from"] != "ni@three-io.example" {
		t.Fatalf("open: %v", open)
	}
	if sn := owner.must(200, "GET", base+"/inbox?status=snoozed", nil)["data"].([]any); len(sn) != 1 {
		t.Fatalf("snoozed: %v", sn)
	}
	owner.must(200, "PATCH", path, map[string]any{"snoozed_until": nil, "category": nil})
	if none := owner.must(200, "GET", base+"/inbox?category=none", nil)["data"].([]any); len(none) != 1 {
		t.Fatalf("uncategorised: %v", none)
	}
	owner.must(400, "PATCH", path, map[string]any{"category": "maybe"})
	owner.must(400, "PATCH", path, map[string]any{"assigned_to": "00000000-0000-0000-0000-000000000000"})
	owner.must(400, "PATCH", path, map[string]any{})

	// Two days on, only the leads still active get the follow-up.
	clock.Add(48 * time.Hour)
	before = len(srv.Messages())
	e.pump(t)
	clock.Add(5 * time.Minute)
	e.pump(t)
	var to []string
	for _, m := range srv.Messages()[before:] {
		to = append(to, m.To[0])
	}
	if len(to) != 2 || !(strings.Contains(fmt.Sprint(to), "ni@three-io.example") && strings.Contains(fmt.Sprint(to), "pp@five-io.example")) {
		t.Fatalf("follow-ups went to %v", to)
	}

	// The overview counts it all.
	st := owner.must(200, "GET", base+"/stats", nil)
	if mb := st["mailboxes"].(map[string]any); mb["active"] != 2.0 || mb["daily_capacity"] != 60.0 ||
		st["sent_7d"].(float64) < 5 || st["replies_7d"].(float64) < 1 || st["unread_replies"] != 1.0 || st["active_sequences"] != 1.0 {
		t.Fatalf("stats: %v", st)
	}
	// Reply rate counts people, not emails: each lead is contacted once,
	// however many follow-ups it got.
	if c, r := st["contacted_leads_7d"].(float64), st["replied_leads_7d"].(float64); c < 1 || c >= st["sent_7d"].(float64) || r < 1 || r > c {
		t.Fatalf("leads: %v", st)
	}

	// Scopes.
	tok := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "r", "scopes": []string{"inbox.read"}})
	reader := &client{t: t, h: e.h, bearer: tok["token"].(string)}
	reader.must(200, "GET", path, nil)
	reader.must(403, "POST", path+"/reply", map[string]any{"body": "x"})
	reader.must(403, "PATCH", path, map[string]any{"read": true})
	owner.must(202, "POST", base+"/mailboxes/"+mailboxes[0].(map[string]any)["id"].(string)+"/sync", nil)

	other := owner.must(201, "POST", "/v1/workspaces", map[string]any{"name": "Other"})
	owner.must(404, "GET", "/v1/workspaces/"+other["id"].(string)+"/inbox/"+liMsg["id"].(string), nil)
}

func TestInboxSkipsSequencerWarmupTags(t *testing.T) {
	e := freshEnv(t)
	clock := &fakeClock{}
	clock.Set(time.Date(2030, 1, 8, 10, 0, 0, 0, time.UTC))
	owner, base, srv := sendingSetup(t, e, clock)
	ws := strings.TrimPrefix(base, "/v1/workspaces/")
	ctx := context.Background()
	var ana string
	for _, m := range owner.must(200, "GET", base+"/mailboxes", nil)["data"].([]any) {
		if m := m.(map[string]any); m["email"] == "ana@acme.test" {
			ana = m["id"].(string)
		}
	}

	id := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "emailbison",
		"credentials": map[string]string{"base_url": "https://send.acme.test", "api_key": "eb-key"}})["id"].(string)
	for _, bad := range []any{[]string{"ab"}, "XK7Q2Z9", []int{1}} {
		if code, _ := owner.do("PATCH", base+"/integrations/"+id, map[string]any{"config": map[string]any{"warmup_tags": bad}}); code != 400 {
			t.Fatalf("warmup_tags %v accepted: %d", bad, code)
		}
	}
	owner.must(200, "PATCH", base+"/integrations/"+id, map[string]any{"config": map[string]any{"warmup_tags": []string{" XK7Q2Z9 "}}})

	deliver := func(raw string) { srv.DeliverAt(t, "ana@acme.test", raw, clock.Now()) }
	// EmailBison's warm-up mail carries its tag, in the subject or the body.
	deliver("From: Sam <sam@pool-io.example>\nTo: ana@acme.test\nSubject: Quick question XK7Q2Z9\nMessage-ID: <w1@pool-io.example>\n\nAre you around Friday?\n")
	deliver("From: Lee <lee@pool-io.example>\nTo: ana@acme.test\nSubject: Lunch\nMessage-ID: <w2@pool-io.example>\n\nThe place on 5th?\n\nXK7Q2Z9\n")
	// Real mail is kept.
	deliver("From: Kim <kim@real-io.example>\nTo: ana@acme.test\nSubject: Partnership\nMessage-ID: <k1@real-io.example>\n\nCan we talk?\n")
	if n, err := e.sync.Sync(ctx, ws, ana); err != nil || n != 1 {
		t.Fatalf("stored %d: %v", n, err)
	}
	var subject string
	e.pool.QueryRow(ctx, `SELECT subject FROM inbound_messages WHERE mailbox_id = $1`, ana).Scan(&subject) //nolint:errcheck
	if subject != "Partnership" {
		t.Fatalf("kept %q", subject)
	}
	// The sequencer's mail is left unread, for its own engagement.
	if srv.Count(t, "ana@acme.test", "INBOX") != 3 {
		t.Fatal("warm-up mail was moved")
	}
}
