package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/track"
)

// expertPage is the expert's browser on the public approval page: no
// session, just the page's own cookie.
type expertPage struct {
	t      *testing.T
	h      http.Handler
	cookie *http.Cookie
}

func (p *expertPage) open(link string) *httptest.ResponseRecorder {
	p.t.Helper()
	u, err := url.Parse(link)
	if err != nil {
		p.t.Fatal(err)
	}
	req := httptest.NewRequest("GET", u.Path, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh)")
	rec := httptest.NewRecorder()
	p.h.ServeHTTP(rec, req)
	for _, c := range rec.Result().Cookies() {
		if c.Name == "outflare_quote_approval" {
			p.cookie = c
		}
	}
	return rec
}

var csrfField = regexp.MustCompile(`name="csrf" value="([^"]+)"`)

// decide posts the page's form, with the token the page gave unless csrf
// says otherwise.
func (p *expertPage) decide(link string, form url.Values, csrf *string) *httptest.ResponseRecorder {
	p.t.Helper()
	u, _ := url.Parse(link)
	if csrf != nil {
		form.Set("csrf", *csrf)
	} else if p.cookie != nil {
		form.Set("csrf", p.cookie.Value)
	}
	req := httptest.NewRequest("POST", u.Path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh)")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	if p.cookie != nil {
		req.AddCookie(p.cookie)
	}
	rec := httptest.NewRecorder()
	p.h.ServeHTTP(rec, req)
	return rec
}

// quoteStory makes a brand with an expert, a story whose release quotes
// them, and a draft quote; it returns the story and quote paths.
func quoteStory(t *testing.T, owner *client, base, handle, expertEmail string) (story, quote string) {
	t.Helper()
	owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "handle": handle, "website": "https://" + handle + ".example"})
	owner.must(201, "POST", base+"/pr-experts", map[string]any{"brand_id": handle, "name": "Ana Ruiz", "email": expertEmail, "title": "CEO"})
	s := owner.must(201, "POST", base+"/stories", map[string]any{"brand_id": handle, "title": "A third of sites block their own customers' bots"})
	story = base + "/stories/" + s["id"].(string)
	owner.must(200, "PATCH", story, map[string]any{"release": "Northpeak looked at sites in October.\n\n\"Blocking every bot also blocks the people your customers send,\" said Ana Ruiz, CEO."})
	s = owner.must(201, "POST", story+"/quotes", map[string]any{"body": "Blocking every bot also blocks the people your customers send."})
	return story, story + "/quotes/" + s["quotes"].([]any)[0].(map[string]any)["id"].(string)
}

func quoteOf(t *testing.T, c *client, story string) map[string]any {
	t.Helper()
	return c.must(200, "GET", story, nil)["quotes"].([]any)[0].(map[string]any)
}

func TestQuoteApprovalThroughALink(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	story, qpath := quoteStory(t, owner, base, "northpeak", "ana@northpeak.example")
	tok := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "Agent", "scopes": []string{"seo.read", "seo.manage"}})
	agent := &client{t: t, h: e.h, bearer: tok["token"].(string)}

	// Asking grants nothing, so an agent can ask; the link is shown once.
	agent.must(400, "POST", qpath+"/approval-requests", map[string]any{"delivery": "pigeon"})
	req := agent.must(201, "POST", qpath+"/approval-requests", map[string]any{"delivery": "link"})
	link, _ := req["url"].(string)
	if req["status"] != "open" || req["delivery"] != "link" || !strings.Contains(link, "/quote-approval/ofqa_") {
		t.Fatalf("request: %v", req)
	}
	q := quoteOf(t, owner, story)
	if ar := q["approval_request"].(map[string]any); ar["id"] != req["id"] || ar["url"] != nil || ar["opened_at"] != nil {
		t.Fatalf("quote's request: %v", q)
	}

	// The page: the expert, the brand, the headline and the words in their
	// paragraph, kept out of search engines and with no scripts.
	p := &expertPage{t: t, h: e.h}
	rec := p.open(link)
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, "Ana Ruiz") || !strings.Contains(body, "Northpeak") || !strings.Contains(body, "block their own customers") ||
		!strings.Contains(body, "Where it appears in the release") || strings.Contains(body, "<script") ||
		!strings.Contains(rec.Header().Get("X-Robots-Tag"), "noindex") || !strings.Contains(rec.Header().Get("Content-Security-Policy"), "default-src 'none'") ||
		p.cookie == nil || !strings.Contains(body, p.cookie.Value) {
		t.Fatalf("page: %d %v\n%s", rec.Code, rec.Header(), body)
	}
	if q := quoteOf(t, owner, story); q["approval_request"].(map[string]any)["opened_at"] == nil {
		t.Fatalf("opened: %v", q)
	}

	// A post without the page's token, or from another site, saves nothing.
	bad := "forged"
	if rec := p.decide(link, url.Values{"decision": {"approve"}}, &bad); rec.Code != 403 {
		t.Fatalf("forged: %d", rec.Code)
	}
	if q := quoteOf(t, owner, story); q["status"] != "draft" {
		t.Fatalf("after a forged post: %v", q)
	}
	p.open(link)

	// Approving records it as a person's approval would, by the expert.
	if rec := p.decide(link, url.Values{"decision": {"approve"}}, nil); rec.Code != 200 || !strings.Contains(rec.Body.String(), "You approved your quote") {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body.String())
	}
	q = quoteOf(t, owner, story)
	if q["status"] != "approved" || q["approved_via"] != "expert_link" || !strings.HasPrefix(q["approval_note"].(string), "Approved by the expert through an approval link on ") ||
		q["approval_request"].(map[string]any)["status"] != "approved" {
		t.Fatalf("approved: %v", q)
	}
	if res := owner.must(200, "POST", story+"/check", nil); res["ok"] != true {
		t.Fatalf("the check takes the expert's approval: %v", res)
	}
	var alerts int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM alerts WHERE workspace_id = $1 AND kind = 'story_quote.decided'`, ws).Scan(&alerts); err != nil || alerts != 1 {
		t.Fatalf("alerts: %d %v", alerts, err)
	}

	// The link works once.
	if rec := p.open(link); rec.Code != 404 || !strings.Contains(rec.Body.String(), "no longer works") {
		t.Fatalf("used link: %d", rec.Code)
	}
	if rec := p.decide(link, url.Values{"decision": {"decline"}}, nil); rec.Code != 404 {
		t.Fatalf("used link post: %d", rec.Code)
	}
	if rec := p.open("http://localhost/quote-approval/ofqa_nothing"); rec.Code != 404 {
		t.Fatalf("unknown link: %d", rec.Code)
	}
	// Only a draft quote goes to its expert.
	owner.must(409, "POST", qpath+"/approval-requests", map[string]any{"delivery": "link"})

	var approved, opened, requested int
	for _, ev := range got() {
		switch ev.Name {
		case "story_quote.approved":
			if ev.Props["via"] == "link" && ev.ActorType == "expert_link" {
				approved++
			}
		case "story_quote.approval_opened":
			opened++
		case "story_quote.approval_requested":
			requested++
		}
	}
	if approved != 1 || opened != 1 || requested != 1 {
		t.Fatalf("events: approved %d opened %d requested %d", approved, opened, requested)
	}
}

func TestQuoteApprovalLinksStopWhenTheQuoteChanges(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	story, qpath := quoteStory(t, owner, base, "northpeak", "ana@northpeak.example")
	ask := func() string {
		t.Helper()
		return owner.must(201, "POST", qpath+"/approval-requests", map[string]any{"delivery": "link"})["url"].(string)
	}
	p := &expertPage{t: t, h: e.h}

	// New words after asking: the link no longer decides anything.
	link := ask()
	owner.must(200, "PATCH", qpath, map[string]any{"body": "Blocking every bot blocks your customers, too."})
	if rec := p.open(link); rec.Code != 404 {
		t.Fatalf("after new words: %d", rec.Code)
	}
	if ar := quoteOf(t, owner, story)["approval_request"].(map[string]any); ar["status"] != "void" || ar["reason"] != "words_changed" {
		t.Fatalf("void: %v", ar)
	}

	// Asking again replaces the open request.
	first := ask()
	second := ask()
	if p.open(first).Code != 404 || p.open(second).Code != 200 {
		t.Fatal("asking again should replace the first link")
	}

	// An expired link fails.
	if _, err := e.pool.Exec(context.Background(), `UPDATE story_quote_approvals SET expires_at = now() - interval '1 minute' WHERE status = 'open'`); err != nil {
		t.Fatal(err)
	}
	if rec := p.open(second); rec.Code != 404 {
		t.Fatalf("expired: %d", rec.Code)
	}
	if ar := quoteOf(t, owner, story)["approval_request"].(map[string]any); ar["status"] != "expired" {
		t.Fatalf("expired request: %v", ar)
	}

	// Suggesting a change: the quote stays a draft with the expert's words.
	link = ask()
	p.open(link)
	if rec := p.decide(link, url.Values{"decision": {"suggest"}, "body": {"blocking every bot blocks your customers too"}}, nil); rec.Code != 400 {
		t.Fatalf("same words: %d", rec.Code)
	}
	words := "Blocking every bot shuts out the people your customers send."
	if rec := p.decide(link, url.Values{"decision": {"suggest"}, "body": {words}, "note": {"Shorter, please."}}, nil); rec.Code != 200 {
		t.Fatalf("suggest: %d %s", rec.Code, rec.Body.String())
	}
	q := quoteOf(t, owner, story)
	ar := q["approval_request"].(map[string]any)
	if q["status"] != "draft" || q["body"] != words || q["suggested_at"] == nil || ar["status"] != "changes_suggested" || ar["suggested_body"] != words || ar["expert_note"] != "Shorter, please." {
		t.Fatalf("suggested: %v", q)
	}
	// A person takes the words by asking again.
	link = ask()
	if q := quoteOf(t, owner, story); q["suggested_at"] != nil || q["approval_request"].(map[string]any)["body"] != words {
		t.Fatalf("asked again: %v", q)
	}

	// Declining leaves the quote a draft.
	p.open(link)
	if rec := p.decide(link, url.Values{"decision": {"decline"}}, nil); rec.Code != 200 {
		t.Fatalf("decline: %d", rec.Code)
	}
	if q := quoteOf(t, owner, story); q["status"] != "draft" || q["approval_request"].(map[string]any)["status"] != "declined" {
		t.Fatalf("declined: %v", q)
	}

	// Withdrawing the quote, or a person recording the approval, voids an
	// open request.
	link = ask()
	owner.must(200, "PATCH", qpath, map[string]any{"status": "withdrawn"})
	if p.open(link).Code != 404 {
		t.Fatal("withdrawn quote's link still works")
	}
	owner.must(200, "PATCH", qpath, map[string]any{"status": "draft"})
	link = ask()
	owner.must(200, "PATCH", qpath, map[string]any{"status": "approved", "approval_note": "On the phone"})
	if q := quoteOf(t, owner, story); p.open(link).Code != 404 || q["approved_via"] != "person" || q["approval_request"].(map[string]any)["reason"] != "approved_by_person" {
		t.Fatalf("approved by a person: %v", q)
	}
	if n := len(owner.must(200, "GET", qpath+"/approval-requests", nil)["data"].([]any)); n != 7 {
		t.Fatalf("requests: %d", n)
	}

	// Removing the expert takes the quote and its requests with it.
	owner.must(200, "PATCH", qpath, map[string]any{"status": "draft"})
	link = ask()
	expert := quoteOf(t, owner, story)["expert_id"].(string)
	owner.must(204, "DELETE", base+"/pr-experts/"+expert, nil)
	if p.open(link).Code != 404 {
		t.Fatal("removed expert's link still works")
	}

	names := map[string]int{}
	for _, ev := range got() {
		names[ev.Name]++
	}
	if names["story_quote.change_suggested"] != 1 || names["story_quote.declined"] != 1 || names["story_quote.approval_requested"] != 8 {
		t.Fatalf("events: %v", names)
	}
}

func TestQuoteApprovalStaysInItsBrand(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	_, qpath := quoteStory(t, owner, base, "northpeak", "ana@northpeak.example")
	owner.must(201, "POST", base+"/brands", map[string]any{"name": "Acme", "handle": "acme", "website": "https://acme.example"})
	tok := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "Acme agent", "scopes": []string{"seo.read", "seo.manage"}, "brand": "acme"})
	acme := &client{t: t, h: e.h, bearer: tok["token"].(string)}
	acme.must(404, "POST", qpath+"/approval-requests", map[string]any{"delivery": "link"})
	acme.must(404, "GET", qpath+"/approval-requests", nil)
	// Email goes through an outreach sequence of the story's brand.
	owner.must(400, "POST", qpath+"/approval-requests", map[string]any{"delivery": "email"})
	other := brandSequence(t, owner, base, "acme", map[string]any{"name": "Acme approvals", "steps": []map[string]any{
		{"subject": "Your quote", "body": "{{custom.approval_link}}"}}})
	owner.must(400, "POST", qpath+"/approval-requests", map[string]any{"delivery": "email", "sequence_id": other})
}

func TestQuoteApprovalLinkIsNeverReplayed(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	_, qpath := quoteStory(t, owner, base, "northpeak", "ana@northpeak.example")
	keyed := &client{t: t, h: e.h, cookie: owner.cookie, header: map[string]string{"Idempotency-Key": "ask-ana-once"}}
	first := keyed.must(201, "POST", qpath+"/approval-requests", map[string]any{"delivery": "link"})["url"].(string)
	second := keyed.must(201, "POST", qpath+"/approval-requests", map[string]any{"delivery": "link"})["url"].(string)
	p := &expertPage{t: t, h: e.h}
	if first == second || p.open(first).Code != 404 || p.open(second).Code != 200 {
		t.Fatalf("a retry should make a new link and void the first: %s %s", first, second)
	}
	var kept int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM idempotency_keys WHERE key = 'ask-ana-once'`).Scan(&kept); err != nil || kept != 0 {
		t.Fatalf("kept answers: %d %v", kept, err)
	}
}

func TestQuoteApprovalEmailGoesThroughOutreach(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	clock := &fakeClock{}
	clock.Set(time.Date(2030, 1, 8, 10, 0, 0, 0, time.UTC)) // a Tuesday morning
	owner, base, srv := sendingSetup(t, e, clock)
	story, qpath := quoteStory(t, owner, base, "northpeak", "lee@northpeak.example")
	owner.must(200, "POST", base+"/brands/northpeak/add", map[string]any{"domains": []string{"acme.test"}})
	ctx := context.Background()

	// The brand's own sequence for approvals: one step carrying the link.
	long := brandSequence(t, owner, base, "northpeak", map[string]any{"name": "Too long", "steps": []map[string]any{
		{"subject": "a", "body": "{{custom.approval_link}}"}, {"body": "b", "delay_days": 3}}})
	owner.must(400, "POST", qpath+"/approval-requests", map[string]any{"delivery": "email", "sequence_id": long})
	seq := brandSequence(t, owner, base, "northpeak", map[string]any{"name": "Quote approvals", "timezone": "UTC",
		"send_start": "09:00", "send_end": "17:00", "steps": []map[string]any{
			{"subject": "Your quote for {{custom.story_title}}", "body": "Hi {{first_name|there}},\n\n\"{{custom.quote}}\"\n\nApprove it, change it or decline: {{custom.approval_link}}\n\n{{sender_first_name}}"}}})
	ask := func(want int) map[string]any {
		t.Helper()
		return owner.must(want, "POST", qpath+"/approval-requests", map[string]any{"delivery": "email", "sequence_id": seq})
	}
	// A draft sequence sends nothing.
	ask(409)
	boxes := mailboxIDs(t, owner, base)
	owner.must(200, "POST", base+"/mailboxes/assign", map[string]any{"mailbox_ids": []string{boxes["ana@acme.test"]}, "sequence_id": seq})
	owner.must(200, "PATCH", base+"/sequences/"+seq, map[string]any{"status": "active"})

	// No mailbox mapped to the sequence: the outreach path's refusal.
	if _, err := e.pool.Exec(ctx, `DELETE FROM mailbox_assignments WHERE sequence_id = $1`, seq); err != nil {
		t.Fatal(err)
	}
	if code, out := owner.do("POST", qpath+"/approval-requests", map[string]any{"delivery": "email", "sequence_id": seq}); code != 409 ||
		out["error"].(map[string]any)["code"] != "blocked" || !strings.Contains(out["error"].(map[string]any)["message"].(string), "no_mailbox_allocated") {
		t.Fatalf("no mailbox: %d %v", code, out)
	}
	owner.must(200, "POST", base+"/mailboxes/assign", map[string]any{"mailbox_ids": []string{boxes["ana@acme.test"]}, "sequence_id": seq})

	req := ask(201)
	if req["status"] != "sending" || req["url"] != nil || req["sequence_id"] != seq {
		t.Fatalf("request: %v", req)
	}
	e.pump(t)
	var sent string
	for _, m := range srv.Messages() {
		if len(m.To) == 1 && m.To[0] == "lee@northpeak.example" {
			if m.From != "ana@acme.test" {
				t.Fatalf("sent from %s, not the sequence's mailbox", m.From)
			}
			sent = string(m.Data)
		}
	}
	link := regexp.MustCompile(`http://\S+/quote-approval/ofqa_[A-Za-z0-9_-]+`).FindString(strings.ReplaceAll(sent, "=\r\n", ""))
	if link == "" || !strings.Contains(sent, "Blocking every bot") {
		t.Fatalf("email: %s", sent)
	}
	ar := quoteOf(t, owner, story)["approval_request"].(map[string]any)
	if ar["status"] != "open" || ar["mailbox_id"] != boxes["ana@acme.test"] || ar["sent_at"] == nil {
		t.Fatalf("sent: %v", ar)
	}
	if rec := (&expertPage{t: t, h: e.h}).open(link); rec.Code != 200 {
		t.Fatalf("emailed link: %d", rec.Code)
	}

	// The send policy applies: asking again once the address is suppressed
	// fails in the sequence, and the person who asked is alerted.
	owner.must(200, "POST", base+"/suppressions", map[string]any{"values": []string{"lee@northpeak.example"}, "reason": "bounced"})
	ask(201)
	e.pump(t)
	ar = quoteOf(t, owner, story)["approval_request"].(map[string]any)
	if ar["status"] != "failed" || !strings.HasPrefix(ar["reason"].(string), "suppressed_email") {
		t.Fatalf("blocked: %v", ar)
	}
	if rec := (&expertPage{t: t, h: e.h}).open(link); rec.Code != 404 {
		t.Fatalf("the replaced link still works: %d", rec.Code)
	}
	var owners int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM alerts WHERE kind = 'story_quote.approval_failed' AND owner_id IS NOT NULL`).Scan(&owners); err != nil || owners != 1 {
		t.Fatalf("alerts for the asker: %d %v", owners, err)
	}
	var sentEv, failed int
	for _, ev := range got() {
		if ev.Name == "story_quote.approval_sent" {
			sentEv++
		}
		if ev.Name == "story_quote.approval_send_failed" && ev.ErrorCode == "blocked" {
			failed++
		}
	}
	if sentEv != 1 || failed != 1 {
		t.Fatalf("events: sent %d failed %d", sentEv, failed)
	}
}

// brandSequence makes a sequence and gives it to a brand.
func brandSequence(t *testing.T, owner *client, base, brand string, body map[string]any) string {
	t.Helper()
	id := owner.must(201, "POST", base+"/sequences", body)["id"].(string)
	owner.must(200, "POST", base+"/brands/"+brand+"/add", map[string]any{"sequence_ids": []string{id}})
	return id
}
