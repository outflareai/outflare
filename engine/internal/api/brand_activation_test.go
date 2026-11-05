package api_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/alerts"
	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/emails"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/journeys"
	"github.com/outflareai/outflare/engine/internal/rules"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Brands own their data, step 4 (plans/brands/README.md): rules, journeys,
// lifecycle email, alerts and webhooks each belong to one brand, are served
// under /v1/brands/{brand}/, and never act on, show or tell another
// brand's data. The workspace routes keep working for older callers: lists
// default to the first brand and take ?brand=.
func TestBrandActivation(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	fake := &fakeResend{verified: true}
	rs := httptest.NewServer(fake)
	defer rs.Close()
	e.integrations.Client.BaseURLs["resend"] = rs.URL
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	first := owner.must(200, "GET", base+"/brands", nil)["data"].([]any)[0].(map[string]any)["id"].(string)
	alpha := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Alpha", "handle": "alpha"})["id"].(string)
	bravo := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Bravo", "handle": "bravo"})["id"].(string)
	a, b := "/v1/brands/"+alpha, "/v1/brands/"+bravo

	tok := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "Bravo app", "brand": "bravo", "scopes": []string{
		"customers.read", "customers.manage", "emails.read", "emails.send", "emails.manage", "webhooks.read", "webhooks.manage", "workspace.read"}})
	bravoApp := &client{t: t, h: e.h, bearer: tok["token"].(string)}

	// The same address is a person in each brand (decision 1).
	person := func(brand, addr string) string {
		t.Helper()
		rec, err := customers.EnsurePersonIn(ctx, e.pool, ws, brand, "email", addr, true, "")
		if err != nil {
			t.Fatal(err)
		}
		return rec
	}
	anaA, anaB := person(alpha, "ana@example.org"), person(bravo, "ana@example.org")
	ids := func(c *client, path string) []string {
		t.Helper()
		var out []string
		for _, r := range c.must(200, "GET", path, nil)["data"].([]any) {
			out = append(out, r.(map[string]any)["id"].(string))
		}
		return out
	}

	// ---- Rules: made in the route's brand, acting on its records only ----

	rule := owner.must(201, "POST", a+"/rules", map[string]any{"name": "Trial started", "trigger": map[string]any{"kind": "event", "event": "trial_started"},
		"emit_event": "customer.trialing"})
	if rule["brand_id"] != alpha {
		t.Fatalf("rule made in %v, want alpha", rule["brand_id"])
	}
	rid := rule["id"].(string)
	owner.must(400, "POST", a+"/rules", map[string]any{"name": "Elsewhere", "brand_id": bravo, "trigger": map[string]any{"kind": "event", "event": "x"}, "emit_event": "y.z"})
	// Each brand can have a rule of the same name.
	owner.must(201, "POST", b+"/rules", map[string]any{"name": "Trial started", "trigger": map[string]any{"kind": "event", "event": "trial_started"},
		"emit_event": "customer.trialing"})
	for _, sub := range []string{"", "/firings"} {
		owner.must(200, "GET", a+"/rules/"+rid+sub, nil)
		owner.must(404, "GET", b+"/rules/"+rid+sub, nil)
		bravoApp.must(404, "GET", a+"/rules/"+rid+sub, nil)
		bravoApp.must(404, "GET", b+"/rules/"+rid+sub, nil)
	}
	owner.must(404, "POST", b+"/rules/"+rid+"/activate", nil)
	owner.must(400, "PATCH", a+"/rules/"+rid, map[string]any{"brand_id": bravo})
	// The dry run, like the engine, sees only Alpha's people.
	for _, rec := range []string{anaA, anaB} {
		if _, err := customers.Record(ctx, e.pool, ws, customers.Event{RecordID: rec, Name: "trial_started"}); err != nil {
			t.Fatal(err)
		}
	}
	sim := owner.must(200, "POST", a+"/rules/"+rid+"/simulate", map[string]any{"days": 1})
	if sim["records"] != 1.0 || sim["sample"].([]any)[0] != anaA {
		t.Fatalf("alpha's rule simulated %v", sim)
	}
	if l := ids(owner, a+"/rules"); len(l) != 1 || l[0] != rid {
		t.Fatalf("alpha's rules: %v", l)
	}
	if l := ids(owner, base+"/rules"); len(l) != 0 {
		t.Fatalf("the workspace route lists the first brand's rules: %v", l)
	}
	if l := ids(owner, base+"/rules?brand=alpha"); len(l) != 1 {
		t.Fatalf("workspace rules of alpha: %v", l)
	}
	if l := ids(owner, base+"/rules?brand=all"); len(l) != 2 {
		t.Fatalf("workspace rules of every brand: %v", l)
	}
	// A rule made on the workspace route goes to the first brand.
	if r := owner.must(201, "POST", base+"/rules", map[string]any{"name": "First", "trigger": map[string]any{"kind": "event", "event": "x"},
		"emit_event": "y.z"}); r["brand_id"] != first {
		t.Fatalf("workspace rule made in %v, want the first brand", r["brand_id"])
	}

	// ---- Journeys: they enrol their brand's people only ----

	j := owner.must(201, "POST", a+"/journeys", map[string]any{"name": "Nurture", "steps": []map[string]any{{"id": "w", "kind": "wait", "wait_seconds": 3600}}})
	if j["brand_id"] != alpha {
		t.Fatalf("journey made in %v", j["brand_id"])
	}
	jid := j["id"].(string)
	owner.must(400, "POST", a+"/journeys", map[string]any{"name": "Elsewhere", "brand_id": bravo})
	owner.must(200, "POST", a+"/journeys/"+jid+"/activate", nil)
	for _, sub := range []string{"", "/runs", "/versions", "/step-secrets"} {
		owner.must(200, "GET", a+"/journeys/"+jid+sub, nil)
		owner.must(404, "GET", b+"/journeys/"+jid+sub, nil)
		bravoApp.must(404, "GET", a+"/journeys/"+jid+sub, nil)
	}
	owner.must(409, "POST", a+"/journeys/"+jid+"/runs", map[string]any{"record_id": anaB})
	owner.must(404, "POST", b+"/journeys/"+jid+"/runs", map[string]any{"record_id": anaB})
	run := owner.must(201, "POST", a+"/journeys/"+jid+"/runs", map[string]any{"record_id": anaA})
	owner.must(404, "GET", b+"/journeys/"+jid+"/runs/"+run["id"].(string), nil)
	owner.must(404, "POST", a+"/journeys/"+jid+"/test", map[string]any{"record_id": anaB})
	owner.must(200, "POST", a+"/journeys/"+jid+"/test", map[string]any{"record_id": anaA})
	var runBrand string
	if err := e.pool.QueryRow(ctx, `SELECT brand_id::text FROM journey_runs WHERE id = $1`, run["id"]).Scan(&runBrand); err != nil || runBrand != alpha {
		t.Fatalf("the run's brand is %q (%v)", runBrand, err)
	}
	if l := ids(owner, base+"/journeys"); len(l) != 0 {
		t.Fatalf("the workspace route lists the first brand's journeys: %v", l)
	}
	if l := ids(owner, base+"/journeys?brand=alpha"); len(l) != 1 {
		t.Fatalf("workspace journeys of alpha: %v", l)
	}
	if m := owner.must(200, "GET", b+"/automation-map", nil); strings.Contains(fmt.Sprint(m), jid) || strings.Contains(fmt.Sprint(m), rid) {
		t.Fatalf("bravo's automation map shows alpha's: %v", m)
	}

	// ---- Lifecycle email: templates, topics and broadcasts per brand ----

	svc := &emails.Service{Pool: e.pool, Store: e.integrations, Box: e.box, PublicURL: "https://outflare.test", Log: log, Resolver: e.dns}
	owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "resend", "credentials": map[string]any{"api_key": "re_test_not_a_real_key"}})
	did := owner.must(201, "POST", base+"/email-provider-domains", map[string]any{"domain": "example.com"})["id"].(string)
	e.dns.setTXT("resend._domainkey.example.com", []string{"p=KEY"})
	if err := svc.RunDomainCheck(ctx, did); err != nil {
		t.Fatal(err)
	}
	// Topics are per brand: each brand has its own "news", and choosing
	// one is per brand too (consent on that brand's person).
	topic := owner.must(201, "POST", a+"/email-topics", map[string]any{"name": "News"})
	if topic["brand_id"] != alpha {
		t.Fatalf("topic brand %v", topic["brand_id"])
	}
	owner.must(404, "GET", b+"/email-topics/"+topic["id"].(string), nil)
	if l := owner.must(200, "GET", b+"/email-topics", nil)["data"].([]any); len(l) != 0 {
		t.Fatalf("bravo sees alpha's topics: %v", l)
	}
	// Bravo's own topic can use the same key: keys are unique per brand.
	bravoNews := owner.must(201, "POST", b+"/email-topics", map[string]any{"name": "News"})
	if bravoNews["brand_id"] != bravo || bravoNews["key"] != "news" || bravoNews["id"] == topic["id"] {
		t.Fatalf("bravo's own news topic: %v", bravoNews)
	}
	owner.must(409, "POST", a+"/email-topics", map[string]any{"name": "News"})
	if l := owner.must(200, "GET", a+"/email-topics", nil)["data"].([]any); len(l) != 1 {
		t.Fatalf("alpha's topics: %v", l)
	}
	tpl := owner.must(201, "POST", a+"/email-templates", map[string]any{"name": "Monthly", "purpose": "lifecycle", "topic": "news",
		"subject": "News", "html": "<p>News</p>", "from": "news@example.com", "publish": true})
	if tpl["brand_id"] != alpha {
		t.Fatalf("template made in %v", tpl["brand_id"])
	}
	owner.must(404, "GET", b+"/email-templates/"+tpl["id"].(string), nil)
	bravoApp.must(404, "GET", a+"/email-templates", nil)
	owner.must(201, "POST", b+"/email-templates", map[string]any{"name": "Bravo news", "purpose": "lifecycle", "subject": "News", "html": "<p>News</p>",
		"from": "news@example.com", "publish": true})
	// Bravo's journeys, broadcasts and emails can't use Alpha's templates.
	owner.must(400, "POST", b+"/journeys", map[string]any{"name": "Borrow", "steps": []map[string]any{{"id": "m", "kind": "email", "template": "monthly"}}})
	owner.must(400, "POST", b+"/email-broadcasts", map[string]any{"name": "Borrow", "template": "monthly"})
	owner.must(404, "POST", b+"/emails", map[string]any{"to": "ana@example.org", "from": "news@example.com", "template": "monthly"})
	bc := owner.must(201, "POST", a+"/email-broadcasts", map[string]any{"name": "October", "template": "monthly"})
	if bc["brand_id"] != alpha || bc["audience_size"] != 1.0 {
		t.Fatalf("broadcast = %v", bc)
	}
	owner.must(404, "GET", b+"/email-broadcasts/"+bc["id"].(string), nil)
	if l := ids(owner, b+"/email-broadcasts"); len(l) != 0 {
		t.Fatalf("bravo's broadcasts: %v", l)
	}

	// An unsubscribe applies to the brand it came from (decision 5): Ana
	// turns off all marketing email in Alpha, and Bravo still reaches her.
	send := func(path string) map[string]any {
		t.Helper()
		em := owner.must(202, "POST", path+"/emails", map[string]any{"to": "ana@example.org", "from": "news@example.com", "purpose": "lifecycle",
			"subject": "Hello", "text": "Hello"})
		if err := svc.Send(ctx, em["id"].(string), false); err != nil {
			t.Fatal(err)
		}
		return owner.must(200, "GET", path+"/emails/"+em["id"].(string), nil)
	}
	owner.must(201, "POST", a+"/records/"+anaA+"/consents", map[string]any{"channel": "email", "status": "withdrawn"})
	if em := send(a); em["status"] != "suppressed" || em["brand_id"] != alpha {
		t.Fatalf("alpha's email after alpha's unsubscribe: %v %v", em["status"], em["brand_id"])
	}
	bEmail := send(b)
	if bEmail["status"] != "sent" || bEmail["brand_id"] != bravo {
		t.Fatalf("bravo's email after alpha's unsubscribe: %v %v", bEmail["status"], bEmail["brand_id"])
	}
	owner.must(404, "GET", a+"/emails/"+bEmail["id"].(string), nil)
	bravoApp.must(200, "GET", b+"/emails/"+bEmail["id"].(string), nil)
	// An admin can apply it to every brand at once.
	if r := owner.must(200, "POST", base+"/email-unsubscribes", map[string]any{"email": "Ana@example.org"}); r["changed"].(float64) < 2 {
		t.Fatalf("unsubscribed in %v brands", r["changed"])
	}
	if em := send(b); em["status"] != "suppressed" {
		t.Fatalf("bravo's email after the admin's unsubscribe for every brand: %v", em["status"])
	}
	bravoApp.must(403, "POST", base+"/email-unsubscribes", map[string]any{"email": "ana@example.org"})
	if !slices.ContainsFunc(got(), func(ev track.Event) bool { return ev.Name == "email.unsubscribed_all_brands" }) {
		t.Fatal("no email.unsubscribed_all_brands event")
	}

	// ---- Webhooks: an endpoint belongs to one brand and hears its events ----

	hookA := owner.must(201, "POST", a+"/webhooks", map[string]any{"url": "https://hooks.example.com/a", "events": []string{"rule.*"}})
	hookB := owner.must(201, "POST", b+"/webhooks", map[string]any{"url": "https://hooks.example.com/b", "events": []string{"rule.*"}})
	if hookA["brand_id"] != alpha || hookB["brand_id"] != bravo {
		t.Fatalf("hooks made in %v and %v", hookA["brand_id"], hookB["brand_id"])
	}
	owner.must(400, "POST", a+"/webhooks", map[string]any{"url": "https://hooks.example.com/c", "brand": "bravo"})
	owner.must(404, "GET", b+"/webhooks/"+hookA["id"].(string), nil)
	bravoApp.must(404, "GET", a+"/webhooks/"+hookA["id"].(string)+"/deliveries", nil)
	if l := ids(bravoApp, b+"/webhooks"); len(l) != 1 || l[0] != hookB["id"] {
		t.Fatalf("bravo's webhooks: %v", l)
	}
	if l := ids(owner, base+"/webhooks"); len(l) != 0 {
		t.Fatalf("the workspace route lists the first brand's webhooks: %v", l)
	}
	owner.must(200, "POST", a+"/rules/"+rid+"/activate", nil)
	delivered := func(hook string) []string {
		t.Helper()
		rows, err := e.pool.Query(ctx, `SELECT event_type || ':' || brand_id::text FROM webhook_deliveries WHERE endpoint_id = $1 ORDER BY id`, hook)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				t.Fatal(err)
			}
			out = append(out, s)
		}
		return out
	}
	if d := delivered(hookA["id"].(string)); len(d) != 1 || d[0] != "rule.activated:"+alpha {
		t.Fatalf("alpha's endpoint got %v", d)
	}
	if d := delivered(hookB["id"].(string)); len(d) != 0 {
		t.Fatalf("bravo's endpoint got alpha's events: %v", d)
	}

	// The live rule fires for Alpha's people only.
	q := jobs.New(e.pool, log)
	q.PollInterval = 20 * time.Millisecond
	(&rules.Service{Pool: e.pool, Log: log}).Register(q)
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { q.Run(runCtx, 2); close(done) }() //nolint:errcheck
	for _, rec := range []string{anaA, anaB} {
		if _, err := customers.Record(ctx, e.pool, ws, customers.Event{RecordID: rec, Name: "trial_started"}); err != nil {
			t.Fatal(err)
		}
	}
	for deadline := time.Now().Add(30 * time.Second); ; {
		var n int
		_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE workspace_id = $1 AND kind LIKE 'rule.%' AND status IN ('queued', 'running')`, ws).Scan(&n)
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("rule jobs never ran")
		}
		time.Sleep(25 * time.Millisecond)
	}
	cancel()
	<-done
	var fired []string
	rows, err := e.pool.Query(ctx, `SELECT record_id::text || ':' || brand_id::text FROM rule_firings WHERE rule_id = $1`, rid)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var f string
		_ = rows.Scan(&f)
		fired = append(fired, f)
	}
	rows.Close()
	if len(fired) != 1 || fired[0] != anaA+":"+alpha {
		t.Fatalf("alpha's rule fired for %v, want only alpha's Ana", fired)
	}

	// ---- Alerts: listed in their brand ----

	for _, brand := range []string{alpha, bravo} {
		if _, err := alerts.Raise(ctx, e.pool, alerts.Alert{WorkspaceID: ws, BrandID: &brand, Kind: journeys.AlertKind, DedupeKey: brand,
			TargetType: "journey", TargetID: jid}); err != nil {
			t.Fatal(err)
		}
	}
	if l := ids(owner, a+"/alerts"); len(l) != 1 {
		t.Fatalf("alpha's alerts: %v", l)
	}
	if l := ids(bravoApp, b+"/alerts"); len(l) != 1 {
		t.Fatalf("bravo's alerts: %v", l)
	}
	bravoApp.must(404, "GET", a+"/alerts", nil)
	if l := ids(owner, base+"/alerts"); len(l) != 0 {
		t.Fatalf("the workspace route lists the first brand's alerts: %v", l)
	}
	owner.must(200, "PUT", b+"/alerts/settings", map[string]any{"kind": journeys.AlertKind, "email": "off"})
	settings := owner.must(200, "GET", a+"/alerts/settings", nil)["settings"].([]any)
	if len(settings) != 0 {
		t.Fatalf("alpha's alert settings show bravo's: %v", settings)
	}
	owner.must(400, "PUT", b+"/alerts/settings", map[string]any{"kind": journeys.AlertKind, "email": "off", "brand_id": alpha})

	// Query: a brand's activation tables hold its rows only.
	r := queryRows(t, owner.must(200, "POST", b+"/query", map[string]any{"sql": "SELECT (SELECT count(*) FROM rules) AS rules, (SELECT count(*) FROM journeys) AS journeys, " +
		"(SELECT count(*) FROM journey_runs) AS runs, (SELECT count(*) FROM emails) AS emails"}))
	if fmt.Sprint(r) != "[[1 0 0 2]]" {
		t.Fatalf("bravo's query counts %v", r)
	}
}
