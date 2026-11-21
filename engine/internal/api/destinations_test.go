package api_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/outflareai/outflare/engine/internal/destinations"
	"github.com/outflareai/outflare/engine/internal/track"
	"github.com/outflareai/outflare/engine/internal/webhooks"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestDestinations(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	var brand string
	if err := e.pool.QueryRow(t.Context(), `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand + "/destinations"

	// What each tool received.
	var mu sync.Mutex
	type call struct {
		url, sig string
		body     map[string]any
	}
	var calls []call
	down := false
	client := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		mu.Lock()
		defer mu.Unlock()
		status := 200
		if down && r.URL.Host == "hooks.acme.example" {
			status = 503
		} else {
			calls = append(calls, call{r.URL.String(), r.Header.Get("webhook-signature"), body})
		}
		return &http.Response{StatusCode: status, Status: http.StatusText(status), Body: io.NopCloser(bytes.NewReader(nil)), Header: http.Header{}}, nil
	})}
	var posts []map[string]any
	slack := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth.test":
			w.Write([]byte(`{"ok": true, "team": "Acme", "team_id": "T1"}`))
		case "/chat.postMessage":
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			posts = append(posts, body)
			mu.Unlock()
			w.Write([]byte(`{"ok": true}`))
		}
	}))
	defer slack.Close()
	e.integrations.Client.BaseURLs["slack"] = slack.URL
	sl := owner.must(201, "POST", "/v1/workspaces/"+ws+"/integrations", map[string]any{"connector": "slack", "credentials": map[string]string{"api_key": "xoxb-test-not-a-real-token"}})["id"].(string)

	// What can't be saved.
	owner.must(400, "POST", base, map[string]any{"kind": "fax", "name": "Fax"})
	owner.must(400, "POST", base, map[string]any{"kind": "webhook", "name": "Hook", "config": map[string]any{"url": "http://localhost/x"}})
	owner.must(400, "POST", base, map[string]any{"kind": "webhook", "name": "Hook", "config": map[string]any{"url": "https://hooks.acme.example/e"}, "events": []string{"Not A Name"}})
	owner.must(400, "POST", base, map[string]any{"kind": "ga4", "name": "GA", "config": map[string]any{"measurement_id": "G-ABC123DEF4"}})
	owner.must(400, "POST", base, map[string]any{"kind": "ga4", "name": "GA", "config": map[string]any{"measurement_id": "UA-1"}, "secret": "ga-test-not-a-real-secret"})
	owner.must(400, "POST", base, map[string]any{"kind": "slack", "name": "Slack", "config": map[string]any{"channel": "C1"}})
	owner.must(400, "POST", base, map[string]any{"kind": "hubspot", "name": "HubSpot", "integration_id": sl})

	hook := owner.must(201, "POST", base, map[string]any{"kind": "webhook", "name": "Our warehouse", "config": map[string]any{"url": "https://hooks.acme.example/e"}})
	if s, _ := hook["signing_secret"].(string); !strings.HasPrefix(s, "whsec_") {
		t.Fatalf("a new webhook destination shows its signing secret once: %v", hook)
	}
	ga := owner.must(201, "POST", base, map[string]any{"kind": "ga4", "name": "Google Analytics", "events": []string{"order_completed"},
		"config": map[string]any{"measurement_id": "G-ABC123DEF4"}, "secret": "ga-test-not-a-real-secret"})
	if ga["has_secret"] != true || ga["signing_secret"] != nil || strings.Contains(mustJSON(ga), "ga-test-not-a-real-secret") {
		t.Fatalf("a secret is kept and never returned: %v", ga)
	}
	owner.must(201, "POST", base, map[string]any{"kind": "slack", "name": "Sales channel", "events": []string{"order_completed"},
		"integration_id": sl, "config": map[string]any{"channel": "C1", "channel_name": "sales"}})
	if list := owner.must(200, "GET", base, nil)["data"].([]any); len(list) != 3 {
		t.Fatalf("list: %v", list)
	}
	one := owner.must(200, "GET", base+"/"+hook["id"].(string), nil)
	if one["signing_secret"] != nil || one["kind"] != "webhook" {
		t.Fatalf("get: %v", one)
	}

	// Two events arrive: every destination takes the order, only the
	// webhook (every tracked event) takes the sign-up.
	if _, err := e.pool.Exec(t.Context(), `UPDATE event_destinations SET matched_until = now() - interval '1 hour' WHERE workspace_id = $1`, ws); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(t.Context(), `INSERT INTO customer_events (workspace_id, brand_id, anonymous_id, user_id, kind, name, raw_name, occurred_at, received_at, properties) VALUES
		($1, $2, 'anon-1', 'u1', 'track', 'order_completed', 'Order Completed', now() - interval '2 minutes', now() - interval '1 minute', '{"plan": "pro", "value": 49, "nested": {"a": 1}}'),
		($1, $2, 'anon-2', NULL, 'track', 'signed_up', 'Signed Up', now() - interval '2 minutes', now() - interval '1 minute', '{}'),
		($1, $2, 'anon-3', NULL, 'page', 'page.viewed', '', now() - interval '2 minutes', now() - interval '1 minute', '{}')`, ws, brand); err != nil {
		t.Fatal(err)
	}
	sender := &destinations.Sender{Pool: e.pool, Store: e.integrations, Box: e.box, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Client: client,
		Hooks: &webhooks.Service{Pool: e.pool, Box: e.box, Client: client}}
	if n, err := destinations.Match(t.Context(), e.pool, ""); err != nil || n != 4 {
		t.Fatalf("match: %d %v", n, err)
	}
	if n, err := destinations.Match(t.Context(), e.pool, ""); err != nil || n != 0 {
		t.Fatalf("an event is matched once: %d %v", n, err)
	}
	// The webhook's receiver is down: its events wait.
	down = true
	res, err := sender.Send(t.Context(), ws, hook["id"].(string))
	if err != nil || !res.Unreachable || res.Sent != 0 {
		t.Fatalf("down: %+v %v", res, err)
	}
	if one := owner.must(200, "GET", base+"/"+hook["id"].(string), nil); one["failing_since"] == nil || one["queued"].(float64) != 2 {
		t.Fatalf("a destination that can't be reached says so: %v", one)
	}
	down = false
	for _, d := range owner.must(200, "GET", base, nil)["data"].([]any) {
		if _, err := sender.Send(t.Context(), ws, d.(map[string]any)["id"].(string)); err != nil {
			t.Fatal(err)
		}
	}
	var hooks, gas int
	for _, c := range calls {
		switch {
		case strings.HasPrefix(c.url, "https://hooks.acme.example/e"):
			hooks++
			if c.sig == "" || c.body["type"] != "event" || c.body["brand_id"] != brand {
				t.Fatalf("webhook delivery: %+v", c)
			}
		case strings.HasPrefix(c.url, "https://www.google-analytics.com/mp/collect"):
			gas++
			ev := c.body["events"].([]any)[0].(map[string]any)
			params := ev["params"].(map[string]any)
			if !strings.Contains(c.url, "measurement_id=G-ABC123DEF4") || !strings.Contains(c.url, "api_secret=ga-test-not-a-real-secret") ||
				c.body["client_id"] != "anon-1" || c.body["user_id"] != "u1" || ev["name"] != "order_completed" || params["plan"] != "pro" || params["nested"] != nil {
				t.Fatalf("ga4 delivery: %+v", c)
			}
		}
	}
	if hooks != 2 || gas != 1 || len(posts) != 1 || posts[0]["channel"] != "C1" {
		t.Fatalf("deliveries: %d webhook, %d ga4, slack %v", hooks, gas, posts)
	}
	one = owner.must(200, "GET", base+"/"+hook["id"].(string), nil)
	if one["sent_24h"].(float64) != 2 || one["queued"].(float64) != 0 || one["failing_since"] != nil || one["last_success_at"] == nil {
		t.Fatalf("after sending: %v", one)
	}
	sends := owner.must(200, "GET", base+"/"+hook["id"].(string)+"/sends?status=sent", nil)["data"].([]any)
	if len(sends) != 2 || sends[0].(map[string]any)["attempts"].(float64) < 1 {
		t.Fatalf("sends: %v", sends)
	}
	owner.must(400, "GET", base+"/"+hook["id"].(string)+"/sends?status=lost", nil)

	// Switched off, nothing is matched; switched back on, it starts from now.
	owner.must(200, "PATCH", base+"/"+hook["id"].(string), map[string]any{"enabled": false})
	owner.must(400, "PATCH", base+"/"+hook["id"].(string), map[string]any{"kind": "slack"})
	if up := owner.must(200, "PATCH", base+"/"+ga["id"].(string), map[string]any{"name": "GA4", "events": []string{}}); up["name"] != "GA4" || up["has_secret"] != true {
		t.Fatalf("update keeps the secret: %v", up)
	}
	owner.must(204, "DELETE", base+"/"+hook["id"].(string), nil)
	owner.must(404, "GET", base+"/"+hook["id"].(string), nil)

	// A brand that isn't this workspace's has none of it.
	owner.must(404, "GET", "/v1/brands/00000000-0000-4000-8000-000000000000/destinations", nil)

	names := map[string]bool{}
	for _, ev := range got() {
		names[ev.Name] = true
		if strings.HasPrefix(ev.Name, "destination.") && strings.Contains(mustJSON(ev.Props), "acme.example") {
			t.Fatalf("an event must not carry the destination's address: %v", ev.Props)
		}
	}
	for _, n := range []string{"destination.created", "destination.updated", "destination.deleted"} {
		if !names[n] {
			t.Fatalf("missing event %s", n)
		}
	}
}
