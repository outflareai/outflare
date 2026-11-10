package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"testing"

	"github.com/outflareai/outflare/engine/internal/geoip/geoiptest"
)

// Partner short-link clicks are kept with redirect link clicks
// (plans/ad-tracker.md, T2): batched, with where the visitor is and a keyed
// address hash, under the partner instead of a link. Click ids from before
// the merge still credit.
func TestPartnerClicksMerged(t *testing.T) {
	geoiptest.Use(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	owner.must(201, "POST", base+"/partner-programs", map[string]any{"name": "Northpeak affiliates", "kind": "affiliate", "landing_url": "https://northpeak.example/pricing"})
	ana := owner.must(201, "POST", base+"/partners", map[string]any{"program": "northpeak-affiliates", "email": "ana@example.com"})
	anaID := ana["id"].(string)

	click := func(ua, ip string) string {
		t.Helper()
		req := httptest.NewRequest("GET", "/r/"+ws+"/ana?sub=yt-review", nil)
		req.Header.Set("User-Agent", ua)
		req.Header.Set("Referer", "https://videos.example/watch")
		req.RemoteAddr = ip + ":40000"
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		if rec.Code != http.StatusFound {
			t.Fatalf("redirect = %d", rec.Code)
		}
		u, _ := url.Parse(rec.Header().Get("Location"))
		key := u.Query().Get("ref_click")
		if !regexp.MustCompile(`^[a-z0-9]{16}$`).MatchString(key) || u.Query().Get("ofclk") != key || u.Query().Get("ref") != "ana" {
			t.Fatalf("location = %v", u)
		}
		return key
	}
	const person = "Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Mobile/15E148 Safari/604.1"
	key := click(person, geoiptest.SpringfieldIP)
	click("Mozilla/5.0 (compatible; Slackbot-LinkExpanding 1.0)", geoiptest.SpringfieldIP)
	if err := e.srv.FlushClicks(ctx); err != nil {
		t.Fatal(err)
	}

	// The click is under the partner, with its sub id, the referring site,
	// where the visitor is and their device.
	var membership, sub, ref, city, model, hash string
	var link *string
	if err := e.pool.QueryRow(ctx, `SELECT membership_id::text, link_id::text, sub_id, referrer_host, city, device_model, ip_hash
		FROM tracking_link_clicks WHERE click_key = $1`, key).Scan(&membership, &link, &sub, &ref, &city, &model, &hash); err != nil {
		t.Fatal(err)
	}
	if membership != anaID || link != nil || sub != "yt-review" || ref != "videos.example" || city != "Springfield" || model != "iPhone" || hash == "" {
		t.Fatalf("click: membership %s link %v sub %q ref %q city %q model %q hash %q", membership, link, sub, ref, city, model, hash)
	}

	// The link scanner's click is kept but not counted.
	got := owner.must(200, "GET", base+"/partners/"+anaID, nil)
	if got["stats"].(map[string]any)["clicks"] != float64(1) {
		t.Fatalf("partner clicks = %v", got["stats"])
	}

	// The key credits a sign-up, and the referral points at the click.
	conv := base + "/partner-conversions"
	lead := owner.must(201, "POST", conv, map[string]any{"type": "lead", "customer_id": "u-1", "email": "bo@example.com", "click_id": key})
	if lead["credited"] != true || lead["partner_id"] != anaID {
		t.Fatalf("lead = %v", lead)
	}
	var pointsAt string
	if err := e.pool.QueryRow(ctx, `SELECT k.click_key FROM partner_referrals f JOIN tracking_link_clicks k ON k.id = f.link_click_id
		WHERE f.id = $1`, lead["referral_id"]).Scan(&pointsAt); err != nil || pointsAt != key {
		t.Fatalf("referral click = %q, %v", pointsAt, err)
	}

	// A click from before the merge, still in partner_clicks, credits by
	// its uuid.
	var old string
	if err := e.pool.QueryRow(ctx, `INSERT INTO partner_clicks (workspace_id, membership_id, sub_id) VALUES ($1, $2, 'old-sub') RETURNING id::text`,
		ws, anaID).Scan(&old); err != nil {
		t.Fatal(err)
	}
	lead = owner.must(201, "POST", conv, map[string]any{"type": "lead", "customer_id": "u-2", "email": "cy@example.com", "click_id": old})
	if lead["credited"] != true || lead["partner_id"] != anaID {
		t.Fatalf("lead from an old click = %v", lead)
	}
	if got := owner.must(200, "GET", base+"/partners/"+anaID, nil); got["stats"].(map[string]any)["clicks"] != float64(2) {
		t.Fatalf("clicks with an old one = %v", got["stats"])
	}

	// A click id in neither form is refused.
	owner.must(400, "POST", conv, map[string]any{"type": "lead", "customer_id": "u-3", "email": "di@example.com", "click_id": "not-a-click"})
}
