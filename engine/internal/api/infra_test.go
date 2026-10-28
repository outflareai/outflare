package api_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/infra"
)

// infraSetup returns the owner, the workspace path and a service whose
// clock the test sets.
func infraSetup(t *testing.T, e *env, clock *fakeClock) (*client, string, string, *infra.Service) {
	t.Helper()
	owner, ws := setupOwner(t, e.h)
	svc := &infra.Service{Pool: e.pool, Mail: e.mail, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: clock.Now}
	return owner, "/v1/workspaces/" + ws, ws, svc
}

// goodDNS gives a domain Google MX, SPF, DKIM and DMARC.
func goodDNS(e *env, domain string) {
	e.dns.setMX(domain, []*net.MX{{Host: "aspmx.l.google.com.", Pref: 1}})
	e.dns.setTXT(domain, []string{"v=spf1 include:_spf.google.com ~all"})
	e.dns.setTXT("google._domainkey."+domain, []string{"v=DKIM1; k=rsa; p=MIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8AMIIBCgKCAQEA"})
	e.dns.setTXT("_dmarc."+domain, []string{"v=DMARC1; p=none"})
}

// addMailbox stands in for a mailbox the person imported.
func addMailbox(t *testing.T, e *env, ws, email string) string {
	t.Helper()
	var id string
	err := e.pool.QueryRow(context.Background(), `
		INSERT INTO mailboxes (workspace_id, email, provider, smtp_host, smtp_port, smtp_username, imap_host, imap_port, imap_username, credentials, status)
		VALUES ($1, $2, 'google', 'smtp.gmail.com', 587, $2, 'imap.gmail.com', 993, $2, '\x00', 'active') RETURNING id::text`, ws, email).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// warmInSequencer has a connected sequencer warm a mailbox up, with the
// given warm-up score.
func warmInSequencer(t *testing.T, e *env, ws, mailbox string, score int) {
	t.Helper()
	ctx := context.Background()
	var seq string
	err := e.pool.QueryRow(ctx, `SELECT id FROM integrations WHERE workspace_id = $1 AND connector = 'emailbison'`, ws).Scan(&seq)
	if errors.Is(err, pgx.ErrNoRows) {
		err = e.pool.QueryRow(ctx, `INSERT INTO integrations (workspace_id, connector, name, credentials)
			VALUES ($1, 'emailbison', 'EmailBison', '\x00') RETURNING id`, ws).Scan(&seq)
	}
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `
		INSERT INTO mailbox_platforms (mailbox_id, integration_id, workspace_id, external_id, status, warmup_enabled, warmup_score)
		VALUES ($1, $2, $3, $5, 'ok', true, $4)
		ON CONFLICT (mailbox_id, integration_id) DO UPDATE SET warmup_enabled = true, warmup_score = $4`, mailbox, seq, ws, score, "ext-"+mailbox); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `UPDATE mailboxes SET warmup_enabled = true, warmup_integration_id = $2 WHERE id = $1`, mailbox, seq); err != nil {
		t.Fatal(err)
	}
}

func items(p map[string]any) map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, it := range p["items"].([]any) {
		it := it.(map[string]any)
		out[it["name"].(string)] = it
	}
	return out
}

func warningCodes(p map[string]any) string {
	var c []string
	for _, w := range p["warnings"].([]any) {
		c = append(c, w.(map[string]any)["code"].(string))
	}
	return strings.Join(c, ",")
}

func TestInfraManualPlanEndToEnd(t *testing.T) {
	defer infra.UseTestTLDs("example")()
	e := freshEnv(t)
	clock := &fakeClock{}
	clock.Set(time.Now())
	owner, base, ws, svc := infraSetup(t, e, clock)
	ctx := context.Background()
	e.runJobs(t)

	// The person already has acme-sales.example; the plan adds mailboxes to it.
	goodDNS(e, "acme-sales.example")
	owner.must(201, "POST", base+"/sending-domains", map[string]any{"domain": "acme-sales.example"})

	preview := owner.must(200, "POST", base+"/infrastructure/preview", map[string]any{"domain_count": 2, "mailboxes_per_domain": 2, "brand": "Acme"})
	if !strings.Contains(warningCodes(preview), "no_personas") {
		t.Fatalf("preview warnings: %v", preview["warnings"])
	}

	owner.must(400, "POST", base+"/infra-plans", map[string]any{"name": "x", "domain_count": 0})
	owner.must(400, "POST", base+"/infra-plans", map[string]any{"name": "x", "domain_count": 2, "supplier": "nobody"})
	p := owner.must(201, "POST", base+"/infra-plans", map[string]any{
		"name": "Q4 outbound", "domain_count": 2, "mailboxes_per_domain": 2, "brand": "Acme",
		"domains": []string{"acme-sales.example"},
	})
	id := p["id"].(string)
	if p["status"] != "draft" || len(p["items"].([]any)) != 2 || warningCodes(p) != "no_personas" {
		t.Fatalf("created: %v %v", p["items"], p["warnings"])
	}
	// A blocking warning stops approval.
	if code, res := owner.do("POST", base+"/infra-plans/"+id+"/approve", nil); code != 409 || !strings.Contains(res["error"].(map[string]any)["message"].(string), "Add at least one person") {
		t.Fatalf("approve without personas: %d %v", code, res)
	}
	p = owner.must(200, "PATCH", base+"/infra-plans/"+id, map[string]any{"personas": []map[string]string{{"first_name": "Jane", "last_name": "Doe"}}})
	it := items(p)
	if len(it) != 6 || it["acme-sales.example"]["owned"] != true || it["getacme.example"]["owned"] != false || it["jane.doe@getacme.example"] == nil {
		t.Fatalf("items: %v", it)
	}
	if p["daily_capacity"] != 120.0 || warningCodes(p) != "" {
		t.Fatalf("plan: %v %v", p["daily_capacity"], p["warnings"])
	}

	// Drafting is not approving: a token without the approve scope cannot.
	tok := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "Planner agent", "scopes": []string{"infrastructure.read", "infrastructure.manage"}})
	agent := &client{t: t, h: e.h, bearer: tok["token"].(string)}
	agent.must(200, "GET", base+"/infra-plans/"+id, nil)
	agent.must(403, "POST", base+"/infra-plans/"+id+"/approve", nil)

	p = owner.must(200, "POST", base+"/infra-plans/"+id+"/approve", nil)
	if p["status"] != "building" || p["approved_at"] == nil {
		t.Fatalf("approved: %v", p)
	}
	owner.must(409, "PATCH", base+"/infra-plans/"+id, map[string]any{"brand": "Other"})
	owner.must(409, "DELETE", base+"/infra-plans/"+id, nil)

	// The owned domain goes to DNS; the new one waits for the person to buy.
	if err := svc.Advance(ctx, id); err != nil {
		t.Fatal(err)
	}
	it = items(owner.must(200, "GET", base+"/infra-plans/"+id, nil))
	if it["getacme.example"]["stage"] != "buying" || !strings.Contains(it["getacme.example"]["detail"].(string), "Buy getacme.example at your registrar and add 2 mailboxes on Google Workspace") {
		t.Fatalf("getacme.example: %v", it["getacme.example"])
	}
	if it["acme-sales.example"]["stage"] != "dns" && it["acme-sales.example"]["stage"] != "done" {
		t.Fatalf("acme-sales.example: %v", it["acme-sales.example"])
	}

	// The person buys it and sets DNS.
	goodDNS(e, "getacme.example")
	if res := owner.must(200, "POST", base+"/infra-plans/"+id+"/bought", map[string]any{"domains": []string{"getacme.example"}}); res["updated"] != 1.0 {
		t.Fatalf("bought: %v", res)
	}
	if err := svc.Advance(ctx, id); err != nil {
		t.Fatal(err)
	}
	waitFor(t, owner, base+"/sending-domains/getacme.example", func(d map[string]any) bool { return d["dkim"] == "pass" })
	waitFor(t, owner, base+"/sending-domains/acme-sales.example", func(d map[string]any) bool { return d["dkim"] == "pass" })
	if err := svc.Advance(ctx, id); err != nil {
		t.Fatal(err)
	}
	p = owner.must(200, "GET", base+"/infra-plans/"+id, nil)
	it = items(p)
	if it["getacme.example"]["stage"] != "done" || it["jane@getacme.example"]["stage"] != "connecting" {
		t.Fatalf("after DNS: %v %v", it["getacme.example"], it["jane@getacme.example"])
	}

	// The person connects the four mailboxes; the plan links them and
	// starts warm-up.
	ids := map[string]string{}
	for _, email := range []string{"jane@getacme.example", "jane.doe@getacme.example", "jane@acme-sales.example", "jane.doe@acme-sales.example"} {
		ids[email] = addMailbox(t, e, ws, email)
	}
	if err := svc.Advance(ctx, id); err != nil {
		t.Fatal(err)
	}
	p = owner.must(200, "GET", base+"/infra-plans/"+id, nil)
	if p["status"] != "warming" || p["counts"].(map[string]any)["mailboxes"].(map[string]any)["warming"] != 4.0 {
		t.Fatalf("warming: %v %v", p["status"], p["counts"])
	}
	var stage string
	var warm bool
	var limit int
	e.pool.QueryRow(ctx, `SELECT lifecycle_stage, warmup_enabled, daily_limit FROM mailboxes WHERE id = $1`, ids["jane@getacme.example"]).Scan(&stage, &warm, &limit) //nolint:errcheck
	// Warm-up runs in a connected sequencer, so it isn't on yet.
	if stage != "warming" || warm || limit != 30 {
		t.Fatalf("mailbox: %s %v %d", stage, warm, limit)
	}
	ov := owner.must(200, "GET", base+"/infrastructure", nil)
	if ov["mailboxes"].(map[string]any)["warming"] != 4.0 || len(ov["not_ready"].([]any)) != 4 || len(ov["plans"].([]any)) != 1 {
		t.Fatalf("overview: %v", ov)
	}

	// Two weeks on, three score well in their sequencer's warm-up and are
	// ready; one is still in spam.
	clock.Add(15 * 24 * time.Hour)
	for email, mid := range ids {
		score := 100
		if email == "jane.doe@acme-sales.example" {
			score = 50
		}
		warmInSequencer(t, e, ws, mid, score)
	}
	if n, err := svc.Lifecycle(ctx); err != nil || n != 3 {
		t.Fatalf("lifecycle moved %d: %v", n, err)
	}
	var reason string
	e.pool.QueryRow(ctx, `SELECT lifecycle_stage, stage_reason FROM mailboxes WHERE id = $1`, ids["jane.doe@acme-sales.example"]).Scan(&stage, &reason) //nolint:errcheck
	if stage != "warming" || !strings.Contains(reason, "Inbox placement is 50%") {
		t.Fatalf("slow mailbox: %s %q", stage, reason)
	}
	// Retire it by hand; the plan is then ready.
	res := owner.must(200, "POST", base+"/infrastructure/move", map[string]any{"mailbox_ids": []string{ids["jane.doe@acme-sales.example"]}, "stage": "retired", "reason": "Never warmed up"})
	if res["moved"] != 1.0 {
		t.Fatalf("retire: %v", res)
	}
	var status string
	e.pool.QueryRow(ctx, `SELECT status, warmup_enabled FROM mailboxes WHERE id = $1`, ids["jane.doe@acme-sales.example"]).Scan(&status, &warm) //nolint:errcheck
	if status != "paused" || warm {
		t.Fatalf("retired mailbox: %s %v", status, warm)
	}
	res = owner.must(200, "POST", base+"/infrastructure/move", map[string]any{"mailbox_ids": []string{ids["jane.doe@acme-sales.example"]}, "stage": "ready"})
	if res["moved"] != 0.0 || !strings.Contains(res["results"].([]any)[0].(map[string]any)["skipped"].(string), "retired mailboxes stay retired") {
		t.Fatalf("un-retire: %v", res)
	}
	if err := svc.Advance(ctx, id); err != nil {
		t.Fatal(err)
	}
	if p = owner.must(200, "GET", base+"/infra-plans/"+id, nil); p["status"] != "ready" || p["completed_at"] == nil {
		t.Fatalf("plan: %v", p["status"])
	}

	// Ready mailboxes whose warm-up score drops rest on their own.
	for _, mid := range ids {
		var ready bool
		e.pool.QueryRow(ctx, `SELECT lifecycle_stage = 'ready' FROM mailboxes WHERE id = $1`, mid).Scan(&ready) //nolint:errcheck
		if ready {
			warmInSequencer(t, e, ws, mid, 33)
		}
	}
	if n, err := svc.Lifecycle(ctx); err != nil || n != 3 {
		t.Fatalf("rest moved %d: %v", n, err)
	}
	e.pool.QueryRow(ctx, `SELECT lifecycle_stage, stage_reason FROM mailboxes WHERE id = $1`, ids["jane@getacme.example"]).Scan(&stage, &reason) //nolint:errcheck
	if stage != "resting" || !strings.Contains(reason, "fell to 33%") {
		t.Fatalf("resting: %s %q", stage, reason)
	}

	// Every move is in the audit log.
	var n int
	e.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE workspace_id = $1 AND action = 'mailbox.stage_changed'`, ws).Scan(&n) //nolint:errcheck
	if n != 7 {
		t.Fatalf("stage_changed events: %d", n)
	}
}

// fakeSupplier records orders and answers with fixed prices.
type fakeSupplier struct {
	mu        sync.Mutex
	orders    []infra.OrderRequest
	status    infra.OrderStatus
	cancelled []string
	fail      error
}

func (f *fakeSupplier) Quote(_ context.Context, _ infra.Account, req infra.QuoteRequest) (infra.Quote, error) {
	q := infra.Quote{Currency: "USD"}
	once, monthly := int64(0), int64(len(req.Domains)*req.MailboxesPerDomain*300)
	for _, d := range req.Domains {
		price := int64(1200)
		q.Domains = append(q.Domains, infra.DomainQuote{Domain: d, Available: d != "getacme.example", PriceCents: &price, Reason: "registered"})
		once += price
	}
	q.OnceCents, q.MonthlyCents = &once, &monthly
	return q, nil
}

func (f *fakeSupplier) Order(_ context.Context, _ infra.Account, req infra.OrderRequest) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return "", f.fail
	}
	f.orders = append(f.orders, req)
	return "ord_1", nil
}

func (f *fakeSupplier) OrderStatus(context.Context, infra.Account, string) (infra.OrderStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status, nil
}

func (f *fakeSupplier) Cancel(_ context.Context, _ infra.Account, email string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelled = append(f.cancelled, email)
	return nil
}

func TestInfraSupplierPlanQuotesOrdersOnce(t *testing.T) {
	defer infra.UseTestTLDs("example")()
	e := freshEnv(t)
	clock := &fakeClock{}
	clock.Set(time.Now())
	owner, base, ws, svc := infraSetup(t, e, clock)
	ctx := context.Background()
	sup := &fakeSupplier{}
	infra.RegisterSupplier("fakesupplier", sup)

	p := owner.must(201, "POST", base+"/infra-plans", map[string]any{
		"name": "Supplier plan", "supplier": "fakesupplier", "domain_count": 2, "mailboxes_per_domain": 1, "brand": "Acme",
		"personas": []map[string]string{{"first_name": "Jane", "last_name": "Doe"}},
	})
	id := p["id"].(string)
	if warningCodes(p) != "quote_pending" {
		t.Fatalf("warnings: %v", p["warnings"])
	}
	runQuote := func() {
		t.Helper()
		if err := svc.Quote(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	runQuote()
	p = owner.must(200, "GET", base+"/infra-plans/"+id, nil)
	if warningCodes(p) != "domains_unavailable" || items(p)["getacme.example"]["available"] != false {
		t.Fatalf("after quote: %v", p["warnings"])
	}
	// Leaving the domains out swaps the taken one for a fresh suggestion.
	p = owner.must(200, "PATCH", base+"/infra-plans/"+id, map[string]any{"domains": []string{"acmehq.example"}})
	runQuote()
	p = owner.must(200, "GET", base+"/infra-plans/"+id, nil)
	it := items(p)
	if warningCodes(p) != "" || it["acmehq.example"] == nil || it["getacme.example"] != nil || it["tryacme.example"] == nil {
		t.Fatalf("after swap: %v %v", p["warnings"], it)
	}

	// Approving needs the price the approver saw.
	owner.must(409, "POST", base+"/infra-plans/"+id+"/approve", nil)
	owner.must(409, "POST", base+"/infra-plans/"+id+"/approve", map[string]any{"once_cents": 2400, "monthly_cents": 1})
	owner.must(200, "POST", base+"/infra-plans/"+id+"/approve", map[string]any{"once_cents": 2400, "monthly_cents": 600})

	for range 3 {
		if err := svc.Advance(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	if len(sup.orders) != 1 || len(sup.orders[0].Domains) != 2 || sup.orders[0].IdempotencyKey != id || len(sup.orders[0].Domains[0].Mailboxes) != 1 {
		t.Fatalf("orders: %+v", sup.orders)
	}
	it = items(owner.must(200, "GET", base+"/infra-plans/"+id, nil))
	if it["acmehq.example"]["stage"] != "buying" || !strings.Contains(it["acmehq.example"]["detail"].(string), "waiting for it to be set up") {
		t.Fatalf("ordered: %v", it["acmehq.example"])
	}
	sup.mu.Lock()
	sup.status = infra.OrderStatus{Status: infra.OrderPending, Domains: []infra.ItemStatus{{Name: "acmehq.example", Status: infra.OrderDone}, {Name: "tryacme.example", Status: infra.OrderFailed, Detail: "registry error"}}}
	sup.mu.Unlock()
	if err := svc.Advance(ctx, id); err != nil {
		t.Fatal(err)
	}
	it = items(owner.must(200, "GET", base+"/infra-plans/"+id, nil))
	if it["acmehq.example"]["stage"] != "dns" || it["tryacme.example"]["stage"] != "failed" || it["jane@tryacme.example"]["stage"] != "cancelled" {
		t.Fatalf("status: %v %v %v", it["acmehq.example"], it["tryacme.example"], it["jane@tryacme.example"])
	}

	// Retiring with cancel_at_supplier cancels the mailbox there.
	mid := addMailbox(t, e, ws, "jane@acmehq.example")
	if err := svc.Advance(ctx, id); err != nil {
		t.Fatal(err)
	}
	owner.must(200, "POST", base+"/infrastructure/move", map[string]any{"mailbox_ids": []string{mid}, "stage": "retired", "cancel_at_supplier": true})
	var kind string
	if err := e.pool.QueryRow(ctx, `SELECT kind FROM jobs WHERE kind = 'infra.cancel'`).Scan(&kind); err != nil {
		t.Fatalf("cancel job: %v", err)
	}
}

func TestInfraOrderFailureIsNeverRetried(t *testing.T) {
	defer infra.UseTestTLDs("example")()
	e := freshEnv(t)
	clock := &fakeClock{}
	clock.Set(time.Now())
	owner, base, _, svc := infraSetup(t, e, clock)
	ctx := context.Background()
	sup := &fakeSupplier{fail: errors.New("card declined")}
	infra.RegisterSupplier("failsupplier", sup)
	p := owner.must(201, "POST", base+"/infra-plans", map[string]any{
		"name": "Fails", "supplier": "failsupplier", "domain_count": 1, "mailboxes_per_domain": 1, "domains": []string{"acmeco.example"},
		"personas": []map[string]string{{"first_name": "Jane"}},
	})
	id := p["id"].(string)
	if err := svc.Quote(ctx, id); err != nil {
		t.Fatal(err)
	}
	owner.must(200, "POST", base+"/infra-plans/"+id+"/approve", map[string]any{"once_cents": 1200, "monthly_cents": 300})
	for range 2 {
		if err := svc.Advance(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	it := items(owner.must(200, "GET", base+"/infra-plans/"+id, nil))
	if it["acmeco.example"]["stage"] != "failed" || !strings.Contains(it["acmeco.example"]["detail"].(string), "card declined") {
		t.Fatalf("failed order: %v", it["acmeco.example"])
	}
	sup.fail = nil
	if err := svc.Advance(ctx, id); err != nil {
		t.Fatal(err)
	}
	if len(sup.orders) != 0 {
		t.Fatalf("retried the order: %v", sup.orders)
	}
}
