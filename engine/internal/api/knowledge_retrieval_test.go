package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/outflareai/outflare/engine/internal/track"
)

// answerKnowledgeQuestion answers fakeClaude's knowledge questions: it
// cites the first passage that shares a word with the question, or says
// the passages don't cover it. It fails the call if a team-only passage
// reaches an answer meant for customers.
func answerKnowledgeQuestion(w http.ResponseWriter, body string) {
	var req struct {
		Messages []struct {
			Content any `json:"content"`
		} `json:"messages"`
	}
	json.Unmarshal([]byte(body), &req)
	raw, _ := json.Marshal(req.Messages)
	text := strings.NewReplacer(`\n`, "\n", `\u003c`, "<", `\u003e`, ">", `\u0026`, "&").Replace(string(raw))
	question := regexp.MustCompile(`(?s)<question>\s*(.*?)\s*</question>`).FindStringSubmatch(text)
	passages := regexp.MustCompile(`(?s)<passage n=\\"(\d+)\\"[^>]*>\s*(.*?)\s*</passage>`).FindAllStringSubmatch(text, -1)
	if strings.Contains(text, "a customer of the business") && strings.Contains(text, "internal margin") {
		w.WriteHeader(400)
		json.NewEncoder(w).Encode(map[string]any{"type": "error", "error": map[string]any{"type": "invalid_request_error", "message": "team-only knowledge reached a customer answer"}})
		return
	}
	answer := map[string]any{"answered": false, "answer": "The knowledge base doesn't say.", "cited": []int{}}
	if len(question) == 2 {
		words := strings.Fields(strings.ToLower(strings.Trim(question[1], "?")))
		for _, p := range passages {
			low := strings.ToLower(p[2])
			for _, word := range words {
				if len(word) > 4 && strings.Contains(low, word) {
					n := 0
					json.Unmarshal([]byte(p[1]), &n)
					plain := strings.Join(strings.Fields(strings.NewReplacer("**", "", "#", "", "|", " ").Replace(p[2])), " ")
					if len(plain) > 240 {
						plain = plain[:240] + "…"
					}
					answer = map[string]any{"answered": true, "answer": "Yes. " + plain, "cited": []int{n, 99}}
					break
				}
			}
			if answer["answered"] == true {
				break
			}
		}
	}
	answerJSON(w, answer)
}

func TestKnowledgeRetrieval(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	kb := base + "/knowledge"
	owner.must(201, "POST", base+"/brands", map[string]any{"name": "Geonode", "handle": "geonode"})
	owner.must(201, "POST", base+"/brands", map[string]any{"name": "Repocket", "handle": "repocket"})

	refunds := owner.must(201, "POST", kb+"/items", map[string]any{"title": "Refunds and cancellations", "visibility": "public",
		"body": "## Refunds\n\nWe refund any plan within 30 days, no questions asked.\n\n## How to cancel\n\n1. Open Settings\n2. Choose Billing\n3. Click Cancel plan"})
	owner.must(201, "POST", kb+"/items", map[string]any{"kind": "snippet", "title": "Do you offer a free trial?", "body": "Yes, every plan has a 7-day free trial.", "visibility": "customers_ai"})
	owner.must(201, "POST", kb+"/items", map[string]any{"title": "Pricing notes", "body": "Our internal margin on the Pro plan is 80%. Discounts up to 20% need a manager.", "visibility": "team"})
	owner.must(201, "POST", kb+"/items", map[string]any{"title": "Repocket payouts", "body": "Repocket pays out monthly by PayPal.", "brand_id": "repocket"})
	owner.must(201, "POST", kb+"/items", map[string]any{"title": "Draft refund idea", "body": "Maybe refund within 60 days.", "status": "draft"})

	// Passages, by words: sections are their own passages, drafts and other
	// audiences are left out.
	p := owner.must(200, "GET", kb+"/passages?q=How+do+I+cancel+my+plan", nil)
	list := p["data"].([]any)
	if len(list) == 0 || list[0].(map[string]any)["heading"] != "How to cancel" || p["meaning"] != false {
		t.Fatalf("passages = %v", p)
	}
	for _, x := range list {
		if x.(map[string]any)["title"] == "Draft refund idea" {
			t.Fatalf("a draft was found: %v", list)
		}
	}
	if l := owner.must(200, "GET", kb+"/passages?q=discounts+margin&audience=customers", nil)["data"].([]any); len(l) != 0 {
		t.Fatalf("team knowledge reached customers: %v", l)
	}
	if l := owner.must(200, "GET", kb+"/passages?q=discounts+margin", nil)["data"].([]any); len(l) != 1 {
		t.Fatalf("team passages = %v", l)
	}
	if l := owner.must(200, "GET", kb+"/passages?q=payouts&brand=geonode", nil)["data"].([]any); len(l) != 0 {
		t.Fatalf("another brand's knowledge: %v", l)
	}
	if l := owner.must(200, "GET", kb+"/passages?q=payouts&brand=repocket", nil)["data"].([]any); len(l) != 1 {
		t.Fatalf("brand knowledge = %v", l)
	}
	owner.must(400, "GET", kb+"/passages?q=", nil)
	owner.must(400, "GET", kb+"/passages?q=x&audience=everyone", nil)

	// Editing the text rebuilds the passages.
	owner.must(200, "PATCH", kb+"/items/"+refunds["id"].(string), map[string]any{"body": "## Refunds\n\nWe refund any plan within 14 days."})
	if l := owner.must(200, "GET", kb+"/passages?q=settings+billing", nil)["data"].([]any); len(l) != 0 {
		t.Fatalf("old passages remain: %v", l)
	}

	// Asking without an AI model still finds the passages, and says why
	// there is no answer.
	a := owner.must(200, "POST", kb+"/ask", map[string]any{"question": "Do you refund annual plans?"})
	ai := a["ai"].(map[string]any)
	if a["answer"] != nil || ai["ready"] != false || ai["problem"] == nil || len(a["passages"].([]any)) == 0 || a["answered"] != false {
		t.Fatalf("ask without AI = %v", a)
	}

	// With the workspace's own Claude, the answer cites its passages.
	cl := httptest.NewServer(fakeClaude{})
	defer cl.Close()
	e.integrations.Client.BaseURLs["claude"] = cl.URL
	owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "claude", "credentials": map[string]any{"api_key": "sk-ant-good"}})
	a = owner.must(200, "POST", kb+"/ask", map[string]any{"question": "Do you refund annual plans?", "audience": "customers"})
	cites := a["citations"].([]any)
	if a["answered"] != true || !strings.HasPrefix(a["answer"].(string), "Yes") || len(cites) != 1 || cites[0].(map[string]any)["item_id"] != refunds["id"] {
		t.Fatalf("answer = %v", a)
	}
	// Team-only knowledge never reaches a customer's answer (the fake fails
	// the call if it does).
	a = owner.must(200, "POST", kb+"/ask", map[string]any{"question": "What is our internal margin?", "audience": "customers"})
	if a["answered"] != false || len(a["passages"].([]any)) != 0 {
		t.Fatalf("customer margin = %v", a)
	}
	a = owner.must(200, "POST", kb+"/ask", map[string]any{"question": "What is our internal margin?"})
	if a["answered"] != true {
		t.Fatalf("team margin = %v", a)
	}
	// Something nobody wrote down is a gap.
	a = owner.must(200, "POST", kb+"/ask", map[string]any{"question": "Do you have an office in Lisbon?"})
	if a["answered"] != false || a["answer"] == nil {
		t.Fatalf("gap = %v", a)
	}
	var questions, unanswered, uses int
	if err := e.pool.QueryRow(t.Context(), `SELECT count(*), count(*) FILTER (WHERE NOT answered), (SELECT count(*) FROM knowledge_uses WHERE workspace_id = $1)
		FROM knowledge_questions WHERE workspace_id = $1`, ws).Scan(&questions, &unanswered, &uses); err != nil || questions != 5 || unanswered != 2 || uses != 2 {
		t.Fatalf("kept %d questions, %d unanswered, %d uses (%v)", questions, unanswered, uses, err)
	}

	// The gaps: unanswered questions, each once, until set aside.
	owner.must(200, "POST", kb+"/ask", map[string]any{"question": "do you have an office in lisbon? "})
	gaps := owner.must(200, "GET", kb+"/gaps", nil)["data"].([]any)
	if len(gaps) != 2 || gaps[0].(map[string]any)["asked"] != 2.0 {
		t.Fatalf("gaps = %v", gaps)
	}
	st := owner.must(200, "GET", kb+"/stats", nil)
	if st["gaps"] != 2.0 || st["live"] != 4.0 || st["drafts"] != 1.0 || st["customers_ai"] != 2.0 || st["help_centre"] != 1.0 || st["questions_30d"] != 6.0 {
		t.Fatalf("stats = %v", st)
	}
	if d := owner.must(200, "POST", kb+"/gaps/dismiss", map[string]any{"question": "Do you have an office in Lisbon?", "reason": "answered"}); d["dismissed"] != 2.0 {
		t.Fatalf("dismissed = %v", d)
	}
	if gaps := owner.must(200, "GET", kb+"/gaps", nil)["data"].([]any); len(gaps) != 1 {
		t.Fatalf("gaps after dismiss = %v", gaps)
	}
	owner.must(400, "POST", kb+"/gaps/dismiss", map[string]any{"question": "x", "reason": "maybe"})
	// Items say how often answers used them.
	if it := owner.must(200, "GET", kb+"/items/"+refunds["id"].(string), nil); it["uses_30d"] != 1.0 {
		t.Fatalf("uses = %v", it["uses_30d"])
	}

	// Agents learn what there is from get_context.
	c := owner.must(200, "GET", base+"/context", nil)
	k := c["knowledge"].(map[string]any)
	if k["articles"] != 3.0 || k["snippets"] != 1.0 || !strings.Contains(c["markdown"].(string), "## Knowledge") {
		t.Fatalf("context knowledge = %v", k)
	}

	seen := map[string]bool{}
	for _, ev := range got() {
		seen[ev.Name] = true
	}
	if !seen["knowledge.asked"] || !seen["knowledge_gap.dismissed"] {
		t.Fatal("no knowledge.asked or knowledge_gap.dismissed event")
	}
}

func TestKnowledgeByMeaning(t *testing.T) {
	e := freshEnv(t)
	ctx := t.Context()
	var ok bool
	if err := e.pool.QueryRow(ctx, `SELECT semantic_search_setup()`).Scan(&ok); err != nil || !ok {
		t.Skip("this database has no pgvector:", err)
	}
	var mu sync.Mutex
	calls := 0
	var dims []any
	oa := httptest.NewServer(fakeEmbeddings{&mu, &calls, &dims})
	defer oa.Close()
	e.integrations.Client.BaseURLs["openai"] = oa.URL
	e.runServerJobs(t)
	owner, ws := setupOwner(t, e.h)
	kb := "/v1/workspaces/" + ws + "/knowledge"
	owner.must(201, "POST", kb+"/items", map[string]any{"title": "Plans", "body": "## Pricing\n\nThe Pro plan costs $49 a month.", "visibility": "public"})
	owner.must(201, "POST", kb+"/items", map[string]any{"title": "Developers", "body": "Connect with our REST API and webhooks.", "visibility": "team"})
	owner.must(201, "POST", "/v1/workspaces/"+ws+"/integrations", map[string]any{"connector": "openai", "credentials": map[string]any{"api_key": openAIKey}})

	// Words alone don't connect "expensive" with a passage about pricing.
	if l := owner.must(200, "GET", kb+"/passages?q=is+it+expensive", nil)["data"].([]any); len(l) != 0 {
		t.Fatalf("words = %v", l)
	}
	if _, err := e.srv.QueueSemanticEmbeds(ctx); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, func() bool {
		var n int
		e.pool.QueryRow(ctx, `SELECT count(*) FROM search_embeddings WHERE workspace_id = $1 AND kind = 'knowledge'`, ws).Scan(&n)
		return n == 2
	})
	p := owner.must(200, "GET", kb+"/passages?q=is+it+expensive&audience=customers", nil)
	l := p["data"].([]any)
	if p["meaning"] != true || len(l) == 0 || l[0].(map[string]any)["heading"] != "Pricing" || l[0].(map[string]any)["match"] != "meaning" {
		t.Fatalf("meaning = %v", p)
	}
	for _, x := range l {
		if x.(map[string]any)["visibility"] == "team" {
			t.Fatalf("team knowledge reached customers by meaning: %v", l)
		}
	}
	// A deleted item's vectors go on the next run.
	id := l[0].(map[string]any)["item_id"].(string)
	owner.must(204, "DELETE", kb+"/items/"+id, nil)
	if _, err := e.srv.QueueSemanticEmbeds(ctx); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, func() bool {
		var n int
		e.pool.QueryRow(ctx, `SELECT count(*) FROM search_embeddings WHERE workspace_id = $1 AND kind = 'knowledge'`, ws).Scan(&n)
		return n == 1
	})
}
