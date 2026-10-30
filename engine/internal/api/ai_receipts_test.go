package api_test

import (
	"bytes"
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
