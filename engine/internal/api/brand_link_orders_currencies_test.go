package api_test

import (
	"context"
	"strings"
	"testing"
	"time"
)

// An invoice in pounds (or any common currency) is kept as invoiced, with
// its US-dollar equivalent at the ECB reference rate Outflare looks up for
// the invoice date; nobody types a rate. Mark Paid records it in that
// currency and sends no money.
func TestLinkOrderInvoiceCurrencies(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	brand := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "handle": "northpeak"})["id"].(string)
	root := "/v1/brands/" + brand + "/link-orders"
	ownerID := owner.must(200, "GET", "/v1/me", nil)["user"].(map[string]any)["id"].(string)
	owner.must(200, "PATCH", root+"/settings", map[string]any{"approver_user_id": ownerID})
	donor := owner.must(201, "POST", root+"/catalog", map[string]any{"domain": "donor.example"})
	ctx := context.Background()

	newOrder := func(target string) string {
		return root + "/" + owner.must(201, "POST", root, map[string]any{
			"donor_profile_id": donor["id"], "target_url": "https://northpeak.example/" + target,
		})["id"].(string)
	}
	invoice := func(o map[string]any, key string) map[string]any {
		t.Helper()
		inv, ok := o[key].(map[string]any)
		if !ok {
			t.Fatalf("%s missing: %v", key, o)
		}
		return inv
	}

	// The ECB reference rates Outflare stores (fetched by package fxrates;
	// written here so the test needs no network): a past Friday and the
	// Thursday before for PLN, and GBP, JPY and EUR days.
	if _, err := e.pool.Exec(ctx, `INSERT INTO exchange_rates (currency, day, usd_per_unit) VALUES
		('PLN', '2026-10-15', 0.2495), ('PLN', '2026-10-16', 0.2501), ('EUR', '2026-10-16', 1.0842),
		('GBP', '2026-11-10', 1.2708), ('GBP', '2026-11-11', 1.2650), ('JPY', '2026-11-10', 0.006512)
		ON CONFLICT (currency, day) DO UPDATE SET usd_per_unit = EXCLUDED.usd_per_unit`); err != nil {
		t.Fatal(err)
	}
	// exchange_rates is shared by every test: take these past days out
	// again so nothing else sees them.
	t.Cleanup(func() {
		e.pool.Exec(context.Background(), `DELETE FROM exchange_rates WHERE (currency, day) IN
			(('PLN', '2026-10-15'), ('PLN', '2026-10-16'), ('EUR', '2026-10-16'), ('GBP', '2026-11-10'), ('GBP', '2026-11-11'), ('JPY', '2026-11-10'))`)
	})
	orders := func() int {
		var n int
		if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM coverage_link_orders WHERE quoted_amount_minor IS NOT NULL`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// GBP: the ECB reference rate for the invoice date, looked up by
	// Outflare. No rate in the request.
	gbp := newOrder("gbp")
	quoted := owner.must(200, "POST", gbp+"/quote", map[string]any{
		"amount_minor": 50000, "currency": "GBP", "invoice_date": "2026-11-10",
	})
	q := invoice(quoted, "quoted_invoice")
	if q["currency"] != "GBP" || q["amount_minor"] != 50000.0 || q["usd_amount_minor"] != 63540.0 ||
		q["fx_rate"] != 1.2708 || q["fx_rate_date"] != "2026-11-10" || q["invoice_date"] != "2026-11-10" || q["fx_rate_source"] != "ecb" {
		t.Fatalf("GBP quote: %v", q)
	}
	// The USD-or-EUR columns hold the US-dollar equivalent, for budgets.
	if quoted["quoted_currency"] != "USD" || quoted["quoted_amount_minor"] != 63540.0 {
		t.Fatalf("GBP legacy columns: %v", quoted)
	}

	// No ECB rate within the 10-day fallback: refused, nothing saved, and
	// the message neither mentions Google nor asks for a typed rate.
	other := newOrder("gbp-norate")
	before := orders()
	for _, body := range []map[string]any{
		{"amount_minor": 1000, "currency": "GBP", "invoice_date": "2026-10-01"}, // 40 days before the first GBP rate
		{"amount_minor": 1000, "currency": "UAH", "invoice_date": "2026-11-10"}, // ECB publishes no UAH
		{"amount_minor": 1000, "currency": "EUR", "invoice_date": "2026-09-01"}, // EUR too
	} {
		res := owner.must(400, "POST", other+"/quote", body)
		msg := strings.ToLower(res["error"].(map[string]any)["message"].(string))
		if !strings.Contains(msg, "ecb reference rate") || strings.Contains(msg, "google") || strings.Contains(msg, "enter") || strings.Contains(msg, "type") {
			t.Fatalf("no-rate message for %v: %q", body, msg)
		}
	}
	if orders() != before {
		t.Fatal("a quote without an ECB rate was saved")
	}
	if got := owner.must(200, "GET", other, nil); got["status"] != "requested" || got["quoted_invoice"] != nil {
		t.Fatalf("refused quote changed the order: %v", got)
	}
	// A client cannot send a rate of its own.
	owner.must(400, "POST", other+"/quote", map[string]any{"amount_minor": 1000, "currency": "GBP", "invoice_date": "2026-11-10", "fx_rate": 2})
	owner.must(400, "POST", other+"/quote", map[string]any{"amount_minor": 1000, "currency": "XYZ"})
	owner.must(400, "POST", other+"/quote", map[string]any{"amount_minor": 1000, "currency": "GBP", "invoice_date": "12/11/2026"})
	owner.must(400, "POST", other+"/quote", map[string]any{"amount_minor": 1000, "currency": "GBP",
		"invoice_date": time.Now().UTC().AddDate(0, 0, 5).Format(time.DateOnly)})

	// PLN on a Saturday: the stored rate of the Friday before, marked ECB.
	pln := newOrder("pln")
	p := invoice(owner.must(200, "POST", pln+"/quote", map[string]any{"amount_minor": 200000, "currency": "PLN", "invoice_date": "2026-10-17"}), "quoted_invoice")
	if p["fx_rate"] != 0.2501 || p["fx_rate_date"] != "2026-10-16" || p["invoice_date"] != "2026-10-17" || p["fx_rate_source"] != "ecb" || p["usd_amount_minor"] != 50020.0 {
		t.Fatalf("PLN quote: %v", p)
	}

	// JPY has no minor unit: ¥75,000 is 75000.
	jpy := newOrder("jpy")
	j := invoice(owner.must(200, "POST", jpy+"/quote", map[string]any{"amount_minor": 75000, "currency": "JPY", "invoice_date": "2026-11-10"}), "quoted_invoice")
	if j["usd_amount_minor"] != 48840.0 || j["fx_rate_source"] != "ecb" {
		t.Fatalf("JPY quote: %v", j)
	}

	// USD needs no rate; EUR keeps euros in the old columns and takes its
	// US-dollar equivalent from ECB.
	usd := newOrder("usd")
	uq := owner.must(200, "POST", usd+"/quote", map[string]any{"amount_minor": 15000, "currency": "USD"})
	u := invoice(uq, "quoted_invoice")
	if u["usd_amount_minor"] != 15000.0 || u["fx_rate"] != 1.0 || u["fx_rate_source"] != "usd" || uq["quoted_currency"] != "USD" || uq["quoted_amount_minor"] != 15000.0 {
		t.Fatalf("USD quote: %v", uq)
	}
	eur := newOrder("eur")
	eq := owner.must(200, "POST", eur+"/quote", map[string]any{"amount_minor": 15000, "currency": "EUR", "invoice_date": "2026-10-16"})
	if ev := invoice(eq, "quoted_invoice"); eq["quoted_currency"] != "EUR" || eq["quoted_amount_minor"] != 15000.0 || ev["usd_amount_minor"] != 16263.0 || ev["fx_rate"] != 1.0842 || ev["fx_rate_date"] != "2026-10-16" || ev["fx_rate_source"] != "ecb" {
		t.Fatalf("EUR quote: %v", eq)
	}

	// Approve freezes the invoice; publish, pass a check, Mark Paid with
	// the final invoice's date, which takes the ECB rate for that day.
	approved := owner.must(200, "POST", gbp+"/approve", nil)
	if f := invoice(approved, "invoice"); f["currency"] != "GBP" || f["amount_minor"] != 50000.0 || f["usd_amount_minor"] != 63540.0 || approved["currency"] != "USD" || approved["donor_amount_minor"] != 63540.0 {
		t.Fatalf("approved: %v", approved)
	}
	owner.must(200, "POST", gbp+"/publish", map[string]any{"published_url": "https://donor.example/gbp"})
	gbpID := gbp[len(root)+1:]
	if _, err := e.pool.Exec(ctx, `UPDATE coverage_link_orders SET last_check_at = now(), last_check_live = true, last_check_target_match = true WHERE id = $1`, gbpID); err != nil {
		t.Fatal(err)
	}
	owner.must(400, "POST", gbp+"/pay", map[string]any{"fx_rate": 1.3})
	owner.must(400, "POST", gbp+"/pay", map[string]any{"invoice_date": "2026-09-01"})
	paid := owner.must(200, "POST", gbp+"/pay", map[string]any{"invoice_date": "2026-11-11"})
	f := invoice(paid, "invoice")
	if paid["status"] != "paid" || f["currency"] != "GBP" || f["amount_minor"] != 50000.0 || f["usd_amount_minor"] != 63250.0 ||
		f["fx_rate"] != 1.265 || f["fx_rate_date"] != "2026-11-11" || f["invoice_date"] != "2026-11-11" || f["fx_rate_source"] != "ecb" {
		t.Fatalf("paid: %v", paid)
	}
	var cur, src string
	var amount, usdMinor int64
	var rate float64
	var rateDay time.Time
	if err := e.pool.QueryRow(ctx, `SELECT currency, amount_minor, usd_amount_minor, fx_rate::float8, fx_rate_date, fx_rate_source FROM coverage_payments WHERE id = $1`,
		paid["payment_id"]).Scan(&cur, &amount, &usdMinor, &rate, &rateDay, &src); err != nil {
		t.Fatal(err)
	}
	if cur != "GBP" || amount != 50000 || usdMinor != 63250 || rate != 1.265 || rateDay.Format(time.DateOnly) != "2026-11-11" || src != "ecb" {
		t.Fatalf("payment row: %s %d %d %v %v %s", cur, amount, usdMinor, rate, rateDay, src)
	}
	var issued time.Time
	if err := e.pool.QueryRow(ctx, `SELECT issued_on FROM coverage_invoices WHERE id = $1`, paid["invoice_id"]).Scan(&issued); err != nil || issued.Format(time.DateOnly) != "2026-11-11" {
		t.Fatalf("invoice issued_on: %v %v", issued, err)
	}

	// The list returns both values too; a USD order paid without a body
	// keeps rate 1.
	list := owner.must(200, "GET", root, nil)["data"].([]any)
	found := false
	for _, row := range list {
		o := row.(map[string]any)
		if o["id"] == gbpID {
			found = invoice(o, "invoice")["usd_amount_minor"] == 63250.0
		}
	}
	if !found {
		t.Fatalf("list lacks the GBP invoice: %v", list)
	}
	owner.must(200, "POST", usd+"/approve", nil)
	owner.must(200, "POST", usd+"/publish", map[string]any{"published_url": "https://donor.example/usd"})
	if _, err := e.pool.Exec(ctx, `UPDATE coverage_link_orders SET last_check_at = now(), last_check_live = true, last_check_target_match = true WHERE id = $1`, usd[len(root)+1:]); err != nil {
		t.Fatal(err)
	}
	up := invoice(owner.must(200, "POST", usd+"/pay", nil), "invoice")
	if up["currency"] != "USD" || up["usd_amount_minor"] != 15000.0 || up["fx_rate"] != 1.0 {
		t.Fatalf("USD paid: %v", up)
	}

	// A row written before invoices had a currency still reads, from the
	// USD-or-EUR columns.
	var legacyID string
	if err := e.pool.QueryRow(ctx, `INSERT INTO coverage_link_orders (workspace_id, brand_id, publisher_id, donor_profile_id, target_url, status, quoted_currency, quoted_amount_minor)
		SELECT workspace_id, brand_id, publisher_id, id, 'https://northpeak.example/old', 'quoted', 'EUR', 9000 FROM coverage_donor_profiles WHERE id = $1 RETURNING id`, donor["id"]).Scan(&legacyID); err != nil {
		t.Fatal(err)
	}
	old := invoice(owner.must(200, "GET", root+"/"+legacyID, nil), "quoted_invoice")
	if old["currency"] != "EUR" || old["amount_minor"] != 9000.0 || old["usd_amount_minor"] != nil {
		t.Fatalf("legacy order: %v", old)
	}
}
