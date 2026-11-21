package api_test

import (
	"context"
	"testing"
)

func TestSEOLinksReport(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	one := func(sql string, args ...any) string {
		t.Helper()
		var id string
		if err := e.pool.QueryRow(ctx, sql, args...).Scan(&id); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return id
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := e.pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}

	brand := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "handle": "northpeak", "website": "https://northpeak.example"})["id"].(string)
	other := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Acme", "handle": "acme", "website": "https://acme.example"})["id"].(string)
	report := "/v1/brands/" + brand + "/seo/links"

	empty := owner.must(200, "GET", report, nil)
	if len(empty["series"].([]any)) != 0 || len(empty["velocity"].([]any)) != 12 || len(empty["lost"].([]any)) != 0 {
		t.Fatalf("empty: %v", empty)
	}

	site := one(`INSERT INTO seo_sites (workspace_id, brand_id, host, start_url, verify_token) VALUES ($1, $2, 'northpeak.example', 'https://northpeak.example/', 'tok') RETURNING id::text`, ws, brand)
	otherSite := one(`INSERT INTO seo_sites (workspace_id, brand_id, host, start_url, verify_token) VALUES ($1, $2, 'acme.example', 'https://acme.example/', 'tok') RETURNING id::text`, ws, other)
	exec(`INSERT INTO seo_competitors (workspace_id, site_id, brand_id, host, status, source) VALUES ($1, $2, $3, 'rival.example', 'tracked', 'manual'), ($1, $2, $3, 'gone.example', 'dismissed', 'manual')`, ws, site, brand)

	seo := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "dataforseo", "credentials": map[string]string{"login": "jo", "password": "pw"}})["id"].(string)
	exec(`DELETE FROM jobs WHERE kind = 'seo.sync'`)
	snap := func(conn, host string, ago, rd int) {
		exec(`INSERT INTO seo_snapshots (workspace_id, integration_id, connector, host, report, day, key, url, metrics)
			VALUES ($1, $2, $3, $4, 'overview', current_date - $5::int, '', '', jsonb_build_object('referring_domains', $6::int))`, ws, seo, conn, host, ago, rd)
	}
	snap("ahrefs", "northpeak.example", 0, 120)
	snap("ahrefs", "northpeak.example", 7, 110)
	snap("ahrefs", "northpeak.example", 28, 100)
	// Ahrefs has the domain, so this is not mixed in. A day of its own: one
	// connection stores one overview row per host and day.
	snap("dataforseo", "northpeak.example", 1, 999)
	snap("ahrefs", "rival.example", 0, 500)
	snap("ahrefs", "gone.example", 0, 900)

	// Links: live ones for the anchors, lost ones, one of them bought.
	link := func(site, brand, src, domain, anchor, status string, rating float64, lostAgo int) string {
		return one(`INSERT INTO backlinks (workspace_id, brand_id, site_id, source_url, target_url, source_domain, anchor, status, source_rating, first_seen_at, lost_at)
			VALUES ($1, $2, $3, $4, 'https://northpeak.example/', $5, $6, $7, $8, now() - interval '90 days',
				CASE WHEN $7 = 'lost' THEN now() - make_interval(days => $9) END) RETURNING id::text`, ws, brand, site, src, domain, anchor, status, rating, lostAgo)
	}
	a := link(site, brand, "https://a.example/1", "a.example", "Northpeak", "live", 40, 0)
	b := link(site, brand, "https://b.example/1", "b.example", "https://northpeak.example", "live", 40, 0)
	link(site, brand, "https://c.example/1", "c.example", "click here", "live", 40, 0)
	link(site, brand, "https://d.example/1", "d.example", "Residential Proxies", "live", 40, 0)
	link(site, brand, "https://e.example/1", "e.example", "best residential proxies", "live", 40, 0)
	link(site, brand, "https://f.example/1", "f.example", "great tool", "live", 40, 0)
	paid := link(site, brand, "https://donor.example/post", "donor.example", "proxies", "lost", 50, 3)
	link(site, brand, "https://strong.example/x", "strong.example", "", "lost", 70, 2)
	link(site, brand, "https://old.example/x", "old.example", "", "lost", 70, 40)
	link(otherSite, other, "https://acme-link.example/x", "acme-link.example", "", "lost", 90, 1)
	exec(`INSERT INTO page_targets (workspace_id, site_id, url, keyword) VALUES ($1, $2, 'https://northpeak.example/residential', 'residential proxies')`, ws, site)
	ev := `INSERT INTO backlink_events (workspace_id, site_id, backlink_id, kind, at, connector) VALUES ($1, $2, $3, $4, now(), 'ahrefs')`
	exec(ev, ws, site, a, "new")
	exec(ev, ws, site, b, "new")
	exec(ev, ws, site, paid, "lost")

	rec := owner.must(201, "POST", base+"/records", map[string]any{"type": "account", "name": "Example publisher", "brand_id": brand})
	pub := owner.must(201, "POST", "/v1/brands/"+brand+"/coverage/publishers", map[string]any{"record_id": rec["id"], "domain": "donor.example"})
	profile := one(`INSERT INTO coverage_donor_profiles (workspace_id, brand_id, publisher_id) VALUES ($1, $2, $3) RETURNING id::text`, ws, brand, pub["id"])
	order := func(status, published string, live *bool, match string) string {
		return one(`INSERT INTO coverage_link_orders (workspace_id, brand_id, publisher_id, donor_profile_id, target_url, status, currency, published_url, anchor_primary,
				last_check_at, last_check_live, last_check_target_match, last_check_anchor_match)
			VALUES ($1, $2, $3, $4, 'https://northpeak.example/residential', $5, 'USD', $6, 'residential proxies', now(), $7, true, $8) RETURNING id::text`,
			ws, brand, pub["id"], profile, status, published, live, match)
	}
	yes, no := true, false
	notLive := order("paid", "https://www.donor.example/post/", &no, "")
	mismatch := order("published", "https://donor.example/two", &yes, "different")
	order("verified", "https://donor.example/three", &yes, "exact")
	order("requested", "", nil, "")

	r := owner.must(200, "GET", report, nil)
	series := r["series"].([]any)
	if len(series) != 2 {
		t.Fatalf("series: %v", series)
	}
	us, rival := series[0].(map[string]any), series[1].(map[string]any)
	if us["host"] != "northpeak.example" || us["competitor"] != false || us["connector"] != "ahrefs" || us["latest"] != 120.0 || us["wow"] != 10.0 || us["mom"] != 20.0 {
		t.Fatalf("us: %v", us)
	}
	if rival["host"] != "rival.example" || rival["competitor"] != true || rival["latest"] != 500.0 {
		t.Fatalf("rival: %v", rival)
	}

	vel := r["velocity"].([]any)
	last := vel[len(vel)-1].(map[string]any)
	if last["new"] != 2.0 || last["lost"] != 1.0 || last["new_domains"] != 2.0 || last["lost_domains"] != 1.0 {
		t.Fatalf("velocity: %v", last)
	}

	lost := r["lost"].([]any)
	if len(lost) != 2 {
		t.Fatalf("lost: %v", lost)
	}
	if l := lost[0].(map[string]any); l["source_domain"] != "donor.example" || l["order_id"] != notLive {
		t.Fatalf("bought lost first: %v", l)
	}
	if l := lost[1].(map[string]any); l["order_id"] != nil {
		t.Fatalf("unbought: %v", l)
	}

	orders := map[string][]any{}
	for _, o := range r["orders"].([]any) {
		m := o.(map[string]any)
		orders[m["id"].(string)] = m["problems"].([]any)
	}
	if len(orders) != 2 || len(orders[notLive]) != 1 || orders[notLive][0] != "not_live" || len(orders[mismatch]) != 1 || orders[mismatch][0] != "anchor_mismatch" {
		t.Fatalf("orders: %v", orders)
	}

	an := r["anchors"].(map[string]any)
	for k, want := range map[string]float64{"branded": 1, "url": 1, "generic": 1, "exact": 1, "partial": 1, "other": 1, "empty": 0} {
		if an[k] != want {
			t.Fatalf("anchors %s: %v", k, an)
		}
	}

	sum := r["summary"].(map[string]any)
	if sum["referring_domains"] != 120.0 || sum["rd_wow"] != 10.0 || sum["lost_links"] != 2.0 || sum["lost_paid"] != 1.0 ||
		sum["bought_live"] != 2.0 || sum["bought_total"] != 3.0 || sum["bought_problems"] != 2.0 || sum["new_domains_week"] != 2.0 {
		t.Fatalf("summary: %v", sum)
	}

	// The other brand sees only its own lost link.
	o := owner.must(200, "GET", "/v1/brands/"+other+"/seo/links", nil)
	if len(o["lost"].([]any)) != 1 || len(o["orders"].([]any)) != 0 || len(o["series"].([]any)) != 0 {
		t.Fatalf("other brand: %v", o)
	}
}
