package api_test

import (
	"strings"
	"testing"
)

// A reply can link to the help centre: the articles found from what the
// customer wrote, or a search, each with its page on the help centre.
// Only what the help centre shows is offered.
func TestSupportArticles(t *testing.T) {
	e := freshEnv(t)
	owner, _ := setupOwner(t, e.h)
	ws := owner.must(201, "POST", "/v1/workspaces", map[string]any{"name": "Help desk", "test": true})["id"].(string)
	base := "/v1/workspaces/" + ws
	kb := base + "/knowledge"
	refunds := owner.must(201, "POST", kb+"/items", map[string]any{"title": "Refunds and cancellations", "visibility": "public",
		"body": "We refund any plan within 30 days of a charge."})
	owner.must(201, "POST", kb+"/items", map[string]any{"title": "Refund exceptions", "visibility": "team", "body": "Refund annual plans pro rata."})
	owner.must(201, "POST", kb+"/items", map[string]any{"title": "Refund wording", "visibility": "customers_ai", "body": "Say refund, not reimbursement."})
	owner.must(201, "POST", kb+"/items", map[string]any{"title": "Refund draft", "visibility": "public", "status": "draft", "body": "Refund soon."})

	if code, res := owner.raw("POST", base+"/mailboxes/import", "text/csv",
		"email,password,smtp_host,smtp_port,imap_host,imap_port\nhelp@northpeak.example,pw,smtp.northpeak.example,465,imap.northpeak.example,993\n"); code != 200 || res["created"] != 1.0 {
		t.Fatalf("import: %d %v", code, res)
	}
	box := owner.must(200, "GET", base+"/mailboxes", nil)["data"].([]any)[0].(map[string]any)["id"].(string)
	owner.must(200, "PUT", base+"/support/mailboxes/"+box, nil)
	conv := owner.must(201, "POST", base+"/test/support-email", map[string]any{"mailbox_id": box, "from": "ana@acme.example",
		"subject": "Refund", "body": "Can I get a refund for this month's charge?"})["conversation_id"].(string)
	cb := base + "/support/conversations/" + conv

	// No help centre yet: the articles show, with nowhere to link.
	l := owner.must(200, "GET", cb+"/articles", nil)
	data := l["data"].([]any)
	if l["suggested"] != true || l["help_centre"] != nil || len(data) != 1 {
		t.Fatalf("no help centre: %v", l)
	}
	if a := data[0].(map[string]any); a["id"] != refunds["id"] || a["url"] != nil || a["title"] != "Refunds and cancellations" {
		t.Fatalf("article: %v", a)
	}

	// Once the help centre is live, each links to its page.
	owner.must(201, "POST", base+"/help-centres", map[string]any{"enabled": true})
	l = owner.must(200, "GET", cb+"/articles", nil)
	a := l["data"].([]any)[0].(map[string]any)
	ref := strings.ReplaceAll(refunds["id"].(string), "-", "")[:8] + "-refunds-and-cancellations"
	if l["help_centre"] == nil || !strings.HasSuffix(a["url"].(string), "/articles/"+ref) {
		t.Fatalf("linked: %v", l)
	}
	if rec := helpGet(t, e.h, "GET", strings.TrimPrefix(a["url"].(string), "http://outflare.test"), "", ""); rec.Code != 200 {
		t.Fatalf("article page %s = %d", a["url"], rec.Code)
	}

	// A search finds by its words; nothing matching is an empty list.
	if l := owner.must(200, "GET", cb+"/articles?q=cancel", nil); l["suggested"] != false || len(l["data"].([]any)) != 1 {
		t.Fatalf("search: %v", l)
	}
	if l := owner.must(200, "GET", cb+"/articles?q=zebra", nil); len(l["data"].([]any)) != 0 {
		t.Fatalf("no match: %v", l)
	}
	owner.must(404, "GET", base+"/support/conversations/00000000-0000-0000-0000-000000000000/articles", nil)
}
