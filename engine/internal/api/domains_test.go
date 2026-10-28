package api_test

import (
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

func issueCodes(d map[string]any) string {
	var codes []string
	for _, i := range d["issues"].([]any) {
		codes = append(codes, i.(map[string]any)["code"].(string))
	}
	return strings.Join(codes, ",")
}

func TestDomainsAddCheckAndSettings(t *testing.T) {
	e := freshEnv(t)
	// good.test is set up properly; bare.test has nothing; listed.test is on the DBL.
	e.dns.setMX("good.test", []*net.MX{{Host: "alt1.aspmx.l.google.com.", Pref: 5}, {Host: "aspmx.l.google.com.", Pref: 1}})
	e.dns.setTXT("good.test", []string{"v=spf1 include:_spf.google.com ~all"})
	e.dns.setTXT("google._domainkey.good.test", []string{"v=DKIM1; k=rsa; p=MIIBIjAN"})
	e.dns.setTXT("_dmarc.good.test", []string{"v=DMARC1; p=quarantine; rua=mailto:d@good.test"})
	e.dns.setMX("listed.test", []*net.MX{{Host: "alt1.aspmx.l.google.com.", Pref: 5}, {Host: "aspmx.l.google.com.", Pref: 1}})
	e.dns.setTXT("listed.test", []string{"v=spf1 include:spf.example.net ~all"})
	e.dns.setTXT("_dmarc.listed.test", []string{"v=DMARC1; p=none"})
	e.dns.setHost("listed.test.dbl.spamhaus.org", []string{"127.0.1.2"})
	e.dns.setHost("listed.test.multi.surbl.org", []string{"127.0.0.1"}) // refused
	e.runJobs(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws

	owner.must(400, "POST", base+"/sending-domains", map[string]any{"domain": "not a domain"})
	added := owner.must(201, "POST", base+"/sending-domains", map[string]any{"domain": "https://Good.test/pricing"})
	// The check runs in the background and can finish before the response is read.
	if added["domain"] != "good.test" || added["source"] != "manual" || (added["health"] != "pending" && added["checked_at"] == nil) {
		t.Fatalf("added: %v", added)
	}
	owner.must(409, "POST", base+"/sending-domains", map[string]any{"domain": "good.test"})
	owner.must(201, "POST", base+"/sending-domains", map[string]any{"domain": "someone@bare.test"})
	owner.must(201, "POST", base+"/sending-domains", map[string]any{"domain": "@listed.test"})

	checked := func(m map[string]any) bool { return m["checked_at"] != nil }
	good := waitFor(t, owner, base+"/sending-domains/good.test", checked)
	if good["health"] != "healthy" || good["dkim_selector"] != "google" || good["dmarc_policy"] != "quarantine" || issueCodes(good) != "no_mailboxes" {
		t.Fatalf("good: %v", good)
	}
	if hosts := fmt.Sprint(good["mx_hosts"]); hosts != "[aspmx.l.google.com alt1.aspmx.l.google.com]" {
		t.Fatalf("mx hosts by preference: %s", hosts)
	}
	if len(good["daily"].([]any)) != 14 {
		t.Fatalf("daily series: %v", good["daily"])
	}

	bare := waitFor(t, owner, base+"/sending-domains/bare.test", checked)
	if bare["health"] != "critical" || issueCodes(bare) != "spf_missing,dkim_missing,dmarc_missing,mx_missing,no_mailboxes" {
		t.Fatalf("bare: %s %v", issueCodes(bare), bare)
	}
	rec := bare["issues"].([]any)[2].(map[string]any)["record"].(map[string]any)
	if rec["host"] != "_dmarc" || rec["value"] != "v=DMARC1; p=none;" {
		t.Fatalf("dmarc fix record: %v", rec)
	}

	listed := waitFor(t, owner, base+"/sending-domains/listed.test", checked)
	if listed["health"] != "critical" || !strings.HasPrefix(issueCodes(listed), "blocklisted,dkim_missing,spf_provider") {
		t.Fatalf("listed: %s", issueCodes(listed))
	}
	lists := fmt.Sprint(listed["blocklists"])
	if !strings.Contains(lists, "status:listed") || !strings.Contains(lists, "status:unknown") {
		t.Fatalf("blocklists: %s", lists)
	}

	// Settings.
	p := base + "/sending-domains/good.test"
	got := owner.must(200, "PATCH", p, map[string]any{"status": "paused", "daily_limit": 120, "notes": "Bought from Cheap Inboxes"})
	if got["status"] != "paused" || got["daily_limit"] != 120.0 || got["notes"] != "Bought from Cheap Inboxes" || !strings.HasPrefix(issueCodes(got), "paused") {
		t.Fatalf("patch: %v", got)
	}
	if got = owner.must(200, "PATCH", p, map[string]any{"daily_limit": nil, "status": "active"}); got["daily_limit"] != nil || got["status"] != "active" {
		t.Fatalf("clear cap: %v", got)
	}
	owner.must(200, "PATCH", p, map[string]any{"notes": ""})
	owner.must(400, "PATCH", p, map[string]any{"daily_limit": 0})
	owner.must(400, "PATCH", p, map[string]any{"status": "error"})
	owner.must(400, "PATCH", p, map[string]any{"mailbox_daily_limit": 501})
	owner.must(404, "PATCH", base+"/sending-domains/nope.test", map[string]any{"notes": "x"})

	if q := owner.must(202, "POST", base+"/sending-domains/check", nil); q["queued"] != 3.0 {
		t.Fatalf("check all: %v", q)
	}
	owner.must(202, "POST", p+"/check", nil)
	owner.must(404, "POST", base+"/sending-domains/nope.test/check", nil)

	// A daily re-check is scheduled for each domain, once.
	var scheduled int
	if err := e.pool.QueryRow(t.Context(), `SELECT count(*) FROM jobs WHERE kind = 'domain.check' AND run_at > now() + interval '23 hours' AND payload->>'domain' = 'good.test'`).Scan(&scheduled); err != nil {
		t.Fatal(err)
	}
	if scheduled != 1 {
		t.Fatalf("scheduled re-checks: %d", scheduled)
	}

	// Removing.
	owner.must(204, "DELETE", base+"/sending-domains/bare.test", nil)
	owner.must(404, "GET", base+"/sending-domains/bare.test", nil)
	owner.must(404, "DELETE", base+"/sending-domains/bare.test", nil)

	stats := owner.must(200, "GET", base+"/stats", nil)["domains"].(map[string]any)
	if stats["total"] != 2.0 || stats["need_attention"] != 1.0 || stats["paused"] != 0.0 {
		t.Fatalf("stats: %v", stats)
	}

	events := owner.must(200, "GET", base+"/audit-events", nil)["data"].([]any)
	seen := map[string]bool{}
	for _, ev := range events {
		seen[ev.(map[string]any)["action"].(string)] = true
	}
	for _, a := range []string{"domain.added", "domain.paused", "domain.resumed", "domain.updated", "domain.removed"} {
		if !seen[a] {
			t.Errorf("no %s audit event", a)
		}
	}

	// A read token reads but cannot change; another workspace sees nothing.
	tok := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "r", "scopes": []string{"mailboxes.read"}})
	reader := &client{t: t, h: e.h, bearer: tok["token"].(string)}
	reader.must(200, "GET", p, nil)
	reader.must(403, "PATCH", p, map[string]any{"status": "paused"})
	reader.must(403, "POST", base+"/sending-domains", map[string]any{"domain": "new.test"})
	other := owner.must(201, "POST", "/v1/workspaces", map[string]any{"name": "Other"})
	ob := "/v1/workspaces/" + other["id"].(string)
	owner.must(404, "GET", ob+"/sending-domains/good.test", nil)
	owner.must(404, "PATCH", ob+"/sending-domains/good.test", map[string]any{"status": "paused"})
	if l := owner.must(200, "GET", ob+"/sending-domains", nil)["data"].([]any); len(l) != 0 {
		t.Fatalf("leaked domains: %v", l)
	}
}

func TestDomainPauseAndCapHoldSends(t *testing.T) {
	e := freshEnv(t)
	clock := &fakeClock{}
	clock.Set(time.Date(2030, 1, 8, 10, 0, 0, 0, time.UTC))
	owner, base, srv := sendingSetup(t, e, clock)
	p := base + "/sending-domains/acme.test"

	d := owner.must(200, "GET", p, nil)
	if d["mailboxes"] != 2.0 || d["mailbox_counts"].(map[string]any)["active"] != 2.0 || d["daily_capacity"] != 60.0 {
		t.Fatalf("domain: %v", d)
	}
	// Mailbox settings for the whole domain.
	owner.must(200, "PATCH", p, map[string]any{"mailbox_daily_limit": 40, "mailbox_min_gap_seconds": 0})
	for _, m := range owner.must(200, "GET", base+"/mailboxes?domain=acme.test", nil)["data"].([]any) {
		if mm := m.(map[string]any); mm["daily_limit"] != 40.0 || mm["min_gap_seconds"] != 0.0 {
			t.Fatalf("mailbox not updated: %v", mm)
		}
	}
	// A domain with mailboxes cannot be removed.
	owner.must(409, "DELETE", p, nil)

	// The domain cap holds the whole domain to one send.
	if got := owner.must(200, "PATCH", p, map[string]any{"daily_limit": 1}); got["daily_capacity"] != 1.0 {
		t.Fatalf("capacity under cap: %v", got)
	}
	_, imp := owner.raw("POST", base+"/leads/import?list=L", "text/csv", "email\none@one-io.example\ntwo@two-io.example\nthree@three-io.example\n")
	seq := owner.must(201, "POST", base+"/sequences", map[string]any{
		"name": "Capped", "target_daily": 1000, "send_days": []int{1, 2, 3, 4, 5, 6, 7},
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
		t.Fatalf("sent under a cap of 1: %d", n)
	}
	reasons := map[string]int{}
	for _, en := range enrollments(t, owner, base, seqID) {
		reasons[fmt.Sprint(en["status"], ":", en["status_reason"])]++
	}
	if reasons["completed:<nil>"] != 1 || reasons["active:domain_daily_limit"] != 2 {
		t.Fatalf("reasons: %v", reasons)
	}

	// Pausing the domain holds everything, even with the cap lifted.
	owner.must(200, "PATCH", p, map[string]any{"daily_limit": nil, "status": "paused"})
	clock.Add(2 * time.Hour)
	for range 2 {
		e.pump(t)
		clock.Add(20 * time.Minute)
	}
	if n := len(srv.Messages()); n != 1 {
		t.Fatalf("sent while paused: %d", n)
	}
	mb := owner.must(200, "GET", base+"/mailboxes", nil)["data"].([]any)[0].(map[string]any)
	owner.must(409, "POST", base+"/mailboxes/"+mb["id"].(string)+"/test-send", map[string]any{"to": "me@acme.test"})

	// Resuming sends the rest.
	owner.must(200, "PATCH", p, map[string]any{"status": "active"})
	for range 3 {
		e.pump(t)
		clock.Add(20 * time.Minute)
	}
	if n := len(srv.Messages()); n != 3 {
		t.Fatalf("sent after resume: %d", n)
	}
}
