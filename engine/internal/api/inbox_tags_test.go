package api_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/inbox"
	"github.com/outflareai/outflare/engine/internal/track"
)

func TestInboxLabelsTagsAndViews(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, base, _ := sendingSetup(t, e, &fakeClock{t: time.Now()})
	ws := base[len("/v1/workspaces/"):]
	ctx := context.Background()
	mb := mailboxIDs(t, owner, base)["ana@acme.test"]

	// Replies as the rules, the AI and a sequencer would leave them.
	for i, r := range []struct {
		from, category, by, source, body string
		confidence                       float64
		review                           bool
		labels                           []string
	}{
		{"hot@a.test", "interested", "rules", "mailbox", "Sounds good", 1, false, []string{"vip"}},
		{"ask@b.test", "question", "ai", "emailbison", "What does it cost?", 0.92, false, nil},
		{"later@c.test", "objection", "ai", "mailbox", "We use a competitor", 0.85, false, []string{"vip", "q4"}},
		{"maybe@d.test", "unsubscribe", "ai", "emailbison", "not for us, stop", 0.6, true, nil},
		{"away@e.test", "auto_reply", "rules", "mailbox", "Thanks for your email", 1, false, nil},
	} {
		if _, err := e.pool.Exec(ctx, `INSERT INTO inbound_messages (workspace_id, mailbox_id, uidvalidity, uid, from_email, kind, body,
			category, category_source, category_confidence, source, needs_review, labels, received_at)
			VALUES ($1, $2, 0, $3::bigint, $4, 'reply', $5, $6, $7, $8, $9, $10, coalesce($11, '{}'::text[]), now() - $3::int * interval '1 minute')`,
			ws, mb, i+1, r.from, r.body, r.category, r.by, r.confidence, r.source, r.review, r.labels); err != nil {
			t.Fatal(err)
		}
	}
	list := func(query string) []map[string]any {
		t.Helper()
		var out []map[string]any
		for _, m := range owner.must(200, "GET", base+"/inbox?"+query, nil)["data"].([]any) {
			out = append(out, m.(map[string]any))
		}
		return out
	}
	from := func(ms []map[string]any) string {
		var s []string
		for _, m := range ms {
			s = append(s, m["from"].(string))
		}
		return fmt.Sprint(s)
	}
	for q, want := range map[string]string{
		"group=hot":                     "[hot@a.test ask@b.test]",
		"group=later,closed":            "[later@c.test maybe@d.test]",
		"category=question,auto_reply":  "[ask@b.test away@e.test]",
		"source=emailbison":             "[ask@b.test maybe@d.test]",
		"needs_review=true":             "[maybe@d.test]",
		"min_confidence=0.9":            "[hot@a.test ask@b.test away@e.test]",
		"tag=q4":                        "[later@c.test]",
		"tag=vip,q4":                    "[hot@a.test later@c.test]",
		"tag=vip&group=hot":             "[hot@a.test]",
		"q=competitor":                  "[later@c.test]",
		"group=automatic&kind=all":      "[away@e.test]",
		"brand=none&needs_review=false": "[hot@a.test ask@b.test later@c.test away@e.test]",
	} {
		if g := from(list(q)); g != want {
			t.Errorf("%s: %s, want %s", q, g, want)
		}
	}
	ask := list("source=emailbison")[0]
	if ask["group"] != "hot" || ask["category_confidence"] != 0.92 || ask["category_source"] != "ai" || ask["source"] != "emailbison" {
		t.Fatalf("reply view: %v", ask)
	}
	for _, q := range []string{"group=warm", "category=maybe", "needs_review=yes", "min_confidence=2"} {
		owner.must(400, "GET", base+"/inbox?"+q, nil)
	}

	// A person settles the unsure opt-out: the label is theirs, sure, and
	// no longer waiting for review; webhooks and agents hear about it.
	maybe := list("needs_review=true")[0]
	// got returns every event so far; count from here.
	since := func(name string, before int) []track.Event { return named(got(), name)[before:] }
	fixed := owner.must(200, "PATCH", base+"/inbox/"+maybe["id"].(string), map[string]any{"category": "not_now", "labels": []string{"Follow up"}})
	if fixed["category"] != "not_now" || fixed["category_source"] != "human" || fixed["category_confidence"] != 1.0 ||
		fixed["needs_review"] != false || fixed["group"] != "later" {
		t.Fatalf("relabelled: %v", fixed)
	}
	cl := since("inbox.classified", 0)
	if len(cl) != 1 || cl[0].Props["category"] != "not_now" || cl[0].Props["category_source"] != "human" || cl[0].Props["group"] != "later" ||
		cl[0].Props["confidence_band"] != "high" || cl[0].WorkspaceID != ws {
		t.Fatalf("inbox.classified: %+v", cl)
	}
	// The same label again changes nothing, so says nothing.
	owner.must(200, "PATCH", base+"/inbox/"+maybe["id"].(string), map[string]any{"category": "not_now"})
	if n := len(since("inbox.classified", 1)); n != 0 {
		t.Fatalf("unchanged label recorded %d times", n)
	}
	owner.must(200, "PATCH", base+"/inbox/"+maybe["id"].(string), map[string]any{"needs_review": true})
	if len(list("needs_review=true")) != 1 {
		t.Fatal("flag for review")
	}

	// Tags: the label used above became a tag, counted with the others.
	tags := map[string]map[string]any{}
	for _, tg := range owner.must(200, "GET", base+"/inbox-tags", nil)["data"].([]any) {
		tags[tg.(map[string]any)["name"].(string)] = tg.(map[string]any)
	}
	if tags["follow up"] == nil || tags["follow up"]["replies"] != 1.0 || tags["follow up"]["color"] != "gray" {
		t.Fatalf("tags: %v", tags)
	}
	vip := owner.must(201, "POST", base+"/inbox-tags", map[string]any{"name": "VIP ", "color": "red"})
	if vip["name"] != "vip" || vip["color"] != "red" || vip["replies"] != 2.0 {
		t.Fatalf("tag: %v", vip)
	}
	owner.must(409, "POST", base+"/inbox-tags", map[string]any{"name": "vip"})
	owner.must(400, "POST", base+"/inbox-tags", map[string]any{"name": "x", "color": "plaid"})
	owner.must(400, "POST", base+"/inbox-tags", map[string]any{"name": ""})
	if c := since("inbox_tag.created", 0); len(c) != 1 {
		t.Fatalf("inbox_tag.created: %+v", c)
	}
	// Renaming a tag renames it on every reply; deleting takes it off.
	owner.must(200, "PATCH", base+"/inbox-tags/"+vip["id"].(string), map[string]any{"name": "key account", "color": "violet"})
	if g := from(list("tag=key+account")); g != "[hot@a.test later@c.test]" {
		t.Fatalf("renamed tag: %s", g)
	}
	owner.must(204, "DELETE", base+"/inbox-tags/"+vip["id"].(string), nil)
	if len(list("tag=key+account")) != 0 {
		t.Fatal("deleted tag still on replies")
	}
	if len(named(got(), "inbox_tag.deleted")) != 1 {
		t.Fatal("inbox_tag.deleted")
	}

	// Saved views: personal by default, shared when asked; a token's are
	// shared, and nobody sees another person's.
	mine := owner.must(201, "POST", base+"/inbox-views", map[string]any{"name": "Hot from EmailBison", "filters": map[string]any{"group": "hot", "source": "emailbison", "unread": true}})
	if mine["shared"] != false || mine["filters"].(map[string]any)["unread"] != "true" {
		t.Fatalf("view: %v", mine)
	}
	owner.must(201, "POST", base+"/inbox-views", map[string]any{"name": "Review", "shared": true, "filters": map[string]any{"needs_review": "true"}})
	owner.must(400, "POST", base+"/inbox-views", map[string]any{"name": "Bad", "filters": map[string]any{"color": "red"}})
	owner.must(400, "POST", base+"/inbox-views", map[string]any{"name": "Bad", "filters": map[string]any{"group": "warm"}})
	tok := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "agent", "scopes": []string{"inbox.read"}})
	agent := &client{t: t, h: e.h, bearer: tok["token"].(string)}
	if vs := agent.must(200, "GET", base+"/inbox-views", nil)["data"].([]any); len(vs) != 1 || vs[0].(map[string]any)["name"] != "Review" {
		t.Fatalf("token sees: %v", vs)
	}
	if v := agent.must(201, "POST", base+"/inbox-views", map[string]any{"name": "Agent", "filters": map[string]any{}}); v["shared"] != true {
		t.Fatalf("token view: %v", v)
	}
	agent.must(404, "PATCH", base+"/inbox-views/"+mine["id"].(string), map[string]any{"name": "Mine now"})
	agent.must(403, "POST", base+"/inbox-tags", map[string]any{"name": "nope"})
	if vs := owner.must(200, "GET", base+"/inbox-views", nil)["data"].([]any); len(vs) != 3 {
		t.Fatalf("owner sees %d views", len(vs))
	}
	owner.must(200, "PATCH", base+"/inbox-views/"+mine["id"].(string), map[string]any{"name": "Hot", "position": 5})
	owner.must(204, "DELETE", base+"/inbox-views/"+mine["id"].(string), nil)
	ev := got()
	for _, name := range []string{"inbox_view.created", "inbox_view.updated", "inbox_view.deleted"} {
		if len(named(ev, name)) == 0 {
			t.Errorf("no %s", name)
		}
	}

	other := owner.must(201, "POST", "/v1/workspaces", map[string]any{"name": "Other"})
	owner.must(404, "PATCH", "/v1/workspaces/"+other["id"].(string)+"/inbox-tags/"+tags["follow up"]["id"].(string), map[string]any{"color": "red"})
}

func TestWebhooksForSomeLabels(t *testing.T) {
	e, owner, ws, _ := webhookEnv(t)
	base := "/v1/workspaces/" + ws
	owner.must(400, "POST", base+"/webhooks", map[string]any{"url": "https://example.com/a", "events": []string{"inbox.classified"}, "categories": []string{"warm"}})
	hot := owner.must(201, "POST", base+"/webhooks", map[string]any{"url": "https://example.com/hot", "events": []string{"inbox.classified"}, "categories": []string{"hot", "referral"}})
	if fmt.Sprint(hot["categories"]) != "[interested meeting_booked question referral]" {
		t.Fatalf("categories: %v", hot["categories"])
	}
	all := owner.must(201, "POST", base+"/webhooks", map[string]any{"url": "https://example.com/all", "events": []string{"inbox.classified"}})
	if fmt.Sprint(all["categories"]) != "[]" {
		t.Fatalf("no categories: %v", all["categories"])
	}

	ctx := context.Background()
	for i, c := range []string{"interested", "not_interested", "referral"} {
		cat := c
		if err := inbox.Classified(ctx, e.pool, inbox.Classification{WorkspaceID: ws, InboundID: fmt.Sprintf("00000000-0000-0000-0000-00000000000%d", i),
			MailboxID: "m", Source: "mailbox", Category: &cat, By: "rules", Confidence: 1}); err != nil {
			t.Fatal(err)
		}
	}
	count := func(id string) (n int) {
		t.Helper()
		if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM webhook_deliveries WHERE endpoint_id = $1 AND event_type = 'inbox.classified'`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if count(hot["id"].(string)) != 2 || count(all["id"].(string)) != 3 {
		t.Fatalf("deliveries: hot %d, all %d", count(hot["id"].(string)), count(all["id"].(string)))
	}
	up := owner.must(200, "PATCH", base+"/webhooks/"+hot["id"].(string), map[string]any{"categories": []string{}})
	if fmt.Sprint(up["categories"]) != "[]" {
		t.Fatalf("cleared: %v", up["categories"])
	}
}
