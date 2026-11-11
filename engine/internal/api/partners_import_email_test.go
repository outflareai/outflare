package api_test

import (
	"context"
	"testing"
	"time"
)

// TestPartnerImportEmailChanged: a partner who changes their email in Tolt
// after an import gets the new address on a later run. When the import's
// address is the record's only email and nobody verified it, the new one
// replaces it (closed as changed, kept in the history); when the record has
// a verified email, the new one is added beside it and nothing closes.
func TestPartnerImportEmailChanged(t *testing.T) {
	ctx := context.Background()
	tolt := &fakeTolt{data: map[string][]map[string]any{}}
	tolt.set("/programs", map[string]any{"id": "prg_1", "name": "Northpeak partners", "currency": "usd", "status": "active"})
	tolt.set("/links",
		map[string]any{"id": "l1", "partner_id": "p1", "param": "ref", "value": "ANA"},
		map[string]any{"id": "l2", "partner_id": "p2", "param": "ref", "value": "BEN"})
	tolt.set("/promotion-codes")
	tolt.set("/customers")
	tolt.set("/transactions")
	tolt.set("/commissions")
	partners := func(ana, ben string) {
		tolt.set("/partners",
			map[string]any{"id": "p1", "first_name": "Ana", "email": ana, "status": "active"},
			map[string]any{"id": "p2", "first_name": "Ben", "email": ben, "status": "active"})
	}
	partners("ana@acme.example", "ben@acme.example")
	e, owner, base, _ := runToltImport(t, tolt)

	if _, err := e.pool.Exec(ctx, `UPDATE record_identities SET verified = true, verified_at = now()
		WHERE namespace = 'email' AND value = 'ben@acme.example'`); err != nil {
		t.Fatal(err)
	}

	type identity struct {
		value, closed string
		verified      bool
	}
	emailsOf := func(code string) []identity {
		t.Helper()
		rows, err := e.pool.Query(ctx, `SELECT i.value, coalesce(i.closed_reason, ''), i.verified FROM record_identities i
			JOIN partner_memberships m ON m.record_id = i.record_id
			WHERE m.code = $1 AND i.namespace = 'email' ORDER BY i.valid_from, i.value`, code)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []identity
		for rows.Next() {
			var i identity
			if err := rows.Scan(&i.value, &i.closed, &i.verified); err != nil {
				t.Fatal(err)
			}
			out = append(out, i)
		}
		return out
	}
	shownEmail := func(code string) string {
		t.Helper()
		for _, p := range owner.must(200, "GET", base+"/partners?program=affiliates&q="+code, nil)["data"].([]any) {
			if m := p.(map[string]any); m["code"] == code {
				return m["email"].(string)
			}
		}
		t.Fatalf("no partner %s", code)
		return ""
	}
	rerun := func() map[string]any {
		t.Helper()
		imports := base + "/partner-programs/affiliates/imports"
		imp := owner.must(201, "POST", imports, map[string]any{})
		e.runCRMImports(t)
		for range 1500 {
			v := owner.must(200, "GET", imports+"/"+imp["id"].(string), nil)
			if v["status"] == "done" {
				return v
			}
			if v["status"] == "failed" {
				t.Fatalf("import = %v", v)
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("the import did not finish")
		return nil
	}
	partnersCount := func(v map[string]any) map[string]any {
		for _, k := range v["summary"].(map[string]any)["kinds"].([]any) {
			if m := k.(map[string]any); m["kind"] == "partners" {
				return m
			}
		}
		return nil
	}
	changed := func(v map[string]any) []string {
		var out []string
		for _, i := range v["summary"].(map[string]any)["issues"].([]any) {
			if m := i.(map[string]any); m["reason"] == "email_changed" {
				out = append(out, m["external_id"].(string))
			}
		}
		return out
	}

	partners("Ana.New@acme.example", "ben.new@acme.example")
	second := rerun()
	if c := partnersCount(second); c["updated"] != float64(2) || c["unchanged"] != float64(0) {
		t.Fatalf("partners on the second run = %v", c)
	}
	if got := changed(second); len(got) != 2 || got[0] != "p1" || got[1] != "p2" {
		t.Fatalf("email_changed issues = %v", got)
	}
	if got := emailsOf("ANA"); len(got) != 2 || got[0] != (identity{"ana@acme.example", "changed", false}) ||
		got[1] != (identity{"ana.new@acme.example", "", false}) {
		t.Fatalf("Ana's emails = %+v", got)
	}
	if got := shownEmail("ANA"); got != "ana.new@acme.example" {
		t.Fatalf("Ana's partner email = %q", got)
	}
	if got := emailsOf("BEN"); len(got) != 2 || got[0] != (identity{"ben@acme.example", "", true}) ||
		got[1] != (identity{"ben.new@acme.example", "", false}) {
		t.Fatalf("Ben's emails = %+v", got)
	}
	if got := shownEmail("BEN"); got != "ben@acme.example" {
		t.Fatalf("Ben's partner email = %q", got)
	}

	third := rerun()
	if c := partnersCount(third); c["unchanged"] != float64(2) || c["updated"] != float64(0) {
		t.Fatalf("partners on the third run = %v", c)
	}
	if got := changed(third); len(got) != 0 {
		t.Fatalf("email_changed issues on the third run = %v", got)
	}
	if got := emailsOf("ANA"); len(got) != 2 {
		t.Fatalf("Ana's emails after the third run = %+v", got)
	}
}
