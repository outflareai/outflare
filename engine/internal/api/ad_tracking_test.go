package api_test

import (
	"context"
	"strings"
	"testing"
)

func TestAdTrackingTemplates(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws

	all := owner.must(200, "GET", base+"/ads/tracking-templates", nil)["data"].([]any)
	if len(all) != 14 {
		t.Fatalf("%d networks", len(all))
	}
	tiktok := owner.must(200, "GET", base+"/ads/tracking-templates?network=tiktok", nil)["data"].([]any)
	if len(tiktok) != 1 {
		t.Fatalf("tiktok: %v", tiktok)
	}
	tt := tiktok[0].(map[string]any)
	if !strings.Contains(tt["template"].(string), "of_campaign=__CAMPAIGN_ID__") || tt["click_ids"].([]any)[0] != "ttclid" ||
		tt["connector"] != "tiktok_ads" || len(tt["cookies"].([]any)) != 1 || tt["cookies"].([]any)[0] != "_ttp" {
		t.Errorf("tiktok: %v", tt)
	}
	meta := owner.must(200, "GET", base+"/ads/tracking-templates?network=meta", nil)["data"].([]any)[0].(map[string]any)
	if meta["connector"] != "meta_ads" || len(meta["cookies"].([]any)) != 2 {
		t.Errorf("meta: %v", meta)
	}
	apple := owner.must(200, "GET", base+"/ads/tracking-templates?network=apple_search_ads", nil)["data"].([]any)[0].(map[string]any)
	if apple["template"] != "" || apple["field"] != nil || apple["connector"] != "apple_ads" {
		t.Errorf("apple search ads: %v", apple)
	}
	owner.must(400, "GET", base+"/ads/tracking-templates?network=myspace", nil)
}

// A click through a tracking template stores the network's ids on the
// visit; the pixel's cookies join the visit's click ids, from a later page
// too, without starting a new visit.
func TestSiteVisitKeepsAdAndCookies(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	site := "https://www.northpeak.example"
	key := owner.must(201, "POST", "/v1/workspaces/"+ws+"/site-keys", map[string]any{"name": "Northpeak", "origins": []string{site}})["key"].(string)
	page := func(path string, props map[string]any) map[string]any {
		return map[string]any{"type": "page", "anonymousId": "ad-visitor-1", "properties": props,
			"context": map[string]any{"page": map[string]any{"url": site + path, "path": path}}}
	}
	send := func(ev map[string]any) {
		t.Helper()
		if rec := sendSiteAs(t, e.h, site, key, iphoneUA, true, ev); rec.Code != 200 {
			t.Fatalf("send: %d %s", rec.Code, rec.Body)
		}
	}
	send(page("/pricing", map[string]any{
		"click_ids": map[string]any{"ttclid": "tt-1", "unknown": "dropped"},
		"ad":        map[string]any{"campaign": "1800001", "adset": "__AID__", "ad": "1800003", "extra": "dropped"},
	}))
	send(page("/signup", map[string]any{"click_ids": map[string]any{"fbp": "fb.1.1761600000000.111"}}))

	type visit struct {
		clickIDs, ad map[string]string
		pageviews    int
	}
	var got []visit
	rows, err := e.pool.Query(ctx, `SELECT click_ids, ad, pageviews FROM site_sessions WHERE workspace_id = $1 AND anonymous_id = 'ad-visitor-1' ORDER BY started_at`, ws)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var v visit
		if err := rows.Scan(&v.clickIDs, &v.ad, &v.pageviews); err != nil {
			t.Fatal(err)
		}
		got = append(got, v)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].pageviews != 2 {
		t.Fatalf("visits: %+v", got)
	}
	v := got[0]
	if len(v.clickIDs) != 2 || v.clickIDs["ttclid"] != "tt-1" || v.clickIDs["fbp"] != "fb.1.1761600000000.111" {
		t.Errorf("click ids %v", v.clickIDs)
	}
	if len(v.ad) != 2 || v.ad["campaign"] != "1800001" || v.ad["ad"] != "1800003" {
		t.Errorf("ad %v", v.ad)
	}

	// The same browser through another ad is a new visit, keeping the cookie.
	send(page("/", map[string]any{"ad": map[string]any{"campaign": "1800009"}}))
	var n int
	var ids map[string]string
	if err := e.pool.QueryRow(ctx, `SELECT count(*) OVER (), click_ids FROM site_sessions WHERE workspace_id = $1 AND anonymous_id = 'ad-visitor-1' ORDER BY started_at DESC LIMIT 1`, ws).Scan(&n, &ids); err != nil {
		t.Fatal(err)
	}
	if n != 2 || ids["fbp"] != "fb.1.1761600000000.111" || ids["ttclid"] != "" {
		t.Errorf("second visit: %d visits, click ids %v", n, ids)
	}
}
