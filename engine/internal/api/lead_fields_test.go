package api_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/customers"
)

// A lead's details live on its person: they fill the record without
// overwriting what a person set there, and cold email's merge tags read
// the person back.
func TestLeadDetailsOnThePerson(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()

	// Sam is already a person, with a job title someone typed in.
	sam := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "",
		"attributes": map[string]any{"job_title": "CTO"},
		"identities": []any{map[string]any{"namespace": "email", "value": "sam@acme.example"}}})["id"].(string)

	lead := owner.must(201, "POST", base+"/leads", map[string]any{"email": "sam@acme.example", "first_name": "Sam", "last_name": "Lee",
		"title": "Engineer", "company": "Acme", "phone": "+61 412 345 678", "linkedin_url": "linkedin.com/in/sam-lee"})
	if lead["record_id"] != sam {
		t.Fatalf("lead joined %v, not Sam %s", lead["record_id"], sam)
	}
	rec := owner.must(200, "GET", base+"/records/"+sam, nil)
	a := rec["attributes"].(map[string]any)
	if rec["name"] != "Sam Lee" || a["first_name"] != "Sam" || a["last_name"] != "Lee" || a["job_title"] != "CTO" ||
		a["linkedin_url"] != "https://linkedin.com/in/sam-lee" {
		t.Fatalf("person: %v", rec)
	}
	companies, _ := a["companies"].([]any)
	if len(companies) != 1 {
		t.Fatalf("companies: %v", a)
	}
	acme := owner.must(200, "GET", base+"/records/"+companies[0].(string), nil)
	if acme["type"] != "account" || acme["name"] != "Acme" {
		t.Fatalf("company: %v", acme)
	}
	var phones int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM record_identities WHERE record_id = $1 AND namespace = 'phone' AND value = '+61412345678'`, sam).Scan(&phones); err != nil || phones != 1 {
		t.Fatalf("phone: %d %v", phones, err)
	}

	// Editing the lead updates what the lead gave, never what a person set.
	owner.must(200, "PATCH", base+"/leads/"+lead["id"].(string), map[string]any{"first_name": "Samuel", "title": "VP Engineering"})
	a = owner.must(200, "GET", base+"/records/"+sam, nil)["attributes"].(map[string]any)
	if a["first_name"] != "Samuel" || a["job_title"] != "CTO" {
		t.Fatalf("after editing the lead: %v", a)
	}

	// Merge tags read the person: the job title a person set goes out.
	vars := map[string]string{"first_name": "Samuel", "title": "VP Engineering", "company": "Acme"}
	if err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error { return customers.PersonVars(ctx, tx, lead["id"].(string), vars) }); err != nil {
		t.Fatal(err)
	}
	if vars["title"] != "CTO" || vars["first_name"] != "Samuel" || vars["company"] != "Acme" || vars["linkedin_url"] != "https://linkedin.com/in/sam-lee" {
		t.Fatalf("merge tags: %v", vars)
	}

	// Someone on a personal mailbox gets no company from it.
	kim := owner.must(201, "POST", base+"/leads", map[string]any{"email": "kim.lee@gmail.com", "first_name": "Kim", "company": "Kim Consulting"})
	if a := owner.must(200, "GET", base+"/records/"+kim["record_id"].(string), nil)["attributes"].(map[string]any); a["companies"] != nil {
		t.Fatalf("a free mailbox made a company: %v", a)
	}
	if n := len(owner.must(200, "GET", base+"/records?type=account", nil)["data"].([]any)); n != 1 {
		t.Fatalf("companies: %d", n)
	}

	// The backfill job copies what older leads know.
	if _, err := e.pool.Exec(ctx, `UPDATE records SET attributes = attributes - 'last_name', attribute_sources = attribute_sources - 'last_name' WHERE id = $1`, sam); err != nil {
		t.Fatal(err)
	}
	if err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		_, err := customers.SyncLeads(ctx, tx, ws, []string{lead["id"].(string)})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if a := owner.must(200, "GET", base+"/records/"+sam, nil)["attributes"].(map[string]any); fmt.Sprint(a["last_name"]) != "Lee" {
		t.Fatalf("resynced: %v", a)
	}
}
