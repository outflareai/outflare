package api_test

import (
	"testing"
	"time"
)

// A person a Sales channel brings in has Source Inbound and the channel as
// their Sub-source, and every person's lead source category follows their
// Source: the Sales queue filters by it, and a corrected Source moves it,
// in the person's history.
func TestSalesLeadSourceCategory(t *testing.T) {
	f := newFeedFixture(t)
	form := slackPost{TS: feedTS(time.Hour, 1), App: "Form Tool", Text: "First name: Ana\nWork email: ana@acme.example"}
	f.slack.set("C1", form)
	ch := f.channel(t, f.brand, f.integ, "C1", 3)
	f.mustPoll(t, ch)
	f.runItem(t, f.item(t, ch, form.TS))
	person := f.person(t, "ana@acme.example")
	if f.attr(t, person, "source") != "inbound" || f.attr(t, person, "source_category") != "inbound" || f.attr(t, person, "sub_source") == nil {
		t.Fatalf("source %v, category %v, sub-source %v", f.attr(t, person, "source"), f.attr(t, person, "source_category"), f.attr(t, person, "sub_source"))
	}
	subs := f.owner.must(200, "GET", "/v1/brands/"+f.brand+"/sub-sources?source=inbound", nil)["data"].([]any)
	if len(subs) != 1 || subs[0].(map[string]any)["key"] != f.attr(t, person, "sub_source") || subs[0].(map[string]any)["people"] != float64(1) {
		t.Fatalf("sub-sources: %v", subs)
	}
	list := func(query string) []any {
		t.Helper()
		return f.owner.must(200, "GET", f.salesRoot()+"/leads?queue=all"+query, nil)["data"].([]any)
	}
	if got := list("&source_category=inbound"); len(got) != 1 || got[0].(map[string]any)["source_category"] != "inbound" {
		t.Fatalf("inbound: %v", got)
	}
	if n := len(list("&source_category=outbound")); n != 0 {
		t.Fatalf("outbound: %d", n)
	}
	f.owner.must(400, "GET", f.salesRoot()+"/leads?queue=all&source_category=sideways", nil)

	// A second post about them changes nothing: Source is set once.
	again := slackPost{TS: feedTS(0, 2), App: "Form Tool", Text: "Work email: ana@acme.example\nNote: again"}
	f.slack.set("C1", form, again)
	f.mustPoll(t, ch)
	f.runItem(t, f.item(t, ch, again.TS))
	if n := f.count(t, `SELECT count(*) FROM attribute_changes WHERE record_id=$1 AND field_key='source'`, person); n != 2 {
		t.Fatalf("source changes: %d", n)
	}

	// The category follows a corrected Source, and has none for a Source
	// that is neither.
	f.exec(t, `UPDATE records SET attributes=attributes||'{"source":"event"}' WHERE id=$1`, person)
	if got := f.attr(t, person, "source_category"); got != "outbound" {
		t.Fatalf("after Events: %v", got)
	}
	if n := len(list("&source_category=outbound")); n != 1 {
		t.Fatalf("outbound after Events: %d", n)
	}
	f.exec(t, `UPDATE records SET attributes=attributes||'{"source":"customer"}' WHERE id=$1`, person)
	if got := f.attr(t, person, "source_category"); got != nil {
		t.Fatalf("after Customer: %v", got)
	}
	if l := list("")[0].(map[string]any); l["source_category"] != nil {
		t.Fatalf("lead after Customer: %v", l["source_category"])
	}
	if n := f.count(t, `SELECT count(*) FROM attribute_changes WHERE record_id=$1 AND field_key='source_category'`, person); n != 3 {
		t.Fatalf("category changes: %d", n)
	}
}

// A channel's first read looks back day by day and ends at now. It leaves
// the cursor a minute behind, so a post Slack shows a moment late, or one
// that lands in the same second, is in the next read.
func TestSalesFeedFirstReadKeepsOverlap(t *testing.T) {
	f := newFeedFixture(t)
	f.slack.set("C1", slackPost{TS: feedTS(time.Hour, 1), App: "Form Tool", Text: "Work email: ana@acme.example"})
	ch := f.channel(t, f.brand, f.integ, "C1", 3)
	f.mustPoll(t, ch)
	var cursor string
	if err := f.e.pool.QueryRow(f.ctx, `SELECT cursor_ts FROM sales_feed_channels WHERE id=$1`, ch).Scan(&cursor); err != nil {
		t.Fatal(err)
	}
	if latest := feedTS(30*time.Second, 0); cursor > latest {
		t.Fatalf("cursor %s is less than 30 seconds behind now (%s)", cursor, latest)
	}
}
