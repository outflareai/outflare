package ads

import (
	"strings"
	"testing"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

func TestXAdsLineItemsAndAudiences(t *testing.T) {
	cl, reqs, _ := fake(t, map[string]any{
		"GET /12/accounts/18ce5/line_items": map[string]any{"data": []any{
			map[string]any{"id": "l1", "campaign_id": "k1", "name": "Developers", "entity_status": "ACTIVE"},
			map[string]any{"id": "l2", "campaign_id": "k1", "name": "", "entity_status": "PAUSED", "deleted": true}}},
		"GET /12/stats/accounts/18ce5": map[string]any{"data": []any{map[string]any{"id": "l1", "id_data": []any{map[string]any{"metrics": map[string]any{
			"billed_charge_local_micro": []any{5_000_000, nil}, "impressions": []any{900, nil}, "clicks": []any{12, nil}}}}}}},
		"PUT /12/accounts/18ce5/line_items/l1":              map[string]any{"data": map[string]any{"id": "l1"}},
		"GET /12/accounts/18ce5/custom_audiences":           map[string]any{"data": []any{map[string]any{"id": "a1", "name": "Old list", "audience_type": "CRM"}, map[string]any{"id": "a2", "name": "Site visitors", "audience_type": "WEB"}}},
		"POST /12/accounts/18ce5/custom_audiences":          map[string]any{"data": map[string]any{"id": "a9"}},
		"POST /12/accounts/18ce5/custom_audiences/a9/users": map[string]any{"data": []any{}},
		"POST /12/accounts/18ce5/custom_audiences/a1/users": map[string]any{"data": []any{}},
	})
	creds := c.Creds{"consumer_key": "ck", "consumer_secret": "cs", "access_token": "at", "access_token_secret": "ats"}
	x, acct := XAds{}, c.AdAccount{ID: "18ce5"}
	last := func() seen { return (*reqs)[len(*reqs)-1] }

	items, err := x.AdItems(ctx, cl, creds, acct, c.AdLevelGroup)
	if err != nil || len(items) != 2 || items[0].ID != "l1" || items[0].ParentID != "k1" || items[0].Status != c.AdActive || items[1].Name != "Line item l2" || items[1].ProviderStatus != "DELETED" {
		t.Fatalf("line items %+v %v", items, err)
	}
	if ads, err := x.AdItems(ctx, cl, creds, acct, c.AdLevelAd); err != nil || len(ads) != 0 {
		t.Fatalf("ads %+v %v", ads, err)
	}
	days, err := x.AdItemDays(ctx, cl, creds, acct, c.AdLevelGroup, "2026-10-01", "2026-10-02")
	if err != nil || len(days) != 1 || days[0].ID != "l1" || days[0].ParentID != "k1" || days[0].Date != "2026-10-01" || days[0].SpendMicros != 5_000_000 || days[0].Clicks != 12 {
		t.Fatalf("line item days %+v %v", days, err)
	}
	if r := last(); !strings.Contains(r.query, "entity=LINE_ITEM") || !strings.Contains(r.query, "entity_ids=l1%2Cl2") {
		t.Fatalf("stats query %s", r.query)
	}
	if err := x.SetAdGroupStatus(ctx, cl, creds, acct, "l1", false); err != nil {
		t.Fatal(err)
	}
	if r := last(); r.method != "PUT" || r.path != "/12/accounts/18ce5/line_items/l1" || r.query != "entity_status=PAUSED" {
		t.Fatalf("pause line item %+v", r)
	}
	if err := x.SetAdGroupStatus(ctx, cl, creds, acct, "L1/..", true); c.KindOf(err) != c.ErrInvalid {
		t.Fatalf("bad id: %v", err)
	}

	as, err := x.Audiences(ctx, cl, creds, acct)
	if err != nil || len(as) != 1 || as[0].ID != "a1" {
		t.Fatalf("only customer lists are listed: %+v %v", as, err)
	}
	if id, err := x.CreateAudience(ctx, cl, creds, acct, "Old list", ""); err != nil || id != "a1" {
		t.Fatalf("a list with the same name is found: %s %v", id, err)
	}
	id, err := x.CreateAudience(ctx, cl, creds, acct, "Top spenders (Outflare)", "")
	if r := last(); err != nil || id != "a9" || r.method != "POST" || !strings.Contains(r.query, "name=Top+spenders+%28Outflare%29") {
		t.Fatalf("create %s %v %+v", id, err, r)
	}
	m := c.AudienceMember{Emails: []string{"e1"}, Phones: []string{"p1"}}
	if _, err := x.IngestAudience(ctx, cl, creds, acct, "a9", c.AudienceAdd, []c.AudienceMember{m, {}}); err != nil {
		t.Fatal(err)
	}
	if r := last(); r.path != "/12/accounts/18ce5/custom_audiences/a9/users" || !strings.Contains(r.body, `"operation_type":"Update"`) || !strings.Contains(r.body, `"email":["e1"]`) || !strings.Contains(r.body, `"phone_number":["p1"]`) || !strings.Contains(r.body, `"expires_at"`) || strings.Count(r.body, `"email"`) != 1 {
		t.Fatalf("add %+v", r)
	}
	if err := x.SyncAudience(ctx, cl, creds, acct, "a1", c.AudienceRemove, m); err != nil {
		t.Fatal(err)
	}
	if r := last(); !strings.Contains(r.body, `"operation_type":"Delete"`) || strings.Contains(r.body, "expires_at") {
		t.Fatalf("remove %+v", r)
	}
	n := len(*reqs)
	if _, err := x.IngestAudience(ctx, cl, creds, acct, "a9", c.AudienceAdd, []c.AudienceMember{{}}); err != nil || len(*reqs) != n {
		t.Fatalf("nobody to send makes no call: %v", err)
	}
	if !x.Definition().Has(c.CapAdsAudiences) {
		t.Fatal("X Ads syncs audiences")
	}
}

func TestRedditAdsAudiences(t *testing.T) {
	cl, reqs, _ := fake(t, map[string]any{
		"GET /ad_accounts/a1/custom_audiences":  map[string]any{"data": []any{map[string]any{"id": "c1", "name": "Old list", "type": "CUSTOMER_LIST"}, map[string]any{"id": "c2", "name": "Visitors", "type": "PIXEL"}}},
		"POST /ad_accounts/a1/custom_audiences": map[string]any{"data": map[string]any{"id": "c9"}},
		"PATCH /custom_audiences/c9/users":      map[string]any{"data": map[string]any{}},
	})
	r, acct, creds := RedditAds{}, c.AdAccount{ID: "a1"}, c.Creds{"access_token": "tok"}
	last := func() seen { return (*reqs)[len(*reqs)-1] }
	as, err := r.Audiences(ctx, cl, creds, acct)
	if err != nil || len(as) != 1 || as[0].ID != "c1" {
		t.Fatalf("audiences %+v %v", as, err)
	}
	if id, err := r.CreateAudience(ctx, cl, creds, acct, "Old list", ""); err != nil || id != "c1" {
		t.Fatalf("found %s %v", id, err)
	}
	id, err := r.CreateAudience(ctx, cl, creds, acct, "Payers (Outflare)", "")
	if rq := last(); err != nil || id != "c9" || !strings.Contains(rq.body, `"type":"CUSTOMER_LIST"`) || !strings.Contains(rq.body, `"name":"Payers (Outflare)"`) {
		t.Fatalf("create %s %v %+v", id, err, rq)
	}
	if _, err := r.IngestAudience(ctx, cl, creds, acct, "c9", c.AudienceAdd, []c.AudienceMember{{Emails: []string{"e1", "e2"}}, {Phones: []string{"p"}}}); err != nil {
		t.Fatal(err)
	}
	if rq := last(); rq.method != "PATCH" || !strings.Contains(rq.body, `"action_type":"ADD"`) || !strings.Contains(rq.body, `"column_order":["EMAIL_SHA256"]`) || !strings.Contains(rq.body, `"user_data":[["e1"],["e2"]]`) {
		t.Fatalf("add %+v", rq)
	}
	if err := r.SyncAudience(ctx, cl, creds, acct, "c9", c.AudienceRemove, c.AudienceMember{Emails: []string{"e1"}}); err != nil {
		t.Fatal(err)
	}
	if rq := last(); !strings.Contains(rq.body, `"action_type":"REMOVE"`) {
		t.Fatalf("remove %+v", rq)
	}
	if !r.Definition().Has(c.CapAdsAudiences) {
		t.Fatal("Reddit Ads syncs audiences")
	}
}
