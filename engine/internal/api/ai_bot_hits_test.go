package api_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/outflareai/outflare/engine/internal/analyticsync"
	"github.com/outflareai/outflare/engine/internal/track"
)

func TestAIBotHits(t *testing.T) {
	e := freshEnv(t)
	ctx := context.Background()
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	events := track.Capture(t)

	// Nothing connected: no hits and no connections, not an error.
	got := owner.must(200, "GET", base+"/ai-bot-hits", nil)
	if len(got["data"].([]any)) != 0 || len(got["connections"].([]any)) != 0 || got["days"].(float64) != 28 {
		t.Fatalf("empty: %v", got)
	}
	owner.must(400, "GET", base+"/ai-bot-hits?days=0", nil)

	acct, zone := strings.Repeat("a", 32), strings.Repeat("c", 32)
	var noZones atomic.Bool
	var queries atomic.Int32
	cf := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/accounts/"+acct+"/rum/site_info/list":
			json.NewEncoder(w).Encode(map[string]any{"result": []any{}})
		case r.URL.Path == "/zones":
			if noZones.Load() {
				w.WriteHeader(403)
				io.WriteString(w, `{"success":false,"errors":[{"code":10000,"message":"Authentication error"}]}`)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"result": []any{map[string]string{"id": zone, "name": "example.com"}}})
		case r.URL.Path == "/graphql":
			queries.Add(1)
			b, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(b), "httpRequestsAdaptiveGroups") || !strings.Contains(string(b), "%GPTBot%") {
				t.Errorf("query: %s", b)
			}
			json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"viewer": map[string]any{"zones": []any{map[string]any{"g": []any{
				map[string]any{"count": 5, "dimensions": map[string]string{"userAgent": "Mozilla/5.0 (compatible; GPTBot/1.2; +https://openai.com/gptbot)", "clientRequestHTTPHost": "www.example.com"}},
				map[string]any{"count": 2, "dimensions": map[string]string{"userAgent": "Mozilla/5.0 (compatible; OAI-SearchBot/1.0)", "clientRequestHTTPHost": "www.example.com"}},
				map[string]any{"count": 1, "dimensions": map[string]string{"userAgent": "Mozilla/5.0 ChatGPT-User/1.0", "clientRequestHTTPHost": "docs.example.com"}},
				map[string]any{"count": 9, "dimensions": map[string]string{"userAgent": "curl/8.0", "clientRequestHTTPHost": "www.example.com"}},
			}}}}}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(cf.Close)
	e.integrations.Client.BaseURLs["cloudflare_web_analytics"] = cf.URL

	i := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "cloudflare_web_analytics",
		"credentials": map[string]string{"api_token": "cf_test_not_a_real_token", "account_id": acct}})
	id := i["id"].(string)
	got = owner.must(200, "GET", base+"/ai-bot-hits", nil)
	if c := got["connections"].([]any); len(c) != 1 || c[0].(map[string]any)["status"] != "pending" {
		t.Fatalf("pending: %v", got)
	}

	syncer := &analyticsync.Syncer{Pool: e.pool, Store: e.integrations, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if _, err := syncer.SyncOnce(ctx, ws, id); err != nil {
		t.Fatal(err)
	}
	// The first run reads the last three complete days.
	if n := queries.Load(); n != 3 {
		t.Fatalf("queries: %d", n)
	}
	got = owner.must(200, "GET", base+"/ai-bot-hits", nil)
	data := got["data"].([]any)
	if len(data) != 2 {
		t.Fatalf("hosts: %v", data)
	}
	www := data[0].(map[string]any)
	if www["host"] != "www.example.com" || www["requests"].(float64) != 21 || www["training"].(float64) != 15 || www["search"].(float64) != 6 || len(www["days"].([]any)) != 3 {
		t.Fatalf("www: %v", www)
	}
	if b := www["bots"].([]any)[0].(map[string]any); b["token"] != "GPTBot" || b["vendor"] != "OpenAI" || b["purpose"] != "training" || b["requests"].(float64) != 15 {
		t.Fatalf("bots: %v", www["bots"])
	}
	if docs := data[1].(map[string]any); docs["user"].(float64) != 3 {
		t.Fatalf("docs: %v", docs)
	}
	if c := got["connections"].([]any)[0].(map[string]any); c["status"] != "ok" || c["last_run_at"] == nil {
		t.Fatalf("ok: %v", c)
	}
	if only := owner.must(200, "GET", base+"/ai-bot-hits?host=docs.example.com", nil)["data"].([]any); len(only) != 1 {
		t.Fatalf("host filter: %v", only)
	}
	if !hasEvent(events(), "ai_bot_logs.synced") {
		t.Fatal("no synced event")
	}

	// Within a day nothing is read again.
	if _, err := syncer.SyncOnce(ctx, ws, id); err != nil || queries.Load() != 3 {
		t.Fatalf("again: %v %d", err, queries.Load())
	}

	// A token without the zone permissions is kept on the connection and
	// doesn't fail the analytics sync.
	e.pool.Exec(ctx, `UPDATE ai_bot_syncs SET last_run_at = now() - interval '2 days' WHERE integration_id = $1`, id)
	noZones.Store(true)
	if _, err := syncer.SyncOnce(ctx, ws, id); err != nil {
		t.Fatal(err)
	}
	c := owner.must(200, "GET", base+"/ai-bot-hits", nil)["connections"].([]any)[0].(map[string]any)
	if c["status"] != "error" || c["error_code"] != "connector_auth" || !strings.Contains(c["message"].(string), "Zone > Zone > Read") {
		t.Fatalf("error: %v", c)
	}
	if !hasEvent(events(), "ai_bot_logs.sync_failed") {
		t.Fatal("no failure event")
	}
	// Earlier hits stay.
	if len(owner.must(200, "GET", base+"/ai-bot-hits", nil)["data"].([]any)) != 2 {
		t.Fatal("hits lost")
	}
}

func hasEvent(evs []track.Event, name string) bool {
	for _, e := range evs {
		if e.Name == name {
			return true
		}
	}
	return false
}
