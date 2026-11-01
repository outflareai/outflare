package api_test

import (
	"fmt"
	"testing"
)

// TestWebhookAttemptsAndActivity: every try of a delivery is kept with its
// answer and time, and an endpoint shows its tries per day and error rate.
func TestWebhookAttemptsAndActivity(t *testing.T) {
	_, owner, ws, _ := webhookEnv(t)
	rc := newReceiver(t)
	base := "/v1/workspaces/" + ws
	hook := owner.must(201, "POST", base+"/webhooks", map[string]any{"url": rc.URL + "/hook", "events": []string{"lead.*"}})
	id := hook["id"].(string)
	if hook["attempts_24h"] != 0.0 || hook["failed_attempts_24h"] != 0.0 {
		t.Fatalf("new endpoint = %v", hook)
	}

	// The receiver fails the test event, then takes it when sent again.
	rc.mu.Lock()
	rc.status = 500
	rc.mu.Unlock()
	test := owner.must(200, "POST", base+"/webhooks/"+id+"/test", nil)
	delivery := fmt.Sprint(int64(test["id"].(float64)))
	rc.mu.Lock()
	rc.status = 200
	rc.mu.Unlock()
	owner.must(200, "POST", base+"/webhooks/"+id+"/deliveries/"+delivery+"/redeliver", nil)

	got := owner.must(200, "GET", base+"/webhooks/"+id+"/deliveries/"+delivery, nil)
	if d := got["delivery"].(map[string]any); d["status"] != "delivered" || d["attempts"] != 2.0 {
		t.Fatalf("delivery = %v", d)
	}
	tries := got["attempts"].([]any)
	if len(tries) != 2 {
		t.Fatalf("attempts = %v", tries)
	}
	first, second := tries[0].(map[string]any), tries[1].(map[string]any)
	if first["ok"] != false || first["status_code"] != 500.0 || first["error"] == nil || first["manual"] != true {
		t.Errorf("first try = %v", first)
	}
	if second["ok"] != true || second["status_code"] != 200.0 || second["error"] != nil {
		t.Errorf("second try = %v", second)
	}
	owner.must(404, "GET", base+"/webhooks/"+id+"/deliveries/999999999", nil)
	owner.must(404, "GET", base+"/webhooks/"+id+"/deliveries/nope", nil)

	if h := owner.must(200, "GET", base+"/webhooks/"+id, nil); h["attempts_24h"] != 2.0 || h["failed_attempts_24h"] != 1.0 {
		t.Errorf("endpoint = %v %v", h["attempts_24h"], h["failed_attempts_24h"])
	}
	act := owner.must(200, "GET", base+"/webhooks/"+id+"/activity", nil)
	days := act["days"].([]any)
	if len(days) != 7 || act["attempts"] != 2.0 || act["failed"] != 1.0 || act["error_rate"] != 0.5 {
		t.Fatalf("activity = %v", act)
	}
	if today := days[6].(map[string]any); today["attempts"] != 2.0 || today["delivered"] != 1.0 || today["failed"] != 1.0 {
		t.Errorf("today = %v", today)
	}
	if n := len(owner.must(200, "GET", base+"/webhooks/"+id+"/activity?days=30", nil)["days"].([]any)); n != 30 {
		t.Errorf("30 days = %d", n)
	}
	owner.must(400, "GET", base+"/webhooks/"+id+"/activity?days=31", nil)
}
