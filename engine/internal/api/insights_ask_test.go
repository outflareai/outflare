package api_test

import (
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/track"
)

// insightAskBodies keeps what the ask box sent the model.
var insightAskBodies struct {
	sync.Mutex
	all []string
}

// answerInsightAsk answers fakeClaude's ask-box calls: a chart the engine
// refuses first, then a good one once told why; nothing for the weather.
func answerInsightAsk(w http.ResponseWriter, body string) {
	insightAskBodies.Lock()
	insightAskBodies.all = append(insightAskBodies.all, body)
	insightAskBodies.Unlock()
	switch {
	case strings.Contains(body, "weather"):
		answerJSON(w, map[string]any{"kind": "none", "name": "", "spec_json": "{}", "explanation": "There is no weather data here. Ask about visits, events or payments."})
	case strings.Contains(body, "cannot be used"):
		answerJSON(w, map[string]any{"kind": "trend", "name": "Sign-ups by week",
			"spec_json":   `{"series": [{"kind": "event", "event": "signed_up"}], "date_range": {"preset": "last_90_days"}, "interval": "week", "breakdown": "property:plan"}`,
			"explanation": "Sign-ups each week for the last 90 days, one line per plan."})
	default:
		answerJSON(w, map[string]any{"kind": "trend", "name": "Sign-ups by week",
			"spec_json": `{"series": [{"kind": "event", "event": "signed_up"}], "interval": "fortnight"}`, "explanation": "x"})
	}
}

func TestInsightAsk(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	e.runServerJobs(t)
	var brand string
	if err := e.pool.QueryRow(t.Context(), `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand
	owner.must(201, "POST", base+"/catalog/events", map[string]any{"name": "signed_up", "label": "Signed up", "description": "Someone made an account."})
	if _, err := e.pool.Exec(t.Context(), `INSERT INTO customer_events (workspace_id, brand_id, anonymous_id, kind, name, occurred_at, properties)
		VALUES ($1, $2, 'a1', 'track', 'signed_up', $3, '{"secret_value": "ana@acme.example"}')`, ws, brand, time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}

	// Without the workspace's own AI model, nothing is asked.
	if c := owner.must(409, "POST", base+"/insights/ask", map[string]any{"question": "How many sign-ups by week?"})["error"].(map[string]any)["code"]; c != "connector_needed" {
		t.Fatalf("no AI: %v", c)
	}
	owner.must(201, "POST", "/v1/workspaces/"+ws+"/integrations", map[string]any{"connector": "claude", "credentials": map[string]any{"api_key": "sk-ant-good"}})
	owner.must(400, "POST", base+"/insights/ask", map[string]any{"question": "  "})
	owner.must(400, "POST", base+"/insights/ask", map[string]any{"question": strings.Repeat("x", 501)})
	owner.must(404, "GET", base+"/insights/ask/00000000-0000-0000-0000-000000000000", nil)

	ask := func(q string) map[string]any {
		t.Helper()
		r := owner.must(202, "POST", base+"/insights/ask", map[string]any{"question": q})
		if r["status"] != "queued" || r["question"] != q {
			t.Fatalf("asked: %v", r)
		}
		return waitFor(t, owner, base+"/insights/ask/"+r["id"].(string), func(m map[string]any) bool { return m["status"] == "ready" || m["status"] == "failed" })
	}

	// The first spec is refused (no such interval), so the model is told
	// why and asked once more; the second is checked and kept.
	a := ask("How many sign-ups by week, by plan?")
	if a["status"] != "ready" || a["kind"] != "trend" || a["name"] != "Sign-ups by week" || a["explanation"] == "" {
		t.Fatalf("answer: %v", a)
	}
	spec := a["spec"].(map[string]any)
	if spec["interval"] != "week" || spec["breakdown"] != "property:plan" {
		t.Fatalf("spec: %v", spec)
	}
	// The spec runs and saves as it is.
	owner.must(200, "POST", base+"/insights/trend", spec)
	owner.must(201, "POST", base+"/insights", map[string]any{"name": a["name"], "kind": "trend", "spec": spec})

	insightAskBodies.Lock()
	bodies := strings.Join(insightAskBodies.all, "\n")
	n := len(insightAskBodies.all)
	insightAskBodies.Unlock()
	if n != 2 || !strings.Contains(bodies, "signed_up") || !strings.Contains(bodies, "Someone made an account") || !strings.Contains(bodies, "fortnight") {
		t.Fatalf("the model was asked %d times, or without the dictionary or the engine's reason", n)
	}
	// Names and descriptions only: never an event's values.
	if strings.Contains(bodies, "acme.example") {
		t.Fatal("an event value reached the model")
	}

	// A question no chart answers says what is missing.
	none := ask("What will the weather be tomorrow?")
	if none["status"] != "ready" || none["kind"] != "" || none["spec"] != nil || !strings.Contains(none["explanation"].(string), "weather") {
		t.Fatalf("no chart: %v", none)
	}

	seen := map[string]int{}
	for _, ev := range got() {
		seen[ev.Name]++
		if strings.HasPrefix(ev.Name, "insight_question.") {
			for k, v := range ev.Props {
				if s, ok := v.(string); ok && strings.Contains(s, "sign-ups") {
					t.Fatalf("%s carries the question in %s", ev.Name, k)
				}
			}
		}
	}
	if seen["insight_question.asked"] != 2 || seen["insight_question.answered"] != 2 {
		t.Fatalf("events: %v", seen)
	}
}
