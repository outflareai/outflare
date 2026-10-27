package api_test

import "testing"

// A lead is a person: its record shows the meeting and deal logged against
// it, and an unsubscribe from cold email is a withdrawn consent on it until
// the person opts in again.
func TestLeadActivityOnThePersonTimeline(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws

	lead := owner.must(201, "POST", base+"/leads", map[string]any{"email": "sam@acme.example", "first_name": "Sam", "title": "CFO"})
	rec, ok := lead["record_id"].(string)
	if !ok || rec == "" {
		t.Fatalf("lead has no record: %v", lead)
	}
	person := owner.must(200, "GET", base+"/records/"+rec, nil)
	if person["name"] != "Sam" || person["origin"] != "prospect" {
		t.Fatalf("person: %v", person)
	}

	m := owner.must(201, "POST", base+"/meetings", map[string]any{"lead_id": lead["id"], "starts_at": "2026-10-20T10:00:00Z", "title": "Intro"})
	owner.must(201, "POST", base+"/deals", map[string]any{"lead_id": lead["id"], "meeting_id": m["id"], "name": "Acme pilot", "amount_cents": 500000, "currency": "USD"})
	tl := owner.must(200, "GET", base+"/records/"+rec+"/timeline?kinds=meeting,deal", nil)["data"].([]any)
	kinds := map[string]bool{}
	for _, it := range tl {
		kinds[it.(map[string]any)["kind"].(string)] = true
	}
	if !kinds["meeting"] || !kinds["deal"] {
		t.Fatalf("timeline: %v", tl)
	}

	// Sam unsubscribes; the suppression and a withdrawn consent stop
	// marketing email until he signs up and agrees to email.
	owner.must(200, "POST", base+"/suppressions", map[string]any{"values": []string{"sam@acme.example"}, "reason": "unsubscribed"})
	consents := owner.must(200, "GET", base+"/records/"+rec+"/consents", nil)["data"].([]any)
	if len(consents) != 1 || consents[0].(map[string]any)["status"] != "withdrawn" {
		t.Fatalf("consents after unsubscribe: %v", consents)
	}
	check := func() bool {
		d := owner.must(200, "POST", base+"/recipients/check", map[string]any{"emails": []string{"sam@acme.example"}})["data"].([]any)
		return d[0].(map[string]any)["allowed"].(bool)
	}
	if check() {
		t.Fatal("an unsubscribed person may be emailed")
	}
	owner.must(201, "POST", base+"/records/"+rec+"/consents", map[string]any{"channel": "email", "status": "granted", "source": "import"})
	if check() {
		t.Fatal("an imported grant cleared an unsubscribe")
	}
	owner.must(201, "POST", base+"/records/"+rec+"/consents", map[string]any{"channel": "email", "status": "granted", "source": "signup_form"})
	if !check() {
		t.Fatal("opting in again did not clear the unsubscribe")
	}
}
