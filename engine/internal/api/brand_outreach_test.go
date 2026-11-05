package api_test

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/track"
)

// Brands own their data, step 5 (plans/brands/README.md): leads and lists,
// sequences and their sends, replies, opt-outs, meetings and deals each
// belong to one brand, are served under /v1/brands/{brand}/, and never
// show or act on another brand's. The workspace routes keep working for
// older callers: lists default to the first brand and take ?brand=.
func TestBrandOutreach(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	first := owner.must(200, "GET", base+"/brands", nil)["data"].([]any)[0].(map[string]any)["id"].(string)
	alpha := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Alpha", "handle": "alpha"})["id"].(string)
	bravo := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Bravo", "handle": "bravo"})["id"].(string)
	a, b := "/v1/brands/"+alpha, "/v1/brands/"+bravo

	tok := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "Bravo app", "brand": "bravo", "scopes": []string{
		"leads.read", "leads.manage", "sequences.read", "sequences.manage", "inbox.read", "outcomes.read", "outcomes.manage", "customers.read"}})
	bravoApp := &client{t: t, h: e.h, bearer: tok["token"].(string)}
	ids := func(c *client, path string) []string {
		t.Helper()
		var out []string
		for _, r := range c.must(200, "GET", path, nil)["data"].([]any) {
			out = append(out, r.(map[string]any)["id"].(string))
		}
		return out
	}

	// ---- Leads: one brand's each ----

	lead := owner.must(201, "POST", a+"/leads", map[string]any{"email": "ana@acme.example", "first_name": "Ana"})
	if lead["brand_id"] != alpha {
		t.Fatalf("lead made in %v, want alpha", lead["brand_id"])
	}
	lid := lead["id"].(string)
	for _, sub := range []string{"", "/sources"} {
		owner.must(200, "GET", a+"/leads/"+lid+sub, nil)
		owner.must(404, "GET", b+"/leads/"+lid+sub, nil)
		bravoApp.must(404, "GET", a+"/leads/"+lid+sub, nil)
		bravoApp.must(404, "GET", b+"/leads/"+lid+sub, nil)
	}
	owner.must(404, "PATCH", b+"/leads/"+lid, map[string]any{"title": "CEO"})
	owner.must(404, "DELETE", b+"/leads/"+lid, nil)
	owner.must(400, "POST", a+"/leads", map[string]any{"email": "elsewhere@acme.example", "brand_id": bravo})
	// The same address in another brand would be another person. Until the
	// contract step drops the per-workspace unique index the version before
	// 0371 upserts on, it is refused with a clear code instead of being
	// merged into Alpha's lead.
	if code, res := owner.do("POST", b+"/leads", map[string]any{"email": "ana@acme.example"}); code != 409 || res["error"].(map[string]any)["code"] != "lead_in_other_brand" {
		t.Fatalf("the same address in bravo: %d %v", code, res)
	}
	var perBrand bool
	if err := e.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_indexes WHERE indexname = 'leads_brand_email_idx')`).Scan(&perBrand); err != nil || !perBrand {
		t.Fatalf("leads have no per-brand unique index (%v)", err)
	}
	// Upserting in the lead's own brand updates it.
	if l := owner.must(200, "POST", a+"/leads", map[string]any{"email": "ANA@acme.example", "title": "CEO"}); l["id"] != lid || l["title"] != "CEO" {
		t.Fatalf("upsert in alpha: %v", l)
	}
	bob := bravoApp.must(201, "POST", b+"/leads", map[string]any{"email": "bob@acme.example"})
	if bob["brand_id"] != bravo {
		t.Fatalf("bravo's token made a lead in %v", bob["brand_id"])
	}
	bravoApp.must(404, "POST", a+"/leads", map[string]any{"email": "carl@acme.example"})
	if l := ids(owner, a+"/leads"); len(l) != 1 || l[0] != lid {
		t.Fatalf("alpha's leads: %v", l)
	}
	if l := ids(bravoApp, b+"/leads"); len(l) != 1 || l[0] != bob["id"] {
		t.Fatalf("bravo's leads: %v", l)
	}
	// The workspace route lists the first brand's, and ?brand= picks one.
	if l := ids(owner, base+"/leads"); len(l) != 0 {
		t.Fatalf("the workspace route lists the first brand's leads: %v", l)
	}
	if l := ids(owner, base+"/leads?brand=alpha"); len(l) != 1 {
		t.Fatalf("workspace leads of alpha: %v", l)
	}
	if l := ids(owner, base+"/leads?brand=all"); len(l) != 2 {
		t.Fatalf("workspace leads of every brand: %v", l)
	}
	if l := owner.must(201, "POST", base+"/leads", map[string]any{"email": "first@acme.example"}); l["brand_id"] != first {
		t.Fatalf("a workspace lead went to %v, want the first brand", l["brand_id"])
	}
	// A CSV import fills the route's brand, and skips another brand's leads.
	code, imp := owner.raw("POST", a+"/leads/import?list=Imported", "text/csv", "email\ndee@acme.example\nbob@acme.example\n")
	if code != 200 || imp["created"] != 1.0 || imp["other_brand"] != 1.0 || imp["brand_id"] != alpha {
		t.Fatalf("alpha's import: %d %v", code, imp)
	}

	// ---- Lists: one brand's, holding its leads only ----

	list := owner.must(201, "POST", a+"/lead-lists", map[string]any{"name": "Targets"})
	if list["brand_id"] != alpha {
		t.Fatalf("list made in %v", list["brand_id"])
	}
	listID := list["id"].(string)
	owner.must(404, "GET", b+"/lead-lists/"+listID, nil)
	owner.must(404, "GET", b+"/lead-lists/"+listID+"/members", nil)
	bravoApp.must(404, "POST", a+"/lead-lists/"+listID+"/leads", map[string]any{"lead_ids": []string{bob["id"].(string)}})
	if r := owner.must(200, "POST", a+"/lead-lists/"+listID+"/leads", map[string]any{"lead_ids": []string{lid, bob["id"].(string)}}); r["added"] != 1.0 {
		t.Fatalf("alpha's list took another brand's lead: %v", r)
	}
	if l := ids(owner, b+"/lead-lists"); len(l) != 0 {
		t.Fatalf("bravo sees alpha's lists: %v", l)
	}
	// The import's list was made in its brand.
	if l := ids(owner, a+"/lead-lists"); len(l) != 2 {
		t.Fatalf("alpha's lists: %v", l)
	}

	// ---- Sequences: one brand's, enrolling its leads only ----

	seq := owner.must(201, "POST", a+"/sequences", map[string]any{"name": "Outbound", "steps": []map[string]any{{"subject": "Hi {{first_name}}", "body": "Hello"}}})
	if seq["brand_id"] != alpha {
		t.Fatalf("sequence made in %v", seq["brand_id"])
	}
	sid := seq["id"].(string)
	for _, sub := range []string{"", "/enrollments"} {
		owner.must(200, "GET", a+"/sequences/"+sid+sub, nil)
		owner.must(404, "GET", b+"/sequences/"+sid+sub, nil)
		bravoApp.must(404, "GET", a+"/sequences/"+sid+sub, nil)
		bravoApp.must(404, "GET", b+"/sequences/"+sid+sub, nil)
	}
	owner.must(404, "PATCH", b+"/sequences/"+sid, map[string]any{"name": "Taken"})
	if r := owner.must(200, "POST", a+"/sequences/"+sid+"/enrollments", map[string]any{"lead_ids": []string{lid, bob["id"].(string)}}); r["enrolled"] != 1.0 {
		t.Fatalf("alpha's sequence enrolled another brand's lead: %v", r)
	}
	var enBrand string
	if err := e.pool.QueryRow(ctx, `SELECT brand_id::text FROM enrollments WHERE sequence_id = $1`, sid).Scan(&enBrand); err != nil || enBrand != alpha {
		t.Fatalf("the enrollment's brand is %q (%v)", enBrand, err)
	}
	if l := ids(owner, base+"/sequences?brand=alpha"); len(l) != 1 {
		t.Fatalf("workspace sequences of alpha: %v", l)
	}
	if l := ids(owner, base+"/sequences"); len(l) != 0 {
		t.Fatalf("the workspace route lists the first brand's sequences: %v", l)
	}

	// ---- Opt-outs: per brand, or every brand ----

	check := func(brand, addr string) bool {
		t.Helper()
		return owner.must(200, "POST", "/v1/brands/"+brand+"/recipients/check", map[string]any{"emails": []string{addr}})["data"].([]any)[0].(map[string]any)["allowed"].(bool)
	}
	owner.must(200, "POST", a+"/suppressions", map[string]any{"values": []string{"optout@acme.example"}, "reason": "unsubscribed"})
	if check(alpha, "optout@acme.example") || !check(bravo, "optout@acme.example") {
		t.Fatal("an opt-out in alpha should stop alpha's email only")
	}
	// Bravo sees only what applies to it.
	sup := func(c *client, path string) []string {
		t.Helper()
		var out []string
		for _, r := range c.must(200, "GET", path, nil)["data"].([]any) {
			out = append(out, r.(map[string]any)["value"].(string))
		}
		return out
	}
	if l := sup(owner, b+"/suppressions"); len(l) != 0 {
		t.Fatalf("bravo's opt-out list shows alpha's: %v", l)
	}
	// A bounce is a fact about the address: every brand.
	owner.must(200, "POST", a+"/suppressions", map[string]any{"values": []string{"gone@acme.example"}, "reason": "bounced"})
	if check(bravo, "gone@acme.example") {
		t.Fatal("a bounce in alpha should stop every brand's email")
	}
	// An admin applies one to every brand.
	owner.must(200, "POST", a+"/suppressions", map[string]any{"values": []string{"all@acme.example"}, "all_brands": true})
	owner.must(200, "POST", base+"/suppressions", map[string]any{"values": []string{"legal.example"}, "reason": "legal"})
	if check(bravo, "all@acme.example") || check(bravo, "x@legal.example") {
		t.Fatal("an opt-out for every brand should stop bravo's email")
	}
	if l := sup(owner, b+"/suppressions"); !slices.Contains(l, "all@acme.example") || !slices.Contains(l, "gone@acme.example") || slices.Contains(l, "optout@acme.example") {
		t.Fatalf("bravo's opt-out list: %v", l)
	}
	// A brand token can't apply one to every brand.
	bravoApp.must(403, "POST", b+"/suppressions", map[string]any{"values": []string{"z@acme.example"}, "all_brands": true})
	// A second brand's opt-out of an address another brand has is its own
	// entry: both brands stop, and a third brand still emails it.
	owner.must(200, "POST", b+"/suppressions", map[string]any{"values": []string{"optout@acme.example"}, "reason": "unsubscribed"})
	if check(alpha, "optout@acme.example") || check(bravo, "optout@acme.example") {
		t.Fatal("two brands' opt-outs of one address should stop both brands")
	}
	if !check(first, "optout@acme.example") {
		t.Fatal("two brands' opt-outs of one address stopped a third brand")
	}
	// A brand removes only its own entries.
	var gone string
	if err := e.pool.QueryRow(ctx, `SELECT id::text FROM suppressions WHERE workspace_id = $1 AND value = 'gone@acme.example'`, ws).Scan(&gone); err != nil {
		t.Fatal(err)
	}
	owner.must(404, "DELETE", b+"/suppressions/"+gone, nil)
	// Alpha's lead shows as suppressed in Alpha only.
	owner.must(201, "POST", b+"/leads", map[string]any{"email": "erin@acme.example"})
	owner.must(200, "POST", a+"/suppressions", map[string]any{"values": []string{"erin@acme.example"}})
	if l := bravoApp.must(200, "GET", b+"/leads?suppressed=true", nil)["data"].([]any); len(l) != 0 {
		t.Fatalf("an opt-out in alpha marks bravo's lead suppressed: %v", l)
	}

	// ---- Replies, meetings and deals ----

	mb := ""
	if err := e.pool.QueryRow(ctx, `INSERT INTO mailboxes (workspace_id, email, provider, auth_method, smtp_host, smtp_port, smtp_username,
		imap_host, imap_port, imap_username, credentials, status, daily_limit, source)
		VALUES ($1, 'sales@alpha.example', 'other', 'password', 'smtp.alpha.example', 587, 'sales', 'imap.alpha.example', 993, 'sales', '\x00', 'active', 30, 'manual')
		RETURNING id::text`, ws).Scan(&mb); err != nil {
		t.Fatal(err)
	}
	var reply string
	if err := e.pool.QueryRow(ctx, `INSERT INTO inbound_messages (workspace_id, mailbox_id, uidvalidity, uid, message_id, in_reply_to, references_ids,
		from_email, from_name, to_email, subject, body, kind, opt_out, lead_id, sequence_id, received_at)
		VALUES ($1, $2, 1, 1, '<r1@acme.example>', '', '', 'ana@acme.example', 'Ana', 'sales@alpha.example', 'Re: Hi', 'Tell me more', 'reply', false, $3, $4, now())
		RETURNING id::text`, ws, mb, lid, sid).Scan(&reply); err != nil {
		t.Fatal(err)
	}
	owner.must(200, "GET", a+"/inbox/"+reply, nil)
	owner.must(404, "GET", b+"/inbox/"+reply, nil)
	owner.must(404, "PATCH", b+"/inbox/"+reply, map[string]any{"read": true})
	if l := ids(owner, a+"/inbox"); len(l) != 1 {
		t.Fatalf("alpha's inbox: %v", l)
	}
	if l := ids(owner, b+"/inbox"); len(l) != 0 {
		t.Fatalf("bravo's inbox shows alpha's reply: %v", l)
	}
	meeting := owner.must(201, "POST", a+"/meetings", map[string]any{"lead_id": lid, "title": "Intro", "starts_at": time.Now().Add(24 * time.Hour).Format(time.RFC3339)})
	if meeting["brand_id"] != alpha {
		t.Fatalf("meeting made in %v", meeting["brand_id"])
	}
	mid := meeting["id"].(string)
	owner.must(404, "GET", b+"/meetings/"+mid, nil)
	bravoApp.must(404, "GET", a+"/meetings/"+mid, nil)
	owner.must(400, "POST", b+"/meetings", map[string]any{"lead_id": lid, "starts_at": time.Now().Format(time.RFC3339)})
	deal := owner.must(201, "POST", a+"/deals", map[string]any{"name": "Acme pilot", "meeting_id": mid})
	if deal["brand_id"] != alpha {
		t.Fatalf("deal made in %v", deal["brand_id"])
	}
	owner.must(404, "GET", b+"/deals/"+deal["id"].(string), nil)
	owner.must(400, "POST", b+"/deals", map[string]any{"name": "Elsewhere", "meeting_id": mid})
	if l := ids(owner, b+"/meetings"); len(l) != 0 {
		t.Fatalf("bravo's meetings: %v", l)
	}
	if l := ids(owner, base+"/deals?brand=alpha"); len(l) != 1 {
		t.Fatalf("workspace deals of alpha: %v", l)
	}

	// ---- Query keeps to the brand ----

	count := `SELECT (SELECT count(*) FROM leads) AS leads, (SELECT count(*) FROM sequences) AS sequences, (SELECT count(*) FROM enrollments) AS enrollments, ` +
		`(SELECT count(*) FROM replies) AS replies, (SELECT count(*) FROM meetings) AS meetings, (SELECT count(*) FROM deals) AS deals`
	if r := queryRows(t, owner.must(200, "POST", a+"/query", map[string]any{"sql": count})); fmt.Sprint(r) != "[[2 1 1 1 1 1]]" {
		t.Fatalf("alpha's query counts %v", r)
	}
	if r := queryRows(t, bravoApp.must(200, "POST", b+"/query", map[string]any{"sql": count})); fmt.Sprint(r) != "[[2 0 0 0 0 0]]" {
		t.Fatalf("bravo's query counts %v", r)
	}

	// ---- Audit: each event carries its brand ----

	for action, want := range map[string]string{"lead.created": alpha, "sequence.created": alpha, "meeting.created": alpha, "deal.created": alpha, "lead_list.created": alpha} {
		var brand *string
		if err := e.pool.QueryRow(ctx, `SELECT brand_id::text FROM audit_events WHERE workspace_id = $1 AND action = $2 ORDER BY id LIMIT 1`, ws, action).Scan(&brand); err != nil {
			t.Fatalf("%s: %v", action, err)
		}
		if brand == nil || *brand != want {
			t.Fatalf("%s carries brand %v, want alpha", action, brand)
		}
	}
	var supBrand *string
	if err := e.pool.QueryRow(ctx, `SELECT brand_id::text FROM audit_events WHERE workspace_id = $1 AND action = 'suppressions.added' ORDER BY id LIMIT 1`, ws).Scan(&supBrand); err != nil || supBrand == nil || *supBrand != alpha {
		t.Fatalf("suppressions.added carries brand %v (%v)", supBrand, err)
	}
	if !slices.ContainsFunc(got(), func(ev track.Event) bool { return ev.Name == "lead.created" }) {
		t.Fatal("no lead.created event")
	}
}
