package api_test

import (
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/coverage/linkorders"
)

// A broker's full database export: every metric column, with the header
// spellings that used to fall through ("Traffic / Organic").
const intermediaryCSV = "Website,Price,Date Added,Geo AhrefsTop 1,Geo AhrefsTop 2,Main language,Article type,Traffic / Organic,DR,Main category,Subcategory,Organic Keywords,Total Keywords,Referring domains,Referring domains / Followed,Referring domains / Not followed,Referring IPs,Referring subnets,Backlinks,Backlinks / Followed,Backlinks / Not followed,Backlinks / Redirects,Backlinks / Internal,Outgoing domains / Followed,Outgoing domains / All time,Outgoing links / Followed,Outgoing links / All time,Intermediary,Theme,Site age,Sample post\n" +
	"news.example,$150,2026-01-15,US,GB,English,Guest post,\"12,500\",54,Business,Finance,\"3,400\",\"5,100\",\"1,200\",900,300,800,600,\"45,000\",\"30,000\",\"15,000\",120,\"2,000\",250,\"1,100\",400,\"3,500\",Acme Links,Fintech,2012,https://news.example/p\n" +
	"blog.example,99,15.03.2025,DE,AT,German,Sponsored,800,31,Tech,Gadgets,200,350,90,70,20,85,60,900,700,200,5,40,30,60,45,120,Northpeak Media,Gadgets,8 years,\n"

func TestLinkOrderCatalogIntermediaryImport(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	brand := owner.must(201, "POST", "/v1/workspaces/"+ws+"/brands", map[string]any{"name": "Northpeak", "handle": "northpeak"})["id"].(string)
	root := "/v1/brands/" + brand + "/link-orders"

	code, out := owner.raw("POST", root+"/catalog/import", "text/csv", intermediaryCSV)
	if code != 200 || out["created"] != 2.0 || len(out["errors"].([]any)) != 0 {
		t.Fatalf("import %d: %v", code, out)
	}
	if ign := out["ignored_columns"].([]any); len(ign) != 1 || ign[0] != "Sample post" {
		t.Fatalf("ignored_columns: %v", ign)
	}

	list := owner.must(200, "GET", root+"/catalog?q=news.example", nil)["data"].([]any)
	if len(list) != 1 {
		t.Fatalf("list: %v", list)
	}
	d := list[0].(map[string]any)
	if d["domain_rating"] != 54.0 || d["traffic"] != 12500.0 || d["country"] != "US" || d["languages"].([]any)[0] != "English" {
		t.Fatalf("core fields: %v", d)
	}
	if d["intermediary"] != "Acme Links" || d["theme"] != "Fintech" || d["site_since"] != "2012-01-01" || d["site_age_years"] == nil {
		t.Fatalf("intermediary, theme, site age: %v", d)
	}
	if d["notes"] != "" || len(d["niches"].([]any)) != 0 {
		t.Fatalf("intermediary and theme must not land in notes or niches: %v", d)
	}
	if p := d["prices"].([]any); len(p) != 1 || p[0].(map[string]any)["amount_minor"] != 15000.0 || p[0].(map[string]any)["currency"] != "USD" {
		t.Fatalf("Price $150 is 15000 cents: %v", p)
	}
	m := d["metrics"].(map[string]any)
	for k, want := range map[string]any{
		"date_added": "2026-01-15", "geo_top1": "US", "geo_top2": "GB", "article_type": "Guest post",
		"main_category": "Business", "subcategory": "Finance", "organic_keywords": 3400.0, "total_keywords": 5100.0,
		"referring_domains": 1200.0, "referring_domains_followed": 900.0, "referring_domains_not_followed": 300.0,
		"referring_ips": 800.0, "referring_subnets": 600.0, "backlinks": 45000.0, "backlinks_followed": 30000.0,
		"backlinks_not_followed": 15000.0, "backlinks_redirects": 120.0, "backlinks_internal": 2000.0,
		"outgoing_domains_followed": 250.0, "outgoing_domains_all_time": 1100.0,
		"outgoing_links_followed": 400.0, "outgoing_links_all_time": 3500.0,
	} {
		if m[k] != want {
			t.Errorf("metrics.%s = %v, want %v", k, m[k], want)
		}
	}
	if d["metrics_source"] != "csv" || d["metrics_at"] == nil {
		t.Fatalf("metrics source: %v", d)
	}
	id := d["id"].(string)

	// Search finds a site by its intermediary or theme; the filter is exact.
	byBroker := owner.must(200, "GET", root+"/catalog?q="+url.QueryEscape("northpeak med"), nil)["data"].([]any)
	if len(byBroker) != 1 || byBroker[0].(map[string]any)["domain"] != "blog.example" {
		t.Fatalf("search by intermediary: %v", byBroker)
	}
	if got := owner.must(200, "GET", root+"/catalog?q=fintech", nil)["data"].([]any); len(got) != 1 {
		t.Fatalf("search by theme: %v", got)
	}
	if got := owner.must(200, "GET", root+"/catalog?intermediary="+url.QueryEscape("acme links"), nil)["data"].([]any); len(got) != 1 {
		t.Fatalf("intermediary filter: %v", got)
	}
	if got := owner.must(200, "GET", root+"/catalog?q=%25", nil)["data"].([]any); len(got) != 0 {
		t.Fatalf("a %% in the search is literal: %v", got)
	}

	// The fields can be edited by hand, and metrics merge key by key.
	patched := owner.must(200, "PATCH", root+"/catalog/items/"+id, map[string]any{
		"intermediary": "Acme Links (Ana)", "theme": "Personal finance", "site_age": "10 years",
		"metrics": map[string]any{"backlinks": 46000, "geo_top2": nil},
	})
	pm := patched["metrics"].(map[string]any)
	if patched["intermediary"] != "Acme Links (Ana)" || patched["theme"] != "Personal finance" || patched["site_age_years"] != 10.0 {
		t.Fatalf("patch: %v", patched)
	}
	if pm["backlinks"] != 46000.0 || pm["geo_top2"] != nil || pm["referring_domains"] != 1200.0 {
		t.Fatalf("metrics merge: %v", pm)
	}
	owner.must(400, "PATCH", root+"/catalog/items/"+id, map[string]any{"metrics": map[string]any{"made_up": 1}})
	owner.must(400, "PATCH", root+"/catalog/items/"+id, map[string]any{"metrics": map[string]any{"backlinks": -1}})
	owner.must(400, "PATCH", root+"/catalog/items/"+id, map[string]any{"site_age": "ancient"})
	cleared := owner.must(200, "PATCH", root+"/catalog/items/"+id, map[string]any{"site_age": nil})
	if cleared["site_since"] != nil || cleared["site_age_years"] != nil {
		t.Fatalf("clear site age: %v", cleared)
	}
	added := owner.must(201, "POST", root+"/catalog", map[string]any{"domain": "manual.example", "intermediary": "Acme Links", "theme": "Travel", "site_age": "2015"})
	if added["intermediary"] != "Acme Links" || added["site_since"] != "2015-01-01" {
		t.Fatalf("create with new fields: %v", added)
	}
}

func TestLinkOrderCatalogLargeImport(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	brand := owner.must(201, "POST", "/v1/workspaces/"+ws+"/brands", map[string]any{"name": "Northpeak", "handle": "northpeak"})["id"].(string)
	root := "/v1/brands/" + brand + "/link-orders"

	// More than the old 4 MB limit in one request: real rows, then blank
	// lines a spreadsheet leaves at the end, which are skipped.
	var b strings.Builder
	b.WriteString("domain,dr,traffic,intermediary\n")
	for i := range 7 {
		fmt.Fprintf(&b, "site%d.example,%d,%d,Acme Links\n", i, 10+i, 1000*i)
	}
	filler := strings.Repeat(",,,\n", (5<<20)/4)
	b.WriteString(filler)
	if b.Len() <= 4<<20 {
		t.Fatalf("body is only %d bytes", b.Len())
	}
	code, out := owner.raw("POST", root+"/catalog/import", "text/csv", b.String())
	if code != 200 || out["created"] != 7.0 || len(out["errors"].([]any)) != 0 {
		t.Fatalf("large import %d: %v", code, out)
	}

	// Rows are written in chunks, one transaction each: a chunk size of 2
	// splits 7 rows into 4 chunks and every row still lands once, with
	// errors numbered by their line in the file.
	old := linkorders.ImportChunkRows
	linkorders.ImportChunkRows = 2
	t.Cleanup(func() { linkorders.ImportChunkRows = old })
	csv := "domain,dr\nsite0.example,20\nsite1.example,21\nnew0.example,30\nbad row,1\nnew1.example,31\nsite2.example,140\nnew2.example,32\n"
	code, out = owner.raw("POST", root+"/catalog/import", "text/csv", csv)
	if code != 200 || out["created"] != 3.0 || out["updated"] != 2.0 {
		t.Fatalf("chunked import %d: %v", code, out)
	}
	errs := out["errors"].([]any)
	if len(errs) != 2 || errs[0].(map[string]any)["row"] != 5.0 || errs[1].(map[string]any)["row"] != 7.0 {
		t.Fatalf("chunked errors: %v", errs)
	}

	// The page sends a big file as batches that each repeat the header;
	// upserting by domain, the batches add up to one catalog.
	for _, batch := range []string{
		"Website,Traffic / Organic\nbatch0.example,100\nbatch1.example,200\n",
		"Website,Traffic / Organic\nbatch1.example,250\nbatch2.example,300\n",
	} {
		owner.must200Raw(t, root+"/catalog/import", batch)
	}
	all := owner.must(200, "GET", root+"/catalog?q=batch", nil)["data"].([]any)
	if len(all) != 3 {
		t.Fatalf("batches: %v", all)
	}
	for _, x := range all {
		if d := x.(map[string]any); d["domain"] == "batch1.example" && d["traffic"] != 250.0 {
			t.Fatalf("later batch should update: %v", d)
		}
	}

	// Over 32 MB is refused with 413 and a message saying to split it.
	code, out = owner.raw("POST", root+"/catalog/import", "text/csv", "domain\n"+strings.Repeat("x", 33<<20))
	if code != 413 {
		t.Fatalf("over the limit: %d %v", code, out)
	}
}

func (c *client) must200Raw(t *testing.T, path, csv string) map[string]any {
	t.Helper()
	code, out := c.raw("POST", path, "text/csv", csv)
	if code != 200 {
		t.Fatalf("%s: %d %v", path, code, out)
	}
	return out
}
