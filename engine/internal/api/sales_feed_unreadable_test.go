package api_test

import (
	"slices"
	"testing"

	"github.com/outflareai/outflare/engine/internal/track"
)

// unreadable overwrites a stored post's sealed content with bytes that can't
// be opened.
func (f *feedFixture) unreadable(t *testing.T, item string) {
	t.Helper()
	f.exec(t, `UPDATE sales_feed_items SET content_sealed='\x00' WHERE id=$1`, item)
}

// openFailures are the sales_feed.item_open_failed events, by item id.
func (f *feedFixture) openFailures(t *testing.T) []string {
	t.Helper()
	var ids []string
	for _, e := range f.events() {
		if e.Name != "sales_feed.item_open_failed" {
			continue
		}
		if e.Outcome != track.Error || e.ErrorCode != "internal" || e.WorkspaceID != f.ws || e.Props["brand_id"] != f.brand || e.Props["feed_channel_id"] == nil {
			t.Fatalf("event: %+v", e)
		}
		ids = append(ids, e.Props["item_id"].(string))
	}
	slices.Sort(ids)
	return ids
}

// A post that can't be opened is left off the conversation page, which
// still loads with the others: also one on the company's record, which
// every conversation with someone there shows.
func TestSalesLeadDetailSkipsUnreadablePost(t *testing.T) {
	f := newFeedFixture(t)
	start, _ := seedFeedItem(t, f, "new_person", "Email: ana@acme.example")
	f.runItem(t, start)
	lead := *f.itemRow(t, start).LeadID
	second, _ := seedFeedItem(t, f, "handover", "Hand over: ana@acme.example is ready")
	f.runItem(t, second)
	company, _ := seedFeedItem(t, f, "new_person", "Someone at https://acme.example asked for pricing")
	f.runItem(t, company)
	if n := len(f.owner.must(200, "GET", f.salesRoot()+"/leads/"+lead, nil)["feed_items"].([]any)); n != 3 {
		t.Fatalf("feed_items before: %d", n)
	}
	f.unreadable(t, second)
	f.unreadable(t, company)

	items := f.owner.must(200, "GET", f.salesRoot()+"/leads/"+lead, nil)["feed_items"].([]any)
	if ids := listedIDs(items); !slices.Equal(ids, []string{start}) {
		t.Fatalf("feed_items: %v", ids)
	}
	want := []string{second, company}
	slices.Sort(want)
	if got := f.openFailures(t); !slices.Equal(got, want) {
		t.Fatalf("events for %v, want %v", got, want)
	}
}

// The list of posts waiting for a teammate leaves out one that can't be
// opened, and still pages through the rest.
func TestFeedItemsSkipUnreadablePost(t *testing.T) {
	f := newFeedFixture(t)
	var items []string
	for range 3 {
		item, _ := needsLook(t, f)
		items = append(items, item)
	}
	// Oldest first: items[2] is the newest, listed first.
	f.exec(t, `UPDATE sales_feed_items SET occurred_at=now()-interval '3 hours' WHERE id=$1`, items[0])
	f.exec(t, `UPDATE sales_feed_items SET occurred_at=now()-interval '2 hours' WHERE id=$1`, items[1])
	f.exec(t, `UPDATE sales_feed_items SET occurred_at=now()-interval '1 hour' WHERE id=$1`, items[2])
	f.unreadable(t, items[2])

	if ids := listedIDs(f.feedItems(t, f.owner, "needs_look")); !slices.Equal(ids, []string{items[1], items[0]}) {
		t.Fatalf("list: %v", ids)
	}
	if got := f.openFailures(t); !slices.Equal(got, []string{items[2]}) {
		t.Fatalf("events: %v", got)
	}
	// A page of two whose first post is unreadable shows one, and the next
	// page carries on after it.
	out := f.owner.must(200, "GET", f.salesRoot()+"/feed-items?status=needs_look&limit=2", nil)
	if ids := listedIDs(out["data"].([]any)); !slices.Equal(ids, []string{items[1]}) || out["next_cursor"] != items[1] {
		t.Fatalf("page 1: %v", out)
	}
	out = f.owner.must(200, "GET", f.salesRoot()+"/feed-items?status=needs_look&limit=2&cursor="+items[1], nil)
	if ids := listedIDs(out["data"].([]any)); !slices.Equal(ids, []string{items[0]}) || out["next_cursor"] != nil {
		t.Fatalf("page 2: %v", out)
	}
}
