package api_test

import (
	"context"
	"testing"

	"github.com/outflareai/outflare/engine/internal/track"
)

// TestProductsFromEvents: an event that names a product and its category
// adds the product with that category, a later category moves it, an event
// without a category only holds the name, and turning off event names as
// a product source turns it off.
func TestProductsFromEvents(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	var brand string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand
	send := func(product, category string) map[string]any {
		t.Helper()
		props := map[string]any{"product": product, "request_count": 120}
		if category != "" {
			props["product_category"] = category
		}
		b := owner.must(200, "POST", "/v1/workspaces/"+ws+"/customer-events", map[string]any{"type": "track", "userId": "u1",
			"event": "Product Usage", "properties": props})
		return b["results"].([]any)[0].(map[string]any)
	}
	products := func(category string) []any {
		t.Helper()
		return owner.must(200, "GET", base+"/products?category="+category, nil)["data"].([]any)
	}

	// A new product with its category: added, and the event is about it.
	r := send("residential_proxies", " Proxies ")
	list := products("proxies")
	if len(list) != 1 {
		t.Fatalf("proxies products: %v", list)
	}
	p := list[0].(map[string]any)
	if p["key"] != "residential_proxies" || p["name"] != "Residential proxies" || p["churn_mode"] != "inactivity" || r["product_id"] != p["id"] {
		t.Fatalf("product from event: %v, event %v", p, r)
	}
	created := 0
	for _, ev := range got() {
		if ev.Name == "product.created" {
			created++
		}
	}
	if created != 1 {
		t.Fatalf("product.created events: %d", created)
	}

	// The same product again changes nothing; a new category moves it.
	if r := send("residential_proxies", "proxies"); r["product_id"] != p["id"] || len(products("")) != 1 {
		t.Fatalf("second event: %v", r)
	}
	send("residential_proxies", "web_data")
	if len(products("proxies")) != 0 || len(products("web_data")) != 1 {
		t.Fatal("the category did not follow the events")
	}

	// Without a category, or with an invalid one, the name is only held.
	if r := send("crawl", ""); r["product_id"] != nil {
		t.Fatalf("event without a category: %v", r)
	}
	send("map", "not a category!")
	if n := len(products("")); n != 1 {
		t.Fatalf("products after events without a valid category: %d", n)
	}
	held := owner.must(200, "GET", base+"/product-names?status=held", nil)["data"].([]any)
	if len(held) != 2 {
		t.Fatalf("held names: %v", held)
	}

	// A held name that later comes with a category becomes the product.
	if r := send("crawl", "web_data"); r["product_id"] == nil {
		t.Fatalf("held name with a category: %v", r)
	}
	if held := owner.must(200, "GET", base+"/product-names?status=held", nil)["data"].([]any); len(held) != 1 {
		t.Fatalf("held names after adding crawl: %v", held)
	}

	// With event names off as a source, nothing is added.
	owner.must(200, "PATCH", base+"/lifecycle", map[string]any{"sources": []string{"billing", "api", "manual"}})
	if r := send("isp_proxies", "proxies"); r["product_id"] != nil {
		t.Fatalf("event names are off: %v", r)
	}
	if n := len(products("")); n != 2 {
		t.Fatalf("products with event names off: %d", n)
	}
}
