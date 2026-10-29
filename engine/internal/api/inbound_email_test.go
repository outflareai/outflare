package api_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/api"
	"github.com/outflareai/outflare/engine/internal/inboundemail"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/track"
)

var inboundSecret = []byte("test-inbound-secret-not-real")

// deliver posts a raw message to the inbound endpoint, signed with secret.
func deliver(h http.Handler, to string, raw []byte, secret []byte) (int, map[string]any) {
	ts := time.Now().Unix()
	req := httptest.NewRequest("POST", "/v1/inbound/email", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "message/rfc822")
	req.Header.Set("X-Outflare-To", to)
	req.Header.Set("X-Outflare-Timestamp", fmt.Sprint(ts))
	req.Header.Set("X-Outflare-Signature", "sha256="+inboundemail.Sign(secret, ts, raw))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func TestInboundEmailOff(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	got := owner.must(200, "GET", "/v1/workspaces/"+ws+"/inbound-addresses", nil)
	if got["available"] != false || len(got["data"].([]any)) != 0 {
		t.Fatalf("off: %v", got)
	}
	owner.must(404, "POST", "/v1/workspaces/"+ws+"/inbound-addresses", map[string]any{"purpose": "support"})
	if code, _ := deliver(e.h, "x@in.example.test", []byte("Subject: hi\r\n\r\nhi"), inboundSecret); code != 404 {
		t.Fatalf("delivery on a server without inbound email: %d", code)
	}
}

func TestInboundEmail(t *testing.T) {
	got := track.Capture(t)
	inbound := &inboundemail.Service{}
	inbound.Enable("in.example.test", inboundSecret)
	inbound.Register(inboundemail.PurposeAIReceipts, inboundemail.Purpose{})
	inbound.Register(inboundemail.PurposeSupport, inboundemail.Purpose{AnyForwarder: true, Suffix: "support"})
	e := freshEnvWith(t, func(o *api.Options) { o.InboundEmail = inbound })
	inbound.Pool = e.pool
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws

	owner.must(400, "POST", base+"/inbound-addresses", map[string]any{"purpose": "leads"})
	owner.must(400, "POST", base+"/inbound-addresses", map[string]any{"purpose": "ai_receipts", "name": "receipts"})
	addr := owner.must(201, "POST", base+"/inbound-addresses", map[string]any{"purpose": "ai_receipts"})
	email := addr["email"].(string)
	if !strings.HasPrefix(email, "acme-outbound-") || !strings.HasSuffix(email, "@in.example.test") {
		t.Fatalf("address: %v", addr)
	}
	if again := owner.must(200, "POST", base+"/inbound-addresses", map[string]any{"purpose": "ai_receipts"}); again["email"] != email {
		t.Fatalf("not kept: %v", again)
	}

	// Support addresses read like support inboxes, one per brand, and can
	// be renamed.
	sup := owner.must(201, "POST", base+"/inbound-addresses", map[string]any{"purpose": "support"})
	if sup["email"] != "acme-outbound-support@in.example.test" || sup["brand_id"] != nil {
		t.Fatalf("support address: %v", sup)
	}
	owner.must(201, "POST", base+"/brands", map[string]any{"name": "Repocket", "handle": "repocket", "website": "https://repocket.com"})
	rp := owner.must(201, "POST", base+"/inbound-addresses", map[string]any{"purpose": "support", "brand": "repocket"})
	if rp["email"] != "repocket-support@in.example.test" || rp["brand_id"] == nil {
		t.Fatalf("brand address: %v", rp)
	}
	owner.must(400, "POST", base+"/inbound-addresses", map[string]any{"purpose": "support", "brand": "nope"})
	owner.must(400, "POST", base+"/inbound-addresses", map[string]any{"purpose": "support", "brand": "repocket", "name": "Help Desk"})
	owner.must(409, "POST", base+"/inbound-addresses", map[string]any{"purpose": "support", "brand": "repocket", "name": "acme-outbound-support"})
	if r := owner.must(201, "POST", base+"/inbound-addresses", map[string]any{"purpose": "support", "brand": "repocket", "name": "help-repocket"}); r["email"] != "help-repocket@in.example.test" {
		t.Fatalf("renamed: %v", r)
	}
	if r := owner.must(200, "POST", base+"/inbound-addresses", map[string]any{"purpose": "support", "brand": "repocket", "name": "help-repocket"}); r["email"] != "help-repocket@in.example.test" {
		t.Fatalf("same name: %v", r)
	}
	if got := owner.must(200, "GET", base+"/inbound-addresses", nil); got["available"] != true || got["domain"] != "in.example.test" || len(got["data"].([]any)) != 3 || len(got["purposes"].([]any)) != 2 {
		t.Fatalf("list: %v", got)
	}

	// Deliveries must be signed, to a live address.
	raw := []byte("From: Anthropic <receipts@anthropic.example>\r\nTo: jack@example.com\r\nSubject: Your receipt\r\nMessage-ID: <r1@example.test>\r\n\r\nTotal $200.00\r\n")
	if code, _ := deliver(e.h, email, raw, []byte("wrong")); code != 401 {
		t.Fatalf("bad signature: %d", code)
	}
	if code, _ := deliver(e.h, "nobody-1234567890@in.example.test", raw, inboundSecret); code != 404 {
		t.Fatalf("unknown address: %d", code)
	}
	if code, _ := deliver(e.h, "repocket-support@in.example.test", raw, inboundSecret); code != 404 {
		t.Fatalf("renamed address: %d", code)
	}
	code, out := deliver(e.h, email, raw, inboundSecret)
	if code != 202 || out["queued"] != true {
		t.Fatalf("delivery: %d %v", code, out)
	}
	if code, out := deliver(e.h, email, raw, inboundSecret); code != 200 || out["queued"] != false {
		t.Fatalf("duplicate: %d %v", code, out)
	}
	// Processing it: the owner's own address forwarded it (a Google
	// Workspace rule keeps them as a recipient); nothing handles receipts
	// in this test, so it is ignored.
	payload, _ := json.Marshal(map[string]string{"id": out["id"].(string)})
	if err := inbound.Process(t.Context(), jobs.Job{Kind: inboundemail.JobProcess, Payload: payload}); err != nil {
		t.Fatal(err)
	}
	list := owner.must(200, "GET", base+"/inbound-emails", nil)["data"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["status"] != "ignored" || list[0].(map[string]any)["forwarded_by"] != "jack@example.com" {
		t.Fatalf("emails: %v", list)
	}

	// Replacing the address refuses mail to the old one.
	repl := owner.must(201, "POST", base+"/inbound-addresses", map[string]any{"purpose": "ai_receipts", "replace": true})
	if repl["email"] == email {
		t.Fatal("not replaced")
	}
	if code, _ := deliver(e.h, email, []byte("Subject: x\r\nMessage-ID: <r2@example.test>\r\n\r\nx"), inboundSecret); code != 404 {
		t.Fatalf("old address: %d", code)
	}
	for _, name := range []string{"inbound_address.created", "inbound_address.renamed", "inbound_address.replaced"} {
		if len(named(got(), name)) == 0 {
			t.Errorf("no %s event", name)
		}
	}
}
