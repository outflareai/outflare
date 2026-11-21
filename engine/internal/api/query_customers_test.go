package api_test

import (
	"strings"
	"testing"
)

// Record history, sales, support and partners in Query: one brand at a time,
// support only with inbox.read, and no secrets or bodies.
func TestQueryCustomerData(t *testing.T) {
	h := freshServer(t)
	owner, ws := setupOwner(t, h)
	base := "/v1/workspaces/" + ws
	np := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "handle": "northpeak"})["id"].(string)
	acme := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Acme", "handle": "acme"})["id"].(string)
	ada := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Ada", "brand_id": np})["id"].(string)
	owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Bea", "brand_id": acme})
	owner.must(200, "PATCH", base+"/records/"+ada, map[string]any{"name": "Ada L"})

	// A record's field history, this brand's only.
	out := owner.must(200, "POST", "/v1/brands/"+np+"/query", map[string]any{
		"sql": "select count(*) filter (where record_id = '" + ada + "'), count(*) from attribute_changes",
	})
	if rows := queryRows(t, out); rows[0][0] == 0.0 || rows[0][0] != rows[0][1] {
		t.Fatalf("attribute_changes: %v", out)
	}

	for _, sql := range []string{
		"select body from support_messages",
		"select chat_token_hash from support_conversations",
		"select postback_url from partner_memberships",
		"select payout_tokens from partner_programs",
		"select details_sealed from sales_leads",
		"select snapshot from record_merges",
	} {
		code, got := owner.do("POST", "/v1/brands/"+np+"/query", map[string]any{"sql": sql})
		if code != 400 || !strings.Contains(strings.ToLower(got["error"].(map[string]any)["message"].(string)), "does not exist") {
			t.Fatalf("%s: %d %v", sql, code, got)
		}
	}

	// Support needs inbox.read; record history needs customers.read only.
	tok := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "CRM agent", "scopes": []string{"customers.read"}, "brand": "northpeak"})
	agent := &client{t: t, h: h, bearer: tok["token"].(string)}
	if code, _ := agent.do("POST", "/v1/brands/"+np+"/query", map[string]any{"sql": "select count(*) from support_conversations"}); code == 200 {
		t.Fatalf("support_conversations read without inbox.read")
	}
	agent.must(200, "POST", "/v1/brands/"+np+"/query", map[string]any{"sql": "select count(*) from attribute_changes"})
}
