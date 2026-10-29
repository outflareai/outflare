package api_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/journeys"
)

// answerJourneyAI answers fakeClaude's journey ai steps: hot for a
// classify question about Ana, cold for anyone else, and a short note
// for a written answer.
func answerJourneyAI(w http.ResponseWriter, body string) {
	if strings.Contains(body, "Answer with exactly one of") {
		answer := "cold"
		if strings.Contains(body, "Ana") {
			answer = "hot"
		}
		answerJSON(w, map[string]any{"answer": answer})
		return
	}
	json.NewEncoder(w).Encode(map[string]any{
		"id": "msg_j", "type": "message", "role": "assistant", "model": "claude-haiku-5-5",
		"content":     []any{map[string]any{"type": "text", "text": "Ana runs ops at Acme and asked about pricing."}},
		"stop_reason": "end_turn", "usage": map[string]any{"input_tokens": 300, "output_tokens": 20},
	})
}

// An ai step asks the workspace's own model, keeps the answer in the
// run's vars, and a branch acts on it.
func TestJourneyAI(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	// Bad ai steps are refused.
	for _, st := range []map[string]any{
		{"id": "a", "kind": "ai", "save_as": "x"},
		{"id": "a", "kind": "ai", "prompt": "Fit?"},
		{"id": "a", "kind": "ai", "prompt": "Fit?", "save_as": "x", "choices": []string{"only"}},
		{"id": "a", "kind": "ai", "prompt": "Fit?", "save_as": "x", "choices": []string{"hot", "cold"}, "max_words": 50},
	} {
		owner.must(400, "POST", base+"/journeys", map[string]any{"name": "Bad", "steps": []map[string]any{st}})
	}
	jid := owner.must(201, "POST", base+"/journeys", map[string]any{"name": "Qualify", "steps": []map[string]any{
		{"id": "fit", "kind": "ai", "prompt": "Is {{person.first_name}} at {{person.company}} a good fit?", "choices": []string{"hot", "cold"}, "save_as": "fit", "next": "check"},
		{"id": "check", "kind": "branch", "conditions": []map[string]any{{"var": "fit.answer", "op": "eq", "value": "hot"}}, "then": "note"},
		{"id": "note", "kind": "ai", "prompt": "Write a one-line note for the sales rep about {{person.first_name}}.", "save_as": "note"},
	}})["id"].(string)
	owner.must(200, "POST", base+"/journeys/"+jid+"/activate", nil)

	q := jobs.New(e.pool, log)
	q.PollInterval = 20 * time.Millisecond
	(&journeys.Service{Pool: e.pool, Log: log, Fields: e.srv, AI: e.srv.JourneyAI()}).Register(q)
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { q.Run(runCtx, 2); close(done) }() //nolint:errcheck
	defer func() { cancel(); <-done }()
	finished := func(addr, first string) map[string]any {
		t.Helper()
		rec, err := customers.EnsurePerson(ctx, e.pool, ws, "email", addr, true, "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.pool.Exec(ctx, `UPDATE records SET attributes = $2 WHERE id = $1`, rec, `{"first_name": "`+first+`", "company": "Acme"}`); err != nil {
			t.Fatal(err)
		}
		run := owner.must(201, "POST", base+"/journeys/"+jid+"/runs", map[string]any{"record_id": rec})["id"].(string)
		deadline := time.Now().Add(60 * time.Second)
		for {
			r := owner.must(200, "GET", base+"/journeys/"+jid+"/runs/"+run, nil)
			if r["status"] != "active" {
				return r
			}
			if time.Now().After(deadline) {
				t.Fatalf("run = %v", r)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}

	// Without an AI model connected, the step fails and says what to do.
	r := finished("bo@example.org", "Bo")
	hist := r["history"].([]any)
	if h := hist[len(hist)-1].(map[string]any); r["status"] != "failed" || h["outcome"] != "failed" || !strings.Contains(h["detail"].(string), "Connect an AI model") {
		t.Fatalf("no model: %v", r)
	}

	owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "claude", "credentials": map[string]any{"api_key": "sk-ant-good"}})
	// Ana is hot: the note is written and kept.
	r = finished("ana@example.org", "Ana")
	vars := r["vars"].(map[string]any)
	if r["status"] != "completed" || vars["fit"].(map[string]any)["answer"] != "hot" ||
		!strings.Contains(vars["note"].(map[string]any)["answer"].(string), "pricing") {
		t.Fatalf("ana = %v", r)
	}
	// Cy is cold: the branch ends the run before the note.
	r = finished("cy@example.org", "Cy")
	if vars := r["vars"].(map[string]any); r["status"] != "completed" || vars["fit"].(map[string]any)["answer"] != "cold" || vars["note"] != nil {
		t.Fatalf("cy = %v", r)
	}
	// Each answer is in the AI usage log, under journey AI steps.
	var calls int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM ai_calls WHERE workspace_id = $1 AND task = 'journeys'`, ws).Scan(&calls); err != nil || calls != 3 {
		t.Fatalf("ai_calls = %d, %v", calls, err)
	}
}
