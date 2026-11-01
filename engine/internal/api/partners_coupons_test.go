package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPartnerCouponOffers(t *testing.T) {
	e := freshEnv(t)
	stripe := fakeStripe(t)
	previous := stripe.Config.Handler
	var mu sync.Mutex
	var couponForm, promoForm map[string]string
	active := ""
	stripe.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && (r.URL.Path == "/v1/coupons" || r.URL.Path == "/v1/promotion_codes" || strings.HasPrefix(r.URL.Path, "/v1/promotion_codes/")) {
			if err := r.ParseForm(); err != nil {
				t.Error(err)
			}
			mu.Lock()
			defer mu.Unlock()
			f := map[string]string{}
			for k := range r.PostForm {
				f[k] = r.PostForm.Get(k)
			}
			w.Header().Set("Content-Type", "application/json")
			if r.URL.Path == "/v1/coupons" {
				couponForm = f
				json.NewEncoder(w).Encode(map[string]any{"id": "coupon_generated"})
				return
			}
			if r.URL.Path == "/v1/promotion_codes" {
				promoForm = f
				json.NewEncoder(w).Encode(map[string]any{"id": "promo_generated"})
				return
			}
			active = f["active"]
			json.NewEncoder(w).Encode(map[string]any{"id": "promo_generated"})
			return
		}
		previous.ServeHTTP(w, r)
	})
	e.integrations.Client.BaseURLs["stripe"] = stripe.URL
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	id := owner.must(201, "POST", base+"/partner-programs", map[string]any{"name": "Partner program", "kind": "affiliate", "auto_accept": true, "landing_url": "https://northpeak.example/"})["id"].(string)
	admin := base + "/partner-programs/" + id
	end := time.Now().Add(24 * time.Hour).UTC().Truncate(time.Second).Format(time.RFC3339)
	offer := map[string]any{"id": "launch", "label": "15% off your first order", "coupon": "", "rules": map[string]any{"percent_bp": 1500, "currency": "USD", "max_redemptions": 3, "first_order_only": true, "min_order_cents": 2000, "ends_at": end}}
	owner.must(200, "PUT", admin+"/portal", map[string]any{"enabled": true, "settings": map[string]any{"coupon_templates": []any{offer}}})
	sess := owner.must(201, "POST", admin+"/portal-sessions", map[string]any{"customer_id": "maya"})
	portal := &portalClient{t: t, h: e.h, base: "https://outflare.test/p/" + id + "/"}
	portal.must(200, "POST", "api/session", map[string]any{"token": signInLink.FindStringSubmatch(sess["sign_in_url"].(string))[1]})
	portal.must(400, "POST", "api/promo-codes", map[string]any{"code": "MAYA15", "template": "unknown"})
	portal.must(409, "POST", "api/promo-codes", map[string]any{"code": "MAYA15", "template": "launch"})
	owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "stripe", "credentials": map[string]any{"api_key": "rk_test_key"}})
	portal.must(200, "POST", "api/promo-codes", map[string]any{"code": "MAYA15", "template": "launch"})
	portal.must(409, "POST", "api/promo-codes", map[string]any{"code": "maya15", "template": "launch"})
	// The partner cannot submit arbitrary discount fields or choose another billing coupon.
	portal.must(400, "POST", "api/promo-codes", map[string]any{"code": "MAYA90", "template": "launch", "rules": map[string]any{"percent_bp": 9000}})
	e.runCRMImports(t)
	wait := func() {
		t.Helper()
		for range 250 {
			me := portal.must(200, "GET", "api/me", nil)
			code := me["promo_code_details"].([]any)[0].(map[string]any)
			if code["status"] == "created" {
				return
			}
			if code["status"] == "failed" {
				t.Fatalf("code failed: %v", code)
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("coupon sync did not finish")
	}
	wait()
	mu.Lock()
	if couponForm["percent_off"] != "15.00" || couponForm["duration"] != "once" || promoForm["restrictions[first_time_transaction]"] != "true" || promoForm["max_redemptions"] != "3" || promoForm["restrictions[minimum_amount]"] != "2000" || promoForm["expires_at"] == "" {
		t.Errorf("native rules not applied: coupon=%v promo=%v", couponForm, promoForm)
	}
	mu.Unlock()
	if owner.must(201, "POST", base+"/partner-conversions", map[string]any{"type": "sale", "customer_id": "buyer_1", "promo_code": "MAYA15", "amount_cents": 3000})["credited"] != true {
		t.Fatal("enabled coupon not attributed")
	}
	owner.must(200, "PATCH", base+"/partners/"+sess["partner_id"].(string)+"/promo-codes/MAYA15", map[string]any{"enabled": false})
	wait()
	mu.Lock()
	if active != "false" {
		t.Error("checkout code not disabled")
	}
	mu.Unlock()
	if owner.must(200, "POST", base+"/partner-conversions", map[string]any{"type": "sale", "customer_id": "buyer_2", "promo_code": "MAYA15", "amount_cents": 3000})["credited"] != false {
		t.Fatal("disabled coupon still attributed")
	}
	// Default discount cannot be both percentage and fixed.
	owner.must(400, "POST", base+"/partners/"+sess["partner_id"].(string)+"/promo-codes", map[string]any{"code": "INVALID", "rules": map[string]any{"percent_bp": 1000, "amount_cents": 500}})
	owner.must(200, "PATCH", admin, map[string]any{"status": "paused"})
	portal.must(403, "POST", "api/promo-codes", map[string]any{"code": "MAYA_NEW", "template": "launch"})

}
