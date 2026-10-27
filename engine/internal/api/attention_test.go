package api_test

import (
	"context"
	"testing"
)

// Every module's to-dos reach the one list Home and agents read: lifecycle
// email, social drafts and customer data sit beside mailboxes and fixes.
func TestStatusCoversEveryModule(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := e.pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	var ses, attio string
	if err := e.pool.QueryRow(ctx, `INSERT INTO integrations (workspace_id, connector, name, credentials) VALUES ($1, 'amazon_ses', 'SES', '\x00') RETURNING id::text`, ws).Scan(&ses); err != nil {
		t.Fatal(err)
	}
	if err := e.pool.QueryRow(ctx, `INSERT INTO integrations (workspace_id, connector, name, credentials) VALUES ($1, 'attio', 'Attio', '\x00') RETURNING id::text`, ws).Scan(&attio); err != nil {
		t.Fatal(err)
	}
	exec(`WITH p AS (INSERT INTO email_providers (workspace_id, integration_id, connector, secrets) VALUES ($1, $2, 'amazon_ses', '\x00') RETURNING id)
		INSERT INTO email_domains (workspace_id, provider_id, domain, status, status_reason) SELECT $1, id, 'mail.acme.example', 'failed', 'DKIM record missing' FROM p`, ws, ses)
	exec(`INSERT INTO social_actions (workspace_id, channel, type, body, utm_content) VALUES ($1, 'reddit', 'reply', 'Happy to help: I work at Acme.', 'reply-1')`, ws)
	exec(`INSERT INTO event_definitions (workspace_id, name, status) VALUES ($1, 'trial.extended', 'quarantined')`, ws)
	exec(`WITH a AS (INSERT INTO records (workspace_id, record_type, name) VALUES ($1, 'person', 'Ana'), ($1, 'person', 'Ana B') RETURNING id)
		INSERT INTO record_merge_candidates (workspace_id, record_a, record_b, record_type, reason)
		SELECT $1, min(id::text)::uuid, max(id::text)::uuid, 'person', 'email' FROM a`, ws)
	exec(`INSERT INTO crm_imports (workspace_id, integration_id, connector, status, error, finished_at) VALUES ($1, $2, 'attio', 'failed', 'rate limited', now())`, ws, attio)

	blockers := map[string]map[string]any{}
	for _, b := range owner.must(200, "GET", "/v1/workspaces/"+ws+"/status", nil)["blockers"].([]any) {
		b := b.(map[string]any)
		blockers[b["code"].(string)] = b
	}
	for code, want := range map[string][2]string{
		"email_domain_failed":   {"lifecycle", "list_email_domains"},
		"social_drafts_waiting": {"social", "list_social_replies"},
		"events_quarantined":    {"customers", "update_event_definition"},
		"duplicates_waiting":    {"customers", "list_record_duplicates"},
		"crm_import_failed":     {"connectors", "get_crm_import"},
	} {
		b := blockers[code]
		if b == nil || b["area"] != want[0] || b["fix"].(map[string]any)["tool"] != want[1] || b["count"].(float64) != 1 {
			t.Errorf("%s: %v", code, b)
		}
	}

	// A later import that worked clears the failed one.
	exec(`INSERT INTO crm_imports (workspace_id, integration_id, connector, status, finished_at) VALUES ($1, $2, 'attio', 'done', now())`, ws, attio)
	for _, b := range owner.must(200, "GET", "/v1/workspaces/"+ws+"/status", nil)["blockers"].([]any) {
		if b.(map[string]any)["code"] == "crm_import_failed" {
			t.Fatal("a CRM import that worked again still shows as failed")
		}
	}
}
