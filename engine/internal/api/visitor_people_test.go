package api_test

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/track"
)

func TestVisitorPersonReveal(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws

	conn := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "rb2b", "credentials": map[string]any{}})
	set := owner.must(200, "GET", base+"/visitors/settings", nil)
	people := set["people_connections"].([]any)
	if set["reveal_people"] != false || len(people) != 1 || people[0].(map[string]any)["id"] != conn["id"] {
		t.Fatalf("settings: %v", set)
	}
	hook := strings.TrimPrefix(people[0].(map[string]any)["webhook_url"].(string), "http://localhost:8080")
	if !strings.HasPrefix(hook, "/v1/visitor-hooks/"+conn["id"].(string)+"/") {
		t.Fatalf("webhook url: %v", hook)
	}
	send := func(path, body string) (int, map[string]any) {
		t.Helper()
		req := httptest.NewRequest("POST", path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		var out map[string]any
		json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	ana := `{"First Name":"Ana","Last Name":"Lee","Title":"Head of Growth","Business Email":"ana@northpeak.example",
		"LinkedIn URL":"https://www.linkedin.com/in/ana-example","Company Name":"Northpeak","Website":"northpeak.example",
		"Captured URL":"https://acme.example/pricing?utm_source=x","Seen At":"2026-10-27T09:00:00Z"}`

	// A forged address is refused.
	if code, _ := send("/v1/visitor-hooks/"+conn["id"].(string)+"/not-the-token", ana); code != 404 {
		t.Fatalf("forged: %d", code)
	}
	// Off: nothing is kept.
	if code, out := send(hook, ana); code != 200 || out["ignored"] != "off" || out["added"] != 0.0 {
		t.Fatalf("off: %d %v", code, out)
	}
	if n := len(owner.must(200, "GET", base+"/records?review=all&type=person", nil)["data"].([]any)); n != 0 {
		t.Fatalf("records while off: %d", n)
	}
	// Turning it on needs the notice.
	owner.must(400, "PUT", base+"/visitors/settings", map[string]any{"reveal_people": true})
	set = owner.must(200, "PUT", base+"/visitors/settings", map[string]any{"reveal_people": true, "accept_people_notice": true})
	if set["reveal_people"] != true || set["people_notice_accepted_at"] == nil {
		t.Fatalf("on: %v", set)
	}

	// A known customer stays as they were; a new person is held.
	owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Bo Chan", "origin": "first_party",
		"identities": []map[string]any{{"namespace": "email", "value": "bo@acme.example"}}})
	code, out := send(hook, `[`+ana+`,{"Business Email":"bo@acme.example","First Name":"Bo"},{"First Name":"No email"},{"Business Email":"me@`+"example.com"+`"}]`)
	if code != 200 || out["received"] != 3.0 || out["added"] != 2.0 || out["known"] != 1.0 || out["skipped"] != 0.0 {
		t.Fatalf("hook: %d %v", code, out)
	}
	// The people list leaves held people out.
	list := owner.must(200, "GET", base+"/records?type=person", nil)["data"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["name"] != "Bo Chan" {
		t.Fatalf("people: %v", list)
	}
	held := owner.must(200, "GET", base+"/records?review=needs_review&acquired_via=rb2b", nil)["data"].([]any)
	if len(held) != 2 {
		t.Fatalf("held: %v", held)
	}
	var anaID string
	for _, h := range held {
		m := h.(map[string]any)
		c := m["contactability"].(map[string]any)
		if m["needs_review"] != true || m["origin"] != "prospect" || c["acquired_via"] != "rb2b" || c["basis"] != "third_party" ||
			c["allowed"].(map[string]any)["cold_outreach"] != false {
			t.Fatalf("held record: %v", m)
		}
		if m["name"] == "Ana Lee" {
			anaID = m["id"].(string)
			if a := m["attributes"].(map[string]any); a["job_title"] != "Head of Growth" || a["linkedin_url"] != "https://www.linkedin.com/in/ana-example" {
				t.Fatalf("attributes: %v", a)
			}
		}
	}
	if anaID == "" {
		t.Fatalf("no record for ana: %v", held)
	}
	if n := int(owner.must(200, "GET", base+"/visitors/settings", nil)["people_to_review"].(float64)); n != 2 {
		t.Fatalf("to review: %d", n)
	}

	// Nobody held is emailed, not even the same person sent again.
	if code, out := send(hook, ana); code != 200 || out["added"] != 0.0 || out["known"] != 1.0 {
		t.Fatalf("again: %d %v", code, out)
	}
	check := owner.must(200, "POST", base+"/recipients/check", map[string]any{"emails": []string{"ana@northpeak.example", "bo@acme.example"}})["data"].([]any)
	if c := check[0].(map[string]any); c["allowed"] != false || c["reasons"].([]any)[0] != "needs_review" {
		t.Fatalf("held check: %v", c)
	}
	if c := check[1].(map[string]any); c["allowed"] != true {
		t.Fatalf("customer check: %v", c)
	}

	// Approving lists them and lets cold email through; rejecting archives.
	rec := owner.must(200, "POST", base+"/records/"+anaID+"/review", map[string]any{"decision": "approve"})
	if rec["needs_review"] != false {
		t.Fatalf("approved: %v", rec)
	}
	owner.must(409, "POST", base+"/records/"+anaID+"/review", map[string]any{"decision": "approve"})
	owner.must(400, "POST", base+"/records/"+anaID+"/review", map[string]any{"decision": "maybe"})
	if c := owner.must(200, "POST", base+"/recipients/check", map[string]any{"emails": []string{"ana@northpeak.example"}})["data"].([]any)[0].(map[string]any); c["allowed"] != true {
		t.Fatalf("approved check: %v", c)
	}
	for _, h := range held {
		if m := h.(map[string]any); m["id"] != anaID {
			if rec := owner.must(200, "POST", base+"/records/"+m["id"].(string)+"/review", map[string]any{"decision": "reject"}); rec["archived"] != true {
				t.Fatalf("rejected: %v", rec)
			}
		}
	}

	// Removing the connection turns its address off.
	owner.must(204, "DELETE", base+"/integrations/"+conn["id"].(string), nil)
	if code, _ := send(hook, ana); code != 404 {
		t.Fatalf("after removal: %d", code)
	}

	names := map[string]int{}
	for _, ev := range got() {
		names[ev.Name]++
	}
	if names["visitor.people_ignored"] != 1 || names["visitor.person_revealed"] != 4 || names["record.review_approved"] != 1 || names["record.review_rejected"] != 1 {
		t.Fatalf("events: %v", names)
	}
}
