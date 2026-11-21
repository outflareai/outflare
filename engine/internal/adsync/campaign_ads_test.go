package adsync

import (
	"context"
	"testing"

	"github.com/outflareai/outflare/engine/internal/connectors"
)

// campaignAds is fakeAds on a platform with no ad groups: its ads sit in
// the campaign.
type campaignAds struct {
	*fakeAds
	ads  []connectors.AdItem
	days []connectors.AdItemDay
}

func (f *campaignAds) CampaignAds(context.Context, *connectors.Client, connectors.Creds, connectors.AdAccount, string, string) ([]connectors.AdItem, []connectors.AdItemDay, error) {
	return f.ads, f.days, nil
}

// A platform with no ad groups has each campaign stored as its own ad
// group, with the campaign's days, and its ads under it: an ad is reached
// through an ad group like everywhere else.
func TestCampaignAds(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	f := &campaignAds{fakeAds: e.f,
		ads: []connectors.AdItem{
			{ID: "i1", ParentID: "c1", Name: "Ten desk lamps", Status: connectors.AdActive, ProviderStatus: "RUNNING", Details: map[string]string{connectors.AdDetailURL: "https://acme.example/lamps"}},
			// Seen in the report only: no word on its status.
			{ID: "i2", ParentID: "c1", Name: "Chairs"},
			{ID: "i3", ParentID: "gone", Name: "No campaign"},
		},
		days: []connectors.AdItemDay{
			{ID: "i1", ParentID: "c1", Date: "2026-11-09", SpendMicros: 700_000, Impressions: 7, Clicks: 1},
			{ID: "i2", ParentID: "c1", Date: "2026-11-09", SpendMicros: 300_000, Impressions: 3},
			{ID: "i3", ParentID: "gone", Date: "2026-11-09", SpendMicros: 5},
		}}
	e.s.Store.Registry = connectors.NewRegistry([]connectors.Connector{f}, nil)
	res, err := e.s.SyncOnce(ctx, e.ws, e.integration)
	if err != nil || res.Campaigns != 1 || res.Ads != 2 || res.AdGroups != 0 {
		t.Fatalf("sync %+v %v", res, err)
	}
	camp := e.entity(t, "c1")
	var group, name, status string
	var budget *int64
	var spend int64
	if err := e.pool.QueryRow(ctx, `SELECT g.id, g.name, g.status, g.daily_budget_micros, (SELECT COALESCE(sum(spend_micros), 0) FROM ad_metrics_daily WHERE ad_entity_id = g.id)
		FROM ad_entities g WHERE g.level = 'ad_group' AND g.parent_id = $1 AND g.external_id = 'c1'`, camp).Scan(&group, &name, &status, &budget, &spend); err != nil {
		t.Fatal(err)
	}
	if name != "Brand search" || status != connectors.AdActive || budget != nil || spend != 1_000_000 {
		t.Fatalf("the campaign as its own ad group: %s %s %v %d", name, status, budget, spend)
	}
	if n := e.count(t, `SELECT count(*) FROM ad_entities WHERE level = 'ad' AND parent_id = $1`, group); n != 2 {
		t.Fatalf("%d ads under the campaign's ad group", n)
	}
	if n := e.count(t, `SELECT count(*) FROM ad_entities WHERE level = 'ad' AND external_id = 'i3'`); n != 0 {
		t.Fatal("an ad of a campaign that isn't stored is left out")
	}
	var st string
	var adSpend int64
	if err := e.pool.QueryRow(ctx, `SELECT e.status, (SELECT sum(spend_micros) FROM ad_metrics_daily WHERE ad_entity_id = e.id) FROM ad_entities e WHERE e.level = 'ad' AND e.external_id = 'i2'`).Scan(&st, &adSpend); err != nil {
		t.Fatal(err)
	}
	if st != connectors.AdOther || adSpend != 300_000 {
		t.Fatalf("report-only ad: %s %d", st, adSpend)
	}
	// The next sync learns the second ad is paused and the first says
	// nothing: one changes, the other keeps what it had. Nothing doubles.
	f.ads = []connectors.AdItem{{ID: "i1", ParentID: "c1", Name: "Ten desk lamps"}, {ID: "i2", ParentID: "c1", Name: "Chairs", Status: connectors.AdPaused, ProviderStatus: "PAUSED"}}
	e.now = e.now.Add(MaxInterval)
	if _, err := e.s.SyncOnce(ctx, e.ws, e.integration); err != nil {
		t.Fatal(err)
	}
	if n := e.count(t, `SELECT count(*) FROM ad_entities WHERE level = 'ad' AND (external_id = 'i1' AND status = 'active' OR external_id = 'i2' AND status = 'paused')`); n != 2 {
		t.Fatalf("statuses after the second sync: %d right", n)
	}
	if n := e.count(t, `SELECT count(*) FROM ad_entities WHERE level IN ('ad_group', 'ad')`); n != 3 {
		t.Fatalf("%d ad groups and ads after two syncs", n)
	}
	// The spend ledger holds the campaign's spend once: ad groups and ads
	// never add to it.
	if n := e.count(t, `SELECT count(DISTINCT ad_entity_id) FROM ad_metrics_daily m JOIN ad_entities x ON x.id = m.ad_entity_id WHERE x.level = 'campaign'`); n != 1 {
		t.Fatalf("campaign days on %d campaigns", n)
	}
}
