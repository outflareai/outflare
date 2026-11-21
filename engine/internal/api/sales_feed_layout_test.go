package api_test

import (
	"testing"
	"time"
)

// A channel's team chooses which of its posts' fields show first. The queue
// row and the conversation page then lead with those, in that order, and a
// form row no longer repeats what the form is in a sentence.
func TestSalesFeedLayout(t *testing.T) {
	f := newFeedFixture(t)
	full := slackPost{TS: feedTS(time.Hour, 1), App: "Form Tool", Text: "First name: Ana\nWork email: ana@acme.example\nExpected monthly traffic: 1TB+\nPreferred contact: Chat\nUse case: Price monitoring for our marketplace in ten countries"}
	bare := slackPost{TS: feedTS(time.Minute*50, 2), App: "Form Tool", Text: "First name: Ben\nWork email: ben@globex.example\nExpected monthly traffic: 100GB\nPreferred contact: Email"}
	f.slack.set("C1", full, bare)
	ch := f.channel(t, f.brand, f.integ, "C1", 3)
	f.mustPoll(t, ch)
	for _, p := range []slackPost{full, bare} {
		f.runItem(t, f.item(t, ch, p.TS))
	}
	rep, _ := f.salesUser(t, "rep@outflare.example", "sdr")
	outsider, _ := f.salesUser(t, "ops@outflare.example", "")
	post := func(email string) map[string]any {
		t.Helper()
		for _, l := range rep.must(200, "GET", f.salesRoot()+"/leads?queue=new", nil)["data"].([]any) {
			if l := l.(map[string]any); l["email"] == email {
				return l["latest_post"].(map[string]any)
			}
		}
		t.Fatalf("no conversation for %s", email)
		return nil
	}
	pairs := func(v any) [][2]string {
		var out [][2]string
		for _, x := range v.([]any) {
			x := x.(map[string]any)
			out = append(out, [2]string{x["label"].(string), x["value"].(string)})
		}
		return out
	}
	same := func(got [][2]string, want ...[2]string) bool {
		if len(got) != len(want) {
			return false
		}
		for i := range got {
			if got[i] != want[i] {
				return false
			}
		}
		return true
	}

	// With nothing chosen: the first three answers that are not about who the
	// person is. What they wrote is one of them, so it is not repeated.
	p := post("ana@acme.example")
	if !same(pairs(p["fields"]), [2]string{"Expected monthly traffic", "1TB+"}, [2]string{"Preferred contact", "Chat"}, [2]string{"Use case", "Price monitoring for our marketplace in ten countries"}) || p["body"] != "" {
		t.Fatalf("default fields: %v", p)
	}

	// The labels on offer are the ones the channel's posts carry, the most
	// common first.
	path := f.salesRoot() + "/feeds/" + ch
	offered := rep.must(200, "GET", path+"/fields", nil)
	labels := offered["data"].([]any)
	if len(labels) != 5 || labels[0].(map[string]any)["posts"] != float64(2) || labels[4].(map[string]any)["label"] != "Use case" || labels[4].(map[string]any)["posts"] != float64(1) || len(offered["preview_fields"].([]any)) != 0 {
		t.Fatalf("labels: %v", offered)
	}

	// A rep saves the layout; someone off the sales desk may not, and neither
	// may anyone for a channel that is not the brand's.
	outsider.must(403, "PUT", path+"/layout", map[string]any{"preview_fields": []string{"Use case"}})
	outsider.must(403, "GET", path+"/fields", nil)
	rep.must(404, "PUT", f.salesRoot()+"/feeds/00000000-0000-4000-8000-000000000000/layout", map[string]any{"preview_fields": []string{"Use case"}})
	rep.must(400, "PUT", path+"/layout", map[string]any{"preview_fields": []string{"Use case", "use case:"}})
	rep.must(400, "PUT", path+"/layout", map[string]any{"preview_fields": []string{"a", "b", "c", "d", "e", "f", "g"}})
	saved := rep.must(200, "PUT", path+"/layout", map[string]any{"preview_fields": []string{"use case", "Expected monthly traffic"}})
	if len(saved["preview_fields"].([]any)) != 2 || !f.hasEvent("sales_feed.layout_updated") {
		t.Fatalf("saved: %v", saved)
	}
	channels := f.owner.must(200, "GET", f.feedsPath(), nil)["channels"].([]any)
	if got := channels[0].(map[string]any)["preview_fields"].([]any); len(got) != 2 || got[0] != "use case" {
		t.Fatalf("channel layout: %v", got)
	}

	// The chosen fields lead, in the chosen order, whatever the label's case;
	// one the post lacks is there with no value.
	p = post("ana@acme.example")
	if !same(pairs(p["fields"]), [2]string{"use case", "Price monitoring for our marketplace in ten countries"}, [2]string{"Expected monthly traffic", "1TB+"}) || p["body"] != "" {
		t.Fatalf("chosen fields: %v", p)
	}
	p = post("ben@globex.example")
	if !same(pairs(p["fields"]), [2]string{"use case", ""}, [2]string{"Expected monthly traffic", "100GB"}) || p["body"] != "" {
		t.Fatalf("missing field: %v", p)
	}

	// The conversation page gets the same fields on the post.
	var lead string
	for _, l := range rep.must(200, "GET", f.salesRoot()+"/leads?queue=new", nil)["data"].([]any) {
		if l := l.(map[string]any); l["email"] == "ana@acme.example" {
			lead = l["id"].(string)
		}
	}
	items := rep.must(200, "GET", f.salesRoot()+"/leads/"+lead, nil)["feed_items"].([]any)
	if len(items) != 1 || !same(pairs(items[0].(map[string]any)["key_fields"]), [2]string{"use case", "Price monitoring for our marketplace in ten countries"}, [2]string{"Expected monthly traffic", "1TB+"}) {
		t.Fatalf("post key fields: %v", items)
	}

	// An empty list hands the choice back to Outflare.
	rep.must(200, "PUT", path+"/layout", map[string]any{"preview_fields": []string{}})
	if p = post("ben@globex.example"); !same(pairs(p["fields"]), [2]string{"Expected monthly traffic", "100GB"}, [2]string{"Preferred contact", "Email"}) {
		t.Fatalf("reset fields: %v", p)
	}
}

// A post with no fields keeps a line of text in the queue; one with fields
// shows the person's own words under them only when they add something.
func TestSalesFeedQueueBody(t *testing.T) {
	f := newFeedFixture(t)
	typed := slackPost{TS: feedTS(time.Hour, 1), User: "U1", Text: "Please look at cy@initech.example"}
	form := slackPost{TS: feedTS(time.Minute*30, 2), App: "Form Tool", Text: "Work email: dee@umbrella.example\nExpected monthly traffic: 1TB+\nContact owner: Sam"}
	f.slack.set("C1", typed, form)
	ch := f.channel(t, f.brand, f.integ, "C1", 3)
	f.mustPoll(t, ch)
	f.runItem(t, f.item(t, ch, typed.TS))
	item := f.item(t, ch, form.TS)
	f.runItem(t, item)
	// A reading that only says the form's fields again.
	if err := f.e.srv.SetSalesFeedReading(f.ctx, f.ws, item, "Expected monthly traffic: 1TB+. Contact owner: Sam", "A form"); err != nil {
		t.Fatal(err)
	}
	posts := map[string]map[string]any{}
	for _, l := range f.owner.must(200, "GET", f.salesRoot()+"/leads?queue=new", nil)["data"].([]any) {
		l := l.(map[string]any)
		posts[l["email"].(string)] = l["latest_post"].(map[string]any)
	}
	if p := posts["cy@initech.example"]; p == nil || len(p["fields"].([]any)) != 0 || p["body"] != "Please look at cy@initech.example" {
		t.Fatalf("free text post: %v", p)
	}
	// The form shows its field and not the reading that repeats it. Who owns
	// it is on the row already, so that is not one of the fields.
	p := posts["dee@umbrella.example"]
	if p == nil {
		t.Fatal("no conversation for the form")
	}
	fields := p["fields"].([]any)
	if len(fields) != 1 || fields[0].(map[string]any)["label"] != "Expected monthly traffic" || p["body"] != "" {
		t.Fatalf("restated form: %v", p)
	}
}

// A rep marks how good a conversation was. The mark is on the conversation,
// in its history, filters the queue, and is counted per source.
func TestSalesLeadQuality(t *testing.T) {
	f := newFeedFixture(t)
	form := slackPost{TS: feedTS(time.Hour, 1), App: "Form Tool", Text: "Work email: ana@acme.example\nUse case: Crawling"}
	other := slackPost{TS: feedTS(time.Minute*50, 2), App: "Form Tool", Text: "Work email: ben@globex.example\nUse case: Testing"}
	crm := slackPost{TS: feedTS(time.Minute*40, 3), App: "CRM Tool", Text: "New contact: cy@initech.example"}
	f.slack.set("C1", form, other, crm)
	ch := f.channel(t, f.brand, f.integ, "C1", 3)
	f.mustPoll(t, ch)
	for _, p := range []slackPost{form, other, crm} {
		f.runItem(t, f.item(t, ch, p.TS))
	}
	rep, _ := f.salesUser(t, "rep@outflare.example", "sdr")
	outsider, _ := f.salesUser(t, "ops@outflare.example", "")
	list := func(query string) []any {
		t.Helper()
		return rep.must(200, "GET", f.salesRoot()+"/leads?queue=new"+query, nil)["data"].([]any)
	}
	ids := map[string]string{}
	for _, l := range list("") {
		l := l.(map[string]any)
		if l["quality"] != nil {
			t.Fatalf("a new conversation has no mark: %v", l["quality"])
		}
		ids[l["email"].(string)] = l["id"].(string)
	}
	rate := func(c *client, want int, email, quality string) map[string]any {
		t.Helper()
		return c.must(want, "POST", f.salesRoot()+"/leads/"+ids[email]+"/actions", map[string]any{"action": "rate", "quality": quality, "version": 1})
	}
	before := list("")[0].(map[string]any)
	if got := rate(rep, 200, "ana@acme.example", "junk"); got["quality"] != "junk" {
		t.Fatalf("rated: %v", got["quality"])
	}
	rate(rep, 200, "ben@globex.example", "good")
	rate(rep, 400, "cy@initech.example", "great")
	rate(outsider, 404, "cy@initech.example", "good")
	if !f.hasEvent("sales.quality_set") {
		t.Fatal("no sales.quality_set event")
	}
	// A mark does not move the conversation in its list or change its version.
	after := list("")[0].(map[string]any)
	if after["id"] != before["id"] || after["version"] != before["version"] {
		t.Fatalf("the list moved: %v then %v", before["id"], after["id"])
	}
	if got := list("&quality=junk"); len(got) != 1 || got[0].(map[string]any)["email"] != "ana@acme.example" {
		t.Fatalf("quality=junk: %v", got)
	}
	if got := list("&quality=unrated"); len(got) != 1 || got[0].(map[string]any)["email"] != "cy@initech.example" {
		t.Fatalf("quality=unrated: %v", got)
	}
	rep.must(400, "GET", f.salesRoot()+"/leads?queue=new&quality=fine", nil)

	tally := map[string]map[string]any{}
	for _, v := range f.owner.must(200, "GET", f.feedsPath(), nil)["source_quality"].([]any) {
		v := v.(map[string]any)
		tally[v["source"].(string)] = v
	}
	if v := tally["Form Tool"]; v["total"] != float64(2) || v["good"] != float64(1) || v["junk"] != float64(1) || v["poor_fit"] != float64(0) || v["reply"] != false {
		t.Fatalf("Form Tool tally: %v", v)
	}
	if v := tally["CRM Tool"]; v["total"] != float64(1) || v["good"] != float64(0) {
		t.Fatalf("CRM Tool tally: %v", v)
	}

	// Clearing the mark puts the conversation back among the unrated.
	if got := rate(rep, 200, "ana@acme.example", ""); got["quality"] != nil {
		t.Fatalf("cleared: %v", got["quality"])
	}
	if n := len(list("&quality=unrated")); n != 2 {
		t.Fatalf("unrated after clearing: %d", n)
	}
}

// New conversations can be narrowed to any teammate's, and the list says how
// many each teammate holds, so the team sees how they are shared out.
func TestSalesNewAssignedTo(t *testing.T) {
	f := newFeedFixture(t)
	posts := []slackPost{
		{TS: feedTS(time.Hour, 1), App: "Form Tool", Text: "Work email: ana@acme.example"},
		{TS: feedTS(time.Minute*50, 2), App: "Form Tool", Text: "Work email: ben@globex.example"},
		{TS: feedTS(time.Minute*40, 3), App: "Form Tool", Text: "Work email: cy@initech.example"},
	}
	f.slack.set("C1", posts...)
	ch := f.channel(t, f.brand, f.integ, "C1", 3)
	f.mustPoll(t, ch)
	for _, p := range posts {
		f.runItem(t, f.item(t, ch, p.TS))
	}
	rep, repID := f.salesUser(t, "rep@outflare.example", "sdr")
	other, otherID := f.salesUser(t, "rep2@outflare.example", "sdr")
	for _, l := range rep.must(200, "GET", f.salesRoot()+"/leads?queue=new", nil)["data"].([]any) {
		l := l.(map[string]any)
		to := repID
		if l["email"] == "cy@initech.example" {
			to = otherID
		}
		rep.must(200, "POST", f.salesRoot()+"/leads/"+l["id"].(string)+"/actions", map[string]any{"action": "assign", "version": l["version"], "assignee_id": to})
	}
	// One rep looks at the other's share.
	out := other.must(200, "GET", f.salesRoot()+"/leads?queue=new&person="+repID, nil)
	if n := len(out["data"].([]any)); n != 2 {
		t.Fatalf("assigned to the first rep: %d", n)
	}
	owners := out["new_owners"].([]any)
	if len(owners) != 2 || owners[0].(map[string]any)["owner_id"] != repID || owners[0].(map[string]any)["count"] != float64(2) || owners[1].(map[string]any)["owner_id"] != otherID || owners[1].(map[string]any)["count"] != float64(1) {
		t.Fatalf("new_owners: %v", owners)
	}
	// Since the last conversation a rep looked at: all three before any, none
	// right after looking at one, and one more when another arrives.
	fresh, _ := f.salesUser(t, "rep3@outflare.example", "sdr")
	if got := fresh.must(200, "GET", f.salesRoot()+"/leads?queue=new", nil); got["new_since_seen"] != float64(3) || got["last_seen_at"] != nil {
		t.Fatalf("before looking: %v %v", got["new_since_seen"], got["last_seen_at"])
	}
	first := out["data"].([]any)[0].(map[string]any)
	fresh.must(200, "POST", f.salesRoot()+"/leads/"+first["id"].(string)+"/actions", map[string]any{"action": "read", "version": first["version"]})
	if got := fresh.must(200, "GET", f.salesRoot()+"/leads?queue=new", nil); got["new_since_seen"] != float64(0) || got["last_seen_at"] == nil {
		t.Fatalf("after looking: %v %v", got["new_since_seen"], got["last_seen_at"])
	}
	late := slackPost{TS: feedTS(0, 9), App: "Form Tool", Text: "Work email: dee@umbrella.example"}
	f.slack.set("C1", append(posts, late)...)
	f.mustPoll(t, ch)
	f.runItem(t, f.item(t, ch, late.TS))
	if got := fresh.must(200, "GET", f.salesRoot()+"/leads?queue=new", nil); got["new_since_seen"] != float64(1) {
		t.Fatalf("after another arrived: %v", got["new_since_seen"])
	}
	if got := rep.must(200, "GET", f.salesRoot()+"/leads?queue=mine", nil)["new_owners"].([]any); len(got) != 0 {
		t.Fatalf("new_owners outside New: %v", got)
	}
}
