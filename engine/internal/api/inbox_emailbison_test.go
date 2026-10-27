package api_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/track"
)

// ebSent records the replies sent through the fake EmailBison.
var ebSent struct {
	sync.Mutex
	calls []string
}

// ebReplies serves the replies folder: only the key "eb-replies" has any.
func ebReplies(w http.ResponseWriter, r *http.Request, some bool) {
	if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/reply") {
		var body struct {
			Message  string `json:"message"`
			SenderID int    `json:"sender_email_id"`
			To       []struct {
				Email string `json:"email_address"`
			} `json:"to_emails"`
		}
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &body)
		to := ""
		if len(body.To) == 1 {
			to = body.To[0].Email
		}
		ebSent.Lock()
		ebSent.calls = append(ebSent.calls, r.URL.Path+" "+to+" "+string(rune('0'+body.SenderID))+" "+body.Message)
		ebSent.Unlock()
		w.Write([]byte(`{"data":{"success":true}}`))
		return
	}
	if !some || r.URL.Query().Get("folder") != "inbox" {
		w.Write([]byte(`{"data":[],"meta":{"last_page":1}}`))
		return
	}
	now := time.Now().UTC()
	at := func(d time.Duration) string { return now.Add(-d).Format(time.RFC3339) }
	switch r.URL.Query().Get("page") {
	case "1":
		w.Write([]byte(`{"data":[
			{"id":501,"sender_email_id":1,"from_email_address":"Lee@Prospect.test","from_name":"Lee","primary_to_email_address":"ana@send-acme.test",
			 "subject":"Re: Quick question","text_body":"Sounds good, can we talk Tuesday?","date_received":"` + at(time.Minute) + `","folder":"Inbox"},
			{"id":"502","sender_email_id":2,"from_email_address":"gone@prospect.test","subject":"Re: Quick question",
			 "html_body":"<p>Please remove me from your list.</p>","date_received":"` + at(2*time.Minute) + `"},
			{"id":503,"sender_email_id":1,"from_email_address":"away@prospect.test","subject":"Out of office","text_body":"Back Monday.",
			 "automated_reply":true,"date_received":"` + at(3*time.Minute) + `"},
			{"id":504,"sender_email_id":1,"from_email_address":"bo@send-acme.test","subject":"warm","text_body":"hi","date_received":"` + at(4*time.Minute) + `"},
			{"id":506,"sender_email_id":99,"from_email_address":"who@prospect.test","subject":"Re: x","text_body":"hello","date_received":"` + at(5*time.Minute) + `"}],
			"meta":{"current_page":1,"last_page":2},"links":{"next":"x"}}`))
	default:
		w.Write([]byte(`{"data":[{"id":505,"sender_email_id":1,"from_email_address":"old@prospect.test","subject":"Re: old","text_body":"old","date_received":"2020-01-02T03:04:05.000000Z"}],
			"meta":{"current_page":2,"last_page":2},"links":{"next":null}}`))
	}
}

func TestEmailBisonRepliesInTheInbox(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	owner.must(201, "POST", base+"/leads", map[string]any{"email": "lee@prospect.test"})

	i := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "emailbison",
		"credentials": map[string]string{"base_url": "https://send.acme.test", "api_key": "eb-replies"}})
	id := i["id"].(string)
	res := owner.must(200, "POST", base+"/platforms/"+id+"/sync", nil)
	// Lee, the opt-out and the auto-reply; not the workspace's own mail, a
	// sender email it doesn't hold, or a reply from before the first read.
	if res["replies"] != 3.0 {
		t.Fatalf("sync: %v", res)
	}
	if res = owner.must(200, "POST", base+"/platforms/"+id+"/sync", nil); res["replies"] != 0.0 {
		t.Fatalf("second sync stored again: %v", res)
	}

	inbox := map[string]map[string]any{}
	for _, m := range owner.must(200, "GET", base+"/inbox?source=emailbison&kind=all", nil)["data"].([]any) {
		m := m.(map[string]any)
		inbox[m["from"].(string)] = m
	}
	if len(inbox) != 3 {
		t.Fatalf("inbox: %v", inbox)
	}
	lee, gone, away := inbox["lee@prospect.test"], inbox["gone@prospect.test"], inbox["away@prospect.test"]
	if lee == nil || lee["source"] != "emailbison" || lee["lead_id"] == nil || lee["kind"] != "reply" || lee["mailbox_email"] != "ana@send-acme.test" {
		t.Fatalf("lee: %v", lee)
	}
	if gone == nil || gone["category"] != "unsubscribe" || !strings.Contains(gone["body"].(string), "remove me from your list") {
		t.Fatalf("opt-out: %v", gone)
	}
	if away == nil || away["kind"] != "auto_reply" {
		t.Fatalf("auto-reply: %v", away)
	}
	var suppressed bool
	if err := e.pool.QueryRow(context.Background(), `SELECT EXISTS (SELECT 1 FROM suppressions WHERE workspace_id = $1 AND value = 'gone@prospect.test')`, ws).Scan(&suppressed); err != nil || !suppressed {
		t.Fatalf("opt-out not suppressed: %v", err)
	}
	if n := len(named(got(), "inbox.replies_synced")); n != 1 {
		t.Fatalf("inbox.replies_synced %d times", n)
	}
	cl := named(got(), "inbox.classified")
	if len(cl) == 0 || cl[0].Props["source"] != "emailbison" {
		t.Fatalf("inbox.classified: %+v", cl)
	}

	// Answering goes out through EmailBison, from the sender email it
	// arrived at; the suppressed address is refused before anything is sent.
	out := owner.must(200, "POST", base+"/inbox/"+lee["id"].(string)+"/reply", map[string]any{"body": "Tuesday works."})
	if out["status"] != "sent" {
		t.Fatalf("reply: %v", out)
	}
	owner.must(409, "POST", base+"/inbox/"+gone["id"].(string)+"/reply", map[string]any{"body": "Sorry!"})
	ebSent.Lock()
	calls := ebSent.calls
	ebSent.Unlock()
	if len(calls) != 1 || calls[0] != "/api/replies/501/reply lee@prospect.test 1 Tuesday works." {
		t.Fatalf("sent: %q", calls)
	}
}
