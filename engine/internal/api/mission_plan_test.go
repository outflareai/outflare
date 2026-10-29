package api_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"testing"
)

// fakeClaude answers the Claude API: the model list (to test a key) and a
// planner draft for any message.
type fakeClaude struct{}

func (fakeClaude) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Header.Get("X-Api-Key") != "sk-ant-good" {
		w.WriteHeader(401)
		io.WriteString(w, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`)
		return
	}
	if r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/v1/models") {
		io.WriteString(w, `{"data":[{"id":"claude-opus-5-5","type":"model","display_name":"Claude Opus 5.5","created_at":"2026-09-01T00:00:00Z"}],"has_more":false,"first_id":"claude-opus-5-5","last_id":"claude-opus-5-5"}`)
		return
	}
	body, _ := io.ReadAll(r.Body)
	if strings.Contains(string(body), "You label replies to cold sales emails") {
		labelReply(w, string(body))
		return
	}
	if strings.Contains(string(body), "You draft a reply to a post on a social network") {
		draftSocial(w, string(body))
		return
	}
	if strings.Contains(string(body), "You write one original post for a company's own account") {
		draftSocialPost(w, string(body))
		return
	}
	if strings.Contains(string(body), "You draft a business's public reply to a customer review") {
		draftReviewAnswer(w, string(body))
		return
	}
	if strings.Contains(string(body), "You write the opening lines of a short, friendly email asking a website to link") {
		answerJSON(w, map[string]any{"opener": "Your hiking pricing page lists tools, and one of its links is broken; our pack guide covers the same ground."})
		return
	}
	if strings.Contains(string(body), "You read a reply to an email that asked a website to link") {
		answerJSON(w, map[string]any{"kind": "agreed", "confidence": 0.95})
		return
	}
	if strings.Contains(string(body), "proposing A/B tests") || strings.Contains(string(body), "You write versions of website copy") ||
		strings.Contains(string(body), "You explain an A/B test's results") || strings.Contains(string(body), "You write the learning of a finished A/B test") ||
		strings.Contains(string(body), "You answer a marketer's question about their company's past A/B tests") {
		answerABTestAI(w, string(body))
		return
	}
	if strings.Contains(string(body), "You answer questions for a business using only the passages") {
		answerKnowledgeQuestion(w, string(body))
		return
	}
	if strings.Contains(string(body), "You read one web page for a link building team") {
		readLinkPage(w)
		return
	}
	if strings.Contains(string(body), "You write fix tickets for a website") {
		writeTicket(w, string(body))
		return
	}
	if strings.Contains(string(body), `"finish_turn"`) {
		agentTurn(w, body)
		return
	}
	// The workspace summary goes to Claude; no lead emails do.
	if strings.Contains(string(body), "@acme-lead-com.example") {
		w.WriteHeader(400)
		io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"lead email leaked into the prompt"}}`)
		return
	}
	draft, _ := json.Marshal(map[string]any{
		"name": "Fintech CFOs, Australia", "goal": "Book 10 meetings with fintech CFOs in Australia by 30 November.",
		"goal_metric": "meetings", "goal_target": 10, "ends_on": "2099-11-30",
		"steps": []map[string]string{
			{"title": "Find CFOs", "detail": "Import 600 CFOs at Australian fintechs."},
			{"title": "Verify", "detail": "Verify every address with ZeroBounce before sending."},
			{"title": "Write and dry-run the sequence", "detail": "Three emails, then start in a dry run."},
		},
		"instructions": "Target CFOs only. Never mention pricing.",
		"max_sends":    3000, "max_spend_cents": 15000,
		"connectors":  []map[string]string{{"id": "zerobounce", "why": "Verify emails so bounces stay under 2%."}, {"id": "made-up", "why": "x"}},
		"questions":   []string{"Should crypto exchanges count as fintechs?"},
		"assumptions": []string{"50 to 500 staff"},
	})
	json.NewEncoder(w).Encode(map[string]any{
		"id": "msg_1", "type": "message", "role": "assistant", "model": "claude-opus-5-5",
		"content":     []any{map[string]any{"type": "text", "text": string(draft)}},
		"stop_reason": "end_turn",
		"usage":       map[string]any{"input_tokens": 3000, "output_tokens": 900},
	})
}

func TestMissionPlan(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	goal := map[string]any{"goal": "Book 10 meetings with fintech CFOs in Australia by 30 November"}

	// Without Claude connected, the planner says what to connect.
	code, out := owner.do("POST", base+"/missions/plan", goal)
	if code != 409 || out["error"].(map[string]any)["code"] != "connector_needed" {
		t.Fatalf("plan without Claude = %d %v", code, out)
	}
	owner.must(422, "POST", base+"/integrations", map[string]any{"connector": "claude", "credentials": map[string]any{"api_key": "sk-ant-bad"}})
	owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "claude", "credentials": map[string]any{"api_key": "sk-ant-good"}})
	owner.must(201, "POST", base+"/leads", map[string]any{"email": "jane@acme-lead-com.example"})

	// A viewer can't plan.
	viewer := agentToken(t, owner, base, "Reader", "missions.read")
	viewer.must(403, "POST", base+"/missions/plan", goal)

	res := owner.must(201, "POST", base+"/missions/plan", goal)
	m := res["mission"].(map[string]any)
	if m["status"] != "pending_approval" || m["name"] != "Fintech CFOs, Australia" {
		t.Fatalf("mission = %v", m)
	}
	if g := m["goal"].(map[string]any); g["metric"] != "meetings" || g["target"].(float64) != 10 {
		t.Errorf("goal = %v", g)
	}
	if l := m["limits"].(map[string]any); l["max_sends"].(float64) != 3000 || l["max_spend_cents"].(float64) != 15000 || l["ends_at"] == nil {
		t.Errorf("limits = %v", l)
	}
	if !strings.HasPrefix(m["plan"].(string), "1. **Find CFOs.**") || m["instructions"] != "Target CFOs only. Never mention pricing." {
		t.Errorf("plan = %q / %q", m["plan"], m["instructions"])
	}
	draft := res["draft"].(map[string]any)
	needs := draft["connectors"].([]any)
	if len(needs) != 1 || needs[0].(map[string]any)["name"] != "ZeroBounce" {
		t.Errorf("connectors = %v", needs)
	}

	// The check-in shows the question and the connect request.
	id := m["id"].(string)
	d := owner.must(200, "GET", base+"/missions/"+id, nil)
	reqs := d["open_connect_requests"].([]any)
	if len(reqs) != 1 || reqs[0].(map[string]any)["data"].(map[string]any)["connector_name"] != "ZeroBounce" {
		t.Errorf("connect requests = %v", reqs)
	}
	var kinds []string
	for _, u := range d["updates"].([]any) {
		kinds = append(kinds, u.(map[string]any)["kind"].(string))
	}
	if !strings.Contains(strings.Join(kinds, ","), "question") {
		t.Errorf("updates = %v", kinds)
	}
	// The plan's cost is in the mission's spend: 3000 in, 900 out at Opus 5.5 prices = 3 cents.
	if p := m["progress"].(map[string]any); p["spend_cents"].(float64) != 3 {
		t.Errorf("spend = %v", p["spend_cents"])
	}
	// A person approves it like any mission.
	owner.must(200, "POST", base+"/missions/"+id+"/approve", map[string]any{"mode": "dry_run"})
}

var (
	turnMu       sync.Mutex
	turnTriggers []string
	missionRe    = regexp.MustCompile(`Outflare-Mission: ([0-9a-f-]{36})`)
	triggerRe    = regexp.MustCompile(`This turn runs because: ([^\\]+)`)
)

// agentTurn plays the built-in agent: it checks mailboxes, posts progress,
// adds a lead (held for approval), then ends the turn.
func agentTurn(w http.ResponseWriter, body []byte) {
	var content []any
	if !strings.Contains(string(body), `"tool_result"`) {
		id := ""
		if m := missionRe.FindSubmatch(body); m != nil {
			id = string(m[1])
		}
		turnMu.Lock()
		if m := triggerRe.FindSubmatch(body); m != nil {
			turnTriggers = append(turnTriggers, string(m[1]))
		}
		turnMu.Unlock()
		use := func(n int, input map[string]any) map[string]any {
			return map[string]any{"type": "tool_use", "id": "t" + string(rune('0'+n)), "name": "call_api", "input": input}
		}
		content = []any{
			use(1, map[string]any{"method": "GET", "path": "/mailboxes", "reason": "Check sending capacity"}),
			use(2, map[string]any{"method": "POST", "path": "/missions/" + id + "/updates", "reason": "Tell the team",
				"body": map[string]any{"kind": "progress", "body": "Checked capacity; adding the first lead for approval."}}),
			use(3, map[string]any{"method": "POST", "path": "/leads", "reason": "First CFO from the list",
				"body": map[string]any{"email": "sam@example.org"}}),
		}
	} else {
		content = []any{map[string]any{"type": "tool_use", "id": "t9", "name": "finish_turn",
			"input": map[string]any{"summary": "Checked capacity and asked to add the first lead.", "next_check_in_minutes": 120, "done": false}}}
	}
	json.NewEncoder(w).Encode(map[string]any{
		"id": "msg_t", "type": "message", "role": "assistant", "model": "claude-opus-5-5",
		"content": content, "stop_reason": "tool_use",
		"usage": map[string]any{"input_tokens": 20000, "output_tokens": 1000, "cache_read_input_tokens": 10000},
	})
}

func TestMissionBuiltinRunner(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()

	// A mission with no Claude can't go to the built-in agent.
	m0 := owner.must(201, "POST", base+"/missions", map[string]any{"name": "By hand", "goal": "x"})
	code, out := owner.do("POST", base+"/missions/"+m0["id"].(string)+"/approve", map[string]any{"runner": "builtin"})
	if code != 409 || out["error"].(map[string]any)["code"] != "connector_needed" {
		t.Fatalf("approve builtin without Claude = %d %v", code, out)
	}
	conn := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "claude", "credentials": map[string]any{"api_key": "sk-ant-good"}})
	res := owner.must(201, "POST", base+"/missions/plan", map[string]any{"goal": "Book 10 meetings with fintech CFOs in Australia"})
	id := res["mission"].(map[string]any)["id"].(string)

	m := owner.must(200, "POST", base+"/missions/"+id+"/approve", map[string]any{"mode": "approve_writes", "runner": "builtin"})
	if m["runner"] != "builtin" || !strings.HasPrefix(m["token_name"].(string), "Outflare agent") || m["turns"].(map[string]any)["next_at"] == nil {
		t.Fatalf("approved = %v", m)
	}
	if n, err := e.srv.RunDueTurns(ctx); err != nil || n != 1 {
		t.Fatalf("first run = %d, %v", n, err)
	}
	m = owner.must(200, "GET", base+"/missions/"+id, nil)["mission"].(map[string]any)
	turns := m["turns"].(map[string]any)
	if turns["count"].(float64) != 1 || turns["summary"] != "Checked capacity and asked to add the first lead." || turns["error"] != "" {
		t.Errorf("turns = %v", turns)
	}
	// The lead waits for a person; the read and the update went through.
	if m["pending_actions"].(float64) != 1 || m["calls"].(float64) < 2 {
		t.Errorf("pending = %v, calls = %v", m["pending_actions"], m["calls"])
	}
	// Plan (3 cents) plus a turn of two calls: 40,000 in, 2,000 out and 20,000 cached = 21 cents.
	if p := m["progress"].(map[string]any); p["spend_cents"].(float64) != 24 {
		t.Errorf("spend = %v", p["spend_cents"])
	}
	// Not due again for two hours.
	if n, _ := e.srv.RunDueTurns(ctx); n != 0 {
		t.Errorf("ran %d turns before they were due", n)
	}
	// A person writing wakes the agent at once.
	owner.must(201, "POST", base+"/missions/"+id+"/updates", map[string]any{"kind": "instruction", "body": "Skip crypto exchanges."})
	if n, _ := e.srv.RunDueTurns(ctx); n != 1 {
		t.Errorf("person's note ran %d turns", n)
	}
	turnMu.Lock()
	got := strings.Join(turnTriggers, " | ")
	turnMu.Unlock()
	if !strings.Contains(got, "first turn") || !strings.Contains(got, "a person wrote to you") {
		t.Errorf("triggers = %s", got)
	}
	// Without Claude, the mission pauses and says why.
	owner.must(204, "DELETE", base+"/integrations/"+conn["id"].(string), nil)
	owner.must(201, "POST", base+"/missions/"+id+"/updates", map[string]any{"kind": "note", "body": "Any news?"})
	e.srv.RunDueTurns(ctx)
	m = owner.must(200, "GET", base+"/missions/"+id, nil)["mission"].(map[string]any)
	if m["status"] != "paused" || !strings.Contains(m["status_reason"].(string), "Connect an AI model") {
		t.Errorf("after disconnect = %v / %v", m["status"], m["status_reason"])
	}
}
