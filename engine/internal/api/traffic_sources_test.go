package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/track"
)

// Traffic sources and custom click fields (plans/ad-tracker.md, T3): a
// source made from a template or by hand says which parameters of a
// tracker click fill which custom field; a campaign's links show the URL to
// paste into the network, and its redirect clicks keep the values and fill
// the tokens in their destination.
func TestTrafficSources(t *testing.T) {
	events := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	var brand string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand

	// The catalogue.
	cat := owner.must(200, "GET", base+"/traffic-source-templates", nil)
	var taboola map[string]any
	for _, x := range cat["data"].([]any) {
		if m := x.(map[string]any); m["id"] == "taboola" {
			taboola = m
		}
	}
	if taboola == nil || taboola["category"] != "native" || len(taboola["parameters"].([]any)) == 0 {
		t.Fatalf("templates: %v", cat)
	}

	// From a template, with its parameters.
	tab := owner.must(201, "POST", base+"/traffic-sources", map[string]any{"template": "taboola"})
	if tab["name"] != "Taboola" || tab["template"] != "taboola" || tab["click_id"].(map[string]any)["param"] != "tblci" ||
		len(tab["parameters"].([]any)) != len(taboola["parameters"].([]any)) || tab["campaigns"] != float64(0) {
		t.Fatalf("from template: %v", tab)
	}
	owner.must(409, "POST", base+"/traffic-sources", map[string]any{"template": "taboola"})
	owner.must(400, "POST", base+"/traffic-sources", map[string]any{"template": "no_such_network"})

	// A custom one: the zone into field 1 (or zone_id), the network's click
	// id and cost, and a postback with Outflare's tokens.
	custom := map[string]any{
		"name":     "Acme Push",
		"click_id": map[string]any{"param": "clickid", "token": "{clickid}"},
		"cost":     map[string]any{"param": "cost", "token": "{cost}"},
		"parameters": []any{
			map[string]any{"name": "Zone", "param": "zone", "token": "{zoneid}", "field": 1, "aliases": []string{"zone_id"}},
			map[string]any{"name": "OS", "param": "os", "token": "{os}", "field": 4},
		},
		"postback": map[string]any{"url": "https://acme.example/pb?clickid={source_click_id}&payout={payout|80%}"},
	}
	src := owner.must(201, "POST", base+"/traffic-sources", custom)
	srcID := src["id"].(string)
	if src["postback"].(map[string]any)["method"] != "GET" || src["currency"] != "USD" {
		t.Fatalf("custom: %v", src)
	}
	for name, change := range map[string]map[string]any{
		"replace left in":    {"postback": map[string]any{"url": "https://acme.example/pb?key=REPLACE&c={source_click_id}"}},
		"unknown token":      {"postback": map[string]any{"url": "https://acme.example/pb?c={clickid}"}},
		"not a web address":  {"postback": map[string]any{"url": "ftp://acme.example/pb?c={source_click_id}"}},
		"field twice":        {"parameters": []any{map[string]any{"name": "A", "param": "a", "field": 2}, map[string]any{"name": "B", "param": "b", "field": 2}}},
		"field out of range": {"parameters": []any{map[string]any{"name": "A", "param": "a", "field": 21}}},
		"param is click id":  {"parameters": []any{map[string]any{"name": "A", "param": "clickid", "field": 2}}},
		"token with space":   {"parameters": []any{map[string]any{"name": "A", "param": "a", "token": "{a b}", "field": 2}}},
		"currency":           {"currency": "dollars"},
	} {
		body := map[string]any{"name": "Bad " + name}
		for k, v := range custom {
			if k != "name" {
				body[k] = v
			}
		}
		for k, v := range change {
			body[k] = v
		}
		owner.must(400, "POST", base+"/traffic-sources", body)
	}

	// A campaign that buys from it; its link shows the URL for the network.
	c := owner.must(201, "POST", base+"/tracking-campaigns", map[string]any{
		"name": "Push spring", "channel": "paid_ads", "source": "acme", "redirect_links": true, "traffic_source": srcID,
	})
	if c["traffic_source_id"] != srcID {
		t.Fatalf("campaign: %v", c)
	}
	// A token that isn't one (a typo) is refused, not left unfilled.
	owner.must(400, "POST", base+"/tracking-campaigns/"+c["code"].(string)+"/links",
		map[string]any{"url": "https://northpeak.example/offer?z={feild1}"})
	link := owner.must(201, "POST", base+"/tracking-campaigns/"+c["code"].(string)+"/links", map[string]any{
		"url": "https://northpeak.example/offer?z={field1}&c={click_id}&src={source_click_id}&geo={country}",
	})
	cu, _ := link["campaign_url"].(string)
	code := regexp.MustCompile(`/l/([a-z0-9]{8})`).FindStringSubmatch(cu)
	if code == nil || !strings.HasSuffix(cu, "?clickid={clickid}&cost={cost}&zone={zoneid}&os={os}") {
		t.Fatalf("campaign_url = %q", cu)
	}
	if got := owner.must(200, "GET", base+"/traffic-sources/"+srcID, nil); got["campaigns"] != float64(1) {
		t.Fatalf("used by: %v", got)
	}

	// A click from the network: the zone by its alias, the click id and the
	// cost; the destination gets this click's values.
	follow := func(code, query string) *url.URL {
		t.Helper()
		req := httptest.NewRequest("GET", "/l/"+code+"?"+query, nil)
		req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.1 Safari/605.1.15")
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		if rec.Code != http.StatusFound {
			t.Fatalf("redirect = %d", rec.Code)
		}
		loc, err := url.Parse(rec.Header().Get("Location"))
		if err != nil {
			t.Fatal(err)
		}
		return loc
	}
	loc := follow(code[1], "zone_id=z+55&clickid=net-abc&cost=0.0125&os=android&other=1")
	q := loc.Query()
	key := q.Get("ofclk")
	if q.Get("z") != "z 55" || q.Get("c") != key || key == "" || q.Get("src") != "net-abc" {
		t.Fatalf("destination = %s", loc)
	}
	if err := e.srv.FlushClicks(ctx); err != nil {
		t.Fatal(err)
	}
	var fields []byte
	var sourceClick string
	var cost int64
	if err := e.pool.QueryRow(ctx, `SELECT fields, source_click_id, cost_micros FROM tracking_link_clicks WHERE click_key = $1`, key).
		Scan(&fields, &sourceClick, &cost); err != nil {
		t.Fatal(err)
	}
	var f map[string]string
	if err := json.Unmarshal(fields, &f); err != nil || f["1"] != "z 55" || f["4"] != "android" || len(f) != 2 || sourceClick != "net-abc" || cost != 12500 {
		t.Fatalf("click: %s %q %d", fields, sourceClick, cost)
	}

	// Without a traffic source, sub1 to sub20 fill the fields.
	plain := owner.must(201, "POST", base+"/tracking-campaigns", map[string]any{"name": "Newsletter swap", "channel": "referral", "source": "swap", "redirect_links": true})
	pl := owner.must(201, "POST", base+"/tracking-campaigns/"+plain["code"].(string)+"/links", map[string]any{"url": "https://northpeak.example/"})
	if pl["campaign_url"] != nil {
		t.Fatalf("no source, campaign_url = %v", pl["campaign_url"])
	}
	pcode := regexp.MustCompile(`/l/([a-z0-9]{8})$`).FindStringSubmatch(pl["redirect_url"].(string))[1]
	key = follow(pcode, "sub1=a&sub3=c&sub21=no").Query().Get("ofclk")
	if err := e.srv.FlushClicks(ctx); err != nil {
		t.Fatal(err)
	}
	if err := e.pool.QueryRow(ctx, `SELECT fields FROM tracking_link_clicks WHERE click_key = $1`, key).Scan(&fields); err != nil {
		t.Fatal(err)
	}
	if string(fields) != `{"1": "a", "3": "c"}` {
		t.Fatalf("sub fields = %s", fields)
	}

	// A source is changed and archived; an archived one can't be given to
	// a campaign, and another brand's never can.
	upd := owner.must(200, "PATCH", base+"/traffic-sources/"+srcID, map[string]any{"name": "Acme Push EU", "currency": "EUR"})
	if upd["name"] != "Acme Push EU" || upd["currency"] != "EUR" || len(upd["parameters"].([]any)) != 2 {
		t.Fatalf("updated: %v", upd)
	}
	owner.must(400, "PATCH", base+"/traffic-sources/"+srcID, map[string]any{"template": "taboola"})
	owner.must(200, "PATCH", base+"/traffic-sources/"+srcID, map[string]any{"status": "archived"})
	owner.must(400, "PATCH", base+"/tracking-campaigns/"+plain["code"].(string), map[string]any{"traffic_source": srcID})
	if l := owner.must(200, "GET", base+"/traffic-sources?status=archived", nil)["data"].([]any); len(l) != 1 {
		t.Fatalf("archived: %v", l)
	}
	other := owner.must(201, "POST", "/v1/workspaces/"+ws+"/brands", map[string]any{"name": "Repocket"})
	owner.must(404, "GET", "/v1/brands/"+other["id"].(string)+"/traffic-sources/"+srcID, nil)
	owner.must(400, "PATCH", base+"/tracking-campaigns/"+plain["code"].(string), map[string]any{"traffic_source": tab["id"].(string) + "x"})
	owner.must(200, "PATCH", base+"/tracking-campaigns/"+c["code"].(string), map[string]any{"traffic_source": ""})

	// Each change is an event.
	for _, name := range []string{"traffic_source.created", "traffic_source.updated", "traffic_source.archived"} {
		found := false
		for _, ev := range events() {
			found = found || ev.Name == name
		}
		if !found {
			t.Errorf("no %s event", name)
		}
	}
}
