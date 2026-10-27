package cheapinboxes

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

func TestPush(t *testing.T) {
	var mu sync.Mutex
	var creates, links, syncs int
	var batch []int
	var created map[string]any
	busy := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		var body map[string]any
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		switch {
		case r.Method == "POST" && r.URL.Path == "/v1/integrations":
			creates++
			if busy {
				w.Header().Set("X-RateLimit-Reset", "1")
				w.WriteHeader(429)
				return
			}
			created = body
			io.WriteString(w, `{"integration":{"id":"int_9","provider":"emailbison"}}`)
		case r.URL.Path == "/v1/integrations/int_9/link-mailboxes":
			links++
			batch = append(batch, len(body["mailbox_ids"].([]any)))
			io.WriteString(w, `{"linked":1}`)
		case r.URL.Path == "/v1/integrations/int_9/sync":
			syncs++
			n := len(body["mailbox_ids"].([]any))
			io.WriteString(w, `{"synced":`+jsonInt(n-1)+`,"failed":1,"skipped":0}`)
		case r.Method == "DELETE" && r.URL.Path == "/v1/integrations/int_9":
			w.WriteHeader(404)
			io.WriteString(w, `{"error":{"code":"NOT_FOUND","message":"Integration not found"}}`)
		default:
			w.WriteHeader(500)
		}
	}))
	defer srv.Close()
	cl := &c.Client{BaseURLs: map[string]string{ID: srv.URL}}
	cr := c.Creds{"api_key": "ci_live_test"}
	ctx := context.Background()
	target := c.PushTarget{Name: "Outflare", BaseURL: "https://app.example.com/emailbison/ws1", APIKey: "oft_x", LoginEmail: "a@b.test", LoginPassword: "pw"}

	// Adding the connection is never retried, even on a rate limit.
	if _, err := (Connector{}).ConnectPush(ctx, cl, cr, target); err == nil || creates != 1 {
		t.Fatalf("busy create: %v, %d calls", err, creates)
	}
	busy = false
	id, err := Connector{}.ConnectPush(ctx, cl, cr, target)
	if err != nil || id != "int_9" {
		t.Fatalf("create: %q %v", id, err)
	}
	login := created["sequencer_login"].(map[string]any)
	if created["provider"] != "emailbison" || created["base_url"] != target.BaseURL || created["api_key"] != "oft_x" || login["email"] != "a@b.test" {
		t.Fatalf("created: %v", created)
	}

	// 800 mailboxes go in batches of 399.
	ids := make([]string, 800)
	for i := range ids {
		ids[i] = "mb"
	}
	res, err := Connector{}.PushMailboxes(ctx, cl, cr, "int_9", ids)
	if err != nil || links != 3 || syncs != 3 || batch[0] != 399 || batch[2] != 2 {
		t.Fatalf("push: %v %d %d %v", err, links, syncs, batch)
	}
	if res.Synced != 797 || res.Failed != 3 {
		t.Fatalf("counts: %+v", res)
	}

	// Already gone there is fine.
	if err := (Connector{}).DisconnectPush(ctx, cl, cr, "int_9"); err != nil {
		t.Fatalf("disconnect: %v", err)
	}
}

func jsonInt(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}
