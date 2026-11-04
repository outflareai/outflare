package api_test

import (
	"slices"
	"testing"
)

// TestFieldLabels covers the owner's decisions of 2026-11-03: every field,
// built in or not, and every event carries sensitivity, an owner, a
// retention and permitted destinations, as labels only.
func TestFieldLabels(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	var me string
	for _, m := range owner.must(200, "GET", "/v1/workspaces/"+ws+"/members", nil)["data"].([]any) {
		me = m.(map[string]any)["user_id"].(string)
	}
	brand := owner.must(200, "GET", "/v1/workspaces/"+ws+"/brands", nil)["data"].([]any)[0].(map[string]any)["id"].(string)
	base := "/v1/brands/" + brand + "/catalog"
	every := []any{"webhooks", "exports", "ai", "published_queries", "email"}
	field := func(key string) map[string]any {
		for _, f := range owner.must(200, "GET", base+"/types/person", nil)["fields"].([]any) {
			if f := f.(map[string]any); f["key"] == key {
				return f
			}
		}
		t.Fatalf("no field %s", key)
		return nil
	}

	// Defaults: nobody, forever, everywhere.
	title := field("job_title")
	if title["owner_id"] != nil || title["retention_days"] != nil || !slices.Equal(title["destinations"].([]any), every) {
		t.Fatalf("defaults: %v", title)
	}

	// A built-in field takes labels and sensitivity, nothing else.
	got := owner.must(200, "PATCH", base+"/types/person/fields/job_title", map[string]any{
		"sensitivity": "confidential", "owner_id": me, "retention_days": 730, "destinations": []string{"exports", "email"}})
	if got["sensitivity"] != "confidential" || got["owner_id"] != me || got["retention_days"] != 730.0 ||
		!slices.Equal(got["destinations"].([]any), []any{"exports", "email"}) {
		t.Fatalf("built-in labels: %v", got)
	}
	owner.must(400, "PATCH", base+"/types/person/fields/job_title", map[string]any{"label": "Mail"})
	// Only what is sent changes; null resets.
	got = owner.must(200, "PATCH", base+"/types/person/fields/job_title", map[string]any{"destinations": nil})
	if got["owner_id"] != me || got["sensitivity"] != "confidential" || !slices.Equal(got["destinations"].([]any), every) {
		t.Fatalf("partial update: %v", got)
	}
	if f := field("job_title"); f["retention_days"] != 730.0 {
		t.Fatalf("read back: %v", f)
	}

	// A custom field, on create and update.
	plan := owner.must(201, "POST", base+"/types/person/fields", map[string]any{"label": "Plan", "value_type": "text", "retention_days": 365})
	if plan["retention_days"] != 365.0 || !slices.Equal(plan["destinations"].([]any), every) {
		t.Fatalf("custom: %v", plan)
	}
	plan = owner.must(200, "PATCH", base+"/types/person/fields/plan", map[string]any{"retention_days": nil, "destinations": []string{}, "sensitivity": "restricted"})
	if plan["retention_days"] != nil || len(plan["destinations"].([]any)) != 0 || plan["sensitivity"] != "restricted" {
		t.Fatalf("custom update: %v", plan)
	}

	// Bad values.
	for _, bad := range []map[string]any{
		{"retention_days": 0}, {"retention_days": 40000}, {"destinations": []string{"fax"}}, {"destinations": []string{"ai", "ai"}},
		{"owner_id": "nope"}, {"owner_id": "00000000-0000-0000-0000-000000000000"},
	} {
		owner.must(400, "PATCH", base+"/types/person/fields/plan", bad)
	}

	// Events.
	ev := owner.must(201, "POST", base+"/events", map[string]any{"name": "product.activated", "owner_id": me, "destinations": []string{"webhooks"}})
	if ev["owner_id"] != me || ev["retention_days"] != nil || !slices.Equal(ev["destinations"].([]any), []any{"webhooks"}) {
		t.Fatalf("event: %v", ev)
	}
	ev = owner.must(200, "PATCH", base+"/events/product.activated", map[string]any{"retention_days": 90, "owner_id": nil})
	if ev["owner_id"] != nil || ev["retention_days"] != 90.0 || !slices.Equal(ev["destinations"].([]any), []any{"webhooks"}) {
		t.Fatalf("event update: %v", ev)
	}
	owner.must(400, "PATCH", base+"/events/product.activated", map[string]any{"destinations": []string{"fax"}})
}
