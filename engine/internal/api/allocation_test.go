package api_test

import (
	"strings"
	"testing"
	"time"
)

// mailboxIDs maps each mailbox's email to its id.
func mailboxIDs(t *testing.T, c *client, base string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, m := range c.must(200, "GET", base+"/mailboxes", nil)["data"].([]any) {
		mm := m.(map[string]any)
		out[mm["email"].(string)] = mm["id"].(string)
	}
	return out
}

// allocated returns the emails of the mailboxes a sequence sends from now.
func allocated(t *testing.T, c *client, base, seq string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, m := range c.must(200, "GET", base+"/sequences/"+seq+"/allocation", nil)["mailboxes"].([]any) {
		mm := m.(map[string]any)
		out[mm["email"].(string)] = mm["state"].(string)
	}
	return out
}

func only(m map[string]string) string {
	for k := range m {
		return k
	}
	return ""
}

func TestAllocationAssignsUndoesLocksAndSuggests(t *testing.T) {
	e := freshEnv(t)
	clock := &fakeClock{}
	clock.Set(time.Date(2030, 1, 8, 10, 0, 0, 0, time.UTC))
	owner, base, _ := sendingSetup(t, e, clock)
	boxes := mailboxIDs(t, owner, base)
	steps := []map[string]any{{"subject": "Hello", "body": "Hi"}}

	// No demand and no mailboxes: activation explains what is missing.
	bare := owner.must(201, "POST", base+"/sequences", map[string]any{"name": "Bare", "steps": steps})
	res := owner.must(400, "PATCH", base+"/sequences/"+bare["id"].(string), map[string]any{"status": "active"})
	if !strings.Contains(res["error"].(map[string]any)["message"].(string), "daily new-lead limit") {
		t.Fatalf("activation error: %v", res)
	}

	// A small target gets one mailbox the moment it is activated.
	a := owner.must(201, "POST", base+"/sequences", map[string]any{"name": "A", "target_daily": 1, "steps": steps})
	aID := a["id"].(string)
	if a["priority"] != "normal" || a["started_by_agent"] != false {
		t.Fatalf("defaults: %v", a)
	}
	a = owner.must(200, "PATCH", base+"/sequences/"+aID, map[string]any{"status": "active"})
	got := allocated(t, owner, base, aID)
	if len(got) != 1 || a["allocation"].(map[string]any)["mailboxes"] != 1.0 {
		t.Fatalf("after activation: %v %v", got, a["allocation"])
	}
	first := only(got)
	mb := owner.must(200, "GET", base+"/mailboxes/"+boxes[first], nil)
	if asg := mb["assignment"].(map[string]any); asg["sequence_id"] != aID || asg["source"] != "automatic" || asg["reason"] == "" {
		t.Fatalf("mailbox assignment: %v", mb["assignment"])
	}

	// Undoing the automatic choice frees the mailbox and it is not picked again.
	changes := owner.must(200, "GET", base+"/allocation/changes?sequence_id="+aID, nil)["data"].([]any)
	if len(changes) != 1 || changes[0].(map[string]any)["action"] != "assign" {
		t.Fatalf("changes: %v", changes)
	}
	owner.must(200, "POST", base+"/allocation/changes/"+changes[0].(map[string]any)["id"].(string)+"/undo", nil)
	owner.must(200, "POST", base+"/allocation/run", nil)
	got = allocated(t, owner, base, aID)
	if len(got) != 1 || only(got) == first {
		t.Fatalf("after undo: %v (first was %s)", got, first)
	}
	second := only(got)

	// Hand-assign the freed mailbox to a draft; drafts keep manual choices.
	b := owner.must(201, "POST", base+"/sequences", map[string]any{"name": "B", "steps": steps})
	bID := b["id"].(string)
	out := owner.must(200, "POST", base+"/mailboxes/assign", map[string]any{"mailbox_ids": []string{boxes[first]}, "sequence_id": bID})
	if r := out["data"].([]any)[0].(map[string]any)["result"]; r != "assigned" {
		t.Fatalf("manual assign: %v", out)
	}
	owner.must(200, "POST", base+"/allocation/run", nil)
	if got := allocated(t, owner, base, bID); len(got) != 1 {
		t.Fatalf("draft lost its manual mailbox: %v", got)
	}

	// manual_only: released by hand, never picked up again automatically.
	owner.must(200, "POST", base+"/mailboxes/lock", map[string]any{"mailbox_ids": []string{boxes[second]}, "kind": "manual_only", "note": "VIP sender"})
	owner.must(200, "POST", base+"/mailboxes/release", map[string]any{"mailbox_ids": []string{boxes[second]}})
	owner.must(200, "POST", base+"/allocation/run", nil)
	if got := allocated(t, owner, base, aID); len(got) != 0 {
		t.Fatalf("manual_only mailbox reassigned: %v", got)
	}
	att := owner.must(200, "GET", base+"/allocation/attention", nil)
	if len(att["shortages"].([]any)) != 1 {
		t.Fatalf("attention: %v", att)
	}

	// Suggest mode proposes instead of acting; applying carries it out.
	owner.must(200, "PATCH", base+"/allocation/settings", map[string]any{"mode": "suggest"})
	owner.must(200, "POST", base+"/mailboxes/lock", map[string]any{"mailbox_ids": []string{boxes[second]}, "kind": nil})
	owner.must(200, "POST", base+"/allocation/run", nil)
	if got := allocated(t, owner, base, aID); len(got) != 0 {
		t.Fatalf("suggest mode acted: %v", got)
	}
	sugs := owner.must(200, "GET", base+"/allocation/suggestions", nil)["data"].([]any)
	if len(sugs) != 1 || sugs[0].(map[string]any)["mailbox_id"] != boxes[second] {
		t.Fatalf("suggestions: %v", sugs)
	}
	owner.must(200, "POST", base+"/allocation/suggestions/"+sugs[0].(map[string]any)["id"].(string)+"/apply", nil)
	if got := allocated(t, owner, base, aID); len(got) != 1 {
		t.Fatalf("after apply: %v", got)
	}

	// The dry run is read-only and agrees nothing more is needed.
	plan := owner.must(200, "GET", base+"/allocation/plan", nil)
	if len(plan["changes"].([]any)) != 0 {
		t.Fatalf("plan: %v", plan)
	}
}

func TestAllocationTeamsSendersAndTokenLimits(t *testing.T) {
	e := freshEnv(t)
	clock := &fakeClock{}
	clock.Set(time.Date(2030, 1, 8, 10, 0, 0, 0, time.UTC))
	owner, base, _ := sendingSetup(t, e, clock)
	boxes := mailboxIDs(t, owner, base)

	fin := owner.must(201, "POST", base+"/teams", map[string]any{"name": "Finance"})["id"].(string)
	owner.must(400, "POST", base+"/teams", map[string]any{"name": "finance"})
	ana := owner.must(201, "POST", base+"/senders", map[string]any{"name": "Ana Smith", "kind": "person", "team_ids": []string{fin}})["id"].(string)

	// Matches come from the display name only, and need confirming.
	m := owner.must(200, "GET", base+"/senders/matches", nil)
	matches := m["data"].([]any)
	if len(matches) != 1 || matches[0].(map[string]any)["mailbox_id"] != boxes["ana@acme.test"] || m["unconfirmed"] != 2.0 {
		t.Fatalf("matches: %v", m)
	}
	owner.must(200, "POST", base+"/mailboxes/sender", map[string]any{"mailbox_ids": []string{boxes["ana@acme.test"]}, "sender_id": ana})
	mb := owner.must(200, "GET", base+"/mailboxes/"+boxes["ana@acme.test"], nil)
	if s := mb["sender"].(map[string]any); s["id"] != ana || s["confirmed"] != true {
		t.Fatalf("sender: %v", mb["sender"])
	}

	// With teams, a Finance sequence only gets Finance's confirmed sender.
	steps := []map[string]any{{"subject": "Hello", "body": "Hi"}}
	s := owner.must(201, "POST", base+"/sequences", map[string]any{"name": "Fin", "team_id": fin, "target_daily": 1000, "steps": steps})
	owner.must(200, "PATCH", base+"/sequences/"+s["id"].(string), map[string]any{"status": "active"})
	if got := allocated(t, owner, base, s["id"].(string)); len(got) != 1 || only(got) != "ana@acme.test" {
		t.Fatalf("team allocation: %v", got)
	}

	// Token limits: the agent stays within its priority and lands in its team.
	tok := owner.must(201, "POST", base+"/api-tokens", map[string]any{
		"name": "Outbound agent", "scopes": []string{"sequences.read", "sequences.manage", "tokens.manage"},
		"daily_send_budget": 50, "max_priority": "normal", "default_team_id": fin,
	})
	view := tok["api_token"].(map[string]any)
	if view["daily_send_budget"] != 50.0 || view["max_priority"] != "normal" || view["may_take_mailboxes"] != false {
		t.Fatalf("token view: %v", view)
	}
	agent := &client{t: t, h: e.h, bearer: tok["token"].(string)}
	agent.must(400, "POST", base+"/sequences", map[string]any{"name": "Urgent", "priority": "high"})
	made := agent.must(201, "POST", base+"/sequences", map[string]any{"name": "Agent run", "target_daily": 10})
	if made["team_id"] != fin || made["started_by_agent"] != true {
		t.Fatalf("agent sequence: %v", made)
	}

	// A token cannot loosen its own limits, or grant more than it has.
	agent.must(403, "PATCH", base+"/api-tokens/"+view["id"].(string), map[string]any{"daily_send_budget": 500})
	agent.must(403, "POST", base+"/api-tokens", map[string]any{"name": "Child", "scopes": []string{"sequences.read"}, "daily_send_budget": 500, "max_priority": "low"})
	agent.must(403, "POST", base+"/api-tokens", map[string]any{"name": "Child", "scopes": []string{"sequences.read"}, "daily_send_budget": 10, "max_priority": "low", "may_take_mailboxes": true})
	agent.must(201, "POST", base+"/api-tokens", map[string]any{"name": "Child", "scopes": []string{"sequences.read"}, "daily_send_budget": 10, "max_priority": "low"})

	// The owner can raise them.
	up := owner.must(200, "PATCH", base+"/api-tokens/"+view["id"].(string), map[string]any{"may_take_mailboxes": true, "max_priority": "high"})
	if up["may_take_mailboxes"] != true || up["max_priority"] != "high" || up["daily_send_budget"] != 50.0 {
		t.Fatalf("token update: %v", up)
	}
	owner.must(400, "PATCH", base+"/api-tokens/"+view["id"].(string), map[string]any{"max_priority": "urgent"})
}

func TestFollowUpsStayOnTheirMailboxWhileItDrains(t *testing.T) {
	e := freshEnv(t)
	clock := &fakeClock{}
	clock.Set(time.Date(2030, 1, 8, 10, 0, 0, 0, time.UTC)) // a Tuesday
	owner, base, srv := sendingSetup(t, e, clock)
	boxes := mailboxIDs(t, owner, base)

	_, imp := owner.raw("POST", base+"/leads/import?list=L", "text/csv", "email\nlee@one-io.example\n")
	a := owner.must(201, "POST", base+"/sequences", map[string]any{
		"name": "A", "target_daily": 1, "send_days": []int{1, 2, 3, 4, 5, 6, 7},
		"steps": []map[string]any{{"subject": "Hello", "body": "Hi"}, {"delay_days": 1, "body": "Following up"}},
	})
	aID := a["id"].(string)
	owner.must(200, "POST", base+"/sequences/"+aID+"/enrollments", map[string]any{"list_id": imp["list_id"]})
	owner.must(200, "PATCH", base+"/sequences/"+aID, map[string]any{"status": "active"})
	from := only(allocated(t, owner, base, aID))
	e.pump(t)
	if msgs := srv.Messages(); len(msgs) != 1 || msgs[0].From != from {
		t.Fatalf("first email: %+v (want from %s)", msgs, from)
	}

	// Moving the mailbox while a lead is mid-sequence drains it instead.
	b := owner.must(201, "POST", base+"/sequences", map[string]any{"name": "B", "steps": []map[string]any{{"subject": "Yo", "body": "x"}}})
	out := owner.must(200, "POST", base+"/mailboxes/assign", map[string]any{"mailbox_ids": []string{boxes[from]}, "sequence_id": b["id"]})
	if r := out["data"].([]any)[0].(map[string]any)["result"]; r != "draining" {
		t.Fatalf("move with a lead in flight: %v", out)
	}
	if st := allocated(t, owner, base, aID)[from]; st != "draining" {
		t.Fatalf("state: %v", allocated(t, owner, base, aID))
	}

	// The follow-up still goes from the same mailbox, in the same thread.
	clock.Add(25 * time.Hour)
	e.pump(t)
	msgs := srv.Messages()
	if len(msgs) != 2 || msgs[1].From != from {
		t.Fatalf("follow-up: %+v (want from %s)", msgs, from)
	}
	if subj := parse(t, msgs[1]).Header.Get("Subject"); subj != "Re: Hello" {
		t.Fatalf("follow-up subject %q", subj)
	}

	// With nothing left in flight, the drain finishes and the mailbox moves on.
	owner.must(200, "POST", base+"/allocation/run", nil)
	if got := allocated(t, owner, base, b["id"].(string)); got[from] != "active" {
		t.Fatalf("after drain: %v", got)
	}
}
