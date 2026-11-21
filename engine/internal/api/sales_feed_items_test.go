package api_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/api"
	"github.com/outflareai/outflare/engine/internal/track"
)

func (f *feedFixture) salesRoot() string { return "/v1/brands/" + f.brand + "/sales" }

// salesUser invites a member with a Sales role (none for "") to the
// fixture's workspace and returns their signed-in client and id.
func (f *feedFixture) salesUser(t *testing.T, email, role string) (*client, string) {
	t.Helper()
	inv := f.owner.must(201, "POST", "/v1/workspaces/"+f.ws+"/invites", map[string]any{"email": email, "role": "operator"})
	c := &client{t: t, h: f.e.h}
	c.must(200, "POST", "/v1/invites/accept", map[string]any{"token": inv["token"], "name": "Example Rep", "password": "test-not-real-password"})
	id := c.must(200, "GET", "/v1/me", nil)["user"].(map[string]any)["id"].(string)
	if role != "" {
		f.owner.must(200, "PUT", f.salesRoot()+"/team/"+id, map[string]any{"role": role})
	}
	return c, id
}

func (f *feedFixture) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := f.e.pool.Exec(f.ctx, sql, args...); err != nil {
		t.Fatal(err)
	}
}

func (f *feedFixture) feedItems(t *testing.T, c *client, status string) []any {
	t.Helper()
	return c.must(200, "GET", f.salesRoot()+"/feed-items?status="+status, nil)["data"].([]any)
}

func (f *feedFixture) actItem(t *testing.T, c *client, want int, item string, body map[string]any) map[string]any {
	t.Helper()
	return c.must(want, "POST", f.salesRoot()+"/feed-items/"+item+"/actions", body)
}

func listedIDs(list []any) []string {
	var ids []string
	for _, x := range list {
		ids = append(ids, x.(map[string]any)["id"].(string))
	}
	return ids
}

// needsLook stores and reads a post naming two people, which needs a look.
func needsLook(t *testing.T, f *feedFixture) (item, channel string) {
	t.Helper()
	item, channel = seedFeedItem(t, f, "new_person", "Contact: ana@acme.example or bo@acme.example")
	f.runItem(t, item)
	if s := f.itemRow(t, item).Status; s != "needs_look" {
		t.Fatalf("status %s", s)
	}
	return item, channel
}

func TestSalesLeadsShowFeedKind(t *testing.T) {
	f := newFeedFixture(t)
	item, ch := seedFeedItem(t, f, "new_person", "Email: ana@acme.example")
	f.runItem(t, item)
	list := f.owner.must(200, "GET", f.salesRoot()+"/leads?queue=new", nil)["data"].([]any)
	if len(list) != 1 {
		t.Fatalf("leads: %v", list)
	}
	l := list[0].(map[string]any)
	if l["feed_kind"] != "new_person" || l["feed_channel_id"] != ch || l["feed_channel_name"] == "" {
		t.Fatalf("lead: %v", l)
	}
	name := l["feed_channel_name"]
	// A removed channel still names where the conversation came from.
	f.exec(t, `UPDATE sales_feed_channels SET removed_at=now(),enabled=false WHERE id=$1`, ch)
	l = f.owner.must(200, "GET", f.salesRoot()+"/leads?queue=new", nil)["data"].([]any)[0].(map[string]any)
	if l["feed_kind"] != "new_person" || l["feed_channel_id"] != ch || l["feed_channel_name"] != name {
		t.Fatalf("lead after removal: %v", l)
	}
	// Only a brand that is gone takes its channels with it; the conversation
	// then has no channel.
	f.exec(t, `DELETE FROM sales_feed_channels WHERE id=$1`, ch)
	l = f.owner.must(200, "GET", f.salesRoot()+"/leads?queue=new", nil)["data"].([]any)[0].(map[string]any)
	if l["feed_kind"] != "new_person" || l["feed_channel_id"] != nil || l["feed_channel_name"] != "" {
		t.Fatalf("lead after the channel row went: %v", l)
	}
}

func TestSalesLeadsFilterByKindAndChannel(t *testing.T) {
	f := newFeedFixture(t)
	a, chA := seedFeedItem(t, f, "new_person", "Email: ana@acme.example")
	b, chB := seedFeedItem(t, f, "handover", "Hand over: bo@beta.example")
	f.runItem(t, a)
	f.runItem(t, b)
	leads := func(q string) []any {
		return f.owner.must(200, "GET", f.salesRoot()+"/leads?queue=all&"+q, nil)["data"].([]any)
	}
	if n := len(leads("")); n != 2 {
		t.Fatalf("all: %d", n)
	}
	if l := leads("kind=handover"); len(l) != 1 || l[0].(map[string]any)["feed_channel_id"] != chB {
		t.Fatalf("kind: %v", l)
	}
	if l := leads("feed_channel=" + chA); len(l) != 1 || l[0].(map[string]any)["feed_kind"] != "new_person" {
		t.Fatalf("channel: %v", l)
	}
	if l := leads("kind=handover&feed_channel=" + chA); len(l) != 0 {
		t.Fatalf("both: %v", l)
	}
	f.owner.must(400, "GET", f.salesRoot()+"/leads?kind=bogus", nil)
	f.owner.must(400, "GET", f.salesRoot()+"/leads?feed_channel=nope", nil)
}

func TestSalesLeadDetailHasFeedItems(t *testing.T) {
	f := newFeedFixture(t)
	first, _ := seedFeedItem(t, f, "new_person", "Email: ana@acme.example\nBudget: 10k")
	second, _ := seedFeedItem(t, f, "handover", "Hand over: ana@acme.example is ready")
	f.exec(t, `UPDATE sales_feed_items SET occurred_at=now()-interval '1 hour' WHERE id=$1`, first)
	at := time.Now().UTC().Truncate(time.Second)
	if err := f.e.srv.SetSalesFeedReplies(f.ctx, f.ws, first, []api.FeedReplyForTest{{Author: "U42", At: at, Text: strings.Repeat("r", 5000)}}); err != nil {
		t.Fatal(err)
	}
	f.runItem(t, first)
	f.runItem(t, second)
	lead := *f.itemRow(t, first).LeadID
	out := f.owner.must(200, "GET", f.salesRoot()+"/leads/"+lead, nil)
	items := out["feed_items"].([]any)
	if len(items) != 2 {
		t.Fatalf("feed_items: %v", items)
	}
	a, b := items[0].(map[string]any), items[1].(map[string]any)
	if a["id"] != first || b["id"] != second {
		t.Fatalf("order: %v %v", a["id"], b["id"])
	}
	if a["text"] != "Email: ana@acme.example\nBudget: 10k" || a["status"] != "read" || a["lead_id"] != lead || !strings.HasPrefix(a["link"].(string), "https://") {
		t.Fatalf("item: %v", a)
	}
	if fs := a["fields"].([]any); len(fs) != 2 || fs[1].(map[string]any)["label"] != "Budget" || fs[1].(map[string]any)["value"] != "10k" {
		t.Fatalf("fields: %v", fs)
	}
	rs := a["replies"].([]any)
	if len(rs) != 1 || rs[0].(map[string]any)["author"] != "U42" || len(rs[0].(map[string]any)["text"].(string)) != 4000 {
		t.Fatalf("replies: %v", rs)
	}
	if b["kind"] != "handover" || len(b["replies"].([]any)) != 0 {
		t.Fatalf("second: %v", b)
	}
}

func TestSalesLeadsLatestPost(t *testing.T) {
	f := newFeedFixture(t)
	first, _ := seedFeedItem(t, f, "new_person", "Email: ana@acme.example\nBudget: 10k")
	second, _ := seedFeedItem(t, f, "handover", "Hand over: ana@acme.example is ready")
	f.exec(t, `UPDATE sales_feed_items SET occurred_at=now()-interval '1 hour' WHERE id=$1`, first)
	f.runItem(t, first)
	f.runItem(t, second)
	lead := *f.itemRow(t, first).LeadID
	if other := f.itemRow(t, second).LeadID; other == nil || *other != lead {
		t.Fatalf("both posts should be on one conversation: %v", other)
	}
	if err := f.e.srv.SetSalesFeedReading(f.ctx, f.ws, first, "", "Ana asks about pricing"); err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("é", 1500)
	if err := f.e.srv.SetSalesFeedReading(f.ctx, f.ws, second, long, "Ready to talk"); err != nil {
		t.Fatal(err)
	}
	var secondAt time.Time
	if err := f.e.pool.QueryRow(f.ctx, `SELECT occurred_at FROM sales_feed_items WHERE id=$1`, second).Scan(&secondAt); err != nil {
		t.Fatal(err)
	}
	listed := func() any {
		t.Helper()
		list := f.owner.must(200, "GET", f.salesRoot()+"/leads?queue=all", nil)["data"].([]any)
		if len(list) != 1 {
			t.Fatalf("leads: %v", list)
		}
		return list[0].(map[string]any)["latest_post"]
	}
	post := func() any {
		t.Helper()
		inList := listed()
		inDetail := f.owner.must(200, "GET", f.salesRoot()+"/leads/"+lead, nil)["lead"].(map[string]any)["latest_post"]
		a, _ := json.Marshal(inList)
		b, _ := json.Marshal(inDetail)
		if string(a) != string(b) {
			t.Fatalf("list %s, detail %s", a, b)
		}
		return inList
	}

	// The newer post's own message, cut to 1,200 characters.
	p, _ := post().(map[string]any)
	if p == nil || p["body"] != strings.Repeat("é", 1200) {
		t.Fatalf("latest_post: %v", p)
	}
	if at, err := time.Parse(time.RFC3339Nano, p["occurred_at"].(string)); err != nil || !at.Equal(secondAt) {
		t.Fatalf("occurred_at %v, want %v", p["occurred_at"], secondAt)
	}

	// The older post is a form: it leads with its field, not with the AI's
	// sentence about it, and has no text of the person's own.
	older := func(p map[string]any) bool {
		fields, _ := p["fields"].([]any)
		if p == nil || len(fields) != 1 || p["body"] != "" {
			return false
		}
		f := fields[0].(map[string]any)
		return f["label"] == "Budget" && f["value"] == "10k"
	}

	// A spam or ignored newest post is skipped; the older one shows.
	for _, status := range []string{"spam", "ignored"} {
		f.exec(t, `UPDATE sales_feed_items SET status=$2 WHERE id=$1`, second, status)
		if p, _ := post().(map[string]any); !older(p) {
			t.Fatalf("%s newest: %v", status, p)
		}
	}

	// A newest post deleted in Slack is skipped too.
	f.exec(t, `UPDATE sales_feed_items SET status='read',deleted_in_slack=true WHERE id=$1`, second)
	if p, _ := post().(map[string]any); !older(p) {
		t.Fatalf("deleted newest: %v", p)
	}

	// A post that can't be opened gives no preview, and the page still loads.
	// (The detail lists every post, so it is checked on the list only.)
	f.exec(t, `UPDATE sales_feed_items SET content_sealed='\x00' WHERE id=$1`, first)
	if p := listed(); p != nil {
		t.Fatalf("unreadable post: %v", p)
	}

	// A conversation with no posts has null. (Posts about its person on no
	// conversation are on its page too, so these go off the person as well.)
	f.exec(t, `UPDATE sales_feed_items SET lead_id=NULL,record_id=NULL WHERE lead_id=$1`, lead)
	if p := post(); p != nil {
		t.Fatalf("no posts: %v", p)
	}
}

func TestFeedItemsNeedsLookList(t *testing.T) {
	f := newFeedFixture(t)
	item, ch := needsLook(t, f)
	list := f.feedItems(t, f.owner, "needs_look")
	if len(list) != 1 {
		t.Fatalf("list: %v", list)
	}
	it := list[0].(map[string]any)
	if it["id"] != item || it["channel_id"] != ch || !slices.Equal(toStrings(it["candidates"]), []string{"ana@acme.example", "bo@acme.example"}) {
		t.Fatalf("item: %v", it)
	}
	if len(f.feedItems(t, f.owner, "spam")) != 0 {
		t.Fatal("spam list not empty")
	}
	f.owner.must(400, "GET", f.salesRoot()+"/feed-items", nil)
	f.owner.must(400, "GET", f.salesRoot()+"/feed-items?status=read", nil)
	f.owner.must(400, "GET", f.salesRoot()+"/feed-items?status=needs_look&feed_channel=nope", nil)
	if l := f.owner.must(200, "GET", f.salesRoot()+"/feed-items?status=needs_look&feed_channel="+ch, nil)["data"].([]any); len(l) != 1 {
		t.Fatalf("by channel: %v", l)
	}
}

func TestFeedItemsPage(t *testing.T) {
	f := newFeedFixture(t)
	var items []string
	for range 3 {
		item, _ := needsLook(t, f)
		items = append(items, item)
	}
	f.exec(t, `UPDATE sales_feed_items SET occurred_at=now()-interval '1 hour' WHERE id=$1`, items[0])
	out := f.owner.must(200, "GET", f.salesRoot()+"/feed-items?status=needs_look&limit=2", nil)
	first := listedIDs(out["data"].([]any))
	if len(first) != 2 || out["next_cursor"] == nil || slices.Contains(first, items[0]) {
		t.Fatalf("page 1: %v", out)
	}
	out = f.owner.must(200, "GET", f.salesRoot()+"/feed-items?status=needs_look&limit=2&cursor="+out["next_cursor"].(string), nil)
	if rest := listedIDs(out["data"].([]any)); !slices.Equal(rest, []string{items[0]}) || out["next_cursor"] != nil {
		t.Fatalf("page 2: %v", out)
	}
}

func TestFeedItemAssignPerson(t *testing.T) {
	f := newFeedFixture(t)
	item, _ := needsLook(t, f)
	me, _ := f.me(t)
	out := f.actItem(t, f.owner, 200, item, map[string]any{"action": "assign_person", "email": " Ana@Acme.example "})
	person := f.person(t, "ana@acme.example")
	if out["status"] != "read" || person == "" || out["record_id"] != person || out["lead_id"] == nil {
		t.Fatalf("item: %v (person %q)", out, person)
	}
	if f.count(t, `SELECT count(*) FROM sales_leads WHERE workspace_id=$1 AND record_id=$2 AND feed_kind='new_person'`, f.ws, person) != 1 {
		t.Fatal("no conversation")
	}
	if f.count(t, `SELECT count(*) FROM sales_feed_items WHERE id=$1 AND resolved_by=$2`, item, me) != 1 {
		t.Fatal("resolved_by not set")
	}
	// The person's history carries the teammate who decided.
	if f.count(t, `SELECT count(*) FROM attribute_changes c JOIN users u ON u.actor_id=c.actor_id WHERE c.record_id=$1 AND c.field_key='companies' AND u.id=$2`, person, me) != 1 {
		t.Fatal("history does not name the teammate")
	}
	var found bool
	for _, e := range f.events() {
		if e.Name == "sales_feed.item_resolved" {
			found = true
			if e.Props["action"] != "assign_person" || e.Props["kind"] != "new_person" || e.Props["feed_channel_id"] == nil || e.ActorType != "human" {
				t.Fatalf("event: %+v", e)
			}
			raw, _ := json.Marshal(e.Props)
			if strings.Contains(string(raw), "acme.example") {
				t.Fatalf("event carries an address: %s", raw)
			}
		}
	}
	if !found {
		t.Fatal("no sales_feed.item_resolved")
	}
	// It has a person now.
	if out := f.actItem(t, f.owner, 409, item, map[string]any{"action": "assign_person", "email": "bo@acme.example"}); errCode(out) != "conflict" {
		t.Fatalf("second assign: %v", out)
	}
	if len(f.feedItems(t, f.owner, "needs_look")) != 0 {
		t.Fatal("still needs a look")
	}
}

func TestFeedItemAssignPersonToCompanyPost(t *testing.T) {
	f := newFeedFixture(t)
	item, _ := seedFeedItem(t, f, "new_person", "Website: https://acme.example")
	f.runItem(t, item)
	row := f.itemRow(t, item)
	if row.Status != "needs_look" || row.RecordID == nil {
		t.Fatalf("company post: %+v", row)
	}
	out := f.actItem(t, f.owner, 200, item, map[string]any{"action": "assign_person", "email": "cy@acme.example"})
	if out["status"] != "read" || out["record_id"] != f.person(t, "cy@acme.example") {
		t.Fatalf("item: %v", out)
	}
}

func TestFeedItemAssignPersonRejectsTeammate(t *testing.T) {
	f := newFeedFixture(t)
	item, _ := needsLook(t, f)
	_, mine := f.me(t)
	f.exec(t, `UPDATE brands SET domains='{ownbrand.example}' WHERE id=$1`, f.brand)
	for _, email := range []string{mine, strings.ToUpper(mine), "sales@ownbrand.example", "not an address", ""} {
		if out := f.actItem(t, f.owner, 400, item, map[string]any{"action": "assign_person", "email": email}); errCode(out) != "invalid_request" {
			t.Fatalf("%q: %v", email, out)
		}
	}
	if f.itemRow(t, item).Status != "needs_look" {
		t.Fatal("changed")
	}
	f.actItem(t, f.owner, 400, item, map[string]any{"action": "bogus"})
}

func TestFeedItemNotSpam(t *testing.T) {
	f := newFeedFixture(t)
	answer := spamAnswer(0.95)
	f.feedAIAnswer(t, &answer)
	item, _ := seedFeedItem(t, f, "new_person", "Email: ana@acme.example")
	f.runItem(t, item)
	if s := f.itemRow(t, item).Status; s != "spam" {
		t.Fatalf("status %s", s)
	}
	if l := f.feedItems(t, f.owner, "spam"); len(l) != 1 || l[0].(map[string]any)["summary"] != "A pitch." {
		t.Fatalf("spam list: %v", l)
	}
	out := f.actItem(t, f.owner, 200, item, map[string]any{"action": "not_spam"})
	person := f.person(t, "ana@acme.example")
	if out["status"] != "read" || person == "" || out["record_id"] != person || out["lead_id"] == nil {
		t.Fatalf("item: %v", out)
	}
	if out := f.actItem(t, f.owner, 409, item, map[string]any{"action": "not_spam"}); errCode(out) != "conflict" {
		t.Fatalf("again: %v", out)
	}
}

func TestFeedItemNotSpamNeedsLook(t *testing.T) {
	f := newFeedFixture(t)
	answer := `{"email":"","name":"","company":"","website":"","message":"","summary":"A pitch.","spam":true,"spam_confidence":0.97}`
	f.feedAIAnswer(t, &answer)
	item, _ := seedFeedItem(t, f, "new_person", "Buy now: ana@acme.example or bo@acme.example")
	f.runItem(t, item)
	if out := f.actItem(t, f.owner, 200, item, map[string]any{"action": "not_spam"}); out["status"] != "needs_look" || out["record_id"] != nil {
		t.Fatalf("item: %v", out)
	}
}

func TestFeedItemIgnore(t *testing.T) {
	f := newFeedFixture(t)
	item, _ := needsLook(t, f)
	if out := f.actItem(t, f.owner, 200, item, map[string]any{"action": "ignore"}); out["status"] != "ignored" {
		t.Fatalf("item: %v", out)
	}
	if len(f.feedItems(t, f.owner, "needs_look")) != 0 {
		t.Fatal("still needs a look")
	}
	if f.count(t, `SELECT count(*) FROM records WHERE workspace_id=$1 AND record_type='person'`, f.ws) != 0 {
		t.Fatal("ignore made a person")
	}
	if out := f.actItem(t, f.owner, 409, item, map[string]any{"action": "ignore"}); errCode(out) != "conflict" {
		t.Fatalf("again: %v", out)
	}
	// A post that was read can't be set aside.
	read, _ := seedFeedItem(t, f, "new_person", "Email: ana@acme.example")
	f.runItem(t, read)
	f.actItem(t, f.owner, 409, read, map[string]any{"action": "ignore"})
}

func TestFeedItemsHiddenFromNonSales(t *testing.T) {
	f := newFeedFixture(t)
	item, _ := needsLook(t, f)
	none, _ := f.salesUser(t, "none@team.example", "")
	if out := none.must(403, "GET", f.salesRoot()+"/feed-items?status=needs_look", nil); errCode(out) != "forbidden" {
		t.Fatalf("list: %v", out)
	}
	none.must(403, "POST", f.salesRoot()+"/feed-items/"+item+"/actions", map[string]any{"action": "ignore"})
	sdr, _ := f.salesUser(t, "sdr@team.example", "sdr")
	if l := f.feedItems(t, sdr, "needs_look"); len(l) != 1 {
		t.Fatalf("sdr list: %v", l)
	}
	f.actItem(t, sdr, 404, "00000000-0000-4000-8000-000000000000", map[string]any{"action": "ignore"})
	f.actItem(t, sdr, 200, item, map[string]any{"action": "ignore"})
}

func TestFeedItemOfOwnedConversationHidden(t *testing.T) {
	f := newFeedFixture(t)
	sdr1, sdr1ID := f.salesUser(t, "sdr1@team.example", "sdr")
	sdr2, sdr2ID := f.salesUser(t, "sdr2@team.example", "sdr")
	sdr3, _ := f.salesUser(t, "sdr3@team.example", "sdr")
	item, _ := seedFeedItem(t, f, "new_person", "Email: ana@acme.example")
	f.runItem(t, item)
	lead := *f.itemRow(t, item).LeadID
	// Taken by one SDR and handed to another; the post is waiting again.
	f.exec(t, `UPDATE sales_leads SET state='active',owner_id=$2,sdr_id=$2,ae_id=$3 WHERE id=$1`, lead, sdr1ID, sdr2ID)
	f.exec(t, `UPDATE sales_feed_items SET status='failed' WHERE id=$1`, item)
	if l := f.feedItems(t, sdr3, "failed"); len(l) != 0 {
		t.Fatalf("third SDR sees it: %v", l)
	}
	f.actItem(t, sdr3, 404, item, map[string]any{"action": "ignore"})
	sdr3.must(404, "GET", f.salesRoot()+"/leads/"+lead, nil)
	for _, c := range []*client{sdr1, sdr2, f.owner} {
		if l := f.feedItems(t, c, "failed"); len(l) != 1 {
			t.Fatalf("visible list: %v", l)
		}
	}
	if out := f.actItem(t, sdr2, 200, item, map[string]any{"action": "ignore"}); out["status"] != "ignored" {
		t.Fatalf("ignore: %v", out)
	}
}

func TestTimelineHasFeedEntries(t *testing.T) {
	f := newFeedFixture(t)
	const secret = "Email: ana@acme.example\nBudget: secret-budget-words"
	read, ch := seedFeedItem(t, f, "new_person", secret)
	f.runItem(t, read)
	company, _ := seedFeedItem(t, f, "context", "Website: https://acme.example secret-company-words")
	f.runItem(t, company)
	spam, _ := seedFeedItem(t, f, "context", "Email: ana@acme.example spam")
	f.runItem(t, spam)
	ignored, _ := seedFeedItem(t, f, "context", "Email: ana@acme.example ignored")
	f.runItem(t, ignored)
	gone, _ := seedFeedItem(t, f, "context", "Email: ana@acme.example gone")
	f.runItem(t, gone)
	f.exec(t, `UPDATE sales_feed_items SET status='spam' WHERE id=$1`, spam)
	f.exec(t, `UPDATE sales_feed_items SET status='ignored' WHERE id=$1`, ignored)
	f.exec(t, `UPDATE sales_feed_items SET deleted_in_slack=true WHERE id=$1`, gone)
	person, acme := f.person(t, "ana@acme.example"), f.company(t, "acme.example")
	if person == "" || acme == "" {
		t.Fatalf("person %q company %q", person, acme)
	}
	timeline := func(rec string) []any {
		t.Helper()
		_, raw := f.owner.do("GET", "/v1/brands/"+f.brand+"/records/"+rec+"/timeline?kinds=feed", nil)
		b, _ := json.Marshal(raw)
		if strings.Contains(string(b), "secret-") || strings.Contains(string(b), "slack.example") {
			t.Fatalf("timeline carries the post: %s", b)
		}
		return raw["data"].([]any)
	}
	got := timeline(person)
	if !slices.Equal(listedIDs(got), []string{read}) {
		t.Fatalf("person timeline: %v", got)
	}
	it := got[0].(map[string]any)
	d := it["detail"].(map[string]any)
	if it["kind"] != "feed" || it["source"] != "slack" || it["title"] == "" || it["record_id"] != person ||
		d["kind"] != "new_person" || d["status"] != "read" || d["feed_channel_id"] != ch || d["lead_id"] != *f.itemRow(t, read).LeadID {
		t.Fatalf("entry: %v", it)
	}
	if ids := listedIDs(timeline(acme)); len(ids) != 2 || !slices.Contains(ids, read) || !slices.Contains(ids, company) {
		t.Fatalf("company timeline: %v", ids)
	}
}

// A post read again for no one after its first conversation was archived
// starts another conversation, whose source can't collide with the first's.
func TestFeedItemArchivedSourceCollision(t *testing.T) {
	f := newFeedFixture(t)
	ch := f.feedChannelOf(t, "new_person", "C1")
	ts := feedTS(time.Hour, 7)
	first, err := f.e.srv.SeedSalesFeedItem(f.ctx, f.ws, f.brand, ch, ts, "Email: ana@acme.example")
	if err != nil {
		t.Fatal(err)
	}
	f.runItem(t, first)
	old := *f.itemRow(t, first).LeadID
	f.exec(t, `UPDATE sales_leads SET state='archived' WHERE id=$1`, old)
	// The same Slack post, stored again.
	f.exec(t, `DELETE FROM sales_feed_items WHERE id=$1`, first)
	again, err := f.e.srv.SeedSalesFeedItem(f.ctx, f.ws, f.brand, ch, ts, "Email: ana@acme.example")
	if err != nil {
		t.Fatal(err)
	}
	f.runItem(t, again)
	r := f.itemRow(t, again)
	if r.Status != "read" || r.LeadID == nil || *r.LeadID == old {
		t.Fatalf("item: %+v", r)
	}
	if f.count(t, `SELECT count(*) FROM sales_leads WHERE workspace_id=$1 AND source='slack' AND source_id LIKE 'C1:'||$2||'%'`, f.ws, ts) != 2 {
		t.Fatal("not two conversations from the post")
	}
}

// A person moved to another brand takes their posts along: they show there,
// and the old brand's reads of the channel neither fail nor store them again.
func TestFeedItemFollowsRecordToOtherBrand(t *testing.T) {
	f := newFeedFixture(t)
	post := slackPost{TS: feedTS(time.Hour, 1), User: "B1", Text: "Contact: ana@acme.example wants a demo"}
	f.slack.set("C1", post)
	ch := f.channel(t, f.brand, f.integ, "C1", 3)
	f.mustPoll(t, ch)
	item := f.item(t, ch, post.TS)
	f.runItem(t, item)
	person, lead := f.person(t, "ana@acme.example"), *f.itemRow(t, item).LeadID
	other := f.owner.must(201, "POST", "/v1/workspaces/"+f.ws+"/brands", map[string]any{"name": "Northpeak"})["id"].(string)
	f.exec(t, `UPDATE records SET brand_id=$2 WHERE id=$1`, person, other)
	if f.count(t, `SELECT count(*) FROM sales_feed_items WHERE id=$1 AND brand_id=$2`, item, other) != 1 {
		t.Fatal("the post did not follow its person")
	}
	detail := f.owner.must(200, "GET", "/v1/brands/"+other+"/sales/leads/"+lead, nil)
	if ids := listedIDs(detail["feed_items"].([]any)); len(ids) != 1 || ids[0] != item {
		t.Fatalf("the post is not on the conversation in its new brand: %v", ids)
	}
	if name := detail["feed_items"].([]any)[0].(map[string]any)["channel_name"]; name != "C1" {
		t.Fatalf("channel name: %v", name)
	}
	if name := detail["lead"].(map[string]any)["feed_channel_name"]; name != "C1" {
		t.Fatalf("conversation's channel name: %v", name)
	}
	_, raw := f.owner.do("GET", "/v1/brands/"+other+"/records/"+person+"/timeline?kinds=feed", nil)
	if ids := listedIDs(raw["data"].([]any)); len(ids) != 1 || ids[0] != item {
		t.Fatalf("timeline in the new brand: %v", ids)
	}
	// The old brand's channel: polled, swept and polled again, cleanly.
	post.Text += " (edited)"
	f.slack.set("C1", post)
	for range 2 {
		f.sweepDue(t, ch)
		f.mustPoll(t, ch)
	}
	if f.code(t, ch) != "" || f.items(t, ch) != 1 {
		t.Fatalf("code %q items %d", f.code(t, ch), f.items(t, ch))
	}
	if c, err := f.e.srv.SalesFeedContent(f.ctx, f.ws, item); err != nil || c.Text == post.Text {
		t.Fatalf("the old brand's sweep changed the other brand's post: %+v %v", c, err)
	}
}

// The conversation page shows the person's and their company's other Slack
// posts too, the ones on no conversation, newest 50, never spam, set aside,
// deleted in Slack or another brand's.
func TestSalesLeadDetailHasRelatedPosts(t *testing.T) {
	f := newFeedFixture(t)
	start, _ := seedFeedItem(t, f, "new_person", "Email: ana@acme.example")
	f.runItem(t, start)
	lead := *f.itemRow(t, start).LeadID
	related := map[string]string{}
	for kind, text := range map[string]string{
		"context": "Note: ana@acme.example asked about invoices",
		"signal":  "Usage alert: ana@acme.example's team is at 95%",
	} {
		id, _ := seedFeedItem(t, f, kind, text)
		f.runItem(t, id)
		related[id] = kind
	}
	company, _ := seedFeedItem(t, f, "new_person", "Someone at https://acme.example asked for pricing")
	f.runItem(t, company)
	if r := f.itemRow(t, company); r.RecordID == nil || *r.RecordID != f.company(t, "acme.example") {
		t.Fatalf("company post: %+v", r)
	}
	related[company] = "new_person"
	for name, set := range map[string]string{"spam": "status='spam'", "ignored": "status='ignored'", "deleted": "deleted_in_slack=true"} {
		id, _ := seedFeedItem(t, f, "context", "Note: ana@acme.example "+name)
		f.runItem(t, id)
		f.exec(t, `UPDATE sales_feed_items SET `+set+` WHERE id=$1`, id)
	}
	// Ben at another company: not about this conversation.
	ben, _ := seedFeedItem(t, f, "context", "Note: ben@birch.example")
	f.runItem(t, ben)
	// Ana in another brand is another person.
	other := f.owner.must(201, "POST", "/v1/workspaces/"+f.ws+"/brands", map[string]any{"name": "Northpeak"})["id"].(string)
	theirCh := f.channel(t, other, f.integ, "C77", 3)
	theirs, err := f.e.srv.SeedSalesFeedItem(f.ctx, f.ws, other, theirCh, feedTS(time.Minute, 99), "Note: ana@acme.example")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.e.srv.RunSalesFeedItem(f.ctx, f.ws, other, theirs, 1); err != nil {
		t.Fatal(err)
	}

	items := f.owner.must(200, "GET", f.salesRoot()+"/leads/"+lead, nil)["feed_items"].([]any)
	got := map[string]map[string]any{}
	for _, x := range items {
		it := x.(map[string]any)
		got[it["id"].(string)] = it
	}
	if len(got) != 1+len(related) || got[start]["lead_id"] != lead {
		t.Fatalf("feed_items: %v", listedIDs(items))
	}
	for id, kind := range related {
		if got[id] == nil || got[id]["lead_id"] != nil || got[id]["kind"] != kind {
			t.Fatalf("related %s (%s): %v", id, kind, got[id])
		}
	}
	// Oldest first, as the timeline reads.
	at := func(i int) time.Time {
		v, _ := time.Parse(time.RFC3339Nano, items[i].(map[string]any)["occurred_at"].(string))
		return v
	}
	for i := 1; i < len(items); i++ {
		if at(i - 1).After(at(i)) {
			t.Fatalf("order: %v", listedIDs(items))
		}
	}
	// Only who may open the conversation sees them.
	sdr, sdrID := f.salesUser(t, "sdr@team.example", "sdr")
	sdr2, _ := f.salesUser(t, "sdr2@team.example", "sdr")
	f.exec(t, `UPDATE sales_leads SET state='active',owner_id=$2,sdr_id=$2 WHERE id=$1`, lead, sdrID)
	sdr2.must(404, "GET", f.salesRoot()+"/leads/"+lead, nil)
	if n := len(sdr.must(200, "GET", f.salesRoot()+"/leads/"+lead, nil)["feed_items"].([]any)); n != 1+len(related) {
		t.Fatalf("owner's view: %d", n)
	}
}

// A teammate who decides a post that joins another rep's conversation gets
// the post back without that conversation's id.
func TestFeedItemResolveHidesOtherRepsConversation(t *testing.T) {
	f := newFeedFixture(t)
	_, sdr1ID := f.salesUser(t, "sdr1@team.example", "sdr")
	sdr2, _ := f.salesUser(t, "sdr2@team.example", "sdr")
	first, _ := seedFeedItem(t, f, "new_person", "Email: ana@acme.example")
	f.runItem(t, first)
	lead := *f.itemRow(t, first).LeadID
	f.exec(t, `UPDATE sales_leads SET state='active',owner_id=$2,sdr_id=$2 WHERE id=$1`, lead, sdr1ID)
	item, _ := needsLook(t, f)
	out := f.actItem(t, sdr2, 200, item, map[string]any{"action": "assign_person", "email": "ana@acme.example"})
	if out["status"] != "read" || out["lead_id"] != nil {
		t.Fatalf("item: %v", out)
	}
	if r := f.itemRow(t, item); r.LeadID == nil || *r.LeadID != lead {
		t.Fatalf("stored: %+v", r)
	}
}

// A post whose job fails on every attempt is marked failed with a code, is
// listed under Failed, and a teammate can still name its person or set it
// aside.
func TestFeedItemFailsOnLastAttempt(t *testing.T) {
	f := newFeedFixture(t)
	f.exec(t, `CREATE FUNCTION test_no_sales_leads() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test: no conversations'; END $$`)
	f.exec(t, `CREATE TRIGGER test_no_sales_leads BEFORE INSERT ON sales_leads FOR EACH ROW EXECUTE FUNCTION test_no_sales_leads()`)
	dropped := false
	drop := func() {
		if !dropped {
			dropped = true
			f.exec(t, `DROP TRIGGER test_no_sales_leads ON sales_leads`)
			f.exec(t, `DROP FUNCTION test_no_sales_leads()`)
		}
	}
	t.Cleanup(drop)
	item, _ := seedFeedItem(t, f, "new_person", "Email: ana@acme.example")
	if err := f.e.srv.RunSalesFeedItem(f.ctx, f.ws, f.brand, item, 1); err == nil {
		t.Fatal("no error to retry")
	}
	if r := f.itemRow(t, item); r.Status != "pending" {
		t.Fatalf("before the last attempt: %+v", r)
	}
	_ = f.e.srv.RunSalesFeedItem(f.ctx, f.ws, f.brand, item, 5)
	if r := f.itemRow(t, item); r.Status != "failed" || r.Code != "sales_feed_item_failed" || r.RecordID != nil {
		t.Fatalf("after the last attempt: %+v", r)
	}
	var ev *track.Event
	for _, e := range f.events() {
		if e.Name == "sales_feed.item_failed" && e.ErrorCode == "sales_feed_item_failed" {
			ev = &e
		}
	}
	if ev == nil || ev.Props["item_id"] != item {
		t.Fatalf("no sales_feed.item_failed event: %+v", ev)
	}
	if b, _ := json.Marshal(ev.Props); strings.Contains(string(b), "test: no conversations") || strings.Contains(string(b), "acme.example") {
		t.Fatalf("event carries the error or the post: %s", b)
	}
	if ids := listedIDs(f.feedItems(t, f.owner, "failed")); len(ids) != 1 || ids[0] != item {
		t.Fatalf("failed list: %v", ids)
	}
	drop()
	out := f.actItem(t, f.owner, 200, item, map[string]any{"action": "assign_person", "email": "ana@acme.example"})
	if out["status"] != "read" || out["lead_id"] == nil {
		t.Fatalf("assign on a failed post: %v", out)
	}
}
