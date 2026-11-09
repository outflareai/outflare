package api_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/connectors/tgapp"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/tgsession"
)

// fakeTelegram stands in for Telegram: a QR sign-in waits until the test
// scans it, and the account has a two-step password.
type fakeTelegram struct {
	mu      sync.Mutex
	scanned chan struct{}
	apps    []tgapp.App
	logouts int
	chats   []*fakeChat
}

func (f *fakeTelegram) QRLogin(ctx context.Context, app tgapp.App, _ []byte, show func(string, time.Time) error) (tgsession.Result, error) {
	f.mu.Lock()
	f.apps = append(f.apps, app)
	f.mu.Unlock()
	if err := show("tg://login?token=fake-token", time.Now().Add(30*time.Second)); err != nil {
		return tgsession.Result{}, err
	}
	select {
	case <-ctx.Done():
		return tgsession.Result{}, ctx.Err()
	case <-f.scanned:
	}
	return tgsession.Result{Session: []byte("fake-session-half"), NeedPassword: true, PasswordHint: "first pet"}, nil
}

func (f *fakeTelegram) Password(_ context.Context, _ tgapp.App, session []byte, password string) (tgsession.Result, error) {
	if password != "right horse" || string(session) != "fake-session-half" {
		return tgsession.Result{Session: session}, tgsession.ErrWrongPassword
	}
	return tgsession.Result{Session: []byte("fake-session-full"), Self: &tgsession.Self{ID: 777001, Username: "acme_sales_1", Phone: "+447700900123", Name: "Acme Sales"}}, nil
}

func (f *fakeTelegram) Check(_ context.Context, _ tgapp.App, session []byte) (tgsession.Health, error) {
	if string(session) != "fake-session-full" {
		return tgsession.Health{}, tgsession.ErrSignedOut
	}
	return tgsession.Health{Self: tgsession.Self{ID: 777001, Username: "acme_sales_1"}, TwoStep: true, LoginEmail: true, OtherSessions: 1}, nil
}

func (f *fakeTelegram) LogOut(context.Context, tgapp.App, []byte) error {
	f.mu.Lock()
	f.logouts++
	f.mu.Unlock()
	return nil
}

func TestTelegramAccountSignIn(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws

	fake := &fakeTelegram{scanned: make(chan struct{})}
	q := jobs.New(e.pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
	q.PollInterval = 20 * time.Millisecond
	(&tgsession.Service{Pool: e.pool, Box: e.box, Store: e.integrations, Telegram: fake, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}).Register(q)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { q.Run(ctx, 2); close(done) }() //nolint:errcheck
	t.Cleanup(func() { cancel(); <-done })

	ben := owner.must(201, "POST", base+"/staff", map[string]any{"name": "Ben Ito"})["id"].(string)
	acc := owner.must(201, "POST", base+"/staff-accounts", map[string]any{"kind": "telegram", "address": "@acme_sales_1", "holder_id": ben})["id"].(string)
	phone := owner.must(201, "POST", base+"/staff-accounts", map[string]any{"kind": "phone", "address": "+447700900124"})["id"].(string)
	tg := base + "/staff-accounts/" + acc + "/telegram"

	if v := owner.must(200, "GET", tg, nil); v["status"] != "not_connected" {
		t.Fatalf("before: %v", v)
	}
	// No Telegram app yet: the person is told to connect one.
	owner.must(400, "POST", tg+"/connect", nil)
	owner.must(422, "POST", base+"/integrations", map[string]any{"connector": "telegram", "credentials": map[string]any{"api_id": "x", "api_hash": "short"}})
	owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "telegram",
		"credentials": map[string]any{"api_id": "1234567", "api_hash": strings.Repeat("AB", 16)}})
	owner.must(400, "POST", base+"/staff-accounts/"+phone+"/telegram/connect", nil)

	// The lock-down checklist can be ticked before the first sign-in.
	owner.must(400, "PATCH", tg, map[string]any{"checklist": map[string]any{"made_up": true}})
	if v := owner.must(200, "PATCH", tg, map[string]any{"checklist": map[string]any{"number_owned": true}}); v["checklist"].(map[string]any)["number_owned"] != true {
		t.Fatalf("checklist: %v", v)
	}

	if v := owner.must(200, "POST", tg+"/connect", nil); v["status"] != "starting" {
		t.Fatalf("connect: %v", v)
	}
	wait := func(status string) map[string]any {
		t.Helper()
		var v map[string]any
		for range 300 {
			v = owner.must(200, "GET", tg, nil)
			if v["status"] == status {
				return v
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("never %s: %v", status, v)
		return nil
	}
	v := wait("waiting_scan")
	if v["login_url"] != "tg://login?token=fake-token" || v["login_expires_at"] == nil || !strings.HasPrefix(v["login_qr"].(string), "data:image/png;base64,") {
		t.Fatalf("qr: %v", v)
	}
	fake.mu.Lock()
	if len(fake.apps) != 1 || fake.apps[0].ID != 1234567 || fake.apps[0].Hash != strings.Repeat("ab", 16) {
		t.Fatalf("app: %+v", fake.apps)
	}
	fake.mu.Unlock()

	close(fake.scanned)
	v = wait("needs_password")
	if v["password_hint"] != "first pet" || v["login_url"] != nil {
		t.Fatalf("password needed: %v", v)
	}
	owner.must(400, "POST", tg+"/password", map[string]any{"password": ""})
	owner.must(200, "POST", tg+"/password", map[string]any{"password": "wrong"})
	for range 300 {
		v = owner.must(200, "GET", tg, nil)
		if v["error_code"] == "wrong_password" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if v["status"] != "needs_password" || v["error_code"] != "wrong_password" {
		t.Fatalf("wrong password: %v", v)
	}
	owner.must(200, "POST", tg+"/password", map[string]any{"password": "right horse"})
	v = wait("connected")
	if v["telegram_username"] != "acme_sales_1" || v["telegram_user_id"] != "777001" || v["connected_at"] == nil {
		t.Fatalf("connected: %v", v)
	}
	// The first check runs by itself.
	for range 300 {
		if v = owner.must(200, "GET", tg, nil); v["last_checked_at"] != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if v["two_step"] != true || v["login_email"] != true || v["other_sessions"] != 1.0 {
		t.Fatalf("checked: %v", v)
	}
	owner.must(202, "POST", tg+"/check", nil)
	owner.must(400, "POST", tg+"/connect", nil)

	// The session and the password are sealed; the password is gone.
	var session, password []byte
	if err := e.pool.QueryRow(context.Background(), `SELECT session_sealed, password_sealed FROM telegram_sessions WHERE account_id = $1`, acc).Scan(&session, &password); err != nil {
		t.Fatal(err)
	}
	if len(session) == 0 || bytes.Contains(session, []byte("fake-session")) || password != nil {
		t.Fatalf("stored session %q, password %q", session, password)
	}

	// Another workspace sees none of it.
	ws2 := owner.must(201, "POST", "/v1/workspaces", map[string]string{"name": "Second"})["id"].(string)
	owner.must(404, "GET", "/v1/workspaces/"+ws2+"/staff-accounts/"+acc+"/telegram", nil)
	owner.must(404, "POST", "/v1/workspaces/"+ws2+"/staff-accounts/"+acc+"/telegram/disconnect", nil)

	// A signed-in account can't be removed; retiring signs it out.
	owner.must(400, "DELETE", base+"/staff-accounts/"+acc, nil)
	owner.must(200, "PATCH", base+"/staff-accounts/"+acc, map[string]any{"status": "retired"})
	v = wait("signed_out")
	fake.mu.Lock()
	if fake.logouts != 1 {
		t.Fatalf("logouts: %d", fake.logouts)
	}
	fake.mu.Unlock()
	if v["checklist"].(map[string]any)["number_owned"] != true {
		t.Fatalf("checklist kept: %v", v)
	}
	owner.must(400, "POST", tg+"/connect", nil)
}

// fakeChat is a chat in fakeTelegram, its messages oldest first.
type fakeChat struct {
	dialog tgsession.Dialog
	msgs   []tgsession.Message
}

func (f *fakeTelegram) Read(ctx context.Context, _ tgapp.App, session []byte, read func(tgsession.Reader) error) ([]byte, error) {
	if string(session) != "fake-session-full" {
		return nil, tgsession.ErrSignedOut
	}
	return session, read(fakeReader{f})
}

type fakeReader struct{ f *fakeTelegram }

func (r fakeReader) Dialogs(_ context.Context, each func(tgsession.Dialog) (bool, error)) error {
	r.f.mu.Lock()
	var list []tgsession.Dialog
	for _, c := range r.f.chats {
		d := c.dialog
		if len(c.msgs) > 0 {
			d.TopMessage = c.msgs[len(c.msgs)-1].ID
		}
		list = append(list, d)
	}
	r.f.mu.Unlock()
	for _, d := range list {
		if more, err := each(d); err != nil || !more {
			return err
		}
	}
	return nil
}

func (r fakeReader) History(_ context.Context, peer tgsession.Peer, offset, minID, limit int) ([]tgsession.Message, error) {
	r.f.mu.Lock()
	defer r.f.mu.Unlock()
	var out []tgsession.Message
	for _, c := range r.f.chats {
		if c.dialog.Peer.Kind != peer.Kind || c.dialog.Peer.ID != peer.ID {
			continue
		}
		if c.dialog.Peer.AccessHash != peer.AccessHash {
			return nil, errors.New("wrong access hash")
		}
		for i := len(c.msgs) - 1; i >= 0 && len(out) < limit; i-- {
			m := c.msgs[i]
			if (offset == 0 || m.ID < offset) && m.ID > minID {
				out = append(out, m)
			}
		}
	}
	return out, nil
}

func TestTelegramSync(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Second)
	cara := &fakeChat{dialog: tgsession.Dialog{Peer: tgsession.Peer{Kind: "user", ID: 5001, AccessHash: 91}, Title: "Cara Diaz", Username: "cara_d", Phone: "+447700900555"}}
	// 150 messages; the first is older than the year the import goes back.
	for i := 1; i <= 150; i++ {
		at := now.Add(time.Duration(i-151) * time.Minute)
		if i == 1 {
			at = now.AddDate(0, 0, -400)
		}
		cara.msgs = append(cara.msgs, tgsession.Message{ID: i, Out: i%2 == 0, From: 5001, FromName: "Cara Diaz", Text: "hello " + strconv.Itoa(i), At: at})
	}
	group := &fakeChat{dialog: tgsession.Dialog{Peer: tgsession.Peer{Kind: "channel", ID: 6001, AccessHash: 92}, Title: "Acme x Northpeak", Members: 4},
		msgs: []tgsession.Message{
			{ID: 10, From: 5002, FromName: "Dev Rao", Text: "pricing question", At: now.Add(-time.Hour)},
			{ID: 11, Out: true, From: 777001, Text: "sent the sheet", Media: "file", FileName: "pricing.pdf", ReplyTo: 10, At: now.Add(-30 * time.Minute)},
		}}
	colleague := &fakeChat{dialog: tgsession.Dialog{Peer: tgsession.Peer{Kind: "user", ID: 777002, AccessHash: 93}, Title: "Acme Support"},
		msgs: []tgsession.Message{{ID: 3, Text: "lunch?", At: now}}}
	saved := &fakeChat{dialog: tgsession.Dialog{Peer: tgsession.Peer{Kind: "user", ID: 777001}, Self: true}, msgs: []tgsession.Message{{ID: 1, Text: "note to self", At: now}}}
	bot := &fakeChat{dialog: tgsession.Dialog{Peer: tgsession.Peer{Kind: "user", ID: 8001}, Bot: true}, msgs: []tgsession.Message{{ID: 1, Text: "beep", At: now}}}
	fake := &fakeTelegram{chats: []*fakeChat{cara, group, colleague, saved, bot}}

	q := jobs.New(e.pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
	q.PollInterval = 20 * time.Millisecond
	(&tgsession.Service{Pool: e.pool, Box: e.box, Store: e.integrations, Telegram: fake, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}).Register(q)
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { q.Run(runCtx, 2); close(done) }() //nolint:errcheck
	t.Cleanup(func() { cancel(); <-done })

	brand := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "website": "https://northpeak.example"})["id"].(string)
	other := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Repocket", "website": "https://repocket.com/"})["id"].(string)
	integ := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "telegram",
		"credentials": map[string]any{"api_id": "1234567", "api_hash": strings.Repeat("ab", 16)}})["id"].(string)
	acc := owner.must(201, "POST", base+"/staff-accounts", map[string]any{"kind": "telegram", "address": "@acme_sales_1"})["id"].(string)
	acc2 := owner.must(201, "POST", base+"/staff-accounts", map[string]any{"kind": "telegram", "address": "@acme_support"})["id"].(string)
	tg := base + "/staff-accounts/" + acc + "/telegram"

	// Not signed in: nothing to sync.
	owner.must(400, "POST", tg+"/sync", map[string]any{"brand_id": brand})
	// Two signed-in accounts (the sign-in itself is TestTelegramAccountSignIn).
	for _, s := range []struct {
		id   string
		user int64
	}{{acc, 777001}, {acc2, 777002}} {
		if _, err := e.pool.Exec(ctx, `INSERT INTO telegram_sessions (account_id, workspace_id, integration_id, status, session_sealed, telegram_user_id, connected_at)
			VALUES ($1, $2, $3, 'connected', $4, $5, now())`, s.id, ws, integ, e.box.Seal([]byte("fake-session-full"), []byte(s.id)), s.user); err != nil {
			t.Fatal(err)
		}
	}
	owner.must(400, "POST", tg+"/sync", nil)
	owner.must(400, "POST", tg+"/sync", map[string]any{"brand_id": "00000000-0000-4000-8000-000000000000"})
	owner.must(400, "POST", tg+"/sync", map[string]any{"brand_id": brand, "history_days": 9999})
	if v := owner.must(200, "POST", tg+"/sync", map[string]any{"brand_id": brand}); v["sync_status"] != "importing" || v["brand_id"] != brand || v["history_days"] != 365.0 {
		t.Fatalf("start: %v", v)
	}
	waitFor := func(what string, ok func(map[string]any) bool) map[string]any {
		t.Helper()
		var v map[string]any
		for range 500 {
			if v = owner.must(200, "GET", tg, nil); ok(v) {
				return v
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("never %s: %v", what, v)
		return nil
	}
	v := waitFor("live", func(v map[string]any) bool { return v["sync_status"] == "live" })
	if v["chats"] != 3.0 || v["messages"] != 151.0 || v["synced_at"] == nil || v["sync_error"] != nil {
		t.Fatalf("synced: %v", v)
	}

	b := "/v1/brands/" + brand + "/telegram-chats"
	chats := owner.must(200, "GET", b, nil)["data"].([]any)
	if len(chats) != 3 {
		t.Fatalf("chats: %v", chats)
	}
	byTitle := map[string]map[string]any{}
	for _, c := range chats {
		c := c.(map[string]any)
		byTitle[c["title"].(string)] = c
	}
	c := byTitle["Cara Diaz"]
	if c["kind"] != "unsorted" || c["message_count"] != 149.0 || c["username"] != "cara_d" || c["phone"] != "+447700900555" || c["peer_id"] != "5001" || c["history_done"] != true || c["account_label"] == nil {
		t.Fatalf("cara: %v", c)
	}
	if g := byTitle["Acme x Northpeak"]; g["members"] != 4.0 || g["message_count"] != 2.0 || g["peer_kind"] != "channel" {
		t.Fatalf("group: %v", g)
	}
	if in := byTitle["Acme Support"]; in["kind"] != "internal" || in["message_count"] != 0.0 {
		t.Fatalf("colleague: %v", in)
	}
	if l := owner.must(200, "GET", b+"?kind=internal", nil)["data"].([]any); len(l) != 1 {
		t.Fatalf("by kind: %v", l)
	}
	owner.must(400, "GET", b+"?kind=nope", nil)
	page := owner.must(200, "GET", b+"?limit=2", nil)
	if len(page["data"].([]any)) != 2 || page["next_cursor"] == nil {
		t.Fatalf("paged: %v", page)
	}
	if rest := owner.must(200, "GET", b+"?limit=2&cursor="+page["next_cursor"].(string), nil); len(rest["data"].([]any)) != 1 || rest["next_cursor"] != nil {
		t.Fatalf("page 2: %v", rest)
	}

	cid := c["id"].(string)
	msgs := owner.must(200, "GET", b+"/"+cid+"/messages", nil)
	list := msgs["data"].([]any)
	if len(list) != 100 || msgs["next_cursor"] == nil {
		t.Fatalf("messages: %d %v", len(list), msgs["next_cursor"])
	}
	if m := list[0].(map[string]any); m["message_id"] != 150.0 || m["text"] != "hello 150" || m["outgoing"] != true || m["sender_id"] != "5001" {
		t.Fatalf("newest: %v", m)
	}
	gm := owner.must(200, "GET", b+"/"+byTitle["Acme x Northpeak"]["id"].(string)+"/messages", nil)["data"].([]any)
	if m := gm[0].(map[string]any); m["file_name"] != "pricing.pdf" || m["media"] != "file" || m["reply_to"] != 10.0 {
		t.Fatalf("group message: %v", m)
	}

	// Content is sealed at rest.
	var sealed []byte
	if err := e.pool.QueryRow(ctx, `SELECT content_sealed FROM telegram_messages WHERE chat_id = $1 AND message_id = 150`, cid).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, []byte("hello")) {
		t.Fatal("message stored in the clear")
	}

	// New messages come in on the next sync.
	fake.mu.Lock()
	cara.msgs = append(cara.msgs, tgsession.Message{ID: 151, From: 5001, FromName: "Cara Diaz", Text: "any update?", At: now})
	fake.mu.Unlock()
	owner.must(200, "POST", tg+"/sync", nil)
	waitFor("new message", func(v map[string]any) bool { return v["messages"] == 152.0 && v["sync_status"] == "live" })

	// A personal chat keeps nothing.
	owner.must(400, "PATCH", b+"/"+cid, map[string]any{"kind": "nope"})
	if p := owner.must(200, "PATCH", b+"/"+cid, map[string]any{"kind": "personal", "private": true}); p["kind"] != "personal" || p["private"] != true || p["message_count"] != 0.0 {
		t.Fatalf("personal: %v", p)
	}
	if l := owner.must(200, "GET", b+"/"+cid+"/messages", nil)["data"].([]any); len(l) != 0 {
		t.Fatalf("personal kept: %v", l)
	}

	// The chats stay in their brand; the account can't move brands.
	owner.must(400, "POST", tg+"/sync", map[string]any{"brand_id": other})
	owner.must(404, "GET", "/v1/brands/"+other+"/telegram-chats/"+cid, nil)
	if l := owner.must(200, "GET", "/v1/brands/"+other+"/telegram-chats", nil)["data"].([]any); len(l) != 0 {
		t.Fatalf("brand leak: %v", l)
	}

	if p := owner.must(200, "POST", tg+"/sync/pause", nil); p["sync_status"] != "paused" {
		t.Fatalf("pause: %v", p)
	}
}
