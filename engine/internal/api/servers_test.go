package api_test

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestSendingServers(t *testing.T) {
	e := freshEnv(t)
	clock := &fakeClock{}
	clock.Set(time.Date(2030, 1, 8, 10, 0, 0, 0, time.UTC))
	owner, base, srv := sendingSetup(t, e, clock)

	// Both mailboxes sign in to the same host, so they share one server.
	list := owner.must(200, "GET", base+"/sending-servers", nil)["data"].([]any)
	if len(list) != 1 {
		t.Fatalf("servers: %v", list)
	}
	id := list[0].(map[string]any)["id"].(string)
	p := base + "/sending-servers/" + id
	checked := func(m map[string]any) bool { return m["checked_at"] != nil }
	sv := waitFor(t, owner, p, checked)
	if sv["host"] != srv.Host || fmt.Sprint(sv["ips"]) != "["+srv.Host+"]" || sv["ips_source"] != "dns" ||
		sv["mailboxes"] != 2.0 || sv["daily_capacity"] != 60.0 || sv["health"] != "healthy" || fmt.Sprint(sv["domains"]) != "[acme.test]" {
		t.Fatalf("server: %v", sv)
	}
	if lists := fmt.Sprint(sv["blocklists"]); !strings.Contains(lists, "zone:zen.spamhaus.org") || strings.Contains(lists, "status:listed") {
		t.Fatalf("blocklists: %s", lists)
	}
	for _, m := range owner.must(200, "GET", base+"/mailboxes?server_id="+id, nil)["data"].([]any) {
		if mm := m.(map[string]any); mm["server_id"] != id || mm["server_host"] != srv.Host {
			t.Fatalf("mailbox server: %v", mm)
		}
	}
	owner.must(400, "GET", base+"/mailboxes?server_id=nope", nil)

	// The server cap holds all of its mailboxes to one send.
	owner.must(200, "PATCH", base+"/sending-domains/acme.test", map[string]any{"mailbox_min_gap_seconds": 0})
	if got := owner.must(200, "PATCH", p, map[string]any{"daily_limit": 1, "name": "Mailreef box 1"}); got["daily_capacity"] != 1.0 || got["name"] != "Mailreef box 1" {
		t.Fatalf("capped: %v", got)
	}
	_, imp := owner.raw("POST", base+"/leads/import?list=L", "text/csv", "email\none@one-io.example\ntwo@two-io.example\nthree@three-io.example\n")
	seq := owner.must(201, "POST", base+"/sequences", map[string]any{
		"name": "Server capped", "target_daily": 1000, "send_days": []int{1, 2, 3, 4, 5, 6, 7},
		"steps": []map[string]any{{"subject": "Hello", "body": "Hi there"}},
	})
	seqID := seq["id"].(string)
	owner.must(200, "POST", base+"/sequences/"+seqID+"/enrollments", map[string]any{"list_id": imp["list_id"]})
	owner.must(200, "PATCH", base+"/sequences/"+seqID, map[string]any{"status": "active"})
	for range 3 {
		e.pump(t)
		clock.Add(5 * time.Minute)
	}
	if n := len(srv.Messages()); n != 1 {
		t.Fatalf("sent under a server cap of 1: %d", n)
	}
	reasons := map[string]int{}
	for _, en := range enrollments(t, owner, base, seqID) {
		reasons[fmt.Sprint(en["status"], ":", en["status_reason"])]++
	}
	if reasons["active:server_daily_limit"] != 2 {
		t.Fatalf("reasons: %v", reasons)
	}

	// A blocklisting pauses the server on its own and holds every send.
	owner.must(200, "PATCH", p, map[string]any{"daily_limit": nil})
	e.dns.setHost("1.0.0.127.zen.spamhaus.org", []string{"127.0.0.2"})
	owner.must(202, "POST", p+"/check", nil)
	sv = waitFor(t, owner, p, func(m map[string]any) bool { return m["status"] == "paused" })
	if sv["paused_by"] != "blocklist" || !strings.HasPrefix(issueCodes(sv), "ip_blocklisted") || sv["health"] != "critical" ||
		!strings.Contains(fmt.Sprint(sv["status_reason"]), "on Spamhaus ZEN") {
		t.Fatalf("auto-paused: %s %v", issueCodes(sv), sv)
	}
	clock.Add(2 * time.Hour)
	for range 2 {
		e.pump(t)
		clock.Add(20 * time.Minute)
	}
	if n := len(srv.Messages()); n != 1 {
		t.Fatalf("sent while blocklisted: %d", n)
	}
	reasons = map[string]int{}
	for _, en := range enrollments(t, owner, base, seqID) {
		reasons[fmt.Sprint(en["status"], ":", en["status_reason"])]++
	}
	if reasons["active:server_paused"] != 2 {
		t.Fatalf("reasons while paused: %v", reasons)
	}
	mb := owner.must(200, "GET", base+"/mailboxes", nil)["data"].([]any)[0].(map[string]any)
	owner.must(409, "POST", base+"/mailboxes/"+mb["id"].(string)+"/test-send", map[string]any{"to": "me@acme.test"})

	// Once delisted it resumes on its own, and the rest go out.
	e.dns.setHost("1.0.0.127.zen.spamhaus.org", nil)
	owner.must(202, "POST", base+"/sending-servers/check", nil)
	sv = waitFor(t, owner, p, func(m map[string]any) bool { return m["status"] == "active" })
	if sv["paused_by"] != nil || sv["status_reason"] != nil {
		t.Fatalf("auto-resumed: %v", sv)
	}
	for range 3 {
		e.pump(t)
		clock.Add(20 * time.Minute)
	}
	if n := len(srv.Messages()); n != 3 {
		t.Fatalf("sent after resume: %d", n)
	}

	// A person's pause stays until a person resumes, whatever the checks say.
	if got := owner.must(200, "PATCH", p, map[string]any{"status": "paused", "notes": "Moving IPs"}); got["paused_by"] != "person" {
		t.Fatalf("manual pause: %v", got)
	}
	owner.must(202, "POST", p+"/check", nil)
	time.Sleep(200 * time.Millisecond)
	if got := owner.must(200, "GET", p, nil); got["status"] != "paused" {
		t.Fatalf("a clear check lifted a manual pause: %v", got)
	}
	owner.must(200, "PATCH", p, map[string]any{"status": "active"})

	// Sending IPs set by hand, then back to DNS.
	got := owner.must(200, "PATCH", p, map[string]any{"ips": []string{"203.0.113.9", " 203.0.113.8", "203.0.113.9", "2001:db8::1"}})
	if fmt.Sprint(got["ips"]) != "[2001:db8::1 203.0.113.8 203.0.113.9]" || got["ips_source"] != "manual" {
		t.Fatalf("manual ips: %v", got)
	}
	sv = waitFor(t, owner, p, func(m map[string]any) bool {
		return strings.Contains(fmt.Sprint(m["blocklists"]), "203.0.113.8")
	})
	if !strings.Contains(fmt.Sprint(sv["blocklists"]), "IPv6 addresses are not checked yet") {
		t.Fatalf("ipv6 listing: %v", sv["blocklists"])
	}
	if got := owner.must(200, "PATCH", p, map[string]any{"ips": nil}); got["ips_source"] != "dns" {
		t.Fatalf("back to dns: %v", got)
	}
	owner.must(400, "PATCH", p, map[string]any{"ips": []string{"not-an-ip"}})
	owner.must(400, "PATCH", p, map[string]any{"ips": []string{}})
	owner.must(400, "PATCH", p, map[string]any{"daily_limit": 0})
	owner.must(400, "PATCH", p, map[string]any{"status": "error"})
	owner.must(404, "PATCH", base+"/sending-servers/00000000-0000-0000-0000-000000000000", map[string]any{"notes": "x"})
	owner.must(404, "GET", base+"/sending-servers/nope", nil)

	// A server with mailboxes cannot be removed.
	owner.must(409, "DELETE", p, nil)

	// A daily re-check is scheduled.
	var scheduled int
	if err := e.pool.QueryRow(t.Context(), `SELECT count(*) FROM jobs WHERE kind = 'server.check' AND run_at > now() + interval '23 hours' AND payload->>'server_id' = $1`, id).Scan(&scheduled); err != nil {
		t.Fatal(err)
	}
	if scheduled != 1 {
		t.Fatalf("scheduled re-checks: %d", scheduled)
	}

	events := owner.must(200, "GET", base+"/audit-events", nil)["data"].([]any)
	seen := map[string]bool{}
	for _, ev := range events {
		seen[ev.(map[string]any)["action"].(string)] = true
	}
	for _, a := range []string{"sending_server.auto_paused", "sending_server.auto_resumed", "sending_server.paused", "sending_server.resumed", "sending_server.updated"} {
		if !seen[a] {
			t.Errorf("no %s audit event", a)
		}
	}

	// A read token reads but cannot change; another workspace sees nothing.
	tok := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "r", "scopes": []string{"mailboxes.read"}})
	reader := &client{t: t, h: e.h, bearer: tok["token"].(string)}
	reader.must(200, "GET", p, nil)
	reader.must(403, "PATCH", p, map[string]any{"status": "paused"})
	reader.must(403, "POST", p+"/check", nil)
	other := owner.must(201, "POST", "/v1/workspaces", map[string]any{"name": "Other"})
	ob := "/v1/workspaces/" + other["id"].(string)
	owner.must(404, "GET", ob+"/sending-servers/"+id, nil)
	owner.must(404, "PATCH", ob+"/sending-servers/"+id, map[string]any{"status": "paused"})
	owner.must(404, "DELETE", ob+"/sending-servers/"+id, nil)
	if l := owner.must(200, "GET", ob+"/sending-servers", nil)["data"].([]any); len(l) != 0 {
		t.Fatalf("leaked servers: %v", l)
	}
}
