package api_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/llm"
	"github.com/outflareai/outflare/engine/internal/research"
	"github.com/outflareai/outflare/engine/internal/tgsession"
)

// summaryModel summarises any chat it is shown, and matches nothing.
type summaryModel struct {
	mu        sync.Mutex
	summaries int
}

func (m *summaryModel) Chat(_ context.Context, req llm.Request) (llm.Response, error) {
	out := map[string]any{"record_id": nil, "confidence": 0.1, "kind": "unsure", "reason": "Nothing to go on."}
	if strings.Contains(req.System, "You summarise") {
		m.mu.Lock()
		m.summaries++
		m.mu.Unlock()
		out = map[string]any{"summary": "Fay wants last month's invoice resent.", "open_items": []string{"Resend the invoice"}, "waiting_on": "us"}
	}
	b, _ := json.Marshal(out)
	return llm.Response{Message: llm.Message{Role: "assistant", Text: string(b)}, Stop: llm.StopEnd, Usage: llm.Usage{InputTokens: 80, OutputTokens: 30}}, nil
}

func (m *summaryModel) Provider() llm.Provider { return llm.Claude }
func (m *summaryModel) ID() string             { return "scripted" }

type summaryAI struct{ model *summaryModel }

func (a summaryAI) ForTask(_ context.Context, _, task string, _ int64) (research.Choice, error) {
	return research.Choice{Provider: llm.Claude, Model: a.model, Task: task}, nil
}

func (a summaryAI) Spent(context.Context, string, research.Choice, llm.Usage, string, string) error {
	return nil
}

func TestTelegramFollowUp(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	chat := func(id int64, title string, msgs ...tgsession.Message) *fakeChat {
		return &fakeChat{dialog: tgsession.Dialog{Peer: tgsession.Peer{Kind: "user", ID: id, AccessHash: id * 3}, Title: title}, msgs: msgs}
	}
	in := func(id int, ago time.Duration, text string) tgsession.Message {
		return tgsession.Message{ID: id, From: 1, Text: text, At: now.Add(-ago)}
	}
	out := func(id int, ago time.Duration, text string) tgsession.Message {
		return tgsession.Message{ID: id, Out: true, From: 777001, Text: text, At: now.Add(-ago)}
	}
	fake := &fakeTelegram{chats: []*fakeChat{
		// Waiting five hours: the holder is told.
		chat(5101, "Fay Lin", out(1, 6*time.Hour, "hello"), in(2, 5*time.Hour, "can you resend the invoice?"), in(3, 290*time.Minute, "thanks")),
		// Waiting an hour: not yet.
		chat(5102, "Gus Moe", in(4, time.Hour, "any update?")),
		// Answered.
		chat(5103, "Hal Ito", in(5, 5*time.Hour, "hi"), out(6, 4*time.Hour, "hi!")),
		// Waiting a week: old history, left alone.
		chat(5104, "Ivy Ng", in(7, 7*24*time.Hour, "hello?")),
	}}
	model := &summaryModel{}
	q := jobs.New(e.pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
	q.PollInterval = 20 * time.Millisecond
	svc := &tgsession.Service{Pool: e.pool, Box: e.box, Store: e.integrations, Telegram: fake, Models: summaryAI{model},
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	svc.Register(q)
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { q.Run(runCtx, 2); close(done) }() //nolint:errcheck
	t.Cleanup(func() { cancel(); <-done })

	brand := owner.must(200, "GET", base+"/brands", nil)["data"].([]any)[0].(map[string]any)["id"].(string)
	me := owner.must(200, "GET", "/v1/me", nil)["user"].(map[string]any)["id"].(string)
	jack := owner.must(201, "POST", base+"/staff", map[string]any{"name": "Jack", "user_id": me})["id"].(string)
	kim := owner.must(201, "POST", base+"/staff", map[string]any{"name": "Kim"})["id"].(string)
	integ := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "telegram",
		"credentials": map[string]any{"api_id": "1234567", "api_hash": strings.Repeat("ab", 16)}})["id"].(string)
	acc := owner.must(201, "POST", base+"/staff-accounts", map[string]any{"kind": "telegram", "address": "@acme_sales_1", "holder_id": kim})["id"].(string)
	if _, err := e.pool.Exec(ctx, `INSERT INTO telegram_sessions (account_id, workspace_id, integration_id, status, session_sealed, telegram_user_id, connected_at)
		VALUES ($1, $2, $3, 'connected', $4, 777001, now())`, acc, ws, integ, e.box.Seal([]byte("fake-session-full"), []byte(acc))); err != nil {
		t.Fatal(err)
	}
	owner.must(200, "POST", base+"/staff-accounts/"+acc+"/telegram/sync", map[string]any{"brand_id": brand})

	chats := "/v1/brands/" + brand + "/telegram-chats"
	var status string
	for range 500 {
		status = owner.must(200, "GET", base+"/staff-accounts/"+acc+"/telegram", nil)["sync_status"].(string)
		if status == "live" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if status != "live" {
		t.Fatalf("sync status %s: %v", status, owner.must(200, "GET", base+"/staff-accounts/"+acc+"/telegram", nil))
	}
	byTitle := map[string]map[string]any{}
	for _, c := range owner.must(200, "GET", chats, nil)["data"].([]any) {
		c := c.(map[string]any)
		byTitle[c["title"].(string)] = c
	}
	if w, _ := time.Parse(time.RFC3339, str(byTitle["Fay Lin"]["waiting_since"])); !w.Equal(now.Add(-5 * time.Hour)) {
		t.Fatalf("fay waiting since: %v", byTitle["Fay Lin"])
	}
	if byTitle["Hal Ito"]["waiting_since"] != nil || byTitle["Gus Moe"]["waiting_since"] == nil {
		t.Fatalf("waiting: %v / %v", byTitle["Hal Ito"], byTitle["Gus Moe"])
	}
	if l := owner.must(200, "GET", chats+"?waiting=true", nil)["data"].([]any); len(l) != 3 {
		t.Fatalf("waiting list: %v", l)
	}
	owner.must(400, "GET", chats+"?waiting=maybe", nil)

	// Only Fay has waited long enough, and recently enough.
	if err := svc.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if err := svc.Tick(ctx); err != nil { // told once
		t.Fatal(err)
	}
	alerts := owner.must(200, "GET", "/v1/brands/"+brand+"/alerts?kind=telegram.waiting", nil)["data"].([]any)
	if len(alerts) != 1 || alerts[0].(map[string]any)["target_id"] != byTitle["Fay Lin"]["id"] {
		t.Fatalf("waiting alerts: %v", alerts)
	}
	if v := alerts[0].(map[string]any); !strings.Contains(str(v["title"]), "waiting on Telegram (@acme_sales_1)") || v["path"] != "telegram/"+byTitle["Fay Lin"]["id"].(string) {
		t.Fatalf("alert view: %v", v)
	}

	// A summary on request.
	fay := chats + "/" + byTitle["Fay Lin"]["id"].(string)
	owner.must(202, "POST", fay+"/summary", nil)
	var sum map[string]any
	for range 500 {
		if s, ok := owner.must(200, "GET", fay, nil)["summary"].(map[string]any); ok {
			sum = s
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if sum == nil || sum["text"] != "Fay wants last month's invoice resent." || sum["waiting_on"] != "us" || sum["stale"] != false || len(sum["open_items"].([]any)) != 1 {
		t.Fatalf("summary: %v", sum)
	}
	// Marked personal: nothing left to summarise.
	ivy := chats + "/" + byTitle["Ivy Ng"]["id"].(string)
	owner.must(200, "PATCH", ivy, map[string]any{"kind": "personal"})
	owner.must(400, "POST", ivy+"/summary", nil)

	// Handing the account to Jack summarises its recent chats (Fay's is
	// already done) and tells him.
	owner.must(200, "PATCH", base+"/staff-accounts/"+acc, map[string]any{"holder_id": jack})
	var handovers []any
	for range 500 {
		handovers = owner.must(200, "GET", "/v1/brands/"+brand+"/alerts?kind=telegram.handover", nil)["data"].([]any)
		model.mu.Lock()
		n := model.summaries
		model.mu.Unlock()
		if len(handovers) == 1 && n >= 3 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(handovers) != 1 || handovers[0].(map[string]any)["owner_id"] != me {
		t.Fatalf("handover alerts: %v", handovers)
	}
	model.mu.Lock()
	n := model.summaries
	model.mu.Unlock()
	// Fay, Gus and Hal are recent; Ivy is personal and a week old.
	if n != 3 {
		t.Fatalf("summaries written: %d", n)
	}
}

func str(v any) string {
	s, _ := v.(string)
	return s
}
