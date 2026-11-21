package api_test

import (
	"context"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// A list of destinations takes up to 100 landers and 100 offers and pages:
// a full test of 25 landers against 31 offers saves and reads back whole,
// on a link, a rule-based path and a flow, and a click goes through both
// stages.
func TestTrackerRotation(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	var brand string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand
	host := ownTrackingDomain(t, e, ws, brand, "go.northpeak.example")

	var landers, offers []any
	for i := 1; i <= 25; i++ {
		l := owner.must(201, "POST", base+"/landers", map[string]any{"name": "Lander " + strconv.Itoa(i), "url": "https://pages.northpeak.example/l/" + strconv.Itoa(i)})
		landers = append(landers, map[string]any{"lander_id": l["id"]})
	}
	for i := 1; i <= 31; i++ {
		o := owner.must(201, "POST", base+"/offers", map[string]any{"name": "Offer " + strconv.Itoa(i), "url": "https://pages.northpeak.example/o/" + strconv.Itoa(i)})
		offers = append(offers, map[string]any{"offer_id": o["id"]})
	}
	all := append(append([]any{}, landers...), offers...)
	// whole fails unless a list read back is the 25 landers then the 31
	// offers, in order, each at the default weight.
	whole := func(what string, got []any) {
		t.Helper()
		if len(got) != 56 {
			t.Fatalf("%s: %d destinations, want 56", what, len(got))
		}
		for i, x := range got {
			d := x.(map[string]any)
			want := all[i].(map[string]any)
			if d["weight"] != float64(100) || d["lander_id"] != want["lander_id"] || d["offer_id"] != want["offer_id"] {
				t.Fatalf("%s[%d]: %v, want %v", what, i, d, want)
			}
		}
	}

	c := owner.must(201, "POST", base+"/tracking-campaigns", map[string]any{"name": "Rotation", "channel": "paid_ads", "source": "acme", "redirect_links": true})
	cpath := base + "/tracking-campaigns/" + c["code"].(string)
	link := owner.must(201, "POST", cpath+"/links", map[string]any{"url": "https://northpeak.example/own"})
	code := regexp.MustCompile(`/l/([a-z0-9]{8})$`).FindStringSubmatch(link["redirect_url"].(string))[1]
	lpath := cpath + "/links/" + link["id"].(string)

	// The link's own list.
	saved := owner.must(200, "PUT", lpath+"/destinations", map[string]any{"destinations": all})["destinations"].([]any)
	whole("saved", saved)
	whole("read back", owner.must(200, "GET", lpath+"/destinations", nil)["destinations"].([]any))

	// Saved again with their ids, every one is kept: none added, none
	// taken off.
	var again []any
	for i, x := range saved {
		d := map[string]any{"id": x.(map[string]any)["id"]}
		for k, v := range all[i].(map[string]any) {
			d[k] = v
		}
		again = append(again, d)
	}
	kept := owner.must(200, "PUT", lpath+"/destinations", map[string]any{"destinations": again})["destinations"].([]any)
	whole("saved again", kept)
	for i := range kept {
		if kept[i].(map[string]any)["id"] != saved[i].(map[string]any)["id"] {
			t.Fatalf("destination %d was made again, not kept", i)
		}
	}

	// A rule-based path's list, and a flow's.
	paths := owner.must(200, "PUT", lpath+"/paths", map[string]any{"paths": []any{map[string]any{
		"name": "Desktop", "conditions": []any{map[string]any{"field": "device", "op": "is", "values": []any{"desktop"}}}, "destinations": all,
	}}})["paths"].([]any)
	whole("path", paths[0].(map[string]any)["destinations"].([]any))
	owner.must(200, "PUT", lpath+"/paths", map[string]any{"paths": []any{}})
	f := owner.must(201, "POST", base+"/flows", map[string]any{"name": "Rotation", "destinations": all})
	whole("flow", f["destinations"].([]any))
	whole("flow set", owner.must(200, "PUT", base+"/flows/"+f["id"].(string)+"/destinations", map[string]any{"destinations": all})["destinations"].([]any))

	// The limits are a stage's own: 100 landers and 100 pages save, and one
	// more of either is refused whole, naming the stage.
	other := owner.must(201, "POST", cpath+"/links", map[string]any{"url": "https://northpeak.example/other"})
	opath := cpath + "/links/" + other["id"].(string) + "/destinations"
	stage := func(d map[string]any, n int) []any {
		var out []any
		for range n {
			out = append(out, d)
		}
		return out
	}
	lander, page := landers[0].(map[string]any), map[string]any{"url": "https://northpeak.example/p"}
	if got := owner.must(200, "PUT", opath, map[string]any{"destinations": append(stage(lander, 100), stage(page, 100)...)})["destinations"].([]any); len(got) != 200 {
		t.Fatalf("a full list: %d destinations, want 200", len(got))
	}
	for stageName, list := range map[string][]any{
		"landers":          append(stage(lander, 101), page),
		"offers and pages": append(stage(page, 101), lander),
	} {
		st, out := owner.do("PUT", opath, map[string]any{"destinations": list})
		if msg, _ := out["error"].(map[string]any)["message"].(string); st != 400 || !strings.Contains(msg, "at most 100 "+stageName) {
			t.Errorf("101 %s: %d %v", stageName, st, out)
		}
	}
	if got := owner.must(200, "GET", opath, nil)["destinations"].([]any); len(got) != 200 {
		t.Fatalf("a refused list changed the saved one: %d destinations, want 200", len(got))
	}

	// A click goes to one of the landers, and its call to action to one of
	// the offers; /click/31 to the 31st.
	follow := func(path string) *url.URL {
		t.Helper()
		req := httptest.NewRequest("GET", path, nil)
		req.Host = host
		req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.1 Safari/605.1.15")
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		if rec.Code != 302 {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body)
		}
		loc, err := url.Parse(rec.Header().Get("Location"))
		if err != nil {
			t.Fatal(err)
		}
		return loc
	}
	seenLanders, seenOffers := map[string]bool{}, map[string]bool{}
	var key string
	for range 300 {
		loc := follow("/l/" + code)
		if !strings.HasPrefix(loc.Path, "/l/") {
			t.Fatalf("a click went to %s, not a lander", loc)
		}
		key = loc.Query().Get("ofclk")
		seenLanders[loc.Path] = true
		next := follow("/click?ofclk=" + key)
		if !strings.HasPrefix(next.Path, "/o/") {
			t.Fatalf("a lander click went to %s, not an offer", next)
		}
		seenOffers[next.Path] = true
	}
	// Chance decides which ones, so ask that the rotation is wide, not
	// that 300 clicks reached every one.
	if len(seenLanders) < 20 || len(seenOffers) < 20 {
		t.Fatalf("300 clicks reached %d landers and %d offers", len(seenLanders), len(seenOffers))
	}
	if loc := follow("/click/31?ofclk=" + key); loc.Path != "/o/31" {
		t.Fatalf("/click/31 went to %s", loc)
	}

	// Landers with nothing after them still save: their pages may link on
	// by themselves. A /click from one goes to the link's own page.
	if got := owner.must(200, "PUT", lpath+"/destinations", map[string]any{"destinations": landers})["destinations"].([]any); len(got) != 25 {
		t.Fatalf("landers alone: %d destinations", len(got))
	}
	loc := follow("/l/" + code)
	if next := follow("/click?ofclk=" + loc.Query().Get("ofclk")); next.Host != "northpeak.example" || next.Path != "/own" {
		t.Fatalf("a lander click with no offers went to %s, want the link's own page", next)
	}
}
