package api_test

import (
	"context"
	"testing"
	"time"
)

// TestFieldSources covers the owner's decision of 2026-11-02: every
// source's latest value of a field is kept; a hand edit wins for the
// brand's days (180 by default, per field too, 0 for forever) unless it is
// pinned; once it expires a source replaces it, and the sweep hands the
// field to the best other source, in the record's history.
func TestFieldSources(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	var brand string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := e.pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	owner.must(201, "POST", base+"/catalog/types/account/fields", map[string]any{"label": "Sector", "value_type": "text"})
	owner.must(201, "POST", base+"/catalog/types/account/fields", map[string]any{"label": "Team note", "value_type": "text"})
	acme := owner.must(201, "POST", base+"/records", map[string]any{"type": "account", "name": "Acme"})["id"].(string)
	rec := base + "/records/" + acme
	set := func(source string, attrs map[string]any) map[string]any {
		t.Helper()
		return owner.must(200, "PATCH", rec, map[string]any{"source": source, "attributes": attrs})
	}
	get := func() map[string]any { return owner.must(200, "GET", rec, nil) }
	values := func() map[string]any { return owner.must(200, "GET", rec+"/field-values", nil) }
	hand := func(field string) map[string]any {
		t.Helper()
		for _, h := range values()["hand_edits"].([]any) {
			if h := h.(map[string]any); h["field"] == field {
				return h
			}
		}
		return nil
	}
	sweep := func() int {
		t.Helper()
		n, err := e.srv.ExpireHandEdits(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	expire := func(field string) {
		exec(`UPDATE hand_edits SET expires_at = now() - interval '1 minute' WHERE record_id = $1 AND field_key = $2`, acme, field)
	}

	// Ana types it; HubSpot disagrees and is kept, not applied.
	set("manual", map[string]any{"sector": "Fintech"})
	h := hand("sector")
	if h == nil || h["pinned"] != false || h["expired"] != false {
		t.Fatalf("hand edit: %v", h)
	}
	if at, _ := time.Parse(time.RFC3339, h["expires_at"].(string)); at.Before(time.Now().Add(179*24*time.Hour)) || at.After(time.Now().Add(181*24*time.Hour)) {
		t.Fatalf("expires in 180 days by default: %v", h["expires_at"])
	}
	if r := set("crm_sync", map[string]any{"sector": "Financial services"}); r["attributes"].(map[string]any)["sector"] != "Fintech" {
		t.Fatalf("a sync replaced a fresh hand edit: %v", r)
	}
	set("enrichment", map[string]any{"sector": "Banking"})
	if v := values()["data"].([]any); len(v) != 3 {
		t.Fatalf("every source's value: %v", v)
	}

	// Its time runs out: the sweep hands the field to the best other
	// source (HubSpot outranks enrichment), as a change in its history.
	expire("sector")
	if n := sweep(); n != 1 {
		t.Fatalf("swept %d", n)
	}
	r := get()
	if r["attributes"].(map[string]any)["sector"] != "Financial services" || r["attribute_sources"].(map[string]any)["sector"] != "crm_sync" {
		t.Fatalf("after expiry: %v %v", r["attributes"], r["attribute_sources"])
	}
	hist := owner.must(200, "GET", rec+"/history?field=sector", nil)["data"].([]any)
	if hist[0].(map[string]any)["source"] != "crm_sync" || hist[0].(map[string]any)["new_value"] != "Financial services" {
		t.Fatalf("history: %v", hist[0])
	}
	if hand("sector") != nil || sweep() != 0 {
		t.Fatal("the expired hand edit is gone and sweeps once")
	}
	var audited int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE workspace_id = $1 AND action = 'record.hand_edit_expired'`, ws).Scan(&audited); err != nil || audited != 1 {
		t.Fatalf("audit %d %v", audited, err)
	}

	// Use this (a hand edit of Ana's old value), then pin it: nothing
	// replaces it, and the pin is in the history.
	set("manual", map[string]any{"sector": "Fintech"})
	pinned := owner.must(200, "POST", rec+"/fields/sector/pin", map[string]any{"pinned": true})
	if pinned["pinned"] != true || pinned["expires_at"] != nil {
		t.Fatalf("pin: %v", pinned)
	}
	exec(`UPDATE hand_edits SET expires_at = now() - interval '1 day' WHERE record_id = $1`, acme)
	sweep()
	set("crm_sync", map[string]any{"sector": "Banking"})
	if get()["attributes"].(map[string]any)["sector"] != "Fintech" {
		t.Fatal("a pinned edit was replaced")
	}
	if hist := owner.must(200, "GET", rec+"/history?field=sector", nil)["data"].([]any); hist[0].(map[string]any)["source"] != "pinned" {
		t.Fatalf("pin in history: %v", hist[0])
	}
	unpinned := owner.must(200, "POST", rec+"/fields/sector/pin", map[string]any{"pinned": false})
	if unpinned["pinned"] != false || unpinned["expires_at"] == nil {
		t.Fatalf("unpin: %v", unpinned)
	}

	// An expired edit that the sweep hasn't reached yet: the next sync wins.
	expire("sector")
	set("crm_sync", map[string]any{"sector": "Insurance"})
	if r := get(); r["attributes"].(map[string]any)["sector"] != "Insurance" || hand("sector") != nil {
		t.Fatalf("sync after expiry: %v", r["attributes"])
	}
	if code, out := owner.do("POST", rec+"/fields/sector/pin", map[string]any{"pinned": true}); code != 409 || out["error"].(map[string]any)["code"] != "not_a_hand_edit" {
		t.Fatalf("pin a synced value: %d %v", code, out)
	}

	// A field only people write: expiring changes nothing.
	set("manual", map[string]any{"team_note": "Call in Q3"})
	expire("team_note")
	sweep()
	if r := get(); r["attributes"].(map[string]any)["team_note"] != "Call in Q3" {
		t.Fatalf("people-only field: %v", r["attributes"])
	}
	if h := hand("team_note"); h == nil || h["expired"] != true {
		t.Fatalf("marked expired: %v", h)
	}

	// The days: a field's own (0 is forever), else the brand's.
	f := owner.must(200, "PATCH", base+"/catalog/types/account/fields/sector", map[string]any{"manual_edit_days": 0})
	if f["manual_edit_days"] != 0.0 {
		t.Fatalf("field days: %v", f)
	}
	set("manual", map[string]any{"sector": "Fintech"})
	if h := hand("sector"); h["expires_at"] != nil {
		t.Fatalf("forever: %v", h)
	}
	owner.must(400, "PATCH", base+"/catalog/types/account/fields/sector", map[string]any{"manual_edit_days": -1})
	owner.must(200, "PATCH", base+"/catalog/types/account/fields/sector", map[string]any{"manual_edit_days": nil})
	b := owner.must(200, "PATCH", "/v1/workspaces/"+ws+"/brands/"+brand, map[string]any{"manual_edit_days": 30})
	if b["manual_edit_days"] != 30.0 {
		t.Fatalf("brand days: %v", b)
	}
	set("manual", map[string]any{"sector": "Payments"})
	if at, _ := time.Parse(time.RFC3339, hand("sector")["expires_at"].(string)); at.After(time.Now().Add(31 * 24 * time.Hour)) {
		t.Fatalf("brand's 30 days: %v", at)
	}
}
