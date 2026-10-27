package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// fakeSmartlead has one campaign with two steps, three leads and two
// email accounts, for the key "sl-key".
type fakeSmartlead struct{}

func (fakeSmartlead) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("api_key") != "sl-key" {
		w.WriteHeader(401)
		return
	}
	var out any
	switch strings.TrimPrefix(r.URL.Path, "/api/v1") {
	case "/campaigns":
		out = []map[string]any{{"id": 42, "name": "Fintech CFOs", "status": "ACTIVE", "max_leads_per_day": 40, "timezone": "Australia/Sydney"}}
	case "/campaigns/42/sequences":
		out = []map[string]any{
			{"seq_number": 1, "seq_delay_details": map[string]int{"delay_in_days": 0}, "subject": "Quick question, {{first_name}}", "email_body": "<p>Hi {{first_name}},</p><p>Saw {{company_name}} is hiring.</p>"},
			{"seq_number": 2, "seq_delay_details": map[string]int{"delay_in_days": 3}, "subject": "", "email_body": "Bumping this.", "sequence_variants": []map[string]string{{"subject": "", "email_body": "Bumping this."}, {"subject": "", "email_body": "Any thoughts?"}}},
		}
	case "/campaigns/42/email-accounts":
		out = []map[string]string{{"from_email": "sam@send-acme.test"}}
	case "/campaigns/42/leads":
		out = map[string]any{"total_leads": "3", "data": []map[string]any{
			{"status": "STARTED", "lead": map[string]any{"email": "ann@fin.test", "first_name": "Ann", "company_name": "Fin", "custom_fields": map[string]string{"city": "Perth"}}},
			{"status": "BLOCKED", "lead": map[string]any{"email": "bob@fin.test", "first_name": "Bob"}},
			{"status": "COMPLETED", "lead": map[string]any{"email": "cat@fin.test", "first_name": "Cat"}},
		}}
	case "/email-accounts/":
		out = []map[string]any{
			{"from_email": "sam@send-acme.test", "from_name": "Sam", "type": "GMAIL", "message_per_day": 30, "is_smtp_success": true, "is_imap_success": true, "warmup_details": map[string]string{"status": "ACTIVE"}},
			{"from_email": "kim@send-acme.test", "from_name": "Kim", "type": "SMTP", "message_per_day": 20, "is_smtp_success": false, "is_imap_success": true},
		}
	default:
		http.NotFound(w, r)
		return
	}
	json.NewEncoder(w).Encode(out)
}

func TestImportFromSmartlead(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()

	i := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "smartlead", "credentials": map[string]string{"api_key": "sl-key"}})
	id := i["id"].(string)

	p := owner.must(200, "GET", base+"/integrations/"+id+"/import-preview", nil)
	camps := p["campaigns"].([]any)
	mb := p["mailboxes"].(map[string]any)
	if len(camps) != 1 || len(camps[0].(map[string]any)["steps"].([]any)) != 2 || mb["total"] != 2.0 || len(mb["missing"].([]any)) != 2 {
		t.Fatalf("preview: %v", p)
	}

	// A token that can't manage leads can only import campaigns.
	tok := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "Seq", "scopes": []string{"sequences.manage", "sequences.read"}})["token"].(string)
	(&client{t: t, h: e.h, bearer: tok}).must(403, "POST", base+"/integrations/"+id+"/imports", map[string]any{})

	run := owner.must(202, "POST", base+"/integrations/"+id+"/imports", map[string]any{})
	if err := e.imports.Execute(ctx, ws, run["id"].(string)); err != nil {
		t.Fatal(err)
	}
	got := owner.must(200, "GET", base+"/imports/"+run["id"].(string), nil)
	sum := got["summary"].(map[string]any)
	if got["status"] != "done" {
		t.Fatalf("run: %v", got)
	}
	c := sum["campaigns"].([]any)[0].(map[string]any)
	l := sum["leads"].(map[string]any)
	if c["created"] != true || c["steps"] != 2.0 || c["enrolled"] != 1.0 || l["suppressed"] != 1.0 || l["skipped"] != 2.0 || l["seen"] != 3.0 {
		t.Fatalf("summary: %v", sum)
	}
	if !strings.Contains(strings.Join(toStrings(c["warnings"]), " "), "A/B variants") {
		t.Fatalf("warnings: %v", c["warnings"])
	}

	seq := owner.must(200, "GET", base+"/sequences/"+c["sequence_id"].(string), nil)
	steps := seq["steps"].([]any)
	first := steps[0].(map[string]any)
	if seq["status"] != "draft" || seq["timezone"] != "Australia/Sydney" || first["body"] != "Hi {{first_name}},\n\nSaw {{company}} is hiring." || steps[1].(map[string]any)["delay_days"] != 3.0 {
		t.Fatalf("sequence: %v", seq)
	}
	if out := owner.must(200, "GET", base+"/suppressions", nil)["data"].([]any); len(out) != 1 || out[0].(map[string]any)["value"] != "bob@fin.test" {
		t.Fatalf("suppressions: %v", out)
	}

	// Running it again reuses the sequence and enrolls nobody twice.
	again := owner.must(202, "POST", base+"/integrations/"+id+"/imports", map[string]any{"mailboxes": false})
	if err := e.imports.Execute(ctx, ws, again["id"].(string)); err != nil {
		t.Fatal(err)
	}
	c2 := owner.must(200, "GET", base+"/imports/"+again["id"].(string), nil)["summary"].(map[string]any)["campaigns"].([]any)[0].(map[string]any)
	if c2["created"] != false || c2["sequence_id"] != c["sequence_id"] || c2["enrolled"] != 0.0 {
		t.Fatalf("second import: %v", c2)
	}
}

func toStrings(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}
