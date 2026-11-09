package api_test

import (
	"fmt"
	"testing"
	"time"
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

	// The receiver fails a real event, then takes it when sent again. A
	// test it fails meanwhile is not counted.
	rc.mu.Lock()
	rc.status = 500
	rc.mu.Unlock()
	owner.must(201, "POST", base+"/leads", map[string]any{"email": "ana@acme.example"})
	var delivery string
	deadline := time.Now().Add(10 * time.Second)
	for delivery == "" && time.Now().Before(deadline) {
		if ds := owner.must(200, "GET", base+"/webhooks/"+id+"/deliveries", nil)["data"].([]any); len(ds) == 1 && ds[0].(map[string]any)["attempts"] == 1.0 {
			delivery = fmt.Sprint(int64(ds[0].(map[string]any)["id"].(float64)))
		}
		time.Sleep(20 * time.Millisecond)
	}
	if delivery == "" {
		t.Fatal("the event was never tried")
	}
	owner.must(200, "POST", base+"/webhooks/"+id+"/test", nil)
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
	if first["ok"] != false || first["status_code"] != 500.0 || first["error"] == nil || first["manual"] != false {
		t.Errorf("first try = %v", first)
	}
	if second["ok"] != true || second["status_code"] != 200.0 || second["error"] != nil || second["manual"] != true {
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
