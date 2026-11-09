package api_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"

	"github.com/emersion/go-msgauth/dkim"
	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/api"
	"github.com/outflareai/outflare/engine/internal/features"
	"github.com/outflareai/outflare/engine/internal/inboundemail"
	"github.com/outflareai/outflare/engine/internal/jobs"
)

func TestAIReceipts(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	inbound := &inboundemail.Service{}
	inbound.Enable("in.example.test", inboundSecret)
	inbound.SetLookupTXT(func(name string) ([]string, error) {
		if name == "t._domainkey.anthropic.com" || name == "t._domainkey.stripe.com" {
			return []string{"v=DKIM1; k=ed25519; p=" + base64.StdEncoding.EncodeToString(pub)}, nil
		}
		return nil, errors.New("no such record")
	})
	e := freshEnvWith(t, func(o *api.Options) { o.InboundEmail = inbound })
	inbound.Pool = e.pool
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	if err := pgx.BeginFunc(t.Context(), e.pool, func(tx pgx.Tx) error {
		_, err := features.Set(t.Context(), tx, ws, features.AIAccounts, true, "")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	acc := owner.must(201, "POST", base+"/ai-accounts", map[string]any{"provider": "claude", "name": "Claude Max", "account_email": "jack@example.com",
		"billing_cycle": "monthly", "renews_on": "2026-10-14", "price_cents": 10000})
	addr := owner.must(201, "POST", base+"/inbound-addresses", map[string]any{"purpose": "ai_receipts"})["email"].(string)

	receive := func(domain, msgID, body string) {
		t.Helper()
		raw := []byte("From: Billing <billing@" + domain + ">\r\nTo: jack@example.com\r\nSubject: Your receipt\r\nMessage-ID: <" + msgID + ">\r\nDate: Wed, 14 Oct 2026 09:00:00 +0000\r\n\r\n" + body + "\r\n")
		var signed bytes.Buffer
		if err := dkim.Sign(&signed, bytes.NewReader(raw), &dkim.SignOptions{Domain: domain, Selector: "t", Signer: priv}); err != nil {
			t.Fatal(err)
		}
		code, out := deliver(e.h, addr, signed.Bytes(), inboundSecret)
		if code != 202 {
			t.Fatalf("delivery: %d %v", code, out)
		}
		payload, _ := json.Marshal(map[string]string{"id": out["id"].(string)})
		if err := inbound.Process(t.Context(), jobs.Job{Kind: inboundemail.JobProcess, Payload: payload}); err != nil {
			t.Fatal(err)
		}
	}

	// A Claude receipt naming the account's email updates it.
	receive("anthropic.com", "c1@example.test", "Receipt from Anthropic, PBC\nReceipt #2845-1093\nClaude Max 20x\nOct 14 – Nov 14, 2026\nAmount paid $200.00\nBilled to jack@example.com")
	list := owner.must(200, "GET", base+"/ai-accounts/receipts", nil)["data"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["status"] != "matched" || list[0].(map[string]any)["account_id"] != acc["id"] ||
		list[0].(map[string]any)["matched_by"] != "account_email" || list[0].(map[string]any)["usd_cents"] != 20000.0 {
		t.Fatalf("receipts: %v", list)
	}
	got := owner.must(200, "GET", base+"/ai-accounts/"+acc["id"].(string), nil)
	if got["renews_on"] != "2026-11-14" || got["plan"] != "Max 20x" || got["price_cents"] != 20000.0 {
		t.Fatalf("account after the receipt: %v", got)
	}

	// A Cursor receipt nobody has an account for waits; ignore it.
	receive("stripe.com", "c2@example.test", "Receipt from Cursor\nCursor Pro\nTotal $20.00")
	review := owner.must(200, "GET", base+"/ai-accounts/receipts?status=needs_review", nil)["data"].([]any)
	if len(review) != 1 || review[0].(map[string]any)["vendor"] != "cursor" {
		t.Fatalf("needs review: %v", review)
	}
	id := review[0].(map[string]any)["id"].(string)
	owner.must(400, "POST", base+"/ai-accounts/receipts/"+id+"/assign", map[string]any{"account_id": "00000000-0000-0000-0000-000000000000"})
	owner.must(200, "POST", base+"/ai-accounts/receipts/"+id+"/ignore", nil)
	owner.must(404, "POST", base+"/ai-accounts/receipts/"+id+"/ignore", nil)
	owner.must(400, "GET", base+"/ai-accounts/receipts?status=bogus", nil)

	// Not a receipt: ignored by inbound email, nothing stored.
	receive("stripe.com", "c3@example.test", "Receipt from Northpeak Notes\nTotal $8.00")
	if n := len(owner.must(200, "GET", base+"/ai-accounts/receipts", nil)["data"].([]any)); n != 2 {
		t.Fatalf("%d receipts, want 2", n)
	}
	emails := owner.must(200, "GET", base+"/inbound-emails", nil)["data"].([]any)
	if emails[0].(map[string]any)["status"] != "ignored" || emails[0].(map[string]any)["reason"] != "not_a_receipt" {
		t.Fatalf("not a receipt: %v", emails[0])
	}
}

// The workspace's receipts address is made as soon as anyone opens the
// addresses list, readable (<slug>-receipts), once, by the system; a name
// the extension uses at the same domain is skipped.
func TestAIReceiptsAddressIsAutomatic(t *testing.T) {
	inbound := &inboundemail.Service{}
	inbound.Enable("in.example.test", nil) // delivered by the extension only
	e := freshEnvWith(t, func(o *api.Options) { o.InboundEmail = inbound })
	inbound.Pool = e.pool
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws

	// Off until AI Accounts is on.
	if got := owner.must(200, "GET", base+"/inbound-addresses", nil)["data"].([]any); len(got) != 0 {
		t.Fatalf("made with AI Accounts off: %v", got)
	}
	if err := pgx.BeginFunc(t.Context(), e.pool, func(tx pgx.Tx) error {
		_, err := features.Set(t.Context(), tx, ws, features.AIAccounts, true, "")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var slug string
	if err := e.pool.QueryRow(t.Context(), `SELECT slug FROM workspaces WHERE id::text = $1 OR slug = $1`, ws).Scan(&slug); err != nil {
		t.Fatal(err)
	}
	// Something else at the domain already has <slug>-receipts.
	inbound.SetTaken(func(_ context.Context, address string) (bool, error) {
		return address == slug+"-receipts@in.example.test", nil
	})
	first := owner.must(200, "GET", base+"/inbound-addresses", nil)["data"].([]any)
	again := owner.must(200, "GET", base+"/inbound-addresses", nil)["data"].([]any)
	if len(first) != 1 || len(again) != 1 || first[0].(map[string]any)["id"] != again[0].(map[string]any)["id"] {
		t.Fatalf("addresses: %v then %v", first, again)
	}
	a := first[0].(map[string]any)
	if a["purpose"] != "ai_receipts" || a["email"] != slug+"-receipts-2@in.example.test" || a["brand_id"] != nil {
		t.Fatalf("address: %v", a)
	}
	var actor string
	if err := e.pool.QueryRow(t.Context(), `SELECT actor_type FROM audit_events WHERE target_id = $1 AND action = 'inbound_address.created'`, a["id"]).Scan(&actor); err != nil || actor != "system" {
		t.Fatalf("audit: %q %v", actor, err)
	}
	// The engine knows its own names, for the extension's check.
	if taken, err := inbound.TakenHere(t.Context(), e.pool, a["email"].(string)); err != nil || !taken {
		t.Fatalf("taken here: %v %v", taken, err)
	}
	// Brands don't get their own: one per workspace.
	owner.must(400, "POST", base+"/inbound-addresses", map[string]any{"purpose": "ai_receipts", "brand": "x"})
	// Without a secret, the signed POST is refused; Deliver still works.
	if code, _ := deliver(e.h, a["email"].(string), []byte("Subject: hi\r\n\r\nhi\r\n"), inboundSecret); code != 401 {
		t.Fatalf("signed delivery without a secret: %d", code)
	}
	if _, _, err := inbound.Deliver(t.Context(), a["email"].(string), []byte("Message-ID: <d1@example.test>\r\nSubject: hi\r\n\r\nhi\r\n")); err != nil {
		t.Fatal(err)
	}
}
