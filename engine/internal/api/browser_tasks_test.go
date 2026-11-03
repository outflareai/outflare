package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"

	browserwork "github.com/outflareai/outflare/engine/internal/browser"
	"github.com/outflareai/outflare/engine/internal/llm"
	"github.com/outflareai/outflare/engine/internal/research"
)

// A scripted site, model and AI for browser tasks: the task signs in
// from the vault, meets MFA, waits for a person, resumes after the
// handback and finishes, with every step logged and no secret anywhere.
type browserSite struct {
	page  string
	typed map[int]string
}

func (d *browserSite) Navigate(_ context.Context, url string) error {
	if !strings.HasPrefix(url, "https://community.example.com") {
		return errors.New("outside")
	}
	d.page = "signin"
	return nil
}

func (d *browserSite) Page(context.Context) (browserwork.Page, error) {
	switch d.page {
	case "signin":
		return browserwork.Page{URL: "https://community.example.com/login", Title: "Sign in", Text: "Sign in", Elements: []browserwork.Element{{Ref: 1, Role: "textbox", Label: "Email"}, {Ref: 2, Role: "password", Label: "Password"}, {Ref: 3, Role: "button", Label: "Sign in"}}}, nil
	case "mfa":
		return browserwork.Page{URL: "https://community.example.com/mfa", Title: "Code", Text: "Enter the code", Elements: []browserwork.Element{{Ref: 1, Role: "textbox", Label: "Code"}}}, nil
	default:
		return browserwork.Page{URL: "https://community.example.com/home", Title: "Home", Text: "Signed in. Your password is " + d.typed[2], Elements: []browserwork.Element{{Ref: 1, Role: "textbox", Label: "Post"}, {Ref: 2, Role: "button", Label: "Publish"}}}, nil
	}
}

func (d *browserSite) Click(_ context.Context, ref int) error {
	if d.page == "signin" && ref == 3 {
		if d.typed[2] != "correct horse battery staple" {
			return nil
		}
		if d.typed[1] == "first" {
			d.page = "mfa"
		} else {
			d.page = "home"
		}
	}
	return nil
}

func (d *browserSite) Type(_ context.Context, ref int, text string, _ bool) error {
	if d.typed == nil {
		d.typed = map[int]string{}
	}
	d.typed[ref] = text
	return nil
}

func (d *browserSite) Close() error { return nil }

type browserOpener struct{ site *browserSite }

func (o browserOpener) Open(_ context.Context, b browserwork.Boundary) (browserwork.Driver, error) {
	if len(b.Hosts) == 0 {
		return nil, errors.New("no hosts")
	}
	o.site.page = ""
	o.site.typed = nil
	return o.site, nil
}

type browserModel struct {
	rounds [][]llm.ToolCall
	seen   []string
	calls  int
}

func (m *browserModel) Chat(_ context.Context, req llm.Request) (llm.Response, error) {
	for _, msg := range req.Messages {
		m.seen = append(m.seen, msg.Text)
		for _, r := range msg.ToolResults {
			m.seen = append(m.seen, r.Content)
		}
	}
	if m.calls >= len(m.rounds) {
		return llm.Response{Message: llm.Message{Role: "assistant", Text: "stopped"}, Stop: llm.StopEnd}, nil
	}
	calls := m.rounds[m.calls]
	m.calls++
	return llm.Response{Message: llm.Message{Role: "assistant", ToolCalls: calls}, Stop: llm.StopToolUse, Usage: llm.Usage{InputTokens: 10, OutputTokens: 5}}, nil
}

func (m *browserModel) Provider() llm.Provider { return llm.Claude }
func (m *browserModel) ID() string             { return "scripted" }

type browserAI struct{ model *browserModel }

func (a browserAI) ForTask(_ context.Context, ws, task string, cents int64) (research.Choice, error) {
	if task != browserwork.AITask {
		return research.Choice{}, errors.New("wrong task " + task)
	}
	return research.Choice{Provider: llm.Claude, Model: a.model, Task: task}, nil
}

func (a browserAI) Spent(context.Context, string, research.Choice, llm.Usage, string, string) error {
	return nil
}

func toolCall(name string, in map[string]any) llm.ToolCall {
	b, _ := json.Marshal(in)
	return llm.ToolCall{ID: fmt.Sprintf("%s-%d", name, len(b)), Name: name, Input: b}
}

func TestBrowserTasks(t *testing.T) {
	e := freshEnv(t)
	ctx := context.Background()
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	brand := owner.must(200, "GET", base+"/brands", nil)["data"].([]any)[0].(map[string]any)["id"].(string)
	apath := "/v1/brands/" + brand + "/accounts"
	google := owner.must(201, "POST", apath, map[string]any{"platform": "google", "label": "Google", "identity": "ops@acme.example", "sign_in": "local_password", "url": "https://accounts.google.example/"})["id"].(string)
	acc := owner.must(201, "POST", apath, map[string]any{"platform": "community", "label": "Community", "identity": "ops@acme.example", "sign_in": "local_password", "depends_on": google, "url": "https://community.example.com/u/acme"})
	accID := acc["id"].(string)
	owner.must(200, "PUT", apath+"/"+accID+"/secrets/password", map[string]any{"value": "correct horse battery staple"})
	path := "/v1/brands/" + brand + "/browser-tasks"

	// Validation.
	for _, bad := range []map[string]any{
		{"goal": "x"},
		{"goal": "x", "hosts": []string{"community.example.com"}, "operations": []string{"sign_in"}},
		{"goal": "x", "hosts": []string{"not a host"}},
		{"goal": "x", "account_id": accID, "max_minutes": 999},
	} {
		if st, res := owner.do("POST", path, bad); st != 400 {
			t.Fatalf("%v: %d %v", bad, st, res)
		}
	}
	// A task with an account gets the account's site and its identity's.
	task := owner.must(201, "POST", path, map[string]any{"goal": "Publish the approved update", "account_id": accID, "hosts": []string{"Community.example.com"},
		"content": map[string]string{"post": "We shipped v2."}})
	id := task["id"].(string)
	if hosts := fmt.Sprint(task["hosts"]); task["status"] != "queued" || hosts != "[accounts.google.example community.example.com]" || fmt.Sprint(task["operations"]) != "[read act sign_in]" {
		t.Fatalf("task: %v", task)
	}
	var queued int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE kind = 'browser.task' AND status = 'queued'`).Scan(&queued); err != nil || queued != 1 {
		t.Fatalf("job queued: %d %v", queued, err)
	}

	// First run: sign in, meet MFA, ask for a person.
	site := &browserSite{}
	model := &browserModel{rounds: [][]llm.ToolCall{
		{toolCall("navigate", map[string]any{"url": "https://community.example.com/login"})},
		{toolCall("type", map[string]any{"ref": 1, "text": "first"}), toolCall("fill_secret", map[string]any{"ref": 2, "kind": "password"}), toolCall("click", map[string]any{"ref": 3})},
		{toolCall("report_sign_in", map[string]any{"outcome": "mfa_required"}), toolCall("need_person", map[string]any{"reason": "mfa", "note": "Approve on the phone"})},
	}}
	svc := &browserwork.Service{Pool: e.pool, Box: e.box, Models: browserAI{model}, Opener: browserOpener{site}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := svc.RunTask(ctx, id); err != nil {
		t.Fatal(err)
	}
	got := owner.must(200, "GET", path+"/"+id, nil)
	if got["status"] != "waiting_person" || got["takeover_id"] == nil || got["runs"] != 1.0 || got["steps"] != 6.0 {
		t.Fatalf("after MFA: %v", got)
	}
	if strings.Contains(jsonText(got), "correct horse") || strings.Contains(strings.Join(model.seen, "\n"), "correct horse") {
		t.Fatal("the password leaked")
	}
	if acc := owner.must(200, "GET", apath+"/"+accID, nil); acc["status"] != "needs_attention" || acc["open_incidents"] != 1.0 {
		t.Fatalf("the sign-in was recorded on the account: %v", acc)
	}
	takeover := got["takeover_id"].(string)
	tpath := "/v1/brands/" + brand + "/account-takeovers/" + takeover
	if tk := owner.must(200, "GET", tpath, nil); tk["reason"] != "mfa" || tk["note"] != "Approve on the phone" {
		t.Fatalf("takeover: %v", tk)
	}

	// The person handles it and hands back: the task is queued again and
	// the second run finishes.
	owner.must(200, "POST", tpath+"/start", nil)
	owner.must(200, "POST", tpath+"/handback", map[string]any{"identity": "ops@acme.example"})
	if got := owner.must(200, "GET", path+"/"+id, nil); got["status"] != "queued" {
		t.Fatalf("after handback: %v", got)
	}
	model.rounds = append(model.rounds,
		[]llm.ToolCall{toolCall("navigate", map[string]any{"url": "https://community.example.com/login"})},
		[]llm.ToolCall{toolCall("type", map[string]any{"ref": 1, "text": "ops@acme.example"}), toolCall("fill_secret", map[string]any{"ref": 2, "kind": "password"}), toolCall("click", map[string]any{"ref": 3})},
		[]llm.ToolCall{toolCall("report_sign_in", map[string]any{"outcome": "authenticated"}), toolCall("type_content", map[string]any{"ref": 1, "name": "post"}), toolCall("click", map[string]any{"ref": 2})},
		[]llm.ToolCall{toolCall("finish", map[string]any{"outcome": "done", "summary": "Published.", "evidence": map[string]any{"url": "https://community.example.com/home"}})},
	)
	if err := svc.RunTask(ctx, id); err != nil {
		t.Fatal(err)
	}
	got = owner.must(200, "GET", path+"/"+id, nil)
	if got["status"] != "done" || got["summary"] != "Published." || got["runs"] != 2.0 || got["evidence"].(map[string]any)["url"] != "https://community.example.com/home" {
		t.Fatalf("done: %v", got)
	}
	log := got["log"].([]any)
	if len(log) != 14 || log[13].(map[string]any)["run"] != 2.0 || log[13].(map[string]any)["tool"] != "finish" {
		t.Fatalf("log: %d %v", len(log), log[len(log)-1])
	}
	if strings.Contains(jsonText(got), "correct horse") {
		t.Fatal("the password is in the log")
	}
	if !strings.Contains(strings.Join(model.seen, "\n"), "A person has just handled") {
		t.Fatal("the resumed run was not told a person handled the step")
	}
	if st, _ := owner.do("POST", path+"/"+id+"/cancel", nil); st != 400 {
		t.Fatalf("cancelling a finished task: %d", st)
	}
	if l := owner.must(200, "GET", path+"?status=done", nil)["data"].([]any); len(l) != 1 {
		t.Fatalf("list: %v", l)
	}

	// An agent's task runs within its grant, or waits for a person.
	agent, agentID := gatedAgent(t, owner, base, "Browser agent", "browser.manage", "browser.read", "accounts.read")
	st, res := agent.do("POST", path, map[string]any{"goal": "Read the pricing page", "hosts": []string{"community.example.com"}, "operations": []string{"read"}})
	if st != 202 || res["queued"] == true {
		t.Fatalf("without a grant the task waits: %d %v", st, res)
	}
	owner.must(201, "POST", base+"/action-grants", map[string]any{"token_id": agentID, "path": "brands/**", "mode": "run"})
	st, res = agent.do("POST", path, map[string]any{"goal": "Read the pricing page", "hosts": []string{"community.example.com"}, "operations": []string{"read"}})
	if st != 201 || res["status"] != "queued" || fmt.Sprint(res["operations"]) != "[read]" {
		t.Fatalf("within the grant: %d %v", st, res)
	}
	c := agent.must(200, "POST", path+"/"+res["id"].(string)+"/cancel", nil)
	if c["status"] != "cancelled" {
		t.Fatalf("cancel: %v", c)
	}
	// Another brand sees nothing.
	other := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "handle": "northpeak"})["id"].(string)
	owner.must(404, "GET", "/v1/brands/"+other+"/browser-tasks/"+id, nil)
}
