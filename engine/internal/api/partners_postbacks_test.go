package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/api"
)

func TestPartnerPostbacksAndPayoutExport(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()

	// The affiliate's own tracker: it takes sales and refuses refunds.
	var mu sync.Mutex
	var got []url.Values
	tracker := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = append(got, r.URL.Query())
		mu.Unlock()
		if r.URL.Query().Get("type") == "refund" {
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer tracker.Close()
	undo := api.SetPartnerPostbackClient(tracker.Client())
	defer undo()

	owner.must(201, "POST", base+"/partner-programs", map[string]any{"name": "Affiliates", "kind": "affiliate",
		"landing_url": "https://northpeak.example/", "min_payout_cents": 0})
	ana := owner.must(201, "POST", base+"/partners", map[string]any{"program": "affiliates", "email": "ana@acme.example", "code": "ana",
		"payout_method": "=PayPal ana@acme.example"})
	pid := ana["id"].(string)
	owner.must(400, "PATCH", base+"/partners/"+pid, map[string]any{"postback_url": "http://tracker.example/pb"})
	owner.must(400, "PATCH", base+"/partners/"+pid, map[string]any{"postback_url": "https://tracker.example/pb?x={nope}"})
	tmpl := tracker.URL + "/pb?clickid={sub_id}&payout={commission}&status={status}&type={type}&cur={currency}"
	if p := owner.must(200, "PATCH", base+"/partners/"+pid, map[string]any{"postback_url": tmpl}); p["postback_url"] != tmpl {
		t.Fatalf("postback_url = %v", p["postback_url"])
	}

	// A click from the affiliate's ad (their click id in sub), then a sale.
	_, loc := owner.rawUA("GET", "/r/"+ws+"/ana?sub=aff-123", "Mozilla/5.0")
	u, _ := url.Parse(loc)
	conv := base + "/partner-conversions"
	sale := owner.must(201, "POST", conv, map[string]any{"type": "sale", "customer_id": "u-1", "click_id": u.Query().Get("ref_click"),
		"amount_cents": 10000, "external_id": "o-1"})
	reward := sale["rewards"].([]any)[0].(map[string]any)["id"].(string)
	// Someone without a postback URL sends nothing.
	owner.must(201, "POST", base+"/partners", map[string]any{"program": "affiliates", "email": "ben@acme.example", "code": "ben"})
	owner.must(201, "POST", conv, map[string]any{"type": "sale", "customer_id": "u-2", "ref": "ben", "amount_cents": 5000})

	e.runCRMImports(t)
	sends := func(want int) []any {
		t.Helper()
		for range 250 {
			list := owner.must(200, "GET", base+"/partners/"+pid+"/postbacks", nil)["data"].([]any)
			done := 0
			for _, s := range list {
				if s.(map[string]any)["status"] != "queued" {
					done++
				}
			}
			if len(list) == want && done == want {
				return list
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("postbacks didn't finish")
		return nil
	}
	first := sends(1)[0].(map[string]any)
	if first["status"] != "sent" || first["http_status"] != 200.0 {
		t.Fatalf("send = %v", first)
	}
	mu.Lock()
	q := got[0]
	mu.Unlock()
	if q.Get("clickid") != "aff-123" || q.Get("payout") != "20.00" || q.Get("status") != "pending" || q.Get("type") != "sale" || q.Get("cur") != "USD" {
		t.Fatalf("postback query = %v", q)
	}

	// Payout export: the due payout with the partner's payout details, safe from spreadsheet formulas.
	owner.must(200, "POST", base+"/partner-rewards/"+reward+"/approve", map[string]any{})
	owner.must(200, "POST", base+"/partner-payouts", map[string]any{"program": "affiliates"})
	code, _, body := owner.text(base + "/partner-payouts/export?program=affiliates")
	lines := strings.Split(strings.TrimSpace(body), "\n")
	if code != 200 || len(lines) != 2 || !strings.HasPrefix(lines[0], "payout_id,program,partner_id,partner_code") ||
		!strings.Contains(lines[1], ",ana,") || !strings.Contains(lines[1], ",'=PayPal ana@acme.example,20.00,USD,due,") {
		t.Fatalf("export %d:\n%s", code, body)
	}
	var exported int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE workspace_id = $1 AND action = 'partner_payouts.exported'`, ws).Scan(&exported); err != nil || exported != 1 {
		t.Fatalf("exported events = %d, %v", exported, err)
	}
	owner.must(400, "GET", base+"/partner-payouts/export?status=sometimes", nil)

	// A refund calls again; the tracker refuses it, so it isn't tried again.
	owner.must(201, "POST", conv, map[string]any{"type": "refund", "customer_id": "u-1", "refund_of": "o-1", "external_id": "r-1"})
	list := sends(2)
	last := list[0].(map[string]any)
	if last["status"] != "failed" || last["http_status"] != 404.0 || last["attempts"] != 1.0 {
		t.Fatalf("refund send = %v", last)
	}
	mu.Lock()
	if len(got) != 2 || got[1].Get("status") != "reversed" {
		t.Fatalf("calls = %v", got)
	}
	mu.Unlock()
}
