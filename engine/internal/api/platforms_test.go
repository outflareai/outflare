package api_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/outflareai/outflare/engine/internal/platforms"
)

// ebWarm is the sender emails with EmailBison's own warm-up on, and the
// warm-up calls it received.
var ebWarm = struct {
	sync.Mutex
	on    map[int]bool
	calls []string
}{on: map[int]bool{}}

// fakeEmailBison has one live campaign with Ana on it, and three sender
// emails, for the key "eb-key".
type fakeEmailBison struct{}

func (fakeEmailBison) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	auth := r.Header.Get("Authorization")
	if auth != "Bearer eb-key" && auth != "Bearer eb-replies" {
		w.WriteHeader(401)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if strings.HasPrefix(r.URL.Path, "/api/replies") {
		ebReplies(w, r, auth == "Bearer eb-replies")
		return
	}
	switch r.URL.Path {
	case "/api/users":
		w.Write([]byte(`{"data":{"id":7,"name":"Acme Agency","email":"ops@acme.test"}}`))
	case "/api/sender-emails":
		ebWarm.Lock()
		on := func(id int) string { return map[bool]string{true: "true", false: "false"}[ebWarm.on[id]] }
		w.Write([]byte(`{"data":[{"id":1,"email":"ana@send-acme.test","name":"Ana","status":"Connected","daily_limit":30,"warmup_score":98,"warmup_enabled":` + on(1) + `},
			{"id":2,"email":"bo@send-acme.test","name":"Bo","status":"Connected","daily_limit":30,"warmup_score":97,"warmup_enabled":` + on(2) + `},
			{"id":3,"email":"cy@send-acme.test","name":"Cy","status":"Disconnected","daily_limit":30,"warmup_enabled":` + on(3) + `}],"meta":{"last_page":1}}`))
		ebWarm.Unlock()
	case "/api/warmup/sender-emails/enable", "/api/warmup/sender-emails/disable":
		var body struct {
			IDs []int `json:"sender_email_ids"`
		}
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &body)
		enable := strings.HasSuffix(r.URL.Path, "/enable")
		ebWarm.Lock()
		for _, id := range body.IDs {
			ebWarm.on[id] = enable
			ebWarm.calls = append(ebWarm.calls, map[bool]string{true: "enable ", false: "disable "}[enable]+string(rune('0'+id)))
		}
		ebWarm.Unlock()
		w.Write([]byte(`{"data":{"success":true}}`))
	case "/api/campaigns":
		w.Write([]byte(`{"data":[{"id":10,"name":"Q4 founders","status":"active"}],"meta":{"last_page":1}}`))
	case "/api/campaigns/10/sender-emails":
		w.Write([]byte(`{"data":[{"id":1,"email":"ana@send-acme.test"}],"meta":{"last_page":1}}`))
	case "/api/campaigns/10/attach-sender-emails", "/api/campaigns/10/remove-sender-emails":
		w.Write([]byte(`{"data":{"success":true}}`))
	default:
		http.NotFound(w, r)
	}
}

func TestPlatformAccountsSyncReviewAndGuards(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws

	i := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "emailbison",
		"credentials": map[string]string{"base_url": "https://send.acme.test", "api_key": "eb-key"}})
	id := i["id"].(string)

	res := owner.must(200, "POST", base+"/platforms/"+id+"/sync", nil)
	if res["campaigns"] != 1.0 || res["mailboxes"] != 3.0 || res["accepted"] != 1.0 {
		t.Fatalf("sync: %v", res)
	}
	list := owner.must(200, "GET", base+"/platforms", nil)["data"].([]any)
	if len(list) != 1 {
		t.Fatalf("platforms: %v", list)
	}
	p := list[0].(map[string]any)
	if !strings.HasPrefix(p["name"].(string), "EmailBison") || p["control"] != "full" || p["awaiting_review"] != true || p["stale"] != false {
		t.Fatalf("platform: %v", p)
	}
	att := owner.must(200, "GET", base+"/allocation/attention", nil)
	if len(att["platforms"].([]any)) != 1 {
		t.Fatalf("attention: %v", att["platforms"])
	}

	// The campaign is a sequence linked to the platform.
	seqs := owner.must(200, "GET", base+"/sequences", nil)["data"].([]any)
	if len(seqs) != 1 {
		t.Fatalf("sequences: %v", seqs)
	}
	seq := seqs[0].(map[string]any)
	sid := seq["id"].(string)
	links := seq["links"].([]any)
	if seq["name"] != "Q4 founders" || len(links) != 1 || links[0].(map[string]any)["external_id"] != "10" {
		t.Fatalf("sequence: %v", seq)
	}

	// The platform owns its status, steps and leads.
	out := owner.must(400, "PATCH", base+"/sequences/"+sid, map[string]any{"status": "paused"})
	if !strings.Contains(out["error"].(map[string]any)["message"].(string), "EmailBison") {
		t.Fatalf("pause: %v", out)
	}
	owner.must(400, "PUT", base+"/sequences/"+sid+"/steps", map[string]any{"steps": []any{}})
	owner.must(400, "POST", base+"/sequences/"+sid+"/enrollments", map[string]any{"lead_ids": []string{}})
	owner.must(400, "DELETE", base+"/sequences/"+sid, nil)
	owner.must(200, "PATCH", base+"/sequences/"+sid, map[string]any{"target_daily": 60})

	// Its mailboxes have no login and say where they are.
	boxes := map[string]map[string]any{}
	for _, m := range owner.must(200, "GET", base+"/mailboxes", nil)["data"].([]any) {
		m := m.(map[string]any)
		boxes[m["email"].(string)] = m
	}
	ana := boxes["ana@send-acme.test"]
	if ana["has_login"] != false || len(ana["platforms"].([]any)) != 1 || ana["platforms"].([]any)[0].(map[string]any)["campaigns"] != 1.0 {
		t.Fatalf("ana: %v", ana)
	}
	if asg := ana["assignment"].(map[string]any); asg["platform"] == nil || !strings.HasPrefix(asg["platform"].(string), "EmailBison") || asg["confirmed"] != true {
		t.Fatalf("ana assignment: %v", asg)
	}

	// A loginless mailbox can't go to a sequence Outflare sends itself.
	own := owner.must(201, "POST", base+"/sequences", map[string]any{"name": "Own"})
	r := owner.must(200, "POST", base+"/mailboxes/assign", map[string]any{"mailbox_ids": []string{boxes["bo@send-acme.test"]["id"].(string)}, "sequence_id": own["id"]})
	if got := r["data"].([]any)[0].(map[string]any); got["result"] != "refused" || !strings.Contains(got["message"].(string), "no login") {
		t.Fatalf("assign to own: %v", r)
	}
	// Nor can a disconnected one join the platform campaign.
	r = owner.must(200, "POST", base+"/mailboxes/assign", map[string]any{"mailbox_ids": []string{boxes["cy@send-acme.test"]["id"].(string)}, "sequence_id": sid})
	if got := r["data"].([]any)[0].(map[string]any); got["result"] != "refused" || !strings.Contains(got["message"].(string), "disconnected") {
		t.Fatalf("assign cy: %v", got)
	}
	r = owner.must(200, "POST", base+"/mailboxes/assign", map[string]any{"mailbox_ids": []string{boxes["bo@send-acme.test"]["id"].(string)}, "sequence_id": sid})
	if got := r["data"].([]any)[0].(map[string]any); got["result"] != "assigned" || !strings.Contains(got["message"].(string), "confirm") {
		t.Fatalf("assign bo: %v", r)
	}
	alloc := owner.must(200, "GET", base+"/sequences/"+sid+"/allocation", nil)
	unconfirmed := 0
	for _, m := range alloc["mailboxes"].([]any) {
		if m.(map[string]any)["confirmed"] == false {
			unconfirmed++
		}
	}
	if unconfirmed == 0 || len(alloc["links"].([]any)) != 1 {
		t.Fatalf("allocation: %v", alloc)
	}

	// Review lets allocation change the account from now on.
	owner.must(200, "POST", base+"/platforms/"+id+"/review", nil)
	p = owner.must(200, "GET", base+"/platforms", nil)["data"].([]any)[0].(map[string]any)
	if p["awaiting_review"] != false {
		t.Fatalf("after review: %v", p)
	}

	// Connections that don't report mailboxes can't be synced.
	zb := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "zerobounce", "credentials": map[string]string{"api_key": "good-key"}})
	owner.must(400, "POST", base+"/platforms/"+zb["id"].(string)+"/sync", nil)
	owner.must(404, "POST", base+"/platforms/00000000-0000-0000-0000-000000000000/sync", nil)
}

func TestWarmupOnAConnectedSequencer(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	ebWarm.Lock()
	// Bo already warms up in EmailBison.
	ebWarm.on, ebWarm.calls = map[int]bool{2: true}, nil
	ebWarm.Unlock()

	id := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "emailbison",
		"credentials": map[string]string{"base_url": "https://send.acme.test", "api_key": "eb-key"}})["id"].(string)
	owner.must(200, "POST", base+"/platforms/"+id+"/sync", nil)
	ids := map[string]string{}
	for _, m := range owner.must(200, "GET", base+"/mailboxes", nil)["data"].([]any) {
		m := m.(map[string]any)
		ids[m["email"].(string)] = m["id"].(string)
	}
	ana := ids["ana@send-acme.test"]

	// The first sync takes Bo's warm-up as EmailBison's, and shows it.
	bo := owner.must(200, "GET", base+"/mailboxes/"+ids["bo@send-acme.test"], nil)
	if w := bo["warmup"].(map[string]any); w["enabled"] != true || w["provider"] != id || w["waiting_on"] != nil {
		t.Fatalf("Bo's existing EmailBison warm-up: %v", w)
	}
	if p := bo["platforms"].([]any)[0].(map[string]any); p["warmup_enabled"] != true {
		t.Fatalf("Bo's platform entry: %v", p)
	}
	if w := owner.must(200, "GET", base+"/mailboxes/"+ana, nil)["warmup"].(map[string]any); w["enabled"] != false || w["provider"] != "outflare" {
		t.Fatalf("Ana untouched: %v", w)
	}
	runJobs := func() {
		t.Helper()
		rows, err := e.pool.Query(ctx, `DELETE FROM jobs WHERE kind = $1 RETURNING payload->>'mailbox_id', payload->>'integration_id'`, platforms.JobWarmup)
		if err != nil {
			t.Fatal(err)
		}
		var todo [][2]string
		for rows.Next() {
			var m, i string
			rows.Scan(&m, &i)
			todo = append(todo, [2]string{m, i})
		}
		rows.Close()
		for _, j := range todo {
			if err := e.platforms.SetWarmup(ctx, ws, j[0], j[1]); err != nil {
				t.Fatal(err)
			}
		}
		owner.must(200, "POST", base+"/platforms/"+id+"/sync", nil)
	}

	// A sequencer that isn't connected, or a mailbox it doesn't have, is refused.
	if code, _ := owner.do("PATCH", base+"/mailboxes/"+ana, map[string]any{"warmup": map[string]any{"enabled": true, "provider": "00000000-0000-0000-0000-000000000000"}}); code != 400 {
		t.Fatalf("unknown provider: %d", code)
	}

	// Ana warms up in EmailBison: Outflare asks EmailBison to turn it on,
	// and says it is waiting until EmailBison shows it.
	w := owner.must(200, "PATCH", base+"/mailboxes/"+ana, map[string]any{"warmup": map[string]any{"enabled": true, "provider": id}})["warmup"].(map[string]any)
	if w["provider"] != id || w["waiting_on"] != "platform" || w["platform"].(map[string]any)["name"] == "" {
		t.Fatalf("after choosing EmailBison: %v", w)
	}
	runJobs()
	w = owner.must(200, "GET", base+"/mailboxes/"+ana, nil)["warmup"].(map[string]any)
	if p := w["platform"].(map[string]any); w["waiting_on"] != nil || p["enabled"] != true || p["score"] != 98.0 {
		t.Fatalf("after EmailBison turned it on: %v", w)
	}
	// Outflare doesn't run warm-up itself, so it can't take it over.
	if code, _ := owner.do("PATCH", base+"/mailboxes/"+ana, map[string]any{"warmup": map[string]any{"provider": "outflare"}}); code != 400 {
		t.Fatalf("moving to Outflare's warm-up: %d", code)
	}

	// Turning it off turns EmailBison's warm-up off.
	owner.must(200, "PATCH", base+"/mailboxes/"+ana, map[string]any{"warmup": map[string]any{"enabled": false}})
	runJobs()
	ebWarm.Lock()
	calls := slices.Clone(ebWarm.calls)
	ebWarm.Unlock()
	if !slices.Equal(calls, []string{"enable 1", "disable 1"}) {
		t.Fatalf("EmailBison calls %v", calls)
	}

	// A mailbox in no sequencer can't be warmed up, alone or in a list.
	own := addMailbox(t, e, ws, "cy@acme.test")
	if code, _ := owner.do("PATCH", base+"/mailboxes/"+own, map[string]any{"warmup": map[string]any{"enabled": true}}); code != 400 {
		t.Fatalf("warm-up with no sequencer: %d", code)
	}
	if code, _ := owner.do("POST", base+"/mailboxes/warmup", map[string]any{"enabled": true, "mailbox_ids": []string{ana, own}}); code != 400 {
		t.Fatalf("bulk warm-up with no sequencer: %d", code)
	}

	// Turning it on again without naming a sequencer keeps EmailBison;
	// turning on every mailbox leaves out the one in no sequencer.
	w = owner.must(200, "PATCH", base+"/mailboxes/"+ana, map[string]any{"warmup": map[string]any{"enabled": true}})["warmup"].(map[string]any)
	if w["provider"] != id || w["enabled"] != true {
		t.Fatalf("on again: %v", w)
	}
	if res := owner.must(200, "POST", base+"/mailboxes/warmup", map[string]any{"enabled": true}); res["updated"] != 3.0 {
		t.Fatalf("every mailbox: %v", res)
	}
	if w := owner.must(200, "GET", base+"/mailboxes/"+own, nil)["warmup"].(map[string]any); w["enabled"] != false || w["provider"] != "outflare" {
		t.Fatalf("mailbox in no sequencer: %v", w)
	}
	owner.must(200, "POST", base+"/mailboxes/warmup", map[string]any{"enabled": false, "provider": "outflare"})
	runJobs()

	// All of EmailBison's mailboxes at once.
	if res := owner.must(200, "POST", base+"/mailboxes/warmup", map[string]any{"enabled": true, "provider": id}); res["updated"] != 3.0 {
		t.Fatalf("bulk: %v", res)
	}
	runJobs()
	ebWarm.Lock()
	on := len(ebWarm.on) == 3 && ebWarm.on[1] && ebWarm.on[2] && ebWarm.on[3]
	ebWarm.Unlock()
	if !on {
		t.Fatalf("bulk did not turn EmailBison warm-up on: %v", ebWarm.on)
	}
}
