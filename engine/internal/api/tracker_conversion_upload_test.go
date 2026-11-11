package api_test

import (
	"context"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/track"
)

// Conversions by pixel, script and upload (plans/ad-tracker.md, T5b).
func TestTrackerConversionUploads(t *testing.T) {
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
	c := owner.must(201, "POST", base+"/tracking-campaigns", map[string]any{"name": "Offer push", "channel": "paid_ads", "source": "acme", "redirect_links": true})
	l := owner.must(201, "POST", base+"/tracking-campaigns/"+c["code"].(string)+"/links", map[string]any{"url": "https://offers.northpeak.example/o1"})
	code := regexp.MustCompile(`/l/([a-z0-9]{8})$`).FindStringSubmatch(l["redirect_url"].(string))[1]
	get := func(h, path string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest("GET", path, nil)
		req.Host = h
		req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.1 Safari/605.1.15")
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		return rec
	}
	click := func() string {
		t.Helper()
		loc, _ := url.Parse(get(host, "/l/"+code).Header().Get("Location"))
		return loc.Query().Get("ofclk")
	}
	src := owner.must(201, "POST", base+"/postback-sources", map[string]any{"name": "Acme Network"})
	token := strings.TrimPrefix(strings.SplitN(src["postback_url"].(string), "?", 2)[0], "https://"+host)
	sid := src["id"].(string)

	// The pixel answers with a GIF whatever happens, and says what did.
	k1 := click()
	rec := get(host, token+"/pixel?cid="+k1+"&payout=2&txid=P-1")
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/gif" || rec.Header().Get("X-Outflare-Outcome") != "accepted" || rec.Body.Len() == 0 {
		t.Fatalf("pixel: %d %v", rec.Code, rec.Header())
	}
	if out := get(host, token+"/pixel?cid=zzzzzzzzzzzzzzzz").Header().Get("X-Outflare-Outcome"); out != "unknown_click" {
		t.Fatalf("unknown click: %s", out)
	}
	if out := get("", token+"/pixel?cid="+k1).Header().Get("X-Outflare-Outcome"); out != "wrong_domain" {
		t.Fatalf("shared domain: %s", out)
	}
	if js := get(host, token+"/pixel.js?cid="+k1).Body.String(); !strings.Contains(js, "new Image()") {
		t.Fatalf("script: %s", js)
	}
	walled := owner.must(201, "POST", base+"/postback-sources", map[string]any{"name": "Walled", "ip_allowlist": []string{"203.0.113.0/24"}})
	wtoken := strings.TrimPrefix(strings.SplitN(walled["postback_url"].(string), "?", 2)[0], "https://"+host)
	if out := get(host, wtoken+"/pixel?cid="+k1).Header().Get("X-Outflare-Outcome"); out != "ip_refused" {
		t.Fatalf("allowlisted source's pixel: %s", out)
	}

	// An upload: a pending sale, its approval days later by transaction id
	// alone, and rows that can't be recorded.
	k2 := click()
	res := owner.must(200, "POST", base+"/tracker-conversions", map[string]any{"source": sid, "conversions": []any{
		map[string]any{"click_id": k2, "transaction_id": "U-1", "payout": 5, "type": "sale", "status": "pending", "converted_at": "2026-11-09T10:00:00Z",
			"params": map[string]any{"p2": "batch-7"}},
		map[string]any{"transaction_id": "U-1", "status": "approved", "payout": "6.5"},
		map[string]any{"transaction_id": "NOPE", "status": "approved"},
		map[string]any{"click_id": "bad"},
		map[string]any{"click_id": "zzzzzzzzzzzzzzzz", "transaction_id": "U-2"},
		map[string]any{"transaction_id": "U-1"},
	}})
	if res["accepted"] != float64(1) || res["updated"] != float64(1) || res["failed"] != float64(4) {
		t.Fatalf("upload: %v", res)
	}
	rows := res["rows"].([]any)
	if rows[2].(map[string]any)["error"] == nil || rows[5].(map[string]any)["error"] == nil {
		t.Fatalf("rows: %v", rows)
	}
	list := owner.must(200, "GET", base+"/tracker-conversions?status=approved", nil)["data"].([]any)
	var up map[string]any
	for _, x := range list {
		if m := x.(map[string]any); m["transaction_id"] == "U-1" {
			up = m
		}
	}
	if up == nil || up["payout"] != "6.5" || up["click_key"] != k2 || !strings.HasPrefix(up["converted_at"].(string), "2026-11-09T10:00:00") ||
		up["params"].(map[string]any)["2"] != "batch-7" {
		t.Fatalf("uploaded conversion: %v", list)
	}

	// A CSV file, with the usual header names.
	k3 := click()
	csv := "\ufeffClick_ID,Transaction_ID,Payout,Status,Type\n" + k3 + ",C-1,3.25,approved,lead\n,U-1,,declined,\n"
	status, body := owner.raw("POST", base+"/tracker-conversions/csv?source="+sid, "text/csv", csv)
	if status != 200 || body["accepted"] != float64(1) || body["updated"] != float64(1) {
		t.Fatalf("csv: %d %v", status, body)
	}

	// Bad uploads.
	owner.must(400, "POST", base+"/tracker-conversions", map[string]any{"source": "nope", "conversions": []any{map[string]any{"click_id": k3}}})
	owner.must(400, "POST", base+"/tracker-conversions", map[string]any{"source": sid, "conversions": []any{}})
	other := owner.must(201, "POST", "/v1/workspaces/"+ws+"/brands", map[string]any{"name": "Repocket"})
	owner.must(400, "POST", "/v1/brands/"+other["id"].(string)+"/tracker-conversions", map[string]any{"source": sid, "conversions": []any{map[string]any{"click_id": k3}}})

	found := false
	for _, ev := range events() {
		found = found || ev.Name == "tracker_conversion.uploaded"
	}
	if !found {
		t.Error("no tracker_conversion.uploaded event")
	}
}
