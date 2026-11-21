package api_test

import (
	"context"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

// The click's own parameter (ofclk) and the campaign's tags reach every
// destination once, whatever the saved URL says, and an offer's "missing
// click id" warning follows what the redirect sends.
func TestTrackerClickToken(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	var brand string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `UPDATE brands SET domains = '{northpeak.example}' WHERE id = $1`, brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand
	host := ownTrackingDomain(t, e, ws, brand, "go.northpeak.example")

	// Saving: ofclk={click_id} says what the redirect does anyway, so it is
	// taken and dropped with the URL's own utm_* and ofc; the rest stays.
	lander := owner.must(201, "POST", base+"/landers", map[string]any{"name": "Review",
		"url": "https://northpeak.example/review?ofclk={click_id}&tag=a&tag=b&utm_source=mine&ofc=zzzzzzzzzz#top"})
	if lander["url"] != "https://northpeak.example/review?tag=a&tag=b#top" {
		t.Fatalf("lander URL saved as %v", lander["url"])
	}
	// Any other ofclk names a different click: refused everywhere a
	// destination is saved, with the reason.
	for _, bad := range []string{"ofclk=abcdefghjkmnpqrs", "ofclk=", "ofclk={source_click_id}", "ofclk={click_id}&ofclk=x"} {
		u := "https://northpeak.example/x?" + bad
		for name, call := range map[string]func() (int, map[string]any){
			"lander": func() (int, map[string]any) {
				return owner.do("POST", base+"/landers", map[string]any{"name": "bad", "url": u})
			},
			"offer": func() (int, map[string]any) {
				return owner.do("POST", base+"/offers", map[string]any{"name": "bad", "url": u})
			},
			"campaign": func() (int, map[string]any) {
				return owner.do("POST", base+"/tracking-campaigns", map[string]any{"name": "bad", "channel": "paid_ads", "source": "acme", "landing_url": u})
			},
		} {
			st, body := call()
			msg, _ := body["error"].(map[string]any)["message"].(string)
			if st != 400 || !strings.Contains(msg, "ofclk is the click id Outflare adds") {
				t.Errorf("%s with %s: %d %v", name, bad, st, body)
			}
		}
	}

	// The warning. A page on the brand's own domain gets the click id from
	// the redirect, so nothing is missing; someone else's page needs
	// {click_id}; the setting says so when the address can't.
	offer := func(name, u string, more map[string]any) map[string]any {
		t.Helper()
		body := map[string]any{"name": name, "url": u}
		for k, v := range more {
			body[k] = v
		}
		return owner.must(201, "POST", base+"/offers", body)
	}
	want := func(o map[string]any, delivery, via string, missing bool) {
		t.Helper()
		if o["click_id_delivery"] != delivery || o["click_id_via"] != via || o["missing_click_id"] != missing {
			t.Errorf("%v: delivery %v via %v missing %v, want %s %s %v", o["name"], o["click_id_delivery"], o["click_id_via"], o["missing_click_id"], delivery, via, missing)
		}
	}
	own := offer("Own page", "https://northpeak.example/buy?plan=pro", nil)
	want(own, "auto", "ofclk", false)
	want(offer("Own tracking domain", "https://go.northpeak.example/buy", nil), "auto", "ofclk", false)
	network := offer("Network", "https://network.example/o?id=7&subid={click_id}", nil)
	want(network, "auto", "template", false)
	bare := offer("Network, no sub id", "https://network.example/o?id=8", nil)
	want(bare, "auto", "none", true)
	want(offer("Elsewhere, ours", "https://pages.example/buy", map[string]any{"click_id_delivery": "first_party"}), "first_party", "ofclk", false)
	want(offer("Ours, through a network", "https://northpeak.example/n", map[string]any{"click_id_delivery": "affiliate"}), "affiliate", "none", true)
	want(offer("No conversions", "https://network.example/free", map[string]any{"click_id_delivery": "unattributed"}), "unattributed", "none", false)
	owner.must(400, "POST", base+"/offers", map[string]any{"name": "x", "url": "https://northpeak.example/x", "click_id_delivery": "magic"})
	want(owner.must(200, "PATCH", base+"/offers/"+bare["id"].(string), map[string]any{"click_id_delivery": "unattributed"}), "unattributed", "none", false)
	want(owner.must(200, "PATCH", base+"/offers/"+bare["id"].(string), map[string]any{"click_id_delivery": "auto"}), "auto", "none", true)
	listed := owner.must(200, "GET", base+"/offers", nil)["data"].([]any)
	for _, o := range listed {
		if o := o.(map[string]any); o["id"] == own["id"] {
			want(o, "auto", "ofclk", false)
		}
	}

	// A link whose list is the lander, the brand's own offer and the
	// network's; and one with no list, whose own page is the fallback.
	c := owner.must(201, "POST", base+"/tracking-campaigns", map[string]any{"name": "Spring", "channel": "paid_ads", "source": "acme", "redirect_links": true})
	cpath := base + "/tracking-campaigns/" + c["code"].(string)
	link := owner.must(201, "POST", cpath+"/links", map[string]any{"url": "https://northpeak.example/own?ofclk={click_id}&ref=1#end", "content": "blue"})
	if link["url"] != "https://northpeak.example/own?ref=1#end" {
		t.Fatalf("link URL saved as %v", link["url"])
	}
	code := regexp.MustCompile(`/l/([a-z0-9]{8})$`).FindStringSubmatch(link["redirect_url"].(string))[1]
	owner.must(200, "PUT", cpath+"/links/"+link["id"].(string)+"/destinations", map[string]any{"destinations": []any{
		map[string]any{"lander_id": lander["id"]}, map[string]any{"offer_id": own["id"]}, map[string]any{"offer_id": network["id"]},
	}})
	plain := owner.must(201, "POST", cpath+"/links", map[string]any{"url": "https://northpeak.example/plain?a=1", "content": "red"})
	plainCode := regexp.MustCompile(`/l/([a-z0-9]{8})$`).FindStringSubmatch(plain["redirect_url"].(string))[1]

	// URLs saved before ofclk was refused still redirect, with one click id.
	for _, q := range []string{
		`UPDATE tracker_landers SET url = url || '&ofclk={click_id}' WHERE id = '` + lander["id"].(string) + `'`,
		`UPDATE tracking_link_destinations SET url = 'https://northpeak.example/review?tag=a&tag=b&ofclk={click_id}#top' WHERE lander_id = '` + lander["id"].(string) + `'`,
		`UPDATE tracker_offers SET url = url || '&ofclk=zzzzzzzzzzzzzzzz' WHERE id = '` + own["id"].(string) + `'`,
		`UPDATE tracking_link_destinations SET url = url || '&ofclk=zzzzzzzzzzzzzzzz' WHERE offer_id = '` + own["id"].(string) + `'`,
	} {
		if _, err := e.pool.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}

	get := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", path, nil)
		req.Host = host
		req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.1 Safari/605.1.15")
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		return rec
	}
	follow := func(path string) *url.URL {
		t.Helper()
		rec := get(path)
		if rec.Code != 302 {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body)
		}
		loc, err := url.Parse(rec.Header().Get("Location"))
		if err != nil {
			t.Fatal(err)
		}
		return loc
	}
	// managed checks a destination carries each of Outflare's parameters
	// once, and returns its click id.
	managed := func(loc *url.URL, content string) string {
		t.Helper()
		q := loc.Query()
		for name, v := range map[string]string{"ofc": c["code"].(string), "utm_source": "acme", "utm_campaign": c["slug"].(string), "utm_content": content} {
			if got := q[name]; len(got) != 1 || got[0] != v {
				t.Errorf("%s: %s = %v, want one %q", loc, name, got, v)
			}
		}
		if got := q["ofclk"]; len(got) != 1 || !regexp.MustCompile(`^[a-z0-9]{16}$`).MatchString(got[0]) {
			t.Fatalf("%s: ofclk = %v, want one click id", loc, got)
		}
		return q.Get("ofclk")
	}

	// The lander, from a URL that names ofclk itself: one click id.
	toLander := follow("/l/" + code + "?ofclk=zzzzzzzzzzzzzzzz&utm_source=theirs")
	key := managed(toLander, "blue")
	if toLander.Path != "/review" || len(toLander.Query()["tag"]) != 2 || toLander.Fragment != "top" {
		t.Fatalf("the lander click went to %s", toLander)
	}
	// Its calls to action, as a page opens them in the same tab or a new
	// one: the same click and campaign, once, on the brand's own offer...
	toOwn := follow("/click/1?ofclk=" + key)
	if k := managed(toOwn, "blue"); k != key || toOwn.Path != "/buy" || toOwn.Query().Get("plan") != "pro" {
		t.Fatalf("/click/1 went to %s, for click %s", toOwn, key)
	}
	// ...and on the network's, whose own parameter keeps the click id too.
	toNetwork := follow("/click/2?ofclk=" + key + "&ofclk=" + key)
	if k := managed(toNetwork, "blue"); k != key || toNetwork.Host != "network.example" || toNetwork.Query().Get("subid") != key || toNetwork.Query().Get("id") != "7" {
		t.Fatalf("/click/2 went to %s, for click %s", toNetwork, key)
	}

	// Another visit is another click, start to end.
	key2 := managed(follow("/l/"+code), "blue")
	if key2 == key {
		t.Fatal("two visits share a click id")
	}
	if k := managed(follow("/click/2?ofclk="+key2), "blue"); k != key2 {
		t.Fatalf("the second visit's offer got click %s, not %s", k, key2)
	}

	// The fallback: a link's own page follows the same rules.
	toPlain := follow("/l/" + plainCode)
	if managed(toPlain, "red"); toPlain.Path != "/plain" || toPlain.Query().Get("a") != "1" {
		t.Fatalf("the plain link went to %s", toPlain)
	}

	// Two different click ids, an empty one or a malformed one go nowhere,
	// rather than to an offer for the wrong click.
	for name, path := range map[string]string{
		"two click ids": "/click/1?ofclk=" + key + "&ofclk=" + key2,
		"reversed":      "/click/1?ofclk=" + key2 + "&ofclk=" + key,
		"empty":         "/click/1?ofclk=",
		"with an empty": "/click/1?ofclk=" + key + "&ofclk=",
		"malformed":     "/click/1?ofclk=" + key + "x",
	} {
		if rec := get(path); rec.Code != 404 {
			t.Errorf("%s: %d, want 404", name, rec.Code)
		}
	}

	// The preview: the same address the redirect sends, for a made-up click,
	// from the URL as written. It says who set each parameter, and it
	// counts nothing.
	var before int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM tracking_link_clicks WHERE workspace_id = $1`, ws).Scan(&before); err != nil {
		t.Fatal(err)
	}
	preview := func(want int, q url.Values) map[string]any {
		t.Helper()
		return owner.must(want, "GET", base+"/destination-preview?"+q.Encode(), nil)
	}
	p := preview(200, url.Values{"destination": {"https://northpeak.example/plain?a=1&ofclk={click_id}&utm_source=mine"}, "campaign": {c["code"].(string)}, "content": {"red"}})
	sample := p["sample_click_id"].(string)
	if regexp.MustCompile(`^[abcdefghijkmnpqrstuvwxyz2-9]{16}$`).MatchString(sample) {
		t.Fatalf("the sample click id %q could be a real click's", sample)
	}
	real := toPlain.Query().Get("ofclk")
	if got, want := p["effective_url"], strings.Replace(toPlain.String(), real, sample, 1); got != want {
		t.Fatalf("the preview says %v, the redirect sent %v", got, want)
	}
	if p["saved_url"] != "https://northpeak.example/plain?a=1" || p["campaign_id"] != c["id"] {
		t.Fatalf("preview: %v", p)
	}
	if got := strings.Join(anyStrings(p["replaced"]), ","); got != "ofclk,utm_source" {
		t.Errorf("replaced = %s", got)
	}
	sources := map[string]string{}
	for _, par := range p["parameters"].([]any) {
		par := par.(map[string]any)
		if _, twice := sources[par["name"].(string)]; twice {
			t.Errorf("the preview has %v twice", par["name"])
		}
		sources[par["name"].(string)] = par["source"].(string)
	}
	for name, source := range map[string]string{"a": "url", "utm_source": "campaign", "utm_campaign": "campaign", "ofc": "campaign", "utm_content": "link", "ofclk": "click"} {
		if sources[name] != source {
			t.Errorf("%s is from %q, want %q", name, sources[name], source)
		}
	}
	// A network's own parameter keeps the click id, and is said to be a
	// token; with no campaign there are no tags, only the click id.
	p = preview(200, url.Values{"destination": {"https://network.example/o?id=7&subid={click_id}#x"}})
	if p["effective_url"] != "https://network.example/o?id=7&subid="+sample+"&ofclk="+sample+"#x" || p["campaign_id"] != nil {
		t.Fatalf("preview without a campaign: %v", p)
	}
	if par := p["parameters"].([]any)[1].(map[string]any); par["name"] != "subid" || par["source"] != "token" {
		t.Errorf("subid: %v", par)
	}
	// What can't be saved can't be previewed, for the same reason.
	if body := preview(400, url.Values{"destination": {"https://northpeak.example/x?ofclk=abcdefghjkmnpqrs"}}); !strings.Contains(body["error"].(map[string]any)["message"].(string), "ofclk is the click id Outflare adds") {
		t.Errorf("a fixed ofclk: %v", body)
	}
	preview(400, url.Values{})
	preview(400, url.Values{"destination": {"not a url"}})
	preview(404, url.Values{"destination": {"https://northpeak.example/x"}, "campaign": {"nosuchcode"}})
	var after int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM tracking_link_clicks WHERE workspace_id = $1`, ws).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Errorf("previews counted %d clicks", after-before)
	}
}
