package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/jobs"
)

func TestSalesDraftAssistanceOwnershipTemplatesAndRecovery(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	p := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Example Buyer", "identities": []map[string]string{{"namespace": "email", "value": "buyer@customer.example"}}})
	brand := p["brand_id"].(string)
	root := "/v1/brands/" + brand + "/sales"
	rep, rid := joinAs(t, owner, ws, "rep@northpeak.example", map[string]any{"role": "operator"})
	other, oid := joinAs(t, owner, ws, "other@northpeak.example", map[string]any{"role": "operator"})
	for id, role := range map[string]string{rid: "sdr", oid: "ae"} {
		owner.must(200, "PUT", root+"/team/"+id, map[string]any{"role": role})
	}
	integration := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "emailbison", "credentials": map[string]string{"base_url": "https://send.acme.test", "api_key": "eb-replies"}})["id"].(string)
	lead := owner.must(201, "POST", root+"/bridge", map[string]any{"source": "test", "source_id": "draft-assist", "record_id": p["id"], "integration_id": integration, "sender_id": "1", "reply_id": "501", "messages": []map[string]any{{"direction": "incoming", "from": "buyer@customer.example", "to": []string{"rep@northpeak.example"}, "cc": []string{}, "subject": "Example", "body": "Please explain the reporting options.", "message_id": "<draft@customer.example>", "provider_id": "501", "occurred_at": time.Now().UTC().Format(time.RFC3339)}}})
	path := root + "/leads/" + lead["id"].(string)
	lead = rep.must(200, "POST", path+"/actions", map[string]any{"action": "assign", "version": lead["version"], "assignee_id": rid})
	var counter int
	request := func(rev any) map[string]any {
		counter++
		return map[string]any{"request_id": fmt.Sprintf("00000000-0000-4000-8000-%012d", counter), "revision": rev, "instructions": "Keep it concise."}
	}
	rep.must(409, "POST", path+"/draft-assist", request(0)) // no workspace AI key
	var calls atomic.Int32
	var truncated atomic.Bool
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") != "Bearer sk-test-not-a-real-key" {
			t.Error("wrong workspace key")
			w.WriteHeader(401)
			return
		}
		if r.Method == "GET" {
			io.WriteString(w, `{"data":[{"id":"gpt-test"}]}`)
			return
		}
		input, _ := io.ReadAll(r.Body)
		calls.Add(1)
		for _, want := range []string{"untrusted source material", "Keep it concise.", "reporting options", "Template:"} {
			if !strings.Contains(string(input), want) {
				t.Errorf("missing prompt context %q", want)
			}
		}
		reason := "stop"
		if truncated.Load() {
			reason = "length"
		}
		json.NewEncoder(w).Encode(map[string]any{"model": "gpt-test", "choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": `{"body":"Thanks for your interest. Which reporting options matter most to your team?"}`}, "finish_reason": reason}}, "usage": map[string]any{"prompt_tokens": 500, "completion_tokens": 80}})
	}))
	defer fake.Close()
	e.integrations.Client.BaseURLs["openai"] = fake.URL
	conn := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "openai", "credentials": map[string]string{"api_key": "sk-test-not-a-real-key"}})
	owner.must(200, "PUT", base+"/ai-settings", map[string]any{"integration_id": conn["id"], "model": "gpt-test", "input_cents_per_million": 250, "output_cents_per_million": 1000, "monthly_limit_cents": 5000})
	template := rep.must(201, "POST", root+"/templates", map[string]any{"name": "Follow up", "body": "Hello {{first_name}}, thanks for asking about reporting options."})
	draft := rep.must(200, "PUT", path+"/draft", map[string]any{"revision": 0, "body": "", "template_id": template["id"]})
	input := request(draft["revision"])
	preview := rep.must(202, "POST", path+"/draft-assist", input)
	if repeated := rep.must(202, "POST", path+"/draft-assist", input); repeated["id"] != preview["id"] {
		t.Fatal("retry made another preview")
	}
	rep.must(409, "POST", path+"/draft-assist", request(draft["revision"]))
	other.must(404, "GET", path+"/draft-assist/"+preview["id"].(string), nil)
	owner.must(404, "POST", path+"/draft-assist", request(0))
	q := jobs.New(e.pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
	q.PollInterval = 10 * time.Millisecond
	q.Only = []string{"sales.draft_assist"}
	e.srv.RegisterJobs(q)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = q.Run(ctx, 1) }()
	defer func() { cancel(); <-done }()
	wait := func(c *client, id string, want string) map[string]any {
		t.Helper()
		deadline := time.Now().Add(30 * time.Second)
		for {
			v := c.must(200, "GET", path+"/draft-assist/"+id, nil)
			if v["status"] == want {
				return v
			}
			if time.Now().After(deadline) {
				t.Fatal("preview did not finish", v)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	preview = wait(rep, preview["id"].(string), "ready")
	saved := rep.must(200, "GET", path, nil)["draft"].(map[string]any)
	if saved["body"] != draft["body"] || calls.Load() != 1 {
		t.Fatal("generation changed the saved draft or repeated provider call", calls.Load())
	}
	draft = rep.must(200, "PUT", path+"/draft", map[string]any{"revision": draft["revision"], "body": preview["body"], "assist_id": preview["id"]})
	if draft["template_version_id"] != saved["template_version_id"] {
		t.Fatal("AI lost template provenance")
	}
	// An edit after generation prevents the old preview from replacing it.
	stale := rep.must(202, "POST", path+"/draft-assist", request(draft["revision"]))
	stale = wait(rep, stale["id"].(string), "ready")
	edited := rep.must(200, "PUT", path+"/draft", map[string]any{"revision": draft["revision"], "body": "My newer wording"})
	rep.must(409, "PUT", path+"/draft", map[string]any{"revision": stale["base_revision"], "body": stale["body"], "assist_id": stale["id"]})
	// Truncation is a failure even when the provider returns syntactically valid JSON.
	truncated.Store(true)
	bad := rep.must(202, "POST", path+"/draft-assist", request(edited["revision"]))
	wait(rep, bad["id"].(string), "failed")
	truncated.Store(false)
	cancel()
	<-done
	// Queued requests lose authority when the lead is reassigned.
	revoked := rep.must(202, "POST", path+"/draft-assist", request(edited["revision"]))
	lead = rep.must(200, "POST", path+"/actions", map[string]any{"action": "assign", "version": lead["version"], "assignee_id": oid})
	rep.must(404, "GET", path+"/draft-assist/"+revoked["id"].(string), nil)
	countBefore := calls.Load()
	ctx2, cancel2 := context.WithCancel(context.Background())
	done2 := make(chan struct{})
	go func() { defer close(done2); _ = q.Run(ctx2, 1) }()
	defer func() { cancel2(); <-done2 }()
	deadline := time.Now().Add(30 * time.Second)
	for {
		var status string
		e.pool.QueryRow(context.Background(), `SELECT status FROM sales_draft_assists WHERE id=$1`, revoked["id"]).Scan(&status)
		if status == "failed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("revoked request did not fail")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if calls.Load() != countBefore {
		t.Fatal("revoked rep called provider")
	}
	// AE has a separate personal draft and can use the same preview flow.
	ae := other.must(202, "POST", path+"/draft-assist", request(0))
	wait(other, ae["id"].(string), "ready")
	cancel2()
	<-done2
	// Recovery does not depend on a crashed worker coming back.
	abandoned := other.must(202, "POST", path+"/draft-assist", request(0))
	if _, err := e.pool.Exec(context.Background(), `UPDATE sales_draft_assists SET status='running',updated_at=now()-interval '3 minutes' WHERE id=$1`, abandoned["id"]); err != nil {
		t.Fatal(err)
	}
	recovered := other.must(202, "POST", path+"/draft-assist", request(0))
	if recovered["id"] == abandoned["id"] {
		t.Fatal("expired preview reused")
	}
	var sends int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM jobs WHERE workspace_id=$1 AND kind='sales.send'`, ws).Scan(&sends); err != nil || sends != 0 {
		t.Fatal("AI preview sent email", sends, err)
	}
	otherBrand := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Other", "handle": "other-ai-preview"})["id"].(string)
	other.must(404, "GET", "/v1/brands/"+otherBrand+"/sales/leads/"+lead["id"].(string)+"/draft-assist/"+ae["id"].(string), nil)
}
