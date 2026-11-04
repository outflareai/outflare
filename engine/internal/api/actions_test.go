package api_test

import (
	"context"
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/track"
)

// gatedAgent makes an API token whose changes go through the action layer.
func gatedAgent(t *testing.T, owner *client, base, name string, scopes ...string) (*client, string) {
	t.Helper()
	out := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": name, "scopes": scopes, "action_gate": true})
	tok := out["api_token"].(map[string]any)
	if tok["action_gate"] != true {
		t.Fatalf("token: %v", tok)
	}
	return &client{t: t, h: owner.h, bearer: out["token"].(string)}, tok["id"].(string)
}

func TestActionLayer(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	agent, agentID := gatedAgent(t, owner, base, "Agent", "sequences.read", "sequences.manage", "webhooks.read", "webhooks.manage", "actions.read", "actions.approve")
	seq := map[string]any{"name": "Agent sequence", "steps": []map[string]any{{"subject": "Hi", "body": "Hello"}}}

	// No grant: the change is held for a person and nothing changes.
	code, res := agent.do("POST", base+"/sequences", seq)
	if code != 202 || res["approval_required"] != true {
		t.Fatalf("no grant: %d %v", code, res)
	}
	held := res["action"].(map[string]any)
	if held["status"] != "held" || held["decided_by_rule"] != "no_grant" || held["body_hash"] == "" {
		t.Fatalf("held action: %v", held)
	}
	if n := len(owner.must(200, "GET", base+"/sequences", nil)["data"].([]any)); n != 0 {
		t.Fatalf("a held change created %d sequences", n)
	}
	// Reads are never gated.
	agent.must(200, "GET", base+"/sequences", nil)

	// The agent can't approve its own change, even with the scope, and
	// neither can another agent on the action layer: deciding is a
	// person's, or an approver token's set up outside it.
	agent.must(403, "POST", base+"/actions/"+held["id"].(string)+"/approve", nil)
	otherAgent, _ := gatedAgent(t, owner, base, "Other agent", "actions.read", "actions.approve")
	otherAgent.must(403, "POST", base+"/actions/"+held["id"].(string)+"/approve", nil)
	approverBot := agentToken(t, owner, base, "Approver bot", "actions.read", "actions.approve")
	if v := approverBot.must(200, "GET", base+"/actions/"+held["id"].(string), nil); v["status"] != "held" || v["expires_at"] == nil {
		t.Fatalf("held view: %v", v)
	}
	// A person approves: it runs as the agent, through the full handler.
	done := owner.must(200, "POST", base+"/actions/"+held["id"].(string)+"/approve", map[string]any{"note": "looks good"})
	if done["status"] != "executed" || done["result_status"].(float64) != 201 || done["decision_note"] != "looks good" {
		t.Fatalf("approved: %v", done)
	}
	if n := len(owner.must(200, "GET", base+"/sequences", nil)["data"].([]any)); n != 1 {
		t.Fatalf("approved change made %d sequences", n)
	}
	owner.must(409, "POST", base+"/actions/"+held["id"].(string)+"/approve", nil)

	// Only a person sets grants; the agent can't widen its own room.
	agent.must(403, "POST", base+"/action-grants", map[string]any{"token_id": agentID, "path": "**", "mode": "run"})
	owner.must(400, "POST", base+"/action-grants", map[string]any{"token_id": agentID, "path": "sequences/**/x", "mode": "run"})
	owner.must(400, "POST", base+"/action-grants", map[string]any{"token_id": agentID, "path": "sequences", "mode": "maybe"})
	// A grant kept to a brand is only for a token bound to that brand.
	brandID := owner.must(200, "GET", base+"/brands", nil)["data"].([]any)[0].(map[string]any)["id"].(string)
	owner.must(400, "POST", base+"/action-grants", map[string]any{"token_id": agentID, "brand": brandID, "path": "sequences", "mode": "run"})
	run := owner.must(201, "POST", base+"/action-grants", map[string]any{"token_id": agentID, "method": "POST", "path": "sequences", "mode": "run", "daily_limit": 1})
	if run["method"] != "POST" || run["daily_limit"].(float64) != 1 {
		t.Fatalf("grant: %v", run)
	}

	// A grant that runs: the change runs and is kept as an action.
	seq["name"] = "Second"
	code, res = agent.do("POST", base+"/sequences", seq)
	if code != 201 {
		t.Fatalf("run grant: %d %v", code, res)
	}
	// Its daily limit reached, the next one is held.
	seq["name"] = "Third"
	code, res = agent.do("POST", base+"/sequences", seq)
	if code != 202 || res["action"].(map[string]any)["decided_by_rule"] != "daily_limit" {
		t.Fatalf("daily limit: %d %v", code, res)
	}
	third := res["action"].(map[string]any)["id"].(string)

	// A more specific refusing grant beats a broad running one.
	owner.must(201, "POST", base+"/action-grants", map[string]any{"token_id": agentID, "path": "**", "mode": "run"})
	owner.must(201, "POST", base+"/action-grants", map[string]any{"token_id": agentID, "path": "sequences/*", "method": "DELETE", "mode": "refuse"})
	list := owner.must(200, "GET", base+"/sequences", nil)["data"].([]any)
	seqID := list[0].(map[string]any)["id"].(string)
	if code, res := agent.do("DELETE", base+"/sequences/"+seqID, nil); code != 403 || res["error"].(map[string]any)["code"] != "action_refused" {
		t.Fatalf("refused: %d %v", code, res)
	}

	// Some changes always need a person, whatever the grants say.
	code, res = agent.do("POST", base+"/webhooks", map[string]any{"url": "https://hooks.example.com/x", "events": []string{"lead.created"}})
	if code != 202 || res["action"].(map[string]any)["decided_by_rule"] != "always_person" {
		t.Fatalf("always a person: %d %v", code, res)
	}
	hook := res["action"].(map[string]any)["id"].(string)

	// A batch: approve one and reject one in one request.
	b := owner.must(200, "POST", base+"/actions/decide", map[string]any{"approve": []string{third}, "reject": []string{hook}, "note": "batch"})
	out := b["data"].([]any)
	if len(out) != 2 || out[0].(map[string]any)["action"].(map[string]any)["status"] != "executed" ||
		out[1].(map[string]any)["action"].(map[string]any)["status"] != "rejected" {
		t.Fatalf("batch: %v", b)
	}
	// Deciding again reports an error per item.
	again := owner.must(200, "POST", base+"/actions/decide", map[string]any{"reject": []string{third}})
	if e := again["data"].([]any)[0].(map[string]any)["error"].(map[string]any); e["code"] != "action_state" {
		t.Fatalf("decided twice: %v", again)
	}

	// The list holds every change the agent made, newest first.
	all := owner.must(200, "GET", base+"/actions?token_id="+agentID+"&limit=2", nil)
	if len(all["data"].([]any)) != 2 || all["next_cursor"] == nil {
		t.Fatalf("list: %v", all)
	}
	heldNow := owner.must(200, "GET", base+"/actions?status=held", nil)["data"].([]any)
	if len(heldNow) != 0 {
		t.Fatalf("still held: %v", heldNow)
	}

	// Revoking a grant: the broad run grant goes and changes are held again.
	grants := owner.must(200, "GET", base+"/action-grants?token_id="+agentID, nil)["data"].([]any)
	for _, g := range grants {
		owner.must(204, "DELETE", base+"/action-grants/"+g.(map[string]any)["id"].(string), nil)
	}
	if code, _ := agent.do("PATCH", base+"/sequences/"+seqID, map[string]any{"name": "Renamed"}); code != 202 {
		t.Fatalf("after revoking: %d", code)
	}

	// Turning the gate off is a person's: a token can't, even with tokens.manage.
	admin := agentToken(t, owner, base, "Admin token", "tokens.manage")
	admin.must(403, "PATCH", base+"/api-tokens/"+agentID, map[string]any{"action_gate": false})
	off := owner.must(200, "PATCH", base+"/api-tokens/"+agentID, map[string]any{"action_gate": false})
	if off["action_gate"] != false {
		t.Fatalf("gate off: %v", off)
	}

	// Every decision is an event; a refusal is the call's event with its
	// error code, since a refused request records no action event.
	ev := got()
	for _, name := range []string{"action.held", "action.approved", "action.rejected", "action_grant.created", "action_grant.revoked", "token.action_gate_changed"} {
		if len(named(ev, name)) == 0 {
			t.Errorf("no %s event", name)
		}
	}
	refused := false
	for _, c := range named(ev, "api.called") {
		if c.ErrorCode == "action_refused" {
			refused = true
		}
	}
	if !refused {
		t.Error("no api.called event with error code action_refused")
	}
}

func TestActionBigBodies(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	agent, agentID := gatedAgent(t, owner, base, "Agent", "sequences.manage", "actions.read")
	big := map[string]any{"name": "Big", "steps": []map[string]any{{"subject": "Hi", "body": strings.Repeat("x", 1<<20+10)}}}

	// Over 1 MiB can't wait for a person: it can't be kept to approve.
	if code, res := agent.do("POST", base+"/sequences", big); code != 413 {
		t.Fatalf("big held: %d %v", code, res)
	}
	// Under a run grant it is passed through to the handler unread (which
	// here refuses it for its own body limit), and kept without a body.
	owner.must(201, "POST", base+"/action-grants", map[string]any{"token_id": agentID, "path": "sequences", "mode": "run"})
	code, res := agent.do("POST", base+"/sequences", big)
	if code < 400 || code >= 500 || res["action"] != nil {
		t.Fatalf("big run reached the handler: %d %v", code, res)
	}
	acts := owner.must(200, "GET", base+"/actions?token_id="+agentID, nil)["data"].([]any)
	if len(acts) != 1 {
		t.Fatalf("actions: %v", acts)
	}
	act := acts[0].(map[string]any)
	if act["status"] != "failed" || int(act["result_status"].(float64)) != code || act["body_hash"] != "" || act["body"] != nil {
		t.Fatalf("big action: %v", act)
	}
	// A small one under the same grant runs and keeps its hash.
	if code, _ := agent.do("POST", base+"/sequences", map[string]any{"name": "Small", "steps": []map[string]any{{"subject": "Hi", "body": "Hello"}}}); code != 201 {
		t.Fatalf("small run: %d", code)
	}
}

func TestActionExpires(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	agent, _ := gatedAgent(t, owner, base, "Agent", "sequences.manage", "actions.read")
	_, res := agent.do("POST", base+"/sequences", map[string]any{"name": "S", "steps": []map[string]any{{"subject": "Hi", "body": "Hello"}}})
	id := res["action"].(map[string]any)["id"].(string)
	// Thirty days pass with nobody deciding.
	if _, err := e.pool.Exec(context.Background(), `UPDATE actions SET expires_at = now() - interval '1 minute' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if v := agent.must(200, "GET", base+"/actions/"+id, nil); v["status"] != "expired" {
		t.Fatalf("expired view: %v", v)
	}
	if l := owner.must(200, "GET", base+"/actions?status=held", nil)["data"].([]any); len(l) != 0 {
		t.Fatalf("expired change still listed as held: %v", l)
	}
	if l := owner.must(200, "GET", base+"/actions?status=expired", nil)["data"].([]any); len(l) != 1 {
		t.Fatalf("expired change not listed: %v", l)
	}
	owner.must(409, "POST", base+"/actions/"+id+"/approve", nil)
	if n := len(owner.must(200, "GET", base+"/sequences", nil)["data"].([]any)); n != 0 {
		t.Fatalf("an expired change ran: %d sequences", n)
	}
}

func TestActionLayerKeepsWorkspacesApart(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	agent, _ := gatedAgent(t, owner, base, "Agent", "sequences.manage", "actions.read")
	_, res := agent.do("POST", base+"/sequences", map[string]any{"name": "S", "steps": []map[string]any{{"subject": "Hi", "body": "Hello"}}})
	id := res["action"].(map[string]any)["id"].(string)

	// The same person's other workspace can't see or decide it, nor grant
	// a token from this one.
	ws2 := owner.must(201, "POST", "/v1/workspaces", map[string]string{"name": "Northpeak"})["id"].(string)
	owner.must(404, "GET", "/v1/workspaces/"+ws2+"/actions/"+id, nil)
	owner.must(404, "POST", "/v1/workspaces/"+ws2+"/actions/"+id+"/approve", nil)
	tok := owner.must(200, "GET", base+"/api-tokens", nil)["data"].([]any)[0].(map[string]any)["id"].(string)
	owner.must(400, "POST", "/v1/workspaces/"+ws2+"/action-grants", map[string]any{"token_id": tok, "path": "**", "mode": "run"})
}

func TestActionTemplates(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	agent, agentID := gatedAgent(t, owner, base, "Agent", "sequences.read", "sequences.manage", "actions.read")
	seq := func(name, tz string) map[string]any {
		return map[string]any{"name": name, "timezone": tz, "steps": []map[string]any{{"subject": "Hi", "body": "Hello"}}}
	}

	// A held change, approved as a template on one field of its body.
	_, res := agent.do("POST", base+"/sequences", seq("One", "Europe/London"))
	held := res["action"].(map[string]any)["id"].(string)
	owner.must(400, "POST", base+"/actions/"+held+"/approve", map[string]any{"remember": map[string]any{"fields": []string{"nope"}}})
	approver := agentToken(t, owner, base, "Approver", "actions.read", "actions.approve")
	approver.must(403, "POST", base+"/actions/"+held+"/approve", map[string]any{"remember": map[string]any{"fields": []string{"timezone"}}})
	done := owner.must(200, "POST", base+"/actions/"+held+"/approve", map[string]any{"remember": map[string]any{"fields": []string{"timezone"}, "daily_limit": 10}})
	if done["status"] != "executed" {
		t.Fatalf("approved: %v", done)
	}
	grants := owner.must(200, "GET", base+"/action-grants?token_id="+agentID, nil)["data"].([]any)
	if len(grants) != 1 {
		t.Fatalf("template grant: %v", grants)
	}
	g := grants[0].(map[string]any)
	if g["path"] != "sequences" || g["method"] != "POST" || g["mode"] != "run" || g["match"].(map[string]any)["timezone"] != "Europe/London" {
		t.Fatalf("template grant: %v", g)
	}

	// A change that fits the template runs; one that doesn't is held.
	if code, res := agent.do("POST", base+"/sequences", seq("Two", "Europe/London")); code != 201 {
		t.Fatalf("fits: %d %v", code, res)
	}
	if code, res := agent.do("POST", base+"/sequences", seq("Three", "America/New_York")); code != 202 {
		t.Fatalf("doesn't fit: %d %v", code, res)
	}

	// A grant made with a template directly.
	owner.must(400, "POST", base+"/action-grants", map[string]any{"token_id": agentID, "path": "sequences", "mode": "run", "match": map[string]any{"x": []int{1}}})
	owner.must(201, "POST", base+"/action-grants", map[string]any{"token_id": agentID, "path": "sequences", "method": "POST", "mode": "refuse", "match": map[string]any{"timezone": "Europe/London", "name": "Blocked"}})
	if code, _ := agent.do("POST", base+"/sequences", seq("Blocked", "Europe/London")); code != 403 {
		t.Fatalf("a more specific template refuses: %d", code)
	}
}

// Edges the gate must get right: a brand-bound agent reads its own held
// change and nothing of another brand's; a mission header never takes an
// agent token around the gate; "**" is what is under a path, not the path.
func TestActionLayerEdges(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	owner.must(201, "POST", base+"/brands", map[string]any{"name": "Geonode", "website": "https://www.geonode.com/"})
	owner.must(201, "POST", base+"/brands", map[string]any{"name": "Repocket", "website": "https://repocket.com/"})
	scopes := []string{"workspace.read", "context.manage", "actions.read"}
	mk := func(name, brand string) (*client, string) {
		t.Helper()
		out := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": name, "scopes": scopes, "brand": brand, "action_gate": true})
		return &client{t: t, h: owner.h, bearer: out["token"].(string)}, out["api_token"].(map[string]any)["id"].(string)
	}
	rp, rpID := mk("Repocket agent", "repocket")
	geo, _ := mk("Geonode agent", "geonode")

	// Held, and readable by the agent that made it, kept to its brand.
	st, res := rp.do("POST", base+"/memories", map[string]any{"text": "Repocket fact", "kind": "fact"})
	if st != 202 {
		t.Fatalf("brand agent's change: %d %v", st, res)
	}
	id := res["action"].(map[string]any)["id"].(string)
	if v := rp.must(200, "GET", base+"/actions/"+id, nil); v["status"] != "held" || v["token_id"] != rpID {
		t.Fatalf("own held action: %v", v)
	}
	if l := rp.must(200, "GET", base+"/actions?status=held", nil)["data"].([]any); len(l) != 1 {
		t.Fatalf("own held list: %v", l)
	}
	rp.must(200, "GET", base+"/action-grants", nil)
	geo.must(404, "GET", base+"/actions/"+id, nil)
	if l := geo.must(200, "GET", base+"/actions", nil)["data"].([]any); len(l) != 0 {
		t.Fatalf("another brand's agent sees: %v", l)
	}
	geo.must(403, "POST", base+"/actions/"+id+"/approve", nil)

	// A mission header on a gated token still goes through the gate.
	agent, agentID := gatedAgent(t, owner, base, "Agent", "sequences.manage", "leads.manage", "actions.read")
	agent.header = map[string]string{"Outflare-Mission": "00000000-0000-0000-0000-000000000001"}
	st, res = agent.do("POST", base+"/sequences", map[string]any{"name": "S", "steps": []map[string]any{{"subject": "Hi", "body": "Hello"}}})
	agent.header = nil
	if st != 202 || res["action"] == nil {
		t.Fatalf("with a mission header: %d %v", st, res)
	}

	// "leads/**" is what is under leads: the collection itself has no
	// grant, so it is held, not refused.
	owner.must(201, "POST", base+"/action-grants", map[string]any{"token_id": agentID, "path": "leads/**", "mode": "refuse"})
	st, res = agent.do("POST", base+"/leads", map[string]any{"email": "ana@acme.example"})
	if st != 202 {
		t.Fatalf("collection under a ** grant: %d %v", st, res)
	}
	// A workspace-wide agent's change carries the brand it is about, from
	// the body; a change about no brand in particular is for someone with
	// every brand to see and decide, never a brand-limited approver.
	leadAction := res["action"].(map[string]any)["id"].(string)
	var geoID string
	for _, b := range owner.must(200, "GET", base+"/brands", nil)["data"].([]any) {
		if b.(map[string]any)["handle"] == "geonode" {
			geoID = b.(map[string]any)["id"].(string)
		}
	}
	st, res = agent.do("POST", base+"/leads", map[string]any{"email": "bo@acme.example", "brand_id": geoID})
	if st != 202 || res["action"].(map[string]any)["brand_id"] != geoID {
		t.Fatalf("brand from the body: %d %v", st, res)
	}
	geoApprover := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "Geonode approver", "scopes": []string{"actions.read", "actions.approve"}, "brand": "geonode"})
	limited := &client{t: t, h: owner.h, bearer: geoApprover["token"].(string)}
	limited.must(404, "GET", base+"/actions/"+leadAction, nil)
	limited.must(404, "POST", base+"/actions/"+leadAction+"/approve", nil)
	limited.must(200, "GET", base+"/actions/"+res["action"].(map[string]any)["id"].(string), nil)
	for _, x := range limited.must(200, "GET", base+"/actions", nil)["data"].([]any) {
		if x.(map[string]any)["brand_id"] == nil {
			t.Fatalf("a brand-limited approver sees a change about no brand: %v", x)
		}
	}

	// Missions are retired: a change to one is refused, never held for a
	// person to approve something that can't run.
	missions, _ := gatedAgent(t, owner, base, "Mission agent", "missions.read", "missions.run", "actions.read")
	st, res = missions.do("POST", base+"/missions", map[string]any{"name": "M", "goal": "Book meetings"})
	if st != 410 || res["error"].(map[string]any)["code"] != "mission_retired" {
		t.Fatalf("a mission write by an agent token: %d %v", st, res)
	}

	// A refusal isn't replayed for an idempotency key once a person
	// grants the change: the retry goes through the gate again.
	owner.must(201, "POST", base+"/action-grants", map[string]any{"token_id": agentID, "path": "leads/*/notes", "mode": "refuse"})
	agent.header = map[string]string{"Idempotency-Key": "note-1"}
	if st, _ := agent.do("POST", base+"/sequences", map[string]any{"name": "Idem", "steps": []map[string]any{{"subject": "Hi", "body": "Hello"}}}); st != 202 {
		t.Fatalf("held: %d", st)
	}
	owner.must(201, "POST", base+"/action-grants", map[string]any{"token_id": agentID, "path": "sequences", "method": "POST", "mode": "run"})
	st, res = agent.do("POST", base+"/sequences", map[string]any{"name": "Idem", "steps": []map[string]any{{"subject": "Hi", "body": "Hello"}}})
	agent.header = nil
	if st != 201 {
		t.Fatalf("retry with the same key after a grant: %d %v", st, res)
	}
	// A failed run keeps the handler's answer.
	st, res = agent.do("POST", base+"/sequences", map[string]any{"name": ""})
	if st != 400 {
		t.Fatalf("bad body under a run grant: %d %v", st, res)
	}
	failed := owner.must(200, "GET", base+"/actions?status=failed", nil)["data"].([]any)
	if len(failed) != 1 || failed[0].(map[string]any)["result"] == nil {
		t.Fatalf("failed run's result: %v", failed)
	}
}

// A grant that runs changes can cap what they spend: spend booked while a
// change runs is linked to it, and once the day's spend plus what the next
// change is likely to cost would pass the limit, changes wait for a person.
func TestActionSpendLimits(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	agent, agentID := gatedAgent(t, owner, base, "Agent", "outcomes.read", "outcomes.manage", "actions.read")
	owner.must(400, "POST", base+"/action-grants", map[string]any{"token_id": agentID, "path": "spend", "mode": "hold", "daily_spend_cents": 500})
	owner.must(400, "POST", base+"/action-grants", map[string]any{"token_id": agentID, "path": "spend", "mode": "run", "daily_spend_cents": 0})
	g := owner.must(201, "POST", base+"/action-grants", map[string]any{"token_id": agentID, "path": "spend", "method": "POST", "mode": "run", "daily_spend_cents": 500, "monthly_spend_cents": 100000})
	if g["daily_spend_cents"] != 500.0 || g["monthly_spend_cents"] != 100000.0 {
		t.Fatalf("grant: %v", g)
	}
	book := func(cents int) (int, map[string]any) {
		return agent.do("POST", base+"/spend", map[string]any{"category": "data", "provider": "Apollo", "amount_cents": cents, "note": "credits"})
	}
	// The first change has no history to reserve from: it runs, and the
	// charge it books is linked to it.
	st, res := book(300)
	if st != 201 {
		t.Fatalf("first spend: %d %v", st, res)
	}
	ran := owner.must(200, "GET", base+"/actions?status=executed", nil)["data"].([]any)
	if len(ran) != 1 || ran[0].(map[string]any)["spend_cents"] != 300.0 {
		t.Fatalf("spend on the action: %v", ran)
	}
	if res["action_id"] != ran[0].(map[string]any)["id"] {
		t.Fatalf("entry's action: %v", res)
	}
	// The next one would likely cost about as much again, past the day's
	// $5: it waits for a person, who can still approve it.
	st, res = book(300)
	if st != 202 || res["action"].(map[string]any)["decided_by_rule"] != "spend_limit" {
		t.Fatalf("over the limit: %d %v", st, res)
	}
	held := res["action"].(map[string]any)["id"].(string)
	done := owner.must(200, "POST", base+"/actions/"+held+"/approve", nil)
	if done["status"] != "executed" || done["spend_cents"] != 300.0 {
		t.Fatalf("approved over the limit: %v", done)
	}
	if n := len(owner.must(200, "GET", base+"/spend", nil)["data"].([]any)); n != 2 {
		t.Fatalf("spend entries: %d", n)
	}
}

// A run grant on paid ads gets the default spend caps when none are
// given; its own stand, and other paths and modes get none.
func TestAdsGrantsGetDefaultSpendCaps(t *testing.T) {
	h := freshServer(t)
	owner, ws := setupOwner(t, h)
	base := "/v1/workspaces/" + ws
	_, tokenID := gatedAgent(t, owner, base, "Ads agent", "leads.read", "leads.manage")
	g := owner.must(201, "POST", base+"/action-grants", map[string]any{"token_id": tokenID, "path": "ads/campaigns/*", "mode": "run"})
	if g["daily_spend_cents"] != 5000.0 || g["monthly_spend_cents"] != 50000.0 {
		t.Fatalf("ads run grant caps: %v %v", g["daily_spend_cents"], g["monthly_spend_cents"])
	}
	g = owner.must(201, "POST", base+"/action-grants", map[string]any{"token_id": tokenID, "path": "brands/*/ads/**", "mode": "run", "daily_spend_cents": 20000})
	if g["daily_spend_cents"] != 20000.0 || g["monthly_spend_cents"] != 50000.0 {
		t.Fatalf("own daily cap kept, month defaulted: %v %v", g["daily_spend_cents"], g["monthly_spend_cents"])
	}
	g = owner.must(201, "POST", base+"/action-grants", map[string]any{"token_id": tokenID, "path": "ads/**", "mode": "hold"})
	if g["daily_spend_cents"] != nil || g["monthly_spend_cents"] != nil {
		t.Fatalf("a hold grant has no caps: %v", g)
	}
	g = owner.must(201, "POST", base+"/action-grants", map[string]any{"token_id": tokenID, "path": "**", "mode": "run"})
	if g["daily_spend_cents"] != nil || g["monthly_spend_cents"] != nil {
		t.Fatalf("a grant on everything keeps no ads caps: %v", g)
	}
}
