package api_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/signals"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Conversation signals: off until a brand switches them on, read on the
// workspace's own AI, raised only when the model is sure, never read
// twice without something new, flagged by hand from a conversation, and
// credited to whoever raised them when they turn into revenue.
func TestConversationSignals(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	var brand string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand
	count := func(sql string, args ...any) int {
		t.Helper()
		var n int
		if err := e.pool.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return n
	}
	acme := owner.must(201, "POST", base+"/records", map[string]any{"type": "account", "name": "Acme"})["id"].(string)
	me := owner.must(200, "GET", "/v1/me", nil)["user"].(map[string]any)["id"].(string)

	// Off to start with, and a transcript is refused while it is.
	if got := owner.must(200, "GET", base+"/conversation-signals", nil); got["enabled"] != false || got["ai_connected"] != false || len(got["channels"].([]any)) != len(signals.Channels) {
		t.Fatalf("defaults: %v", got)
	}
	call := map[string]any{"title": "Quarterly review", "text": "Ana: we are doubling the team next quarter and will need about twice the volume. We also looked at Globex."}
	owner.must(409, "POST", base+"/records/"+acme+"/transcripts", call)
	owner.must(400, "PUT", base+"/conversation-signals", map[string]any{"enabled": true, "channels": []string{"fax"}})
	captured := track.Capture(t)
	has := func(name string) bool {
		for _, ev := range captured() {
			if ev.Name == name {
				return true
			}
		}
		return false
	}
	if got := owner.must(200, "PUT", base+"/conversation-signals", map[string]any{"enabled": true}); got["enabled"] != true {
		t.Fatalf("on: %v", got)
	}
	if !has("conversation_signals.updated") {
		t.Fatal("no conversation_signals.updated event")
	}
	owner.must(400, "POST", base+"/records/"+acme+"/transcripts", map[string]any{"text": " "})
	owner.must(404, "POST", base+"/records/00000000-0000-0000-0000-000000000000/transcripts", call)
	tr := owner.must(201, "POST", base+"/records/"+acme+"/transcripts", call)["id"].(string)
	if count(`SELECT count(*) FROM jobs WHERE kind = $1 AND payload->>'ref' = $2`, signals.JobConversation, tr) != 1 {
		t.Fatal("read not queued")
	}
	// The text is sealed.
	if count(`SELECT count(*) FROM conversation_transcripts WHERE id = $1 AND content_sealed IS NOT NULL AND position('doubling' in encode(content_sealed, 'escape')) = 0`, tr) != 1 {
		t.Fatal("transcript not sealed")
	}

	// Without the workspace's own AI nothing is read.
	if err := e.srv.RunConversationRead(ctx, ws, brand, "call", tr); err != nil {
		t.Fatal(err)
	}
	if count(`SELECT count(*) FROM signals WHERE record_id = $1`, acme) != 0 || !has("conversation.read_failed") {
		t.Fatal("read without AI")
	}

	// A fake model: sure of an upsell, unsure of a competitor.
	var prompts []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "GET" {
			io.WriteString(w, `{"data":[{"id":"gpt-test"}]}`)
			return
		}
		body, _ := io.ReadAll(r.Body)
		prompts = append(prompts, string(body))
		found := `{"sentiment":{"label":"positive","quote":"this has been great for us"},"signals":[{"type":"upsell","title":"Doubling the team","detail":"They expect twice the volume next quarter. Offer a bigger plan.","quote":"we are doubling the team next quarter","confidence":0.9},{"type":"competitor","title":"Looked at Globex","detail":"Mentioned in passing.","quote":"We also looked at Globex","confidence":0.3}]}`
		json.NewEncoder(w).Encode(map[string]any{"model": "gpt-test", "choices": []any{map[string]any{"message": map[string]any{"role": "assistant",
			"content": found}, "finish_reason": "stop"}}, "usage": map[string]any{"prompt_tokens": 400, "completion_tokens": 80}})
	}))
	t.Cleanup(srv.Close)
	e.integrations.Client.BaseURLs["openai"] = srv.URL
	conn := owner.must(201, "POST", "/v1/workspaces/"+ws+"/integrations", map[string]any{"connector": "openai", "credentials": map[string]any{"api_key": openAIKey}})
	owner.must(200, "PUT", "/v1/workspaces/"+ws+"/ai-settings", map[string]any{"integration_id": conn["id"], "model": "gpt-test",
		"input_cents_per_million": 10, "output_cents_per_million": 50, "monthly_limit_cents": 5000})
	if got := owner.must(200, "GET", base+"/conversation-signals", nil); got["ai_connected"] != true {
		t.Fatalf("ai: %v", got)
	}
	if err := e.srv.RunConversationRead(ctx, ws, brand, "call", tr); err != nil {
		t.Fatal(err)
	}
	list := owner.must(200, "GET", base+"/signals?record="+acme, nil)["data"].([]any)
	if len(list) != 1 {
		t.Fatalf("signals: %v", list)
	}
	g := list[0].(map[string]any)
	data := g["data"].(map[string]any)
	if g["type"] != "upsell" || g["source"] != "agent" || g["title"] != "Doubling the team" || data["channel"] != "call" || data["ref"] != tr ||
		data["quote"] != "we are doubling the team next quarter" {
		t.Fatalf("signal: %v", g)
	}
	// How they sounded is on the company, with their words and in its
	// history.
	if s, _ := owner.must(200, "GET", base+"/records/"+acme, nil)["attributes"].(map[string]any)["sentiment"].(map[string]any); s["label"] != "positive" || s["value"] != 85.0 ||
		s["trend"] != "flat" || s["conversations"] != 1.0 {
		t.Fatalf("sentiment: %v", s)
	}
	if count(`SELECT count(*) FROM conversation_sentiments WHERE record_id = $1 AND label = 'positive' AND quote = 'this has been great for us'`, acme) != 1 ||
		count(`SELECT count(*) FROM attribute_changes WHERE record_id = $1 AND field_key = 'sentiment.label' AND new_value = '"positive"' AND source = 'conversation'`, acme) != 1 {
		t.Fatal("sentiment rows")
	}
	if len(prompts) != 1 || !strings.Contains(prompts[0], "doubling the team") || !strings.Contains(prompts[0], "upsell (Upsell)") {
		t.Fatalf("prompt: %v", prompts)
	}
	// Read: the text is gone, the spend is booked, and it isn't read again.
	if count(`SELECT count(*) FROM conversation_transcripts WHERE id = $1 AND content_sealed IS NULL AND read_at IS NOT NULL`, tr) != 1 ||
		count(`SELECT count(*) FROM ai_calls WHERE workspace_id = $1 AND task = 'conversation_signals'`, ws) != 1 || !has("conversation.read") {
		t.Fatal("after the read")
	}
	if err := e.srv.RunConversationRead(ctx, ws, brand, "call", tr); err != nil || len(prompts) != 1 {
		t.Fatalf("read twice: %v %d", err, len(prompts))
	}
	if got := owner.must(200, "GET", base+"/conversation-signals", nil); got["read_30d"] != 1.0 || got["raised_30d"] != 1.0 {
		t.Fatalf("counts: %v", got)
	}

	// A support conversation with a person: read once the customer wrote,
	// and again only when they write more.
	ana := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Ana Silva",
		"identities": []any{map[string]any{"namespace": "email", "value": "ana@acme.example"}}})["id"].(string)
	var conv string
	if err := e.pool.QueryRow(ctx, `INSERT INTO support_conversations (workspace_id, brand_id, record_id, subject, contact_email, first_message_at, last_message_at)
		VALUES ($1, $2, $3, 'Limits', 'ana@acme.example', now(), now()) RETURNING id::text`, ws, brand, ana).Scan(&conv); err != nil {
		t.Fatal(err)
	}
	say := func(kind, body string) {
		t.Helper()
		if _, err := e.pool.Exec(ctx, `INSERT INTO support_messages (workspace_id, conversation_id, kind, record_id, body) VALUES ($1, $2, $3, $4, $5)`, ws, conv, kind, ana, body); err != nil {
			t.Fatal(err)
		}
	}
	say("out", "Hello, how can we help?")
	if err := e.srv.RunConversationRead(ctx, ws, brand, "support", conv); err != nil || len(prompts) != 1 {
		t.Fatalf("read with nothing from the customer: %v %d", err, len(prompts))
	}
	say("in", "We keep hitting the limit. we are doubling the team next quarter.")
	if err := e.srv.RunConversationRead(ctx, ws, brand, "support", conv); err != nil || len(prompts) != 2 {
		t.Fatalf("support read: %v %d", err, len(prompts))
	}
	if got := owner.must(200, "GET", base+"/signals?record="+ana, nil)["data"].([]any); len(got) != 1 || got[0].(map[string]any)["data"].(map[string]any)["channel"] != "support" {
		t.Fatalf("support signal: %v", got)
	}
	if err := e.srv.RunConversationRead(ctx, ws, brand, "support", conv); err != nil || len(prompts) != 2 {
		t.Fatalf("unchanged read: %v %d", err, len(prompts))
	}
	// A channel switched off isn't read.
	owner.must(200, "PUT", base+"/conversation-signals", map[string]any{"enabled": true, "channels": []string{"call"}})
	say("in", "Any news?")
	if err := e.srv.RunConversationRead(ctx, ws, brand, "support", conv); err != nil || len(prompts) != 2 {
		t.Fatalf("read with the channel off: %v %d", err, len(prompts))
	}

	// Flag for sales: by hand, from the conversation.
	owner.must(400, "POST", base+"/signals", map[string]any{"record_id": ana, "type": "churn_risk", "conversation": map[string]any{"channel": "fax", "ref": conv}})
	flag := owner.must(201, "POST", base+"/signals", map[string]any{"record_id": ana, "type": "churn_risk", "title": "Asked to cancel",
		"conversation": map[string]any{"channel": "support", "ref": conv}})
	if d := flag["data"].(map[string]any); flag["source"] != "manual" || d["channel"] != "support" || d["ref"] != conv {
		t.Fatalf("flag: %v", flag)
	}
	// Won, and the account paid afterwards: the credit is the flagger's.
	owner.must(200, "PATCH", base+"/signals/"+flag["id"].(string), map[string]any{"status": "done", "outcome": "won"})
	if _, err := e.pool.Exec(ctx, `INSERT INTO transactions (workspace_id, brand_id, record_id, kind, amount_minor, brand_amount_minor, source, source_id, occurred_at)
		VALUES ($1, $2, $3, 'payment', 12000, 12000, 'test', 'c1', now() + interval '1 minute')`, ws, brand, ana); err != nil {
		t.Fatal(err)
	}
	owner.must(400, "GET", base+"/signal-credit?days=1", nil)
	credit := owner.must(200, "GET", base+"/signal-credit", nil)
	people := credit["people"].([]any)
	if credit["credit_days"] != 90.0 || len(people) != 1 {
		t.Fatalf("credit: %v", credit)
	}
	if p := people[0].(map[string]any); p["user_id"] != me || p["flagged"] != 1.0 || p["confirmed"] != 1.0 || p["won"] != 1.0 || p["revenue_minor"] != 12000.0 {
		t.Fatalf("person: %v", p)
	}
	by := map[string]map[string]any{}
	for _, s := range credit["sources"].([]any) {
		m := s.(map[string]any)
		key := m["source"].(string)
		if m["ai"] == true {
			key += ":ai"
		}
		by[key] = m
	}
	if by["call:ai"]["raised"] != 1.0 || by["support:ai"]["raised"] != 1.0 || by["support"]["won"] != 1.0 || by["support"]["revenue_minor"] != 12000.0 {
		t.Fatalf("sources: %v", by)
	}
}
