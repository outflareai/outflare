package api_test

import (
	"context"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/geoip/geoiptest"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Offers (plans/ad-tracker.md, T6a): a link's destination can be an offer,
// whose network adds the click id to its URL; a conversion on its click gets
// the offer's payout (by country, or always when fixed) and counts against
// its daily cap, after which clicks go to its fallback offer.
func TestTrackerOffers(t *testing.T) {
	geoiptest.Use(t)
	events := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	var brand string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand
	host := ownTrackingDomain(t, e, ws, brand, "go.northpeak.example")
	springfield := geoiptest.SpringfieldIP + ":51000"

	// The catalogue, and a source made from a template takes its click id
	// parameter and writes its postback URL in the network's macros.
	cat := owner.must(200, "GET", base+"/affiliate-network-templates", nil)
	tpls := cat["data"].([]any)
	if len(tpls) == 0 {
		t.Fatal("no affiliate-network templates")
	}
	tpl := tpls[0].(map[string]any)
	fromTpl := owner.must(201, "POST", base+"/postback-sources", map[string]any{"name": "From a template", "network": tpl["id"]})
	if fromTpl["click_id_param"] != tpl["click_id_param"] ||
		!strings.Contains(fromTpl["postback_url"].(string), "cid="+tpl["macros"].(map[string]any)["cid"].(string)) {
		t.Fatalf("source from %v: %v", tpl, fromTpl)
	}
	owner.must(400, "POST", base+"/postback-sources", map[string]any{"name": "Nope", "network": "no_such_network"})

	src := owner.must(201, "POST", base+"/postback-sources", map[string]any{"name": "Acme Network", "click_id_param": "aff_sub"})
	sid := src["id"].(string)
	token := strings.TrimPrefix(strings.SplitN(src["postback_url"].(string), "?", 2)[0], "https://"+host)

	// Offers: B is the fallback, A has a fixed payout by country and a cap
	// of one conversion a day.
	b := owner.must(201, "POST", base+"/offers", map[string]any{"name": "Offer B", "url": "https://offers.northpeak.example/b", "source_id": sid,
		"payout": "1.5", "conversion_type": "lead"})
	a := owner.must(201, "POST", base+"/offers", map[string]any{"name": "Offer A", "url": "https://offers.northpeak.example/a?o=7", "source_id": sid,
		"payout_mode": "fixed", "payout": 2, "country_payouts": map[string]any{"us": "3.25"}, "daily_cap": 1, "fallback_offer_id": b["id"]})
	if a["tracking_url"] != "https://offers.northpeak.example/a?o=7&aff_sub={click_id}" || a["missing_click_id"] != false ||
		a["country_payouts"].(map[string]any)["US"] != "3.25" || a["cap_kind"] != "conversions" {
		t.Fatalf("offer A: %v", a)
	}
	bare := owner.must(201, "POST", base+"/offers", map[string]any{"name": "Own page", "url": "https://northpeak.example/buy"})
	if bare["missing_click_id"] != true {
		t.Fatalf("an offer without a click id: %v", bare)
	}
	for name, body := range map[string]map[string]any{
		"payout":   {"name": "x1", "url": "https://northpeak.example/x", "payout": -1},
		"country":  {"name": "x2", "url": "https://northpeak.example/x", "country_payouts": map[string]any{"USA": 1}},
		"mode":     {"name": "x3", "url": "https://northpeak.example/x", "payout_mode": "cpa"},
		"source":   {"name": "x4", "url": "https://northpeak.example/x", "source_id": "00000000-0000-0000-0000-000000000000"},
		"token":    {"name": "x5", "url": "https://northpeak.example/x?s={nope}"},
		"cap kind": {"name": "x6", "url": "https://northpeak.example/x", "cap_kind": "sales"},
	} {
		if st, _ := owner.do("POST", base+"/offers", body); st != 400 {
			t.Errorf("%s: %d, want 400", name, st)
		}
	}
	owner.must(409, "POST", base+"/offers", map[string]any{"name": "offer a", "url": "https://northpeak.example/x"})
	owner.must(400, "PATCH", base+"/offers/"+a["id"].(string), map[string]any{"fallback_offer_id": a["id"]})

	// A link whose one destination is offer A.
	c := owner.must(201, "POST", base+"/tracking-campaigns", map[string]any{"name": "Offer push", "channel": "paid_ads", "source": "acme", "redirect_links": true})
	cpath := base + "/tracking-campaigns/" + c["code"].(string)
	l := owner.must(201, "POST", cpath+"/links", map[string]any{"url": "https://northpeak.example/own"})
	code := regexp.MustCompile(`/l/([a-z0-9]{8})$`).FindStringSubmatch(l["redirect_url"].(string))[1]
	dests := owner.must(200, "PUT", cpath+"/links/"+l["id"].(string)+"/destinations", map[string]any{"destinations": []any{
		map[string]any{"offer_id": a["id"]},
	}})["destinations"].([]any)
	if d := dests[0].(map[string]any); d["offer_id"] != a["id"] || d["offer_name"] != "Offer A" || d["url"] != "https://offers.northpeak.example/a?o=7" {
		t.Fatalf("destinations: %v", dests)
	}
	owner.must(400, "PUT", cpath+"/links/"+l["id"].(string)+"/destinations", map[string]any{"destinations": []any{
		map[string]any{"offer_id": "00000000-0000-0000-0000-000000000000"},
	}})
	get := func(addr, path string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest("GET", path, nil)
		req.Host = host
		req.RemoteAddr = addr
		req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.1 Safari/605.1.15")
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		return rec
	}
	click := func() *url.URL {
		t.Helper()
		loc, err := url.Parse(get(springfield, "/l/"+code).Header().Get("Location"))
		if err != nil {
			t.Fatal(err)
		}
		return loc
	}

	// The click goes to offer A with the click id in the network's
	// parameter; its conversion gets A's US payout whatever the postback
	// says, and uses up A's cap.
	first := click()
	key := first.Query().Get("ofclk")
	if first.Path != "/a" || first.Query().Get("aff_sub") != key || first.Query().Get("o") != "7" {
		t.Fatalf("first click went to %s", first)
	}
	if rec := get(springfield, token+"?cid="+key+"&payout=9&txid=A-1"); rec.Code != 200 {
		t.Fatalf("postback: %d %s", rec.Code, rec.Body)
	}
	conv := owner.must(200, "GET", base+"/tracker-conversions?offer="+a["id"].(string), nil)["data"].([]any)
	if len(conv) != 1 || conv[0].(map[string]any)["payout"] != "3.25" || conv[0].(map[string]any)["offer_id"] != a["id"] {
		t.Fatalf("offer A's conversions: %v", conv)
	}
	if got := owner.must(200, "GET", base+"/offers/"+a["id"].(string), nil); got["conversions_today"] != float64(1) || got["cap_reached"] != true ||
		got["clicks_today"] != float64(1) || got["destinations"] != float64(1) {
		t.Fatalf("offer A after a conversion: %v", got)
	}

	// Capped, its clicks go to the fallback offer B, whose payout fills a
	// postback that sends none, with B's conversion type.
	second := click()
	if second.Path != "/b" || second.Query().Get("aff_sub") == "" {
		t.Fatalf("second click went to %s", second)
	}
	get(springfield, token+"?cid="+second.Query().Get("ofclk")+"&txid=B-1")
	third := click()
	get(springfield, token+"?cid="+third.Query().Get("ofclk")+"&txid=B-2&payout=4")
	byTx := map[string]map[string]any{}
	for _, x := range owner.must(200, "GET", base+"/tracker-conversions?offer="+b["id"].(string), nil)["data"].([]any) {
		m := x.(map[string]any)
		byTx[m["transaction_id"].(string)] = m
	}
	if byTx["B-1"]["payout"] != "1.5" || byTx["B-1"]["type"] != "lead" || byTx["B-2"]["payout"] != "4" {
		t.Fatalf("offer B's conversions: %v", byTx)
	}

	// A new URL goes to the destinations; an offer in use can't be archived.
	owner.must(200, "PATCH", base+"/offers/"+a["id"].(string), map[string]any{"url": "https://offers.northpeak.example/a2"})
	dests = owner.must(200, "GET", cpath+"/links/"+l["id"].(string)+"/destinations", nil)["destinations"].([]any)
	if dests[0].(map[string]any)["url"] != "https://offers.northpeak.example/a2" {
		t.Fatalf("destination after the offer's new URL: %v", dests)
	}
	owner.must(400, "PATCH", base+"/offers/"+a["id"].(string), map[string]any{"status": "archived"})
	owner.must(200, "PATCH", base+"/offers/"+bare["id"].(string), map[string]any{"status": "archived"})
	if n := len(owner.must(200, "GET", base+"/offers", nil)["data"].([]any)); n != 2 {
		t.Fatalf("active offers: %d, want 2", n)
	}
	if n := len(owner.must(200, "GET", base+"/offers?status=all", nil)["data"].([]any)); n != 3 {
		t.Fatalf("all offers: %d, want 3", n)
	}

	// Another brand sees none of them, and can't use them.
	other := owner.must(201, "POST", "/v1/workspaces/"+ws+"/brands", map[string]any{"name": "Repocket"})
	obase := "/v1/brands/" + other["id"].(string)
	if n := len(owner.must(200, "GET", obase+"/offers", nil)["data"].([]any)); n != 0 {
		t.Fatalf("another brand sees %d offers", n)
	}
	owner.must(404, "GET", obase+"/offers/"+a["id"].(string), nil)
	owner.must(400, "POST", obase+"/offers", map[string]any{"name": "Theirs", "url": "https://northpeak.example/x", "source_id": sid})
	owner.must(400, "POST", obase+"/offers", map[string]any{"name": "Theirs", "url": "https://northpeak.example/x", "fallback_offer_id": b["id"]})

	found := map[string]bool{}
	for _, ev := range events() {
		found[ev.Name] = true
	}
	for _, name := range []string{"tracker_offer.created", "tracker_offer.updated", "tracker_offer.archived"} {
		if !found[name] {
			t.Errorf("no %s event", name)
		}
	}
}
