package ads

import (
	"strings"
	"testing"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// A live read is one SELECT, and no part of it can carry another clause.
func TestGoogleQuery(t *testing.T) {
	q, limit, err := googleQuery(c.AdQuery{Resource: "campaign", Fields: []string{"campaign.id", "metrics.search_impression_share"},
		Conditions: []string{"campaign.name LIKE '%from limit%'", "segments.date DURING LAST_30_DAYS", "campaign.status IN ('ENABLED', 'PAUSED')"}, Orderings: []string{"metrics.clicks DESC"}})
	want := "SELECT campaign.id, metrics.search_impression_share FROM campaign WHERE campaign.name LIKE '%from limit%' AND segments.date DURING LAST_30_DAYS AND campaign.status IN ('ENABLED', 'PAUSED') ORDER BY metrics.clicks DESC LIMIT 101"
	if err != nil || limit != 100 || q != want {
		t.Fatalf("query %q %d %v", q, limit, err)
	}
	for name, bad := range map[string]c.AdQuery{
		"no fields":        {Resource: "campaign"},
		"resource":         {Resource: "campaign, customer", Fields: []string{"campaign.id"}},
		"field clause":     {Resource: "campaign", Fields: []string{"campaign.id FROM customer"}},
		"condition limit":  {Resource: "campaign", Fields: []string{"campaign.id"}, Conditions: []string{"campaign.id > 0 LIMIT 99999"}},
		"condition order":  {Resource: "campaign", Fields: []string{"campaign.id"}, Conditions: []string{"campaign.id > 0 ORDER  BY campaign.id"}},
		"condition quote":  {Resource: "campaign", Fields: []string{"campaign.id"}, Conditions: []string{"campaign.name = 'a' LIMIT 5 --'"}},
		"condition second": {Resource: "campaign", Fields: []string{"campaign.id"}, Conditions: []string{"campaign.id = 1; SELECT customer.id FROM customer"}},
		"not a comparison": {Resource: "campaign", Fields: []string{"campaign.id"}, Conditions: []string{"1 = 1"}},
		"ordering":         {Resource: "campaign", Fields: []string{"campaign.id"}, Orderings: []string{"campaign.id DESC LIMIT 5"}},
		"limit":            {Resource: "campaign", Fields: []string{"campaign.id"}, Limit: 5000},
	} {
		if _, _, err := googleQuery(bad); c.KindOf(err) != c.ErrInvalid {
			t.Errorf("%s: %v", name, err)
		}
	}

	// One row more than asked for is how a cut answer is told.
	cl, _ := googleFake(t, func(q string) any {
		if !strings.HasSuffix(q, "LIMIT 2") {
			t.Errorf("query %q", q)
		}
		return map[string]any{"results": []any{map[string]any{"campaign": map[string]any{"id": "1"}}, map[string]any{"campaign": map[string]any{"id": "2"}}}}
	}, nil)
	res, err := GoogleAds{}.AdQuery(ctx, cl, c.Creds{"access_token": "tok"}, c.AdAccount{ID: "111"}, c.AdQuery{Resource: "campaign", Fields: []string{"campaign.id"}, Limit: 1})
	if err != nil || res.Complete || res.RowCount != 1 || !strings.HasSuffix(res.Query, "LIMIT 1") {
		t.Fatalf("cut: %+v %v", res, err)
	}
}
