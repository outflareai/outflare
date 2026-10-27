package api_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/track"
)

// fakeEmbeddings answers OpenAI's embeddings API with vectors that put
// texts about price in one direction and texts about an API in another,
// so meaning-based search has something to find. Its vectors are longer
// than Outflare stores, as text-embedding-3-large's are.
type fakeEmbeddings struct {
	mu    *sync.Mutex
	calls *int
	dims  *[]any
}

func (f fakeEmbeddings) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Header.Get("Authorization") != "Bearer "+openAIKey {
		w.WriteHeader(401)
		io.WriteString(w, `{"error":{"message":"Incorrect API key"}}`)
		return
	}
	if r.Method == "GET" && r.URL.Path == "/models" {
		io.WriteString(w, `{"data":[{"id":"gpt-test"}]}`)
		return
	}
	var req struct {
		Model      string   `json:"model"`
		Input      []string `json:"input"`
		Dimensions any      `json:"dimensions"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	if r.URL.Path != "/embeddings" || req.Model != "text-embedding-3-small" {
		w.WriteHeader(400)
		io.WriteString(w, `{"error":{"message":"unexpected call"}}`)
		return
	}
	f.mu.Lock()
	*f.calls++
	*f.dims = append(*f.dims, req.Dimensions)
	f.mu.Unlock()
	data := []any{}
	for i, text := range req.Input {
		v := make([]float64, 1536)
		v[3] = 0.2
		low := strings.ToLower(text)
		for _, w := range []string{"price", "pricing", "expensive", "budget", "cost"} {
			if strings.Contains(low, w) {
				v[0] = 1
			}
		}
		for _, w := range []string{"api", "integrat"} {
			if strings.Contains(low, w) {
				v[1] = 1
			}
		}
		data = append(data, map[string]any{"index": i, "embedding": v})
	}
	json.NewEncoder(w).Encode(map[string]any{"model": req.Model, "data": data, "usage": map[string]any{"prompt_tokens": 40 * len(req.Input)}})
}

func TestSemanticSearch(t *testing.T) {
	e := freshEnv(t)
	ctx := context.Background()
	var ok bool
	if err := e.pool.QueryRow(ctx, `SELECT semantic_search_setup()`).Scan(&ok); err != nil || !ok {
		t.Skip("this database has no pgvector:", err)
	}
	got := track.Capture(t)
	var mu sync.Mutex
	calls := 0
	var dims []any
	oa := httptest.NewServer(fakeEmbeddings{&mu, &calls, &dims})
	defer oa.Close()
	e.integrations.Client.BaseURLs["openai"] = oa.URL
	e.runServerJobs(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws

	rp := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Repocket", "website": "repocket.com"})["id"].(string)
	gn := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Geonode", "website": "geonode.com"})["id"].(string)
	ada := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Ada Lovelace", "brand_id": rp})["id"].(string)
	grace := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Grace Hopper", "brand_id": gn})["id"].(string)
	var mbRP, mbGN string
	for _, m := range []struct {
		email, brand string
		id           *string
	}{{"sam@repocket.example", rp, &mbRP}, {"sam@geonode.example", gn, &mbGN}} {
		if err := e.pool.QueryRow(ctx, `INSERT INTO mailboxes (workspace_id, brand_id, email, provider, smtp_host, smtp_port, smtp_username, imap_host, imap_port, imap_username, credentials, status)
			VALUES ($1, $2, $3, 'google', 'smtp.gmail.com', 587, $3, 'imap.gmail.com', 993, $3, '\x00', 'active') RETURNING id::text`, ws, m.brand, m.email).Scan(m.id); err != nil {
			t.Fatal(err)
		}
	}
	reply := func(mb, record, body string) string {
		var id string
		if err := e.pool.QueryRow(ctx, `INSERT INTO inbound_messages (workspace_id, mailbox_id, uidvalidity, uid, from_email, subject, body, record_id, received_at, kind)
			VALUES ($1, $2, 0, nextval('inbound_external_uid_seq'), 'someone@northpeak.example', 'Re: your note', $3, $4, now(), 'reply') RETURNING id::text`, ws, mb, body, record).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	tooMuch := reply(mbRP, ada, "Honestly this is too expensive for our budget right now.")
	reply(mbRP, ada, "Do you have something we could integrate with our stack?")
	reply(mbGN, grace, "We would need to know what it costs per seat.")

	// Another workspace's replies are never found.
	other := owner.must(201, "POST", "/v1/workspaces", map[string]string{"name": "Other"})["id"].(string)

	// Before an AI connection that makes embeddings, it is on but waits.
	st := owner.must(200, "GET", base+"/semantic-search", nil)
	if st["enabled"] != true || st["available"] != true || st["problem"].(map[string]any)["code"] != "connector_needed" {
		t.Fatalf("before a connection: %v", st)
	}
	owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "openai", "credentials": map[string]any{"api_key": openAIKey}})
	owner.must(201, "POST", "/v1/workspaces/"+other+"/integrations", map[string]any{"connector": "openai", "credentials": map[string]any{"api_key": openAIKey}})
	st = owner.must(200, "GET", base+"/semantic-search", nil)
	if st["provider"] != "openai" || st["model"] != "text-embedding-3-small" || st["waiting"].(float64) != 5 || st["problem"] != nil {
		t.Fatalf("with a connection: %v", st)
	}

	// Words alone don't find "too expensive" for "unhappy with price".
	if hits := owner.must(200, "GET", base+"/search?q=unhappy+with+price", nil)["data"].([]any); len(hits) != 0 {
		t.Fatalf("words: %v", hits)
	}
	// The scheduler queues a job per workspace, and the job embeds all.
	if n, err := e.srv.QueueSemanticEmbeds(ctx); err != nil || n != 2 {
		t.Fatalf("queued %d: %v", n, err)
	}
	waitUntil(t, func() bool {
		st = owner.must(200, "GET", base+"/semantic-search", nil)
		return st["waiting"].(float64) == 0 && st["embedded"].(float64) == 5
	})
	mu.Lock()
	if dims[0] != float64(1024) {
		t.Errorf("dimensions asked for: %v", dims)
	}
	mu.Unlock()

	hits := owner.must(200, "GET", base+"/search?q=unhappy+with+price&mode=meaning&kinds=message", nil)["data"].([]any)
	if len(hits) != 3 {
		t.Fatalf("meaning: %v", hits)
	}
	first := hits[0].(map[string]any)
	if first["kind"] != "message" || first["record_name"] == nil || first["snippet"] == "" || first["rank"].(float64) < hits[2].(map[string]any)["rank"].(float64) {
		t.Fatalf("best by meaning: %v", hits)
	}
	if id := first["id"]; id != tooMuch && hits[1].(map[string]any)["id"] != tooMuch {
		t.Fatalf("too expensive is not near the top: %v", hits)
	}

	// Brands and scopes, as for word search.
	tok := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "Repocket inbox", "scopes": []string{"inbox.read"}, "brand": "repocket"})
	rpc := &client{t: t, h: e.h, bearer: tok["token"].(string)}
	hits = rpc.must(200, "GET", base+"/search?q=price&mode=meaning", nil)["data"].([]any)
	if len(hits) != 2 {
		t.Fatalf("one brand: %v", hits)
	}
	for _, h := range hits {
		if h.(map[string]any)["record_name"] != nil {
			t.Fatalf("a record name without customers.read: %v", h)
		}
	}
	if code, _ := rpc.do("GET", base+"/search?q=price&mode=meaning&kinds=record", nil); code != 403 {
		t.Fatalf("records without customers.read: %d", code)
	}
	owner.must(400, "GET", base+"/search?q=price&mode=meaning&kinds=email", nil)
	owner.must(400, "GET", base+"/search?q=price&mode=nope", nil)

	// Embeddings are a tool of their own in AI usage.
	var tools int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM ai_calls WHERE workspace_id = $1 AND task = 'semantic_search' AND provider = 'openai'`, ws).Scan(&tools); err != nil || tools < 2 {
		t.Fatalf("ai_calls: %d %v", tools, err)
	}

	// An archived record's vector goes on the next run; an unchanged one
	// isn't embedded again.
	if _, err := e.pool.Exec(ctx, `UPDATE records SET archived_at = now() WHERE id = $1`, grace); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	before := calls
	mu.Unlock()
	if n, err := e.srv.QueueSemanticEmbeds(ctx); err != nil || n != 2 {
		t.Fatalf("queued again %d: %v", n, err)
	}
	waitUntil(t, func() bool {
		return owner.must(200, "GET", base+"/semantic-search", nil)["embedded"].(float64) == 4
	})
	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	if calls != before {
		t.Errorf("unchanged items embedded again: %d calls, was %d", calls, before)
	}
	mu.Unlock()

	// Turning it off deletes the workspace's vectors and stops search.
	if out := owner.must(200, "PUT", base+"/semantic-search", map[string]any{"enabled": false}); out["enabled"] != false || out["embedded"].(float64) != 0 {
		t.Fatalf("off: %v", out)
	}
	if code, out := owner.do("GET", base+"/search?q=price&mode=meaning", nil); code != 409 || queryCode(out) != "semantic_search_off" {
		t.Fatalf("search when off: %d %v", code, out)
	}
	var left int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM search_embeddings WHERE workspace_id = $1`, other).Scan(&left); err != nil || left != 0 {
		t.Fatalf("the other workspace has no records, so no vectors: %d %v", left, err)
	}
	if n, err := e.srv.QueueSemanticEmbeds(ctx); err != nil || n != 1 {
		t.Fatalf("queued when off %d: %v", n, err)
	}
	owner.must(400, "PUT", base+"/semantic-search", map[string]any{})
	owner.must(200, "PUT", base+"/semantic-search", map[string]any{"enabled": true})

	names := map[string]int{}
	for _, ev := range got() {
		names[ev.Name]++
		if ev.Name == "search.ran" {
			if _, ok := ev.Props["q"]; ok {
				t.Fatalf("the search terms in an event: %v", ev)
			}
		}
	}
	if names["search.embedded"] == 0 || names["semantic_search.updated"] != 2 || names["search.ran"] == 0 {
		t.Fatalf("events: %v", names)
	}
}
