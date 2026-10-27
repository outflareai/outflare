package api_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/mailbox/mailtest"
)

type fakeDNS struct {
	mx    map[string][]*net.MX
	txt   map[string][]string
	host  map[string][]string // blocklist answers; anything else is not found
	cname map[string]string
	mu    sync.RWMutex // guards host once jobs are running
}

func (f *fakeDNS) LookupCNAME(_ context.Context, name string) (string, error) {
	if v, ok := f.cname[name]; ok {
		return v + ".", nil
	}
	return "", &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}

// setHost changes a host answer while jobs may be reading; nil removes it.
func (f *fakeDNS) setHost(name string, v []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if v == nil {
		delete(f.host, name)
		return
	}
	f.host[name] = v
}

func (f *fakeDNS) LookupHost(_ context.Context, name string) ([]string, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	if v, ok := f.host[name]; ok {
		return v, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}

func (f *fakeDNS) LookupMX(_ context.Context, name string) ([]*net.MX, error) {
	if v, ok := f.mx[name]; ok {
		return v, nil
	}
	return nil, errors.New("no such host")
}

func (f *fakeDNS) LookupTXT(_ context.Context, name string) ([]string, error) {
	if v, ok := f.txt[name]; ok {
		return v, nil
	}
	return nil, errors.New("no such host")
}

// runJobs works the queue until the test ends.
func (e *env) runJobs(t *testing.T) {
	q := jobs.New(e.pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
	q.PollInterval = 20 * time.Millisecond
	e.mail.Register(q)
	e.send.Register(q)
	e.sync.Register(q)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { q.Run(ctx, 4); close(done) }() //nolint:errcheck
	t.Cleanup(func() { cancel(); <-done })
}

// waitFor polls a mailbox until cond holds.
func waitFor(t *testing.T, c *client, path string, cond func(map[string]any) bool) map[string]any {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		m := c.must(200, "GET", path, nil)
		if cond(m) {
			return m
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting on %s: %v", path, m)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func TestMailboxImportVerifyAndManage(t *testing.T) {
	e := freshEnv(t)
	srv := mailtest.Start(t, map[string]string{"ana@acme.test": "app-pass-1", "bo@acme.test": "app-pass-2"})
	e.mail.Dialer.TLSConfig = srv.ClientTLS
	e.dns.mx["gw.test"] = []*net.MX{{Host: "aspmx.l.google.com."}}
	e.dns.txt["acme.test"] = []string{"v=spf1 include:_spf.google.com ~all"}
	e.dns.txt["_dmarc.acme.test"] = []string{"v=DMARC1; p=none"}
	e.runJobs(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws

	csv := fmt.Sprintf("Email,Password,SMTP Host,SMTP Port,IMAP Host,IMAP Port,Daily Limit,Tag\n"+
		"ana@acme.test,app-pass-1,%[1]s,%[2]d,%[1]s,%[3]d,40,Warm\n"+
		"bo@acme.test,WRONG,%[1]s,%[2]d,%[1]s,%[3]d,,warm\n"+
		"cy@gw.test,pw,,,,,,\n"+
		"dee@nowhere.test,pw,,,,,,\n"+
		"bad email,pw,,,,,,\n", srv.Host, srv.SMTPPort, srv.IMAPPort)

	if code, _ := owner.raw("POST", base+"/mailboxes/import", "application/json", csv); code != 415 {
		t.Fatalf("non-CSV content type: %d", code)
	}
	code, res := owner.raw("POST", base+"/mailboxes/import", "text/csv", csv)
	if code != 200 || res["created"] != 3.0 || res["updated"] != 0.0 {
		t.Fatalf("import: %d %v", code, res)
	}
	errs := res["errors"].([]any)
	if len(errs) != 2 {
		t.Fatalf("errors: %v", errs)
	}
	lines := map[float64]bool{}
	for _, e := range errs {
		lines[e.(map[string]any)["line"].(float64)] = true
	}
	if !lines[5] || !lines[6] {
		t.Fatalf("error lines: %v", errs)
	}

	list := owner.must(200, "GET", base+"/mailboxes", nil)["data"].([]any)
	if len(list) != 3 {
		t.Fatalf("list: %v", list)
	}
	byEmail := map[string]map[string]any{}
	for _, m := range list {
		mm := m.(map[string]any)
		byEmail[mm["email"].(string)] = mm
		if _, leaked := mm["credentials"]; leaked {
			t.Fatal("credentials in response")
		}
	}
	ana, bo, cy := byEmail["ana@acme.test"], byEmail["bo@acme.test"], byEmail["cy@gw.test"]
	if ana["daily_limit"] != 40.0 || bo["daily_limit"] != 30.0 || ana["pool_name"] != "Warm" || bo["pool_id"] != ana["pool_id"] {
		t.Fatalf("limits and pools: %v %v", ana, bo)
	}
	if cy["provider"] != "google" || cy["smtp_host"] != "smtp.gmail.com" || cy["smtp_port"] != 465.0 {
		t.Fatalf("google defaults: %v", cy)
	}

	// The verify jobs sign in to the test servers.
	anaPath := base + "/mailboxes/" + ana["id"].(string)
	boPath := base + "/mailboxes/" + bo["id"].(string)
	got := waitFor(t, owner, anaPath, func(m map[string]any) bool { return m["status"] != "verifying" })
	if got["status"] != "active" || got["smtp_ok"] != true || got["imap_ok"] != true || got["last_verified_at"] == nil {
		t.Fatalf("ana: %v", got)
	}
	got = waitFor(t, owner, boPath, func(m map[string]any) bool { return m["status"] != "verifying" })
	if got["status"] != "error" || got["smtp_ok"] != false || !strings.Contains(fmt.Sprint(got["status_reason"]), "sign-in rejected") {
		t.Fatalf("bo: %v", got)
	}

	// DNS health for the domain.
	acme := waitFor(t, owner, base+"/sending-domains/acme.test", func(m map[string]any) bool { return m["checked_at"] != nil })
	doms := owner.must(200, "GET", base+"/sending-domains", nil)["data"].([]any)
	if len(doms) != 2 || acme["spf"] != "pass" || acme["dkim"] != "missing" || acme["dmarc"] != "none_policy" || acme["mailboxes"] != 2.0 {
		t.Fatalf("domains: %v", doms)
	}

	// Re-importing with the right password fixes bo, keeping its id.
	fixed := fmt.Sprintf("email,password,smtp_host,smtp_port,imap_host,imap_port\nbo@acme.test,app-pass-2,%[1]s,%[2]d,%[1]s,%[3]d\n",
		srv.Host, srv.SMTPPort, srv.IMAPPort)
	if code, res := owner.raw("POST", base+"/mailboxes/import", "text/csv", fixed); code != 200 || res["updated"] != 1.0 {
		t.Fatalf("reimport: %d %v", code, res)
	}
	got = waitFor(t, owner, boPath, func(m map[string]any) bool { return m["status"] == "active" })
	if got["daily_limit"] != 30.0 || got["pool_name"] != "Warm" {
		t.Fatalf("reimport kept settings: %v", got)
	}

	// Pause, then resume re-verifies.
	got = owner.must(200, "PATCH", anaPath, map[string]any{"status": "paused", "daily_limit": 25})
	if got["status"] != "paused" || got["daily_limit"] != 25.0 {
		t.Fatalf("pause: %v", got)
	}
	// Wait for that check to finish, so it can't land after the resume below.
	verified := got["last_verified_at"]
	owner.must(202, "POST", anaPath+"/verify", nil)
	got = waitFor(t, owner, anaPath, func(m map[string]any) bool { return m["last_verified_at"] != verified })
	if got["status"] != "paused" {
		t.Fatalf("verify must not unpause: %v", got)
	}
	// The re-check can finish before the response is read, so the reply may
	// already say active; either way a new check must run.
	verified = got["last_verified_at"]
	got = owner.must(200, "PATCH", anaPath, map[string]any{"status": "active"})
	if got["status"] != "verifying" && got["status"] != "active" {
		t.Fatalf("resume: %v", got)
	}
	waitFor(t, owner, anaPath, func(m map[string]any) bool {
		return m["status"] == "active" && m["last_verified_at"] != verified
	})

	// Pools.
	owner.must(200, "PATCH", anaPath, map[string]any{"pool_id": nil})
	if got = owner.must(200, "GET", anaPath, nil); got["pool_id"] != nil {
		t.Fatalf("pool cleared: %v", got)
	}
	p := owner.must(201, "POST", base+"/mailbox-pools", map[string]any{"name": "Cold"})
	owner.must(409, "POST", base+"/mailbox-pools", map[string]any{"name": "cold"})
	owner.must(200, "PATCH", anaPath, map[string]any{"pool_id": p["id"]})
	pools := owner.must(200, "GET", base+"/mailbox-pools", nil)["data"].([]any)
	if len(pools) != 2 {
		t.Fatalf("pools: %v", pools)
	}
	owner.must(204, "DELETE", base+"/mailbox-pools/"+p["id"].(string), nil)
	if got = owner.must(200, "GET", anaPath, nil); got["pool_id"] != nil {
		t.Fatalf("deleted pool still set: %v", got)
	}
	owner.must(400, "PATCH", anaPath, map[string]any{"daily_limit": 1000})
	owner.must(400, "PATCH", anaPath, map[string]any{"status": "error"})

	// Single add through the API.
	added := owner.must(201, "POST", base+"/mailboxes", map[string]any{
		"email": "eve@gw.test", "password": "pw", "daily_limit": 10,
	})
	if added["provider"] != "google" || added["source"] != "api" {
		t.Fatalf("added: %v", added)
	}
	owner.must(400, "POST", base+"/mailboxes", map[string]any{"email": "x@nowhere.test", "password": "pw"})

	owner.must(204, "DELETE", base+"/mailboxes/"+added["id"].(string), nil)
	owner.must(404, "GET", base+"/mailboxes/"+added["id"].(string), nil)

	// Audit trail.
	events := owner.must(200, "GET", base+"/audit-events", nil)["data"].([]any)
	seen := map[string]bool{}
	for _, ev := range events {
		seen[ev.(map[string]any)["action"].(string)] = true
	}
	for _, want := range []string{"mailboxes.imported", "mailbox.verified", "mailbox.verify_failed", "mailbox.paused", "mailbox.resumed", "mailbox_pool.created", "mailbox.added", "mailbox.deleted"} {
		if !seen[want] {
			t.Errorf("no %s audit event", want)
		}
	}
}

func TestMailboxScopesAndIsolation(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	csv := "email,password,smtp_host,smtp_port,imap_host,imap_port\na@x.test,pw,127.0.0.1,1,127.0.0.1,1\n"
	if code, res := owner.raw("POST", base+"/mailboxes/import", "text/csv", csv); code != 200 || res["created"] != 1.0 {
		t.Fatalf("import: %d %v", code, res)
	}
	id := owner.must(200, "GET", base+"/mailboxes", nil)["data"].([]any)[0].(map[string]any)["id"].(string)

	// A read-only token can list but not change.
	tok := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "reader", "scopes": []string{"mailboxes.read"}})
	reader := &client{t: t, h: e.h, bearer: tok["token"].(string)}
	reader.must(200, "GET", base+"/mailboxes/"+id, nil)
	reader.must(403, "PATCH", base+"/mailboxes/"+id, map[string]any{"daily_limit": 5})
	if code, _ := reader.raw("POST", base+"/mailboxes/import", "text/csv", csv); code != 403 {
		t.Fatalf("reader import: %d", code)
	}

	// Another workspace cannot see it.
	other := owner.must(201, "POST", "/v1/workspaces", map[string]any{"name": "Other"})
	ob := "/v1/workspaces/" + other["id"].(string)
	owner.must(404, "GET", ob+"/mailboxes/"+id, nil)
	owner.must(404, "DELETE", ob+"/mailboxes/"+id, nil)
	if list := owner.must(200, "GET", ob+"/mailboxes", nil)["data"].([]any); len(list) != 0 {
		t.Fatalf("leak: %v", list)
	}
}
