package paidads

import (
	"context"
	"testing"
	"time"
)

// A paid landing carries the click id and the campaign; the sign-up page
// on another domain keeps the click id but not the campaign, and the
// network never reports the click. The campaign comes from the landing
// with the same click id. A visit with a different click id lends nothing.
func TestLedgerCampaignFromLandingWithSameClickID(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	min := time.Minute
	session := func(record *string, anon string, at time.Time, gclid, ad string) {
		t.Helper()
		f.q(`INSERT INTO site_sessions (brand_id, workspace_id, record_id, anonymous_id, started_at, utm_source, utm_medium, channel, click_ids, ad)
			VALUES ($1, $2, $3, $4, $5, 'google', 'cpc', 'paid_ads', jsonb_build_object('gclid', $6::text), $7::jsonb) RETURNING id::text`,
			f.brand, f.ws, record, anon, at, gclid, ad)
	}
	campaign := func(record string) (*string, string) {
		t.Helper()
		var c *string
		var matched string
		if err := f.pool.QueryRow(ctx, `SELECT campaign_entity_id::text, matched_by FROM ad_acquisitions WHERE brand_id = $1 AND record_id = $2`, f.brand, record).Scan(&c, &matched); err != nil {
			t.Fatal(err)
		}
		return c, matched
	}

	// Ada: landing, then the app with the same gclid and no campaign.
	ada := f.person("Ada")
	session(nil, "anon-ada", f.now.Add(-90*min), "Cj0ada", `{"campaign": "111"}`)
	session(&ada, "anon-ada", f.now.Add(-80*min), "Cj0ada", `{}`)
	f.event(ada, nil, "signed_up", f.now.Add(-70*min), `{}`)

	// Bo: landing on one campaign, then a different paid click with no
	// campaign before the sign-up. The latest click is the second one, and
	// its campaign is unknown: the first click's campaign is not borrowed.
	bo := f.person("Bo")
	session(&bo, "anon-bo", f.now.Add(-90*min), "Cj0bo1", `{"campaign": "111"}`)
	session(&bo, "anon-bo", f.now.Add(-80*min), "Cj0bo2", `{}`)
	f.event(bo, nil, "signed_up", f.now.Add(-70*min), `{}`)

	// Cy: landing on Spring search, a different click on Brand search,
	// then the app with the first click's id again: that click's campaign.
	cy := f.person("Cy")
	session(&cy, "anon-cy", f.now.Add(-90*min), "Cj0cy1", `{"campaign": "111"}`)
	session(&cy, "anon-cy", f.now.Add(-85*min), "Cj0cy2", `{"campaign": "222"}`)
	session(&cy, "anon-cy", f.now.Add(-80*min), "Cj0cy1", `{}`)
	f.event(cy, nil, "signed_up", f.now.Add(-70*min), `{}`)
	// Ada's visitor id is linked to her record, as identify does.
	f.exec(`INSERT INTO site_visitors (workspace_id, anonymous_id, record_id, first_seen_at, last_seen_at) VALUES ($1, 'anon-ada', $2, now(), now())`, f.ws, ada)

	if _, err := f.ledger().RunWorkspace(ctx, f.ws); err != nil {
		t.Fatal(err)
	}
	if c, matched := campaign(ada); c == nil || *c != f.spring || matched != "campaign_param" {
		t.Fatalf("ada: %v %s", c, matched)
	}
	if c, matched := campaign(bo); c != nil {
		t.Fatalf("bo borrowed another click's campaign: %v %s", *c, matched)
	}
	if c, _ := campaign(cy); c == nil || *c != f.spring {
		t.Fatalf("cy: %v", c)
	}
}

// An acquisition already written with its campaign unknown (before the
// ledger looked at the landing) is filled from the landing on a later run.
func TestLedgerFillsCampaignFromLanding(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	min := time.Minute
	dee := f.person("Dee")
	f.q(`INSERT INTO site_sessions (brand_id, workspace_id, anonymous_id, started_at, utm_source, utm_medium, channel, click_ids, ad)
		VALUES ($1, $2, 'anon-dee', $3, 'google', 'cpc', 'paid_ads', '{"gclid": "Cj0dee"}', '{"campaign": "111"}') RETURNING id::text`, f.brand, f.ws, f.now.Add(-90*min))
	app := f.q(`INSERT INTO site_sessions (brand_id, workspace_id, record_id, anonymous_id, started_at, utm_source, utm_medium, channel, click_ids)
		VALUES ($1, $2, $3, 'anon-dee', $4, 'google', 'cpc', 'paid_ads', '{"gclid": "Cj0dee"}') RETURNING id::text`, f.brand, f.ws, dee, f.now.Add(-80*min))
	// Eli: the same shape, but the landing had another click id.
	eli := f.person("Eli")
	f.q(`INSERT INTO site_sessions (brand_id, workspace_id, record_id, anonymous_id, started_at, utm_source, utm_medium, channel, click_ids, ad)
		VALUES ($1, $2, $3, 'anon-eli', $4, 'google', 'cpc', 'paid_ads', '{"gclid": "Cj0eli1"}', '{"campaign": "111"}') RETURNING id::text`, f.brand, f.ws, eli, f.now.Add(-90*min))
	eliApp := f.q(`INSERT INTO site_sessions (brand_id, workspace_id, record_id, anonymous_id, started_at, utm_source, utm_medium, channel, click_ids)
		VALUES ($1, $2, $3, 'anon-eli', $4, 'google', 'cpc', 'paid_ads', '{"gclid": "Cj0eli2"}') RETURNING id::text`, f.brand, f.ws, eli, f.now.Add(-80*min))
	for _, x := range []struct{ record, session, gclid string }{{dee, app, "Cj0dee"}, {eli, eliApp, "Cj0eli2"}} {
		f.exec(`INSERT INTO ad_acquisitions (workspace_id, brand_id, record_id, connector, click_id_kind, click_id, session_id, clicked_at, matched_by, window_days, signed_up_at)
			VALUES ($1, $2, $3, 'google_ads', 'gclid', $4, $5, $6, 'utm', 30, $7)`, f.ws, f.brand, x.record, x.gclid, x.session, f.now.Add(-80*min), f.now.Add(-70*min))
	}

	res, err := f.ledger().RunWorkspace(ctx, f.ws)
	if err != nil {
		t.Fatal(err)
	}
	if res.Filled != 1 {
		t.Fatalf("run: %+v", res)
	}
	var c, eliC *string
	var matched, ext string
	if err := f.pool.QueryRow(ctx, `SELECT campaign_entity_id::text, matched_by, campaign_external_id FROM ad_acquisitions WHERE brand_id = $1 AND record_id = $2`, f.brand, dee).Scan(&c, &matched, &ext); err != nil {
		t.Fatal(err)
	}
	if c == nil || *c != f.spring || matched != "campaign_param" || ext != "111" {
		t.Fatalf("dee: %v %s %q", c, matched, ext)
	}
	if err := f.pool.QueryRow(ctx, `SELECT campaign_entity_id::text FROM ad_acquisitions WHERE brand_id = $1 AND record_id = $2`, f.brand, eli).Scan(&eliC); err != nil || eliC != nil {
		t.Fatalf("eli: %v %v", eliC, err)
	}
}
