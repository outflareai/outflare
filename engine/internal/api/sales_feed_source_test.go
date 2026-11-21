package api_test

import (
	"net/url"
	"testing"
	"time"
)

// A conversation carries the app that posted the Slack post which started
// it. The queue filters by that app and by whether a conversation came from
// a channel at all, the settings list the apps, and the list counts the new
// conversations nobody owns. A person's post has no source.
func TestSalesLeadSourceFromPostingApp(t *testing.T) {
	f := newFeedFixture(t)
	form := slackPost{TS: feedTS(time.Hour, 1), App: "Form Tool", Text: "First name: Ana\nWork email: ana@acme.example\nWhat is your goal?: We need 500GB for a crawler"}
	crm := slackPost{TS: feedTS(time.Minute*50, 2), App: "CRM Tool", Text: "New contact: ben@globex.example"}
	typed := slackPost{TS: feedTS(time.Minute*40, 3), User: "U1", Text: "Please look at cy@initech.example"}
	f.slack.set("C1", form, crm, typed)
	ch := f.channel(t, f.brand, f.integ, "C1", 3)
	f.mustPoll(t, ch)
	for _, p := range []slackPost{form, crm, typed} {
		f.runItem(t, f.item(t, ch, p.TS))
	}
	list := func(query string) []any {
		t.Helper()
		return f.owner.must(200, "GET", "/v1/brands/"+f.brand+"/sales/leads?queue=all"+query, nil)["data"].([]any)
	}
	sources := map[string]any{}
	for _, l := range list("") {
		l := l.(map[string]any)
		sources[l["email"].(string)] = l["feed_source"]
	}
	if len(sources) != 3 || sources["ana@acme.example"] != "Form Tool" || sources["ben@globex.example"] != "CRM Tool" || sources["cy@initech.example"] != nil {
		t.Fatalf("sources: %v", sources)
	}
	only := list("&source=" + url.QueryEscape("Form Tool"))
	if len(only) != 1 || only[0].(map[string]any)["email"] != "ana@acme.example" {
		t.Fatalf("source filter: %v", only)
	}
	// The row leads with the form's answer, not its first lines (the name
	// and address the row already shows), and does not repeat it as text.
	p, _ := only[0].(map[string]any)["latest_post"].(map[string]any)
	fields, _ := p["fields"].([]any)
	if len(fields) != 1 || fields[0].(map[string]any)["label"] != "What is your goal?" || fields[0].(map[string]any)["value"] != "We need 500GB for a crawler" || p["body"] != "" {
		t.Fatalf("preview: %v", p)
	}
	// Several apps at once, and apps together with email replies.
	if n := len(list("&source=" + url.QueryEscape("Form Tool") + "&source=" + url.QueryEscape("CRM Tool"))); n != 2 {
		t.Fatalf("two sources: %d", n)
	}
	if n := len(list("&arrived=reply&source=" + url.QueryEscape("CRM Tool"))); n != 1 {
		t.Fatalf("a source or a reply: %d", n)
	}
	if n := len(list("&feed_channel=" + ch + "&feed_channel=00000000-0000-4000-8000-000000000000")); n != 3 {
		t.Fatalf("two channels: %d", n)
	}
	if n := len(list("&feed_channel=00000000-0000-4000-8000-000000000000")); n != 0 {
		t.Fatalf("another channel: %d", n)
	}
	f.owner.must(400, "GET", "/v1/brands/"+f.brand+"/sales/leads?queue=all&feed_channel=nope", nil)
	if n := len(list("&arrived=channel")); n != 3 {
		t.Fatalf("arrived=channel: %d", n)
	}
	if n := len(list("&arrived=reply")); n != 0 {
		t.Fatalf("arrived=reply: %d", n)
	}
	f.owner.must(400, "GET", "/v1/brands/"+f.brand+"/sales/leads?queue=all&arrived=elsewhere", nil)

	out := f.owner.must(200, "GET", "/v1/brands/"+f.brand+"/sales/leads?queue=new", nil)
	if out["new_count"] != float64(3) {
		t.Fatalf("new_count: %v", out["new_count"])
	}
	names := f.owner.must(200, "GET", f.feedsPath(), nil)["sources"].([]any)
	if len(names) != 2 || names[0] != "CRM Tool" || names[1] != "Form Tool" {
		t.Fatalf("settings sources: %v", names)
	}

	// The post itself says which app wrote it, on the conversation page.
	lead := only[0].(map[string]any)["id"].(string)
	detail := f.owner.must(200, "GET", "/v1/brands/"+f.brand+"/sales/leads/"+lead, nil)
	items := detail["feed_items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["source"] != "Form Tool" {
		t.Fatalf("post source: %v", items)
	}
}
