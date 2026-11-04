package api_test

import (
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
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
		unsure := ""
		if strings.Contains(string(body), "unsure.example") {
			unsure = "The page is about car parts, not hiking."
		}
		answerJSON(w, map[string]any{"opener": "Your hiking pricing page lists tools, and one of its links is broken; our pack guide covers the same ground.", "unsure": unsure})
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
	if strings.Contains(string(body), "A customer asked a business a question its knowledge base couldn't answer") {
		answerFromReply(w, string(body))
		return
	}
	if strings.Contains(string(body), "You sort a business's support conversations into its topics") {
		topic := ""
		if strings.Contains(string(body), "invoice") {
			topic = "Billing"
		}
		answerJSON(w, map[string]any{"topic": topic})
		return
	}
	if strings.Contains(string(body), "You read pairs of a customer's question to a business") {
		answerLearnReplies(w, string(body))
		return
	}
	if strings.Contains(string(body), "You answer customers for a business, by chat or email") {
		answerSupportAssistant(w, string(body))
		return
	}
	if strings.Contains(string(body), "You look for the answer to a customer's question that a business's knowledge base doesn't have yet") {
		answerFindAnswer(w, string(body))
		return
	}
	if strings.Contains(string(body), "You check a support reply against the articles") {
		answerJSON(w, map[string]any{"supported": !strings.Contains(string(body), "$99"), "unsupported": []string{}})
		return
	}
	if strings.Contains(string(body), "You answer questions for a business using only the passages") {
		answerKnowledgeQuestion(w, string(body))
		return
	}
	if strings.Contains(string(body), "You answer one question about a person or company for a marketing automation") {
		answerJourneyAI(w, string(body))
		return
	}
	if strings.Contains(string(body), "You read one web page for a link building team") {
		readLinkPage(w)
		return
	}
	if strings.Contains(string(body), "You write the opening lines of a short email pitching a story") {
		answerJSON(w, map[string]any{"opener": "You covered bot blocking last week. We measured 1,200 sites, and a third block their own customers' bots."})
		return
	}
	if strings.Contains(string(body), "You read a journalist's reply to a story pitch") {
		answerJSON(w, map[string]any{"kind": "wants_data"})
		return
	}
	if strings.Contains(string(body), "You suggest digital PR story ideas") || strings.Contains(string(body), "You write a first draft of a press release") {
		answerStories(w, string(body))
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
