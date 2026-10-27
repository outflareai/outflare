package api_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/track"
)

func TestConnectionUsageLimits(t *testing.T) {
	e := freshEnv(t)
	events := track.Capture(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	if _, err := e.pool.Exec(ctx, `UPDATE users SET email_verified_at = now()`); err != nil {
		t.Fatal(err)
	}
	e.runCRMImports(t) // works the server's jobs, the warning emails among them

	// A free connection has no usage; a paid one starts with its
	// connector's starting limit.
	pl := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "plausible", "credentials": map[string]string{"api_key": "pk", "site_id": "example.com"}})["id"].(string)
	owner.must(404, "GET", base+"/integrations/"+pl+"/usage", nil)
	id := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "dataforseo", "credentials": map[string]string{"login": "jo", "password": "pw"}})["id"].(string)
	list := owner.must(200, "GET", base+"/connection-usage", nil)["data"].([]any)
	if len(list) != 1 {
		t.Fatalf("paid connections = %v", list)
	}
	u := list[0].(map[string]any)
	l := u["limits"].(map[string]any)
	if u["integration_id"] != id || l["unit"] != "USD" || l["monthly_limit"].(float64) != 10 || l["daily_limit"] != nil || l["default"] != true || l["hard_stop"] != true {
		t.Fatalf("usage = %v", u)
	}

	// Limits are checked.
	owner.must(400, "PUT", base+"/integrations/"+id+"/usage/limits", map[string]any{"unit": "credit"})
	owner.must(400, "PUT", base+"/integrations/"+id+"/usage/limits", map[string]any{"daily_limit": -1})
	owner.must(400, "PUT", base+"/integrations/"+id+"/usage/limits", map[string]any{"daily_limit": 5, "monthly_limit": 1})

	// A daily limit of 3.5 cents: an overview (3 cents) passes and takes the
	// day past 80%, so the next report would pass the limit.
	before := e.outbox.count()
	u = owner.must(200, "PUT", base+"/integrations/"+id+"/usage/limits", map[string]any{"daily_limit": 0.035, "monthly_limit": 10})
	if l := u["limits"].(map[string]any); l["daily_limit"].(float64) != 0.035 || l["default"] != false {
		t.Fatalf("limits = %v", l)
	}
	owner.must(200, "GET", base+"/seo/"+id+"/reports/overview?target=example.com", nil)
	code, out := owner.do("GET", base+"/seo/"+id+"/reports/keywords?target=example.com", nil)
	if msg, _ := out["error"].(map[string]any)["message"].(string); code != 402 || out["error"].(map[string]any)["code"] != "budget_reached" || !strings.Contains(msg, "daily limit") {
		t.Fatalf("over the daily limit = %d %v", code, out)
	}
	u = owner.must(200, "GET", base+"/integrations/"+id+"/usage", nil)
	used := u["used"].(map[string]any)
	if today := used["today"].(float64); today < 0.03 || today > 0.031 || used["month"].(float64) != today || len(u["days"].([]any)) != 1 {
		t.Fatalf("usage after a report = %v", u)
	}
	if a := u["alerts"].([]any); len(a) != 1 || a[0].(map[string]any)["level"].(float64) != 80 || a[0].(map[string]any)["period"] != "day" {
		t.Fatalf("alerts = %v", a)
	}

	// Only warning: the call goes on, and reaching the limit says so once.
	owner.must(200, "PUT", base+"/integrations/"+id+"/usage/limits", map[string]any{"daily_limit": 0.035, "monthly_limit": 10, "hard_stop": false})
	owner.must(200, "GET", base+"/seo/"+id+"/reports/keywords?target=example.com", nil)
	owner.must(200, "GET", base+"/seo/"+id+"/reports/keywords?target=example.org", nil)
	var warned, reached int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE action = 'connection_limit.warned'), count(*) FILTER (WHERE action = 'connection_limit.reached')
		FROM audit_events WHERE workspace_id = $1`, ws).Scan(&warned, &reached); err != nil {
		t.Fatal(err)
	}
	if warned != 1 || reached != 1 {
		t.Errorf("warned %d, reached %d", warned, reached)
	}
	deadline := time.Now().Add(5 * time.Second)
	for e.outbox.count() < before+2 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	e.outbox.mu.Lock()
	sent := e.outbox.sent[before:]
	e.outbox.mu.Unlock()
	if len(sent) != 2 {
		t.Fatalf("warning emails = %d", len(sent))
	}
	for _, m := range sent {
		if m.To != "jack@example.com" || !strings.Contains(m.Text, "/connectors/"+id) || !strings.Contains(m.Text, "daily limit") {
			t.Errorf("email = %+v", m)
		}
	}

	// The SEO budget is the monthly limit, and keeps the daily one.
	owner.must(200, "PUT", base+"/seo/"+id+"/settings", map[string]any{"monthly_budget": 5})
	l = owner.must(200, "GET", base+"/integrations/"+id+"/usage", nil)["limits"].(map[string]any)
	if l["monthly_limit"].(float64) != 5 || l["daily_limit"].(float64) != 0.035 || l["hard_stop"] != false {
		t.Errorf("limits after the SEO budget = %v", l)
	}
	// No monthly limit: the SEO budget says so.
	owner.must(200, "PUT", base+"/integrations/"+id+"/usage/limits", map[string]any{"daily_limit": nil, "monthly_limit": nil})
	if b := owner.must(200, "GET", base+"/seo", nil)["data"].([]any)[0].(map[string]any)["budget"].(map[string]any); b["unlimited"] != true || b["default"] != false {
		t.Errorf("budget without a monthly limit = %v", b)
	}
	// Back to the starting limit.
	l = owner.must(200, "DELETE", base+"/integrations/"+id+"/usage/limits", nil)["limits"].(map[string]any)
	if l["default"] != true || l["monthly_limit"].(float64) != 10 || l["daily_limit"] != nil {
		t.Errorf("reset limits = %v", l)
	}

	// Another workspace sees none of it.
	ws2 := owner.must(201, "POST", "/v1/workspaces", map[string]string{"name": "Other"})["id"].(string)
	owner.must(404, "GET", "/v1/workspaces/"+ws2+"/integrations/"+id+"/usage", nil)
	if l := owner.must(200, "GET", "/v1/workspaces/"+ws2+"/connection-usage", nil)["data"].([]any); len(l) != 0 {
		t.Errorf("other workspace = %v", l)
	}

	names := map[string]bool{}
	for _, ev := range events() {
		names[ev.Name] = true
	}
	for _, want := range []string{"connection_limits.updated", "connection_limits.reset", "connection_limit.warned", "connection_limit.reached", "connection_limit.emailed"} {
		if !names[want] {
			t.Errorf("no %s event", want)
		}
	}
}
