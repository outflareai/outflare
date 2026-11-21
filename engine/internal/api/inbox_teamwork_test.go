package api_test

import (
	"context"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/track"
)

// A reply can sit with a team, carry notes only the team sees, and have
// teammates looped in, who then find it under following=me. Support
// conversations get the same loop-in.
func TestInboxTeamwork(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, base, _ := sendingSetup(t, e, &fakeClock{t: time.Now()})
	ws := base[len("/v1/workspaces/"):]
	ctx := context.Background()
	mb := mailboxIDs(t, owner, base)["ana@acme.test"]
	bb := "/v1/brands/" + brandOf(t, e, `SELECT outflare_first_brand($1)::text`, ws)
	var me string
	if err := e.pool.QueryRow(ctx, `SELECT user_id::text FROM memberships WHERE workspace_id = $1`, ws).Scan(&me); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for i, from := range []string{"one@a.test", "two@b.test"} {
		var id string
		if err := e.pool.QueryRow(ctx, `INSERT INTO inbound_messages (workspace_id, mailbox_id, uidvalidity, uid, from_email, kind, body, received_at)
			VALUES ($1, $2, 0, $3::bigint, $4, 'reply', 'Tell me more', now() - $3::int * interval '1 minute') RETURNING id::text`,
			ws, mb, i+1, from).Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	one := bb + "/inbox/" + ids[0]
	count := func(query string) int {
		t.Helper()
		return len(owner.must(200, "GET", bb+"/inbox?"+query, nil)["data"].([]any))
	}

	// Nothing yet.
	if g := owner.must(200, "GET", one, nil); len(g["notes"].([]any)) != 0 || len(g["followers"].([]any)) != 0 || g["message"].(map[string]any)["team_id"] != nil {
		t.Fatalf("a new reply: %v", g)
	}
	if count("following=me") != 0 {
		t.Fatal("following something before being looped in")
	}

	// Move it to a team, and back to none.
	team := owner.must(201, "POST", base+"/teams", map[string]any{"name": "Sales"})["id"].(string)
	owner.must(400, "PATCH", one, map[string]any{"team_id": "nope"})
	owner.must(400, "PATCH", one, map[string]any{"team_id": "00000000-0000-0000-0000-000000000000"})
	if m := owner.must(200, "PATCH", one, map[string]any{"team_id": team}); m["team_id"] != team || m["team_name"] != "Sales" {
		t.Fatalf("moved to a team: %v", m)
	}
	if count("team_id="+team) != 1 {
		t.Fatal("team_id filter")
	}
	owner.must(400, "GET", bb+"/inbox?team_id=nope", nil)
	if m := owner.must(200, "PATCH", one, map[string]any{"team_id": nil}); m["team_id"] != nil {
		t.Fatalf("back to no team: %v", m)
	}

	// A note for the team.
	owner.must(400, "POST", one+"/notes", map[string]any{"body": "  "})
	owner.must(404, "POST", bb+"/inbox/00000000-0000-0000-0000-000000000000/notes", map[string]any{"body": "x"})
	n := owner.must(201, "POST", one+"/notes", map[string]any{"body": "Called them, wants a quote."})
	if n["body"] != "Called them, wants a quote." || n["author"].(map[string]any)["id"] != me {
		t.Fatalf("note: %v", n)
	}
	if g := owner.must(200, "GET", one, nil)["notes"].([]any); len(g) != 1 || g[0].(map[string]any)["id"] != n["id"] {
		t.Fatalf("notes on the reply: %v", g)
	}

	// Loop a teammate in: once, however often it is asked.
	owner.must(400, "PUT", one+"/followers/00000000-0000-0000-0000-000000000000", nil)
	for range 2 {
		if f := owner.must(200, "PUT", one+"/followers/"+me, nil)["followers"].([]any); len(f) != 1 || f[0].(map[string]any)["id"] != me {
			t.Fatalf("looped in: %v", f)
		}
	}
	if count("following=me") != 1 || count("following="+me) != 1 {
		t.Fatal("following filter")
	}
	owner.must(400, "GET", bb+"/inbox?following=nope", nil)
	if f := owner.must(200, "GET", one, nil)["followers"].([]any); len(f) != 1 {
		t.Fatalf("followers on the reply: %v", f)
	}
	if f := owner.must(200, "DELETE", one+"/followers/"+me, nil)["followers"].([]any); len(f) != 0 || count("following=me") != 0 {
		t.Fatalf("taken off: %v", f)
	}

	// Asking the AI for a draft without an AI model says so and sends
	// nothing.
	if d := owner.must(200, "POST", one+"/suggest-reply", nil); d["answer"] != nil || d["ai"].(map[string]any)["problem"] == nil {
		t.Fatalf("draft without an AI model: %v", d)
	}
	if len(named(got(), "inbox.reply_suggested")) != 1 {
		t.Error("no inbox.reply_suggested event")
	}

	// Another workspace sees none of it.
	other := owner.must(201, "POST", "/v1/workspaces", map[string]any{"name": "Other"})["id"].(string)
	ob := "/v1/brands/" + brandOf(t, e, `SELECT outflare_first_brand($1)::text`, other)
	owner.must(404, "POST", ob+"/inbox/"+ids[0]+"/notes", map[string]any{"body": "x"})
	owner.must(404, "PUT", ob+"/inbox/"+ids[0]+"/followers/"+me, nil)

	for name, want := range map[string]int{"inbox.note_added": 1, "inbox.follower_added": 1, "inbox.follower_removed": 1} {
		if g := len(named(got(), name)); g != want {
			t.Errorf("%d %s events, want %d", g, name, want)
		}
	}
}
