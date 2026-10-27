package api_test

import (
	"context"
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/track"
)

func TestPlaybooks(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	anon := &client{t: t, h: e.h}

	// The catalog is public.
	list := anon.must(200, "GET", "/v1/playbooks", nil)["data"].([]any)
	if len(list) == 0 {
		t.Fatal("no playbooks")
	}
	p := anon.must(200, "GET", "/v1/playbooks/welcome-new-signups", nil)
	if p["tier"] != "obvious" || !strings.Contains(p["prompt"].(string), "{{brand}}") ||
		p["source_url"] != "https://github.com/outflareai/outflare/blob/main/playbooks/welcome-new-signups/playbook.yaml" {
		t.Fatalf("playbook: %v", p)
	}
	anon.must(404, "GET", "/v1/playbooks/nope", nil)

	// A new workspace has no email provider and no events.
	wp := owner.must(200, "GET", base+"/playbooks/welcome-new-signups", nil)
	r := wp["readiness"].(map[string]any)
	if r["ready"] != false || len(r["missing_connections"].([]any)) != 1 || len(r["missing_data"].([]any)) != 1 || len(r["missing_optional"].([]any)) != 1 {
		t.Fatalf("readiness: %v", r)
	}

	// No email provider of its own, no run: never anyone else's account.
	start := map[string]any{"inputs": map[string]any{"brand": "Northpeak"}}
	res := owner.must(409, "POST", base+"/playbooks/welcome-new-signups/runs", start)
	if res["error"].(map[string]any)["code"] != "connector_needed" {
		t.Fatalf("no provider: %v", res)
	}
	if _, err := e.pool.Exec(context.Background(), `INSERT INTO integrations (workspace_id, connector, name, credentials) VALUES ($1, 'resend', 'Resend', '\x00')`, ws); err != nil {
		t.Fatal(err)
	}
	r = owner.must(200, "GET", base+"/playbooks/welcome-new-signups", nil)["readiness"].(map[string]any)
	if len(r["missing_connections"].([]any)) != 0 || r["ready"] != false {
		t.Fatalf("with a provider, still no events: %v", r)
	}

	// Inputs are checked.
	owner.must(400, "POST", base+"/playbooks/welcome-new-signups/runs", map[string]any{})
	owner.must(400, "POST", base+"/playbooks/welcome-new-signups/runs", map[string]any{"inputs": map[string]any{"brand": "Northpeak", "colour": "red"}})
	owner.must(404, "POST", base+"/playbooks/nope/runs", start)

	// Starting it makes a mission draft with the prompt filled in.
	m := owner.must(201, "POST", base+"/playbooks/welcome-new-signups/runs", start)
	if m["status"] != "draft" || m["mode"] != "dry_run" || m["playbook_id"] != "welcome-new-signups" || m["playbook_version"] != float64(1) {
		t.Fatalf("mission: %v", m)
	}
	ins := m["instructions"].(string)
	if !strings.Contains(ins, "for Northpeak") || !strings.Contains(ins, "{{first_win}}") || !strings.Contains(ins, "Guardrails:") {
		t.Fatalf("instructions: %s", ins)
	}
	if !strings.Contains(m["plan"].(string), "1. Read the brand's context") || !strings.Contains(m["plan"].(string), "(createJourney)") {
		t.Fatalf("plan: %s", m["plan"])
	}
	if n := owner.must(200, "GET", base+"/playbooks/welcome-new-signups", nil)["runs"]; n != float64(1) {
		t.Fatalf("runs = %v", n)
	}
	started := named(got(), "playbook.started")
	if len(started) != 1 || started[0].Props["playbook_id"] != "welcome-new-signups" || started[0].WorkspaceID != ws {
		t.Fatalf("playbook.started: %+v", started)
	}
}
