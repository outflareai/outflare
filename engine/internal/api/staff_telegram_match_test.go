package api_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/llm"
	"github.com/outflareai/outflare/engine/internal/research"
	"github.com/outflareai/outflare/engine/internal/tgsession"
)

// matchModel picks the candidate named Eli Park, and calls anything else
// personal.
type matchModel struct {
	mu    sync.Mutex
	calls int
}

var candidateLine = regexp.MustCompile(`- id ([0-9a-f-]{36}): Eli Park`)

func (m *matchModel) Chat(_ context.Context, req llm.Request) (llm.Response, error) {
	m.mu.Lock()
	m.calls++
	m.mu.Unlock()
	out := map[string]any{"record_id": nil, "confidence": 0.2, "kind": "personal", "reason": "Family chat about dinner."}
	if c := candidateLine.FindStringSubmatch(req.Messages[0].Text); c != nil {
		out = map[string]any{"record_id": c[1], "confidence": 0.8, "kind": "customer", "reason": "Same name, and the chat is about an order."}
	}
	b, _ := json.Marshal(out)
	return llm.Response{Message: llm.Message{Role: "assistant", Text: string(b)}, Stop: llm.StopEnd, Usage: llm.Usage{InputTokens: 50, OutputTokens: 20}}, nil
}

func (m *matchModel) Provider() llm.Provider { return llm.Claude }
func (m *matchModel) ID() string             { return "scripted" }

type matchAI struct{ model *matchModel }

func (a matchAI) ForTask(_ context.Context, _, task string, _ int64) (research.Choice, error) {
	return research.Choice{Provider: llm.Claude, Model: a.model, Task: task}, nil
}

func (a matchAI) Spent(context.Context, string, research.Choice, llm.Usage, string, string) error {
	return nil
}

func TestTelegramMatching(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	msg := func(id int, text string) tgsession.Message {
		return tgsession.Message{ID: id, From: 1, Text: text, At: now.Add(time.Duration(id-100) * time.Minute)}
	}
	chat := func(kind string, id int64, title, username, phone string, msgs ...tgsession.Message) *fakeChat {
		return &fakeChat{dialog: tgsession.Dialog{Peer: tgsession.Peer{Kind: kind, ID: id, AccessHash: id * 3}, Title: title, Username: username, Phone: phone, Members: 3}, msgs: msgs}
	}
	fake := &fakeTelegram{chats: []*fakeChat{
		chat("user", 5001, "Cara Diaz", "", "", msg(1, "hi")),
		chat("user", 5003, "Dev R", "", "+447700900556", msg(2, "quote please")),
		chat("channel", 6001, "Acme x Northpeak", "", "", msg(3, "Loop in ana@northpeak.example please")),
		chat("user", 5004, "Eli Park", "eli_p", "", msg(4, "where is my order?")),
		chat("user", 5005, "Mum", "", "", msg(5, "dinner on sunday?")),
	}}
	model := &matchModel{}
	q := jobs.New(e.pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
	q.PollInterval = 20 * time.Millisecond
	(&tgsession.Service{Pool: e.pool, Box: e.box, Store: e.integrations, Telegram: fake, Models: matchAI{model},
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}).Register(q)
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { q.Run(runCtx, 2); close(done) }() //nolint:errcheck
	t.Cleanup(func() { cancel(); <-done })

	brand := owner.must(200, "GET", base+"/brands", nil)["data"].([]any)[0].(map[string]any)["id"].(string)
	recs := "/v1/brands/" + brand + "/records"
	cara := owner.must(201, "POST", recs, map[string]any{"type": "person", "name": "Cara Diaz",
		"identities": []any{map[string]any{"namespace": "telegram.user", "value": "5001"}}})["id"].(string)
	dev := owner.must(201, "POST", recs, map[string]any{"type": "person", "name": "Dev Rao",
		"identities": []any{map[string]any{"namespace": "phone", "value": "+447700900556"}}})["id"].(string)
	northpeak := owner.must(201, "POST", recs, map[string]any{"type": "account", "name": "Northpeak",
		"identities": []any{map[string]any{"namespace": "domain", "value": "northpeak.example"}}})["id"].(string)
	eli := owner.must(201, "POST", recs, map[string]any{"type": "person", "name": "Eli Park"})["id"].(string)

	me := owner.must(200, "GET", "/v1/me", nil)["user"].(map[string]any)["id"].(string)
	jack := owner.must(201, "POST", base+"/staff", map[string]any{"name": "Jack", "user_id": me})["id"].(string)
	integ := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "telegram",
		"credentials": map[string]any{"api_id": "1234567", "api_hash": strings.Repeat("ab", 16)}})["id"].(string)
	acc := owner.must(201, "POST", base+"/staff-accounts", map[string]any{"kind": "telegram", "address": "@acme_sales_1", "holder_id": jack})["id"].(string)
	acc2 := owner.must(201, "POST", base+"/staff-accounts", map[string]any{"kind": "telegram", "address": "@acme_sales_2"})["id"].(string)
	for i, id := range []string{acc, acc2} {
		if _, err := e.pool.Exec(ctx, `INSERT INTO telegram_sessions (account_id, workspace_id, integration_id, status, session_sealed, telegram_user_id, connected_at)
			VALUES ($1, $2, $3, 'connected', $4, $5, now())`, id, ws, integ, e.box.Seal([]byte("fake-session-full"), []byte(id)), 777001+i); err != nil {
			t.Fatal(err)
		}
	}
	owner.must(200, "POST", base+"/staff-accounts/"+acc+"/telegram/sync", map[string]any{"brand_id": brand})

	chats := "/v1/brands/" + brand + "/telegram-chats"
	var list []any
	for range 500 {
		// All first: chats synced between the two reads would look matched.
		all := owner.must(200, "GET", chats+"?account_id="+acc, nil)["data"].([]any)
		list = owner.must(200, "GET", chats+"?account_id="+acc+"&match_status=pending", nil)["data"].([]any)
		if len(all) == 5 && len(list) == 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	byTitle := map[string]map[string]any{}
	for _, c := range owner.must(200, "GET", chats+"?account_id="+acc, nil)["data"].([]any) {
		c := c.(map[string]any)
		byTitle[c["title"].(string)] = c
	}
	check := func(title, status, reason, record string) map[string]any {
		t.Helper()
		c := byTitle[title]
		got := ""
		if r, ok := c["suggested_record"].(map[string]any); ok {
			got = r["id"].(string)
		}
		if r, ok := c["record"].(map[string]any); ok && status == "confirmed" {
			got = r["id"].(string)
		}
		if c["match_status"] != status || (reason != "" && c["match_reason"] != reason) || got != record {
			t.Fatalf("%s: %v", title, c)
		}
		return c
	}
	if c := check("Cara Diaz", "confirmed", "telegram", cara); c["kind"] != "customer" {
		t.Fatalf("cara kind: %v", c)
	}
	check("Dev R", "suggested", "phone", dev)
	check("Acme x Northpeak", "suggested", "domain", northpeak)
	if c := check("Eli Park", "suggested", "ai", eli); c["match_note"] == nil || c["mine"] != true {
		t.Fatalf("eli: %v", c)
	}
	if c := check("Mum", "no_match", "ai", ""); c["suggested_kind"] != "personal" || c["kind"] != "unsorted" {
		t.Fatalf("mum: %v", c)
	}

	// The holder's queue.
	queue := owner.must(200, "GET", chats+"?match_status=suggested,no_match&mine=true", nil)["data"].([]any)
	if len(queue) != 4 {
		t.Fatalf("queue: %v", queue)
	}
	owner.must(400, "GET", chats+"?match_status=maybe", nil)
	owner.must(400, "GET", chats+"?mine=maybe", nil)

	// Confirming gives the record the chat's Telegram id, username and phone.
	devChat := byTitle["Dev R"]["id"].(string)
	owner.must(404, "POST", chats+"/"+devChat+"/match", map[string]any{"record_id": "00000000-0000-4000-8000-000000000000"})
	if c := owner.must(200, "POST", chats+"/"+devChat+"/match", map[string]any{"record_id": dev}); c["match_status"] != "confirmed" || c["record"].(map[string]any)["name"] != "Dev Rao" || c["match_reason"] != "phone" {
		t.Fatalf("confirmed: %v", c)
	}
	ids := owner.must(200, "GET", recs+"/"+dev+"/identities", nil)["data"].([]any)
	found := false
	for _, i := range ids {
		i := i.(map[string]any)
		if i["namespace"] == "telegram.user" && i["value"] == "5003" && i["verified"] == true {
			found = true
		}
	}
	if !found {
		t.Fatalf("identities: %v", ids)
	}
	// Changing it to someone else moves the Telegram id.
	eliChat := byTitle["Eli Park"]["id"].(string)
	owner.must(200, "POST", chats+"/"+eliChat+"/match", map[string]any{"record_id": dev})
	owner.must(200, "POST", chats+"/"+eliChat+"/match", map[string]any{"record_id": eli})
	for _, i := range owner.must(200, "GET", recs+"/"+dev+"/identities", nil)["data"].([]any) {
		if i := i.(map[string]any); i["value"] == "5004" && i["valid_to"] == nil {
			t.Fatalf("dev kept eli's telegram: %v", i)
		}
	}
	// None of these: it waits for someone to pick.
	if c := owner.must(200, "POST", chats+"/"+byTitle["Acme x Northpeak"]["id"].(string)+"/match", map[string]any{"record_id": nil}); c["match_status"] != "no_match" || c["suggested_record"] != nil {
		t.Fatalf("no match: %v", c)
	}
	// A personal chat can't be matched.
	mum := byTitle["Mum"]["id"].(string)
	owner.must(200, "PATCH", chats+"/"+mum, map[string]any{"kind": "personal"})
	owner.must(400, "POST", chats+"/"+mum+"/match", map[string]any{"record_id": eli})

	// The second account's chat with Dev matches by itself now.
	owner.must(200, "POST", base+"/staff-accounts/"+acc2+"/telegram/sync", map[string]any{"brand_id": brand})
	var c2 map[string]any
	for range 500 {
		l := owner.must(200, "GET", chats+"?account_id="+acc2+"&record_id="+dev, nil)["data"].([]any)
		if len(l) == 1 {
			c2 = l[0].(map[string]any)
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if c2 == nil || c2["match_status"] != "confirmed" || c2["match_reason"] != "telegram" || c2["mine"] != false {
		t.Fatalf("second account: %v", c2)
	}
	model.mu.Lock()
	defer model.mu.Unlock()
	if model.calls == 0 {
		t.Fatal("the AI was never asked")
	}
}
