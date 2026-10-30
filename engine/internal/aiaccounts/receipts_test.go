package aiaccounts

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/db"
)

// Made-up receipts in the shapes the AI companies and their payment
// processors send.
const claudeMax = `Receipt from Anthropic, PBC
Receipt #2845-1093
Date paid Oct 14, 2026
Payment method Visa ending in 4242

Claude Max 20x
Oct 14 – Nov 14, 2026
Qty 1     $200.00

Subtotal  $200.00
Total     $200.00
Amount paid $200.00

Billed to ana@acme.example`

const chatgptApple = `Your receipt from Apple.
ChatGPT Plus (Monthly)
Renews November 20, 2026
Total $19.99`

const cursorEUR = `Receipt from Cursor
Invoice number ABCD1234-0007
Cursor Pro
Qty 1
Total €18,15
Amount paid €18,15`

const teamSeats = `Receipt from OpenAI
ChatGPT Team
6 seats
Nov 2 – Dec 2, 2026
Total $180.00`

func TestReadReceipt(t *testing.T) {
	when := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		name string
		m    Message
		ok   bool
		want Receipt
	}{
		{"claude via its own mail", Message{Body: claudeMax, Subject: "Your receipt from Anthropic", Date: when, DKIM: []string{"mail.anthropic.com"}}, true,
			Receipt{Vendor: "anthropic", Provider: "claude", Plan: "Max 20x", Currency: "USD", ChargedOn: "2026-10-14", PeriodStart: "2026-10-14", PeriodEnd: "2026-11-14", AccountEmail: "ana@acme.example",
				Identifiers: map[string]string{"invoice_prefix": "anthropic:2845", "card_last4": "anthropic:4242"}}},
		{"chatgpt through the app store", Message{Body: chatgptApple, Subject: "Your receipt from Apple.", Date: when, DKIM: []string{"email.apple.com"}}, true,
			Receipt{Vendor: "openai", Provider: "chatgpt", Plan: "Plus", Currency: "USD", ChargedOn: "2026-10-14", Identifiers: map[string]string{}}},
		{"cursor in euros through stripe", Message{Body: cursorEUR, Subject: "Your Cursor receipt", Date: when, DKIM: []string{"stripe.com"}}, true,
			Receipt{Vendor: "cursor", Provider: "cursor", Plan: "Pro", Currency: "EUR", ChargedOn: "2026-10-14", Identifiers: map[string]string{"invoice_prefix": "cursor:ABCD1234"}}},
		{"team seats", Message{Body: teamSeats, Subject: "Receipt", Date: when, DKIM: []string{"openai.com"}}, true,
			Receipt{Vendor: "openai", Provider: "chatgpt", Plan: "Team", Currency: "USD", ChargedOn: "2026-10-14", PeriodStart: "2026-11-02", PeriodEnd: "2026-12-02", Identifiers: map[string]string{}}},
		{"unsigned", Message{Body: claudeMax, Date: when}, false, Receipt{}},
		{"another company's receipt through stripe", Message{Body: "Receipt from Northpeak Notes\nTotal $8.00", Date: when, DKIM: []string{"stripe.com"}}, false, Receipt{}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, ok := ReadReceipt(c.m)
			if ok != c.ok {
				t.Fatalf("ok = %v, want %v (%+v)", ok, c.ok, got)
			}
			if !ok {
				return
			}
			w := c.want
			if got.Vendor != w.Vendor || got.Provider != w.Provider || got.Plan != w.Plan || got.Currency != w.Currency || got.ChargedOn != w.ChargedOn ||
				got.PeriodStart != w.PeriodStart || got.PeriodEnd != w.PeriodEnd || got.AccountEmail != w.AccountEmail || fmt.Sprint(got.Identifiers) != fmt.Sprint(w.Identifiers) {
				t.Errorf("got  %+v\nwant %+v", got, w)
			}
		})
	}
	amounts := map[string]int64{claudeMax: 20000, chatgptApple: 1999, cursorEUR: 1815, teamSeats: 18000}
	for body, want := range amounts {
		got, _ := ReadReceipt(Message{Body: body, Date: when, DKIM: []string{"stripe.com", "anthropic.com", "openai.com", "apple.com"}})
		if got.AmountMinor == nil || *got.AmountMinor != want {
			t.Errorf("amount of %.30q = %v, want %d", body, got.AmountMinor, want)
		}
	}
	if r, _ := ReadReceipt(Message{Body: teamSeats, Date: when, DKIM: []string{"openai.com"}}); r.Seats == nil || *r.Seats != 6 {
		t.Errorf("seats = %v", r.Seats)
	}
}

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	pool, err := db.Connect(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := db.Migrate(context.Background(), pool); err != nil {
		t.Fatal(err)
	}
	return pool
}

func TestApplyReceipt(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tag := fmt.Sprint(time.Now().UnixNano())
	var ws, ana, theo string
	if err := pool.QueryRow(ctx, `INSERT INTO workspaces (name, slug) VALUES ('Acme', 'acme-r' || $1) RETURNING id::text`, tag).Scan(&ws); err != nil {
		t.Fatal(err)
	}
	user := func(name string) string {
		var id string
		if err := pool.QueryRow(ctx, `WITH a AS (INSERT INTO actors (type, display_name) VALUES ('human', $1) RETURNING id)
			INSERT INTO users (actor_id, email, name, password_hash) SELECT id, $2, $1, '' FROM a RETURNING id::text`,
			name, name+"-"+tag+"@acme.example").Scan(&id); err != nil {
			t.Fatal(err)
		}
		_, _ = pool.Exec(ctx, `INSERT INTO memberships (workspace_id, user_id, role) VALUES ($1, $2, 'operator')`, ws, id)
		return id
	}
	ana, theo = user("ana"), user("theo")
	// No euro rate around the receipts' days until the test adds one.
	clearRates := func() {
		_, _ = pool.Exec(ctx, `DELETE FROM exchange_rates WHERE currency = 'EUR' AND day BETWEEN '2026-10-01' AND '2026-10-31'`)
	}
	clearRates()
	t.Cleanup(clearRates)
	account := func(provider, email, cycle, renews string, price int64, owner string) string {
		var id string
		if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
			name := provider + " " + owner[:4]
			in := Input{Provider: &provider, Name: &name, AccountEmail: &email, BillingCycle: &cycle, RenewsOn: &renews, PriceCents: &price,
				Members: &[]MemberInput{{UserID: owner, Role: RoleOwner}}}
			if err := in.Validate(true); err != nil {
				return err
			}
			var err error
			id, err = Create(ctx, tx, ws, in, nil)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return id
	}
	anaMax := account("claude", "ana@acme.example", "monthly", "2026-10-14", 10000, ana)
	theoPlus := account("chatgpt", "", "monthly", "2026-10-20", 2000, theo)
	anaPlus := account("chatgpt", "", "monthly", "2026-11-03", 2000, ana)

	apply := func(fwd string, m Message) Applied {
		t.Helper()
		r, ok := ReadReceipt(m)
		if !ok {
			t.Fatalf("not read: %+v", m)
		}
		var out Applied
		if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
			var err error
			out, err = ApplyReceipt(ctx, tx, ws, "", fwd, r)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return out
	}
	when := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)

	// By the account email on the receipt, forwarded by someone else: the
	// account moves to the paid period's end, with the new plan and price.
	if got := apply(theo, Message{Body: claudeMax, Date: when, DKIM: []string{"anthropic.com"}}); got.Status != "matched" || got.AccountID != anaMax || got.MatchedBy != "account_email" {
		t.Fatalf("by email: %+v", got)
	}
	a, _ := Get(ctx, pool, ws, anaMax)
	if a.RenewsOn == nil || *a.RenewsOn != "2026-11-14" || a.Plan == nil || *a.Plan != "Max 20x" || a.PriceCents == nil || *a.PriceCents != 20000 {
		t.Fatalf("account after receipt: %+v", a)
	}
	// Next month's receipt names no email but carries the same receipt
	// number prefix: matched by the identifier it learned.
	next := "Receipt from Anthropic, PBC\nReceipt #2845-1094\nClaude Max 20x\nNov 14 – Dec 14, 2026\nAmount paid $200.00"
	if got := apply(theo, Message{Body: next, Date: when.AddDate(0, 1, 0), DKIM: []string{"anthropic.com"}}); got.Status != "matched" || got.AccountID != anaMax || got.MatchedBy != "known_id" {
		t.Fatalf("by identifier: %+v", got)
	}
	// Theo forwards his ChatGPT Plus: his only ChatGPT account.
	if got := apply(theo, Message{Body: chatgptApple, Date: time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC), DKIM: []string{"apple.com"}}); got.AccountID != theoPlus || got.MatchedBy != "forwarder" {
		t.Fatalf("by forwarder: %+v", got)
	}
	a, _ = Get(ctx, pool, ws, theoPlus)
	if a.RenewsOn == nil || *a.RenewsOn != "2026-11-20" || *a.PriceCents != 1999 {
		t.Fatalf("theo's plus: %+v", a)
	}
	// Nobody's forward, but one ChatGPT account renews that day.
	if got := apply("", Message{Body: chatgptApple, Date: time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC), DKIM: []string{"apple.com"}}); got.AccountID != anaPlus || got.MatchedBy != "expected_renewal" {
		t.Fatalf("by renewal: %+v", got)
	}
	// A Cursor receipt nobody has an account for waits for a person.
	review := apply(ana, Message{Body: cursorEUR, Date: when, DKIM: []string{"stripe.com"}})
	if review.Status != "needs_review" || review.AccountID != "" {
		t.Fatalf("needs review: %+v", review)
	}
	cursorAcc := account("cursor", "", "monthly", "", 0, ana)
	if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		_, err := ResolveReceipt(ctx, tx, ws, review.ReceiptID, cursorAcc, ana)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	a, _ = Get(ctx, pool, ws, cursorAcc)
	if a.Plan == nil || *a.Plan != "Pro" || *a.PriceCents != 0 {
		t.Fatalf("a euro receipt sets the plan but not the US price: %+v", a)
	}
	// Once that day's rate arrives, the receipt gets its US amount and the
	// account its price: €18.15 at 1.08 dollars a euro.
	if _, err := pool.Exec(ctx, `INSERT INTO exchange_rates (currency, day, usd_per_unit) VALUES ('EUR', '2026-10-13', 1.08)`); err != nil {
		t.Fatal(err)
	}
	if n, err := ConvertPending(ctx, pool); err != nil || n < 1 {
		t.Fatalf("convert: %d %v", n, err)
	}
	a, _ = Get(ctx, pool, ws, cursorAcc)
	var usd *int64
	_ = pool.QueryRow(ctx, `SELECT usd_cents FROM ai_receipts WHERE id::text = $1`, review.ReceiptID).Scan(&usd)
	if usd == nil || *usd != 1960 || *a.PriceCents != 1960 {
		t.Fatalf("converted: receipt %v, account %+v", usd, a)
	}
	// A euro receipt arriving now is converted straight away.
	if got := apply(ana, Message{Body: cursorEUR, Date: when, DKIM: []string{"stripe.com"}}); got.AccountID != cursorAcc {
		t.Fatalf("second cursor receipt: %+v", got)
	}
	_ = pool.QueryRow(ctx, `SELECT usd_cents FROM ai_receipts WHERE workspace_id = $1 ORDER BY created_at DESC LIMIT 1`, ws).Scan(&usd)
	if usd == nil || *usd != 1960 {
		t.Errorf("converted on arrival: %v", usd)
	}
	if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		_, err := ResolveReceipt(ctx, tx, ws, review.ReceiptID, "", ana)
		return err
	}); err != ErrReceiptNotFound {
		t.Errorf("resolving twice: %v", err)
	}
	var events int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE workspace_id = $1 AND action LIKE 'ai_receipt.%'`, ws).Scan(&events)
	if events != 6 {
		t.Errorf("%d receipt events, want 6", events)
	}
}
