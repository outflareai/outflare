package api_test

import (
	"testing"
	"time"
)

// An importer that still names a person merged into another keeps working:
// the conversation moved with the merge, and the next send lands on it.
func TestSalesBridgeFollowsAMergedRecord(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	person := func(name, email string) map[string]any {
		return owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": name, "identities": []map[string]any{{"namespace": "email", "value": email}}})
	}
	old, kept := person("Ana Example", "ana@customer.example"), person("Ana B. Example", "ana.b@customer.example")
	root := "/v1/brands/" + old["brand_id"].(string) + "/sales"
	integration := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "emailbison", "credentials": map[string]string{"base_url": "https://send.acme.test", "api_key": "eb-replies"}})["id"].(string)
	send := func(want int, provider string) map[string]any {
		t.Helper()
		return owner.must(want, "POST", root+"/bridge", map[string]any{"source": "test", "source_id": "merged", "record_id": old["id"], "integration_id": integration, "sender_id": "1", "reply_id": "501",
			"messages": []map[string]any{{"direction": "incoming", "from": "ana@customer.example", "to": []string{"sender@northpeak.example"}, "cc": []string{}, "subject": "Example conversation", "body": "Example body " + provider,
				"message_id": "<" + provider + "@customer.example>", "provider_id": provider, "occurred_at": time.Now().UTC().Format(time.RFC3339)}}})
	}
	lead := send(201, "501")
	owner.must(200, "POST", base+"/records/"+kept["id"].(string)+"/merge", map[string]any{"from": old["id"]})
	again := send(201, "502")
	if again["id"] != lead["id"] || again["record_id"] != kept["id"] {
		t.Fatalf("after the merge: %v", again)
	}
}
